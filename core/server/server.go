//go:build !darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"nekobox_core/gen"
	"nekobox_core/internal"
	"nekobox_core/internal/boxbox"
	"nekobox_core/internal/boxmain"
	"nekobox_core/internal/process"
	"nekobox_core/internal/wg"

	//	"nekobox_core/internal/sys"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/shlex"
	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

// instanceMu guards check-and-set on the core instance globals
// (internal.BoxInstance, internal.InstanceCancel, internal.ExtraProcess)
// shared by Start/Stop, the SIGTERM + waitpid watchers in main.go, and
// the TestCurrent readers below (BUG-017). It is held only across the
// decision + the pointer write — never across box creation, Start/Run,
// or teardown.
var instanceMu sync.Mutex

// instanceRunning reports under lock whether an instance is published.
func instanceRunning() bool {
	instanceMu.Lock()
	defer instanceMu.Unlock()
	return internal.BoxInstance != nil
}

// getInstance returns the published instance (nil when idle) under lock.
func getInstance() *boxbox.Box {
	instanceMu.Lock()
	defer instanceMu.Unlock()
	return internal.BoxInstance
}

// publishInstance installs a freshly created instance, refusing when a
// concurrent Start won the race (the caller must tear down bi itself).
func publishInstance(bi *boxbox.Box, cancel context.CancelFunc) error {
	instanceMu.Lock()
	defer instanceMu.Unlock()
	if internal.BoxInstance != nil {
		return errors.New("instance already started")
	}
	internal.BoxInstance = bi
	internal.InstanceCancel = cancel
	return nil
}

// takeInstance removes and returns the running instance (nil when idle)
// so the caller can tear it down without holding the lock.
func takeInstance() (*boxbox.Box, context.CancelFunc) {
	instanceMu.Lock()
	defer instanceMu.Unlock()
	bi, cancel := internal.BoxInstance, internal.InstanceCancel
	internal.BoxInstance = nil
	internal.InstanceCancel = nil
	return bi, cancel
}

// shutdownInstance stops the published instance (if any) plus the extra
// process without holding instanceMu across teardown. Shared by the
// SIGTERM handler and the waitpid watcher in main.go.
func shutdownInstance() {
	internal.ResetSystemProxy()
	bi, cancel := takeInstance()
	instanceMu.Lock()
	ep := internal.ExtraProcess
	internal.ExtraProcess = nil
	instanceMu.Unlock()
	if bi != nil {
		if cancel != nil {
			cancel()
		}
		boxmain.CloseMonitor(bi.StartCtx)
	}
	if ep != nil {
		ep.Stop()
	}
}

// server is used to implement myservice.MyServiceServer.
type server struct {
}

// To returns a pointer to the given value.
func To[T any](v T) *T {
	return &v
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		return false
	}
	return !info.IsDir()
}

func (s *server) IsSupported(ctx context.Context, in *gen.Type) (*gen.Supported, error) {
	out := new(gen.Supported)
	ok := true
	if in.Type == "naive" {
		ok = CheckNaive()
	}
	out.Ok = ok
	return out, nil
}

func (s *server) CacheHTTP(ctx context.Context, in *gen.CacheURLRequest) (*gen.CacheURLResult_, error) {
	out := new(gen.CacheURLResult_)
	var exists bool
	path, err := internal.CacheHttpBool(in.HTTPURL, in.UseDefaultOutbound, &exists)
	if err != nil {
		return nil, err
	}
	out.FilePath = path
	out.Exists = exists
	return out, nil
}

// allowedExtraProcessBinaries is the allowlist for the Start RPC's
// extra-process binary. The GUI never sets these fields today, so the
// list holds only the binaries the GUI is expected to use (by basename,
// case-insensitive on Windows): the patched core itself and xray.
// Any other basename is rejected (BUG-002).
var allowedExtraProcessBinaries = map[string]bool{
	"nekobox_core":     true,
	"nekobox_core.exe": true,
	"xray":             true,
	"xray.exe":         true,
}

func validateExtraProcessBinary(path string) error {
	base := strings.ToLower(filepath.Base(path))
	// filepath.Base strips directory components, so "C:\\Windows\\cmd.exe"
	// -> "cmd.exe" and "../../bin/evil" -> "evil".
	if !allowedExtraProcessBinaries[base] {
		return fmt.Errorf("extra process binary %q not allowed", path)
	}
	return nil
}

// validateExtraConfDir confines the extra-process conf dir: the cleaned
// absolute joined path (dir + "extra.conf") must stay inside the cleaned
// absolute conf dir, and a relative dir must not escape the process
// working directory via "..". The same validation applies to the dir
// itself before any use (BUG-002).
func validateExtraConfDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("extra process conf dir is empty")
	}
	cleanDir := filepath.Clean(dir)
	if cleanDir == ".." || strings.HasPrefix(cleanDir, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("extra process conf dir escapes base: %q", dir)
	}
	absBase, err := filepath.Abs(cleanDir)
	if err != nil {
		return "", fmt.Errorf("extra process conf dir: %w", err)
	}
	if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("extra process conf dir: %w", err)
		}
		cwd = filepath.Clean(cwd)
		if absBase != cwd && !strings.HasPrefix(absBase, cwd+string(os.PathSeparator)) {
			return "", fmt.Errorf("extra process conf dir escapes base: %q", dir)
		}
	}
	joined := filepath.Join(absBase, "extra.conf")
	if joined != absBase && !strings.HasPrefix(joined, absBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("extra process conf dir escapes base: %q", dir)
	}
	return joined, nil
}

func (s *server) Start(ctx context.Context, in *gen.LoadConfigReq) (*gen.ErrorResp, error) {
	out := new(gen.ErrorResp)
	var err error
	clearInstance := true

	defer func() {
		if err != nil {
			out.Error = (err.Error())
			if clearInstance {
				instanceMu.Lock()
				internal.BoxInstance = nil
				instanceMu.Unlock()
			}
		} else {
			context.AfterFunc(internal.BoxInstance.StartCtx, func() {
				internal.ResetSystemProxy()
			})
		}
	}()

	if internal.Debug {
		log.Println("Start:", in.CoreConfig)
	}

	if instanceRunning() {
		err = errors.New("instance already started")
		return out, nil
	}

	if in.NeedExtraProcess {
		if verr := validateExtraProcessBinary(in.ExtraProcessPath); verr != nil {
			err = verr
			return out, nil
		}
		args, e := shlex.Split(in.GetExtraProcessArgs_())
		if e != nil {
			err = E.Cause(e, "Failed to parse args")
			return out, nil
		}
		if in.ExtraProcessConf != "" {
			extraConfPath, verr := validateExtraConfDir(in.ExtraProcessConfDir)
			if verr != nil {
				err = verr
				return out, nil
			}
			f, e := os.OpenFile(extraConfPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0700)
			if e != nil {
				err = E.Cause(e, "Failed to open extra.conf")
				return out, nil
			}
			defer f.Close()
			_, e = f.WriteString(in.ExtraProcessConf)
			if e != nil {
				err = E.Cause(e, "Failed to write extra.conf")
				return out, nil
			}
			for idx, arg := range args {
				if strings.Contains(arg, "%s") {
					args[idx] = fmt.Sprintf(arg, extraConfPath)
					break
				}
			}
		} else if in.ExtraProcessConfDir != "" {
			// Even with no conf payload, a supplied dir must still be
			// in-bounds before any process spawn using it.
			if _, verr := validateExtraConfDir(in.ExtraProcessConfDir); verr != nil {
				err = verr
				return out, nil
			}
		}

		ep := process.NewProcess(in.ExtraProcessPath, args, in.ExtraNoOut)
		instanceMu.Lock()
		internal.ExtraProcess = ep
		instanceMu.Unlock()
		err = ep.Start()
		if err != nil {
			return out, nil
		}
	}

	bi, boxCancel, err := boxmain.Create([]byte(in.CoreConfig))
	if err != nil {
		return out, nil
	}
	if perr := publishInstance(bi, boxCancel); perr != nil {
		// A concurrent Start won the race: tear down what we built
		// and leave the winner published.
		bi.CloseWithTimeout(boxCancel, 2*time.Second, log.Println)
		clearInstance = false
		err = perr
		return out, nil
	}
	//	if strings.Contains(
	//		in.CoreConfig, "tun-in") && strings.Contains(
	//		in.CoreConfig, "172.19.0.1/24") {
	//		err := sys.SetSystemDNS("172.19.0.2", boxInstance.Network().InterfaceMonitor())
	//		if err != nil {
	//			log.Println("Failed to set system DNS:", err)
	//		}
	//		needUnsetDNS = true
	//	}

	return out, nil
}

func (s *server) EnableSystemProxy(ctx context.Context, in *gen.SystemProxy) (*gen.ErrorResp, error) {
	out := new(gen.ErrorResp)
	var err error

	defer func() {
		if err != nil {
			out.Error = (err.Error())
		}
	}()

	// BUG-018: narrowing i32 to uint16 without a range check wraps
	// silently (70000 -> 4464) and programs the wrong port. Validate
	// BEFORE the cast.
	if in.Port <= 0 || in.Port > 65535 {
		err = fmt.Errorf("invalid proxy port %d: want 1..65535", in.Port)
		return out, nil
	}
	err = internal.SetSystemProxy(ctx, in.Address, uint16(in.Port), in.SupportSocks)

	return out, nil
}

func (s *server) DisableSystemProxy(ctx context.Context, in *gen.EmptyReq) (*gen.ErrorResp, error) {
	out := new(gen.ErrorResp)
	var err error

	defer func() {
		if err != nil {
			out.Error = (err.Error())
		}
	}()

	err = internal.ResetSystemProxy()

	return out, nil
}

func (s *server) Stop(ctx context.Context, in *gen.EmptyReq) (*gen.ErrorResp, error) {
	out := new(gen.ErrorResp)
	var err error

	internal.ResetSystemProxy()
	// BUG-009: also restore a leftover system-DNS override (the proxy
	// reset above never covered DNS, so quitting with system DNS applied
	// left it installed). Runs before the early return below so it fires
	// even when no instance is running.
	restoreSystemDNSOnStop()

	defer func() {
		if err != nil {
			out.Error = (err.Error())
		}
	}()

	bi, boxCancel := takeInstance()
	if bi == nil {
		return out, err
	}

	bi.CloseWithTimeout(boxCancel, time.Second*2, log.Println)

	instanceMu.Lock()
	ep := internal.ExtraProcess
	internal.ExtraProcess = nil
	instanceMu.Unlock()
	if ep != nil {
		ep.Stop()
	}

	return out, nil
}

func (s *server) CheckConfig(ctx context.Context, in *gen.LoadConfigReq) (*gen.ErrorResp, error) {
	out := new(gen.ErrorResp)
	err := boxmain.Check([]byte(in.CoreConfig))
	if err != nil {
		out.Error = (err.Error())
		return out, nil
	}
	return out, nil
}

func (s *server) QueryIPTest(ctx context.Context, in *gen.EmptyReq) (out *gen.QueryIPTestResponse, _ error) {
	results := IPReporter.Results()
	out = &gen.QueryIPTestResponse{}
	for _, r := range results {
		errStr := ""
		if r.Error != nil {
			errStr = r.Error.Error()
		}

		out.Results = append(out.Results, &gen.IPTestResp{
			OutboundTag: (r.Tag),
			IP:          (r.Result.IP),
			CountryCode: (r.Result.CountryCode),
			Error:       (errStr),
		})
	}
	return out, nil
}

func (s *server) IPTest(ctx context.Context, in *gen.IPTestRequest) (*gen.QueryIPTestResponse, error) {
	out := new(gen.QueryIPTestResponse)
	var testInstance *boxbox.Box
	var cancel context.CancelFunc
	var err error
	testInstance, cancel, err = boxmain.Create([]byte(in.Config))
	if err != nil {
		return nil, err
	}
	defer testInstance.CloseWithTimeout(cancel, 2*time.Second, log.Println)

	outboundTags := in.OutboundTags
	if in.UseDefaultOutbound {
		outbound := testInstance.Outbound().Default()
		outboundTags = []string{outbound.Tag()}
	}

	if err := validateOutboundTags(outboundTags); err != nil {
		return out, err
	}
	maxConcurrency := clampMaxConcurrency(in.MaxConcurrency)
	timeout := time.Duration(in.TestTimeoutMs) * time.Millisecond
	results := BatchIPTest(currentTestCtx(), testInstance, outboundTags, int(maxConcurrency), timeout)

	res := make([]*gen.IPTestResp, 0, len(results))
	for idx, data := range results {
		errStr := ""
		if data.Error != nil {
			errStr = data.Error.Error()
		}
		tag := outboundTags[idx]

		res = append(res, &gen.IPTestResp{
			OutboundTag: (tag),
			IP:          (data.Result.IP),
			CountryCode: (data.Result.CountryCode),
			Error:       (errStr),
		})
	}
	out.Results = res
	return out, nil
}

// prefer the requested tags, the default one follows route.final
func resolveLiveTags(i *boxbox.Box, tags []string) []string {
	outbounds := service.FromContext[adapter.OutboundManager](i.Context())
	known := make([]string, 0, len(tags))
	for _, tag := range tags {
		if _, found := outbounds.Outbound(tag); found {
			known = append(known, tag)
		}
	}
	if len(known) == 0 {
		return []string{i.Outbound().Default().Tag()}
	}
	return known
}

func (s *server) Test(ctx context.Context, in *gen.TestReq) (*gen.TestResp, error) {
	out := new(gen.TestResp)
	var testInstance *boxbox.Box
	var cancel context.CancelFunc
	var err error
	var twice = true
	if in.TestCurrent {
		testInstance = getInstance()
		if testInstance == nil {
			out.Results = []*gen.URLTestResp{{
				OutboundTag: ("proxy"),
				LatencyMs:   (int32(0)),
				Error:       ("Instance is not running"),
			}}
			return out, nil
		}
	} else {
		testInstance, cancel, err = boxmain.Create([]byte(in.Config))
		if err != nil {
			return out, err
		}
		defer cancel()
		defer testInstance.Close()
	}

	outboundTags := in.OutboundTags
	if in.TestCurrent {
		outboundTags = resolveLiveTags(testInstance, in.OutboundTags)
	} else if in.UseDefaultOutbound {
		outbound := testInstance.Outbound().Default()
		outboundTags = []string{outbound.Tag()}
	}

	var maxConcurrency = clampMaxConcurrency(in.MaxConcurrency)
	if err := validateOutboundTags(outboundTags); err != nil {
		return out, err
	}
	results := BatchURLTest(currentTestCtx(), testInstance, outboundTags, in.URL,
		int(maxConcurrency), twice, time.Duration(in.TestTimeoutMs)*time.Millisecond)

	res := make([]*gen.URLTestResp, 0)
	for idx, data := range results {
		errStr := ""
		if data.Error != nil {
			errStr = data.Error.Error()
		}
		res = append(res, &gen.URLTestResp{
			OutboundTag: (outboundTags[idx]),
			LatencyMs:   (int32(data.Duration.Milliseconds())),
			Error:       (errStr),
		})
	}

	out.Results = res
	return out, nil
}

func (s *server) StopTest(ctx context.Context, in *gen.EmptyReq) (*gen.EmptyResp, error) {
	out := new(gen.EmptyResp)

	resetTestScope()

	return out, nil
}

func (s *server) QueryURLTest(ctx context.Context, in *gen.EmptyReq) (*gen.QueryURLTestResponse, error) {
	out := new(gen.QueryURLTestResponse)
	results := URLReporter.Results()
	for _, r := range results {
		errStr := ""
		if r.Error != nil {
			errStr = r.Error.Error()
		}
		out.Results = append(out.Results, &gen.URLTestResp{
			OutboundTag: (r.Tag),
			LatencyMs:   (int32(r.Duration.Milliseconds())),
			Error:       (errStr),
		})
	}
	return out, nil
}

// QueryStats aggregates lifetime totals per outbound tag, including
// closed connections (counters keep accumulating after close). For a
// snapshot of what is connected right now, see ListConnections.
func (s *server) QueryStats(ctx context.Context, in *gen.EmptyReq) (*gen.QueryStatsResp, error) {
	out := new(gen.QueryStatsResp)
	out.Ups = make(map[string]int64)
	out.Downs = make(map[string]int64)
	if bi := getInstance(); bi != nil {
		trafficManager := bi.TrafficManager()
		if trafficManager != nil {
			for _, c := range trafficManager.Connections() {
				out.Ups[c.Outbound] += c.Upload.Load()
				out.Downs[c.Outbound] += c.Download.Load()
			}
			for _, c := range trafficManager.ClosedConnections() {
				out.Ups[c.Outbound] += c.Upload.Load()
				out.Downs[c.Outbound] += c.Download.Load()
			}
		}
	}
	return out, nil
}

func destHostPort(dest string) (string, string) {
	if strings.HasPrefix(dest, "[") {
		end := strings.Index(dest, "]:")
		if end > 1 {
			return dest[1:end], dest[end+2:]
		}
		end = strings.Index(dest, "]")
		if end > 1 {
			return dest[1:end], ""
		}
		return dest, ""
	}
	if host, port, err := net.SplitHostPort(dest); err == nil {
		return host, port
	}
	return dest, ""
}

func isUninformativeConnection(process, dest, protocol, outbound string) bool {
	if isNekoboxCoreConnection(process, outbound) {
		return true
	}
	host, port := destHostPort(dest)
	if host == "172.19.0.2" || host == "fdfe:dcba:9876::2" {
		return true
	}
	dnsPort := port == "53" || port == "853"
	if dnsPort && (host == "172.19.0.1" || host == "fdfe:dcba:9876::1") {
		return true
	}
	if process != "" {
		return false
	}
	if strings.EqualFold(protocol, "dns") {
		return true
	}
	return dnsPort
}

func isNekoboxCoreConnection(process, outbound string) bool {
	normalize := func(raw string) string {
		base := strings.ToLower(filepath.Base(raw))
		base = strings.TrimSuffix(base, ".exe")
		base = strings.ReplaceAll(base, "-", "_")
		return base
	}
	p := normalize(process)
	o := normalize(outbound)
	return p == "nekobox_core" || p == "nekoboxcore" ||
		o == "nekobox_core" || o == "nekoboxcore"
}

// maxListConnections caps a ListConnections response. The GUI renders
// the whole list, so an unbounded busy host could produce arbitrarily
// large RPC responses; the newest entries win and Truncated is set.
// Unlike QueryStats (lifetime totals incl. closed), this is a snapshot
// of active connections right now.
const maxListConnections = 500

// capConnections keeps the newest limit entries by CreatedAt (UnixMilli)
// and reports whether anything was dropped.
func capConnections(res []*gen.ConnectionMetaData, limit int) ([]*gen.ConnectionMetaData, bool) {
	if len(res) <= limit {
		return res, false
	}
	sort.Slice(res, func(i, j int) bool { return res[i].GetCreatedAt() > res[j].GetCreatedAt() })
	return res[:limit], true
}

func (s *server) ListConnections(ctx context.Context, in *gen.EmptyReq) (*gen.ListConnectionsResp, error) {
	out := new(gen.ListConnectionsResp)
	if internal.BoxInstance == nil {
		return out, nil
	}
	trafficManager := internal.BoxInstance.TrafficManager()
	if trafficManager == nil {
		return out, errors.New("no traffic manager found")
	}
	connections := trafficManager.Connections()

	res := make([]*gen.ConnectionMetaData, 0)
	for _, c := range connections {
		process := ""
		if c.Metadata.ProcessInfo != nil {
			// filepath.Base handles both / and \ so a Qt-style path still
			// yields just the executable name on Windows.
			process = filepath.Base(c.Metadata.ProcessInfo.ProcessPath)
		}
		if isUninformativeConnection(process, c.Metadata.Destination.String(),
			c.Metadata.Protocol, c.Outbound) {
			continue
		}
		r := &gen.ConnectionMetaData{
			ID:        (c.ID.String()),
			CreatedAt: (c.CreatedAt.UnixMilli()),
			Upload:    (c.Upload.Load()),
			Download:  (c.Download.Load()),
			Outbound:  (c.Outbound),
			Network:   (c.Metadata.Network),
			Dest:      (c.Metadata.Destination.String()),
			Protocol:  (c.Metadata.Protocol),
			Domain:    (c.Metadata.Domain),
			Process:   (process),
		}
		res = append(res, r)
	}
	capped, truncated := capConnections(res, maxListConnections)
	if truncated {
		log.Printf("[ListConnections] %d active, returning newest %d", len(res), maxListConnections)
	}
	out.Connections = capped
	out.Truncated = truncated
	return out, nil
}

func (s *server) SpeedTest(ctx context.Context, in *gen.SpeedTestRequest) (*gen.SpeedTestResponse, error) {
	out := new(gen.SpeedTestResponse)
	if !in.TestDownload && !in.TestUpload && !in.SimpleDownload && !in.OnlyCountry {
		return out, errors.New("cannot run empty test")
	}
	var testInstance *boxbox.Box
	var cancel context.CancelFunc
	outboundTags := in.OutboundTags
	var err error
	if in.TestCurrent {
		testInstance = getInstance()
		if testInstance == nil {
			out.Results = []*gen.SpeedTestResult_{{
				OutboundTag: ("proxy"),
				Error:       ("Instance is not running"),
			}}
			return out, nil
		}
	} else {
		testInstance, cancel, err = boxmain.Create([]byte(in.Config))
		if err != nil {
			return out, err
		}
		defer cancel()
		defer testInstance.Close()
	}

	if in.TestCurrent {
		outboundTags = resolveLiveTags(testInstance, in.OutboundTags)
	} else if in.UseDefaultOutbound {
		outbound := testInstance.Outbound().Default()
		outboundTags = []string{outbound.Tag()}
	}

	if err := validateOutboundTags(outboundTags); err != nil {
		return out, err
	}
	results := BatchSpeedTest(currentTestCtx(), testInstance, outboundTags,
		in.TestDownload, in.TestUpload, in.SimpleDownload,
		in.SimpleDownloadAddr, time.Duration(in.TimeoutMs)*time.Millisecond,
		in.OnlyCountry, in.CountryConcurrency)

	res := make([]*gen.SpeedTestResult_, 0)
	for _, data := range results {
		errStr := ""
		if data.Error != nil {
			errStr = data.Error.Error()
		}
		res = append(res, &gen.SpeedTestResult_{
			DlSpeed:       (data.DlSpeed),
			UlSpeed:       (data.UlSpeed),
			Latency:       (data.Latency),
			OutboundTag:   (data.Tag),
			Error:         (errStr),
			ServerName:    (data.ServerName),
			ServerCountry: (data.ServerCountry),
			Cancelled:     (data.Cancelled),
		})
	}

	out.Results = res
	return out, nil
}

func (s *server) QuerySpeedTest(ctx context.Context, in *gen.EmptyReq) (*gen.QuerySpeedTestResponse, error) {
	out := new(gen.QuerySpeedTestResponse)
	res, isRunning := SpTQuerier.Result()
	errStr := ""
	if res.Error != nil {
		errStr = res.Error.Error()
	}
	out.Result_ = &gen.SpeedTestResult_{
		DlSpeed:       (res.DlSpeed),
		UlSpeed:       (res.UlSpeed),
		Latency:       (res.Latency),
		OutboundTag:   (res.Tag),
		Error:         (errStr),
		ServerName:    (res.ServerName),
		ServerCountry: (res.ServerCountry),
		Cancelled:     (res.Cancelled),
	}
	out.IsRunning = (isRunning)
	return out, nil
}

func (s *server) GenWgKeyPair(ctx context.Context, _ *gen.EmptyReq) (out *gen.GenWgKeyPairResponse, _ error) {
	res := new(gen.GenWgKeyPairResponse)
	privateKey, err := wg.GeneratePrivateKey()
	if err != nil {
		res.Error = err.Error()
		return res, nil
	}
	res.PrivateKey = privateKey.String()
	res.PublicKey = privateKey.PublicKey().String()
	return res, nil
}

func (s *server) QueryCountryTest(ctx context.Context, in *gen.EmptyReq) (*gen.QueryCountryTestResponse, error) {
	out := new(gen.QueryCountryTestResponse)
	results := CountryResults.Results()
	for _, res := range results {
		var errStr string
		if res.Error != nil {
			errStr = res.Error.Error()
		}
		out.Results = append(out.Results, &gen.SpeedTestResult_{
			DlSpeed:       (res.DlSpeed),
			UlSpeed:       (res.UlSpeed),
			Latency:       (res.Latency),
			OutboundTag:   (res.Tag),
			Error:         (errStr),
			ServerName:    (res.ServerName),
			ServerCountry: (res.ServerCountry),
			Cancelled:     (res.Cancelled),
		})
	}
	return out, nil
}
