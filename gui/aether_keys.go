package main

// WARP key renewal, ported from PattN's AetherKeysWindow and
// AetherIdentityManager. The core registers identities and exits
// (--register); new keys are gathered in a renewal folder and moved over
// the ones in use only once every file of the kind is there, so a failed
// or cancelled run leaves the working keys alone.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// aetherKeysSettings are the WARP keys card's own settings. No profile's
// settings reach them and they reach no profile (PattN AetherKeysSettings).
type aetherKeysSettings struct {
	// all | wg | masque | gool | mim — the kinds --register takes on
	// aether 2.3.0. gool-classic is a protocol, not a registration.
	Kind string `json:"kind,omitempty"`
	// EnrollAddress is --enroll-address: where the calls to the WARP API
	// go. This core has no --api-sni/--api-host; the server name and the
	// HTTP host stay api.cloudflareclient.com.
	EnrollAddress string `json:"enrollAddress,omitempty"`
	Fragment      bool   `json:"fragment,omitempty"`
	FragmentSize  string `json:"fragmentSize,omitempty"`
	FragmentDelay string `json:"fragmentDelay,omitempty"`
	Ech           bool   `json:"ech,omitempty"`
	EchDNS        string `json:"echDns,omitempty"`
	EchDomain     string `json:"echDomain,omitempty"`
	Fingerprint   string `json:"fingerprint,omitempty"`
	// Carrier reaches the API through Tor or Psiphon instead of directly
	// (--tor-reverse / --psiphon-reverse, which this core applies to the
	// registrations whatever the protocol).
	Carrier string `json:"carrier,omitempty"`
	// Command is blank unless it was edited by hand, and then it runs as
	// written.
	Command string `json:"command,omitempty"`
}

// AetherKeyInfo is one key file of the identity folder and the identity
// it holds. Only the device and the addresses WARP gave it are read: the
// private key, certificate and access token in the same file are never
// read, logged or handed to the GUI.
type AetherKeyInfo struct {
	File     string `json:"file"`
	Label    string `json:"label"`
	Present  bool   `json:"present"`
	DeviceID string `json:"deviceId,omitempty"`
	IPv4     string `json:"ipv4,omitempty"`
	IPv6     string `json:"ipv6,omitempty"`
}

// AetherKeysStatus is the live renewal progress the WARP keys card polls.
type AetherKeysStatus struct {
	Running     bool   `json:"running"`
	Kind        string `json:"kind,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	LastErrorAt string `json:"lastErrorAt,omitempty"`
	LastOKAt    string `json:"lastOkAt,omitempty"`
}

// PattN's RenewTimeout and RenewThroughCarrierTimeout: a carrier has to
// come up before the API calls can ride it.
const (
	aetherKeysTimeout        = 4 * time.Minute
	aetherKeysCarrierTimeout = 10 * time.Minute
)

// The key files aether 2.3.0 writes, measured against the shipped binary.
// It writes no base aether.toml and no aether-masque-gool.toml, so a list
// taken from a newer fork would wait for a file that never comes.
const (
	aetherKeyWgFile          = "aether-wg.toml"
	aetherKeyWgInnerFile     = "aether-wg-secondary.toml"
	aetherKeyMasqueFile      = "aether-masque.toml"
	aetherKeyMasqueInnerFile = "aether-masque-secondary.toml"
	aetherKeysReadyFile      = "ready"
)

var aetherKeyKinds = []string{"all", "wg", "masque", "gool", "mim"}

var aetherKeyFiles = []struct{ name, label string }{
	{aetherKeyWgFile, "WireGuard"},
	{aetherKeyWgInnerFile, "WireGuard inner"},
	{aetherKeyMasqueFile, "MASQUE"},
	{aetherKeyMasqueInnerFile, "MASQUE inner"},
}

var (
	aetherKeysReady    = regexp.MustCompile(`identities ready: \S`)
	aetherKeyField     = regexp.MustCompile(`^(device_id|ipv4|ipv6)\s*=\s*"([^"]*)"\s*$`)
	aetherKeyMultiline = regexp.MustCompile(`"""|'''`)
)

// aetherKeysFilesOf names the files a --register of [kind] writes.
func aetherKeysFilesOf(kind string) []string {
	switch aetherEnumDefault(aetherKeyKinds, kind, "all") {
	case "wg":
		return []string{aetherKeyWgFile}
	case "masque":
		return []string{aetherKeyMasqueFile}
	case "gool":
		return []string{aetherKeyWgFile, aetherKeyWgInnerFile}
	case "mim":
		return []string{aetherKeyMasqueFile, aetherKeyMasqueInnerFile}
	default:
		out := make([]string, 0, len(aetherKeyFiles))
		for _, f := range aetherKeyFiles {
			out = append(out, f.name)
		}
		return out
	}
}

// buildAetherKeysArguments renders the registration run of [s]. Only the
// flags aether 2.3.0 takes are named: PattN's --api-address/--api-sni/
// --api-host belong to a newer fork and would fail the whole run here.
func buildAetherKeysArguments(s aetherKeysSettings) []string {
	args := []string{"--register", aetherEnumDefault(aetherKeyKinds, s.Kind, "all")}
	if v, ok := aetherSettingValue(s.EnrollAddress); ok {
		args = append(args, "--enroll-address", v)
	}
	if s.Fragment {
		args = append(args, "--fragment")
		if r, ok := aetherRangeParse(s.FragmentSize, aetherFragmentSizeBounds); ok {
			args = append(args, "--fragment-size", r.String())
		}
		if r, ok := aetherRangeParse(s.FragmentDelay, aetherFragmentDelayBounds); ok {
			args = append(args, "--fragment-delay", r.String())
		}
	}
	if s.Ech {
		args = append(args, "--ech", "auto")
		if v, ok := aetherSettingValue(s.EchDNS); ok {
			args = append(args, "--ech-dns", v)
		} else {
			args = append(args, "--ech-dns", aetherECHDNS)
		}
		if v, ok := aetherSettingValue(s.EchDomain); ok {
			args = append(args, "--ech-domain", v)
		} else {
			args = append(args, "--ech-domain", aetherECHDomain)
		}
	}
	args = append(args, aetherFingerprintArguments(aetherFingerprintDefault(s.Fingerprint))...)
	switch s.Carrier {
	case "tor":
		args = append(args, "--tor-reverse")
	case "psiphon":
		args = append(args, "--psiphon-reverse")
	}
	return args
}

// aetherKeysProblem says why [s] cannot run, or nil when it can. A
// hand-written command is only checked for what it registers: the
// settings it replaces do not count, and the core names what else it
// does not take (PattN AetherKeys.Problem).
func aetherKeysProblem(s aetherKeysSettings) error {
	if strings.TrimSpace(s.Command) != "" {
		args, _, ok := aetherCoreOfCommand(s.Command)
		if !ok || !containsStr(args, "--register") {
			return fmt.Errorf("the command must register keys: it needs --register")
		}
		kind, _ := aetherValueAfter(args, "--register")
		if !containsStr(aetherKeyKinds, kind) {
			return fmt.Errorf("--register needs one of: all, wg, masque, gool, mim")
		}
		return nil
	}
	if v := strings.TrimSpace(s.EnrollAddress); v != "" && !aetherKeysEnrollOK(v) {
		return fmt.Errorf("API address %q is not an address the core can dial: a name or IP, alone or with :port", v)
	}
	if s.Fragment {
		if v := strings.TrimSpace(s.FragmentSize); v != "" {
			if _, ok := aetherRangeParse(v, aetherFragmentSizeBounds); !ok {
				return fmt.Errorf("fragment size %q is outside %d-%d", v, aetherFragmentSizeBounds.min, aetherFragmentSizeBounds.max)
			}
		}
		if v := strings.TrimSpace(s.FragmentDelay); v != "" {
			if _, ok := aetherRangeParse(v, aetherFragmentDelayBounds); !ok {
				return fmt.Errorf("fragment delay %q is outside %d-%d", v, aetherFragmentDelayBounds.min, aetherFragmentDelayBounds.max)
			}
		}
	}
	if s.Ech {
		if v := strings.TrimSpace(s.EchDNS); v != "" && !aetherIsECHDNS(v) {
			return fmt.Errorf("ECH DNS resolver %q is not a resolver the core takes", v)
		}
		if v := strings.TrimSpace(s.EchDomain); v != "" && !aetherIsECHDomain(v) {
			return fmt.Errorf("ECH domain %q is not a domain name", v)
		}
	}
	return nil
}

// aetherKeysEnrollOK reads an address as --enroll-address takes it: a
// name or IP, alone or followed by :port, an IPv6 one in brackets.
func aetherKeysEnrollOK(v string) bool {
	if aetherKeysEnrollHostOK(v) {
		return true
	}
	sep := strings.LastIndex(v, ":")
	if sep <= 0 {
		return false
	}
	host := v[:sep]
	if strings.Contains(host, ":") && !(strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")) {
		return false
	}
	port := v[sep+1:]
	if len(port) < 1 || len(port) > 5 || strings.Trim(port, "0123456789") != "" {
		return false
	}
	n := parsePortInt(port)
	return n >= 1 && n <= 65535 && aetherKeysEnrollHostOK(host)
}

// aetherKeysEnrollHostOK is the host half: an IP address or a domain
// name, never something that could read as an option (PattN
// AetherKeys.IsEnrollHost).
func aetherKeysEnrollHostOK(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if _, ok := aetherNormalizeAddress(host); ok {
		return true
	}
	return aetherIsECHDomain(host)
}

func aetherKeysReadyLine(line string) bool { return aetherKeysReady.MatchString(line) }

// aetherKeysRunArguments returns the arguments a run starts the core
// with: the hand-written command's, or the settings'.
func aetherKeysRunArguments(s aetherKeysSettings) ([]string, bool) {
	if cmd := strings.TrimSpace(s.Command); cmd != "" {
		if args, _, ok := aetherCoreOfCommand(cmd); ok && containsStr(args, "--register") {
			return args, true
		}
	}
	return buildAetherKeysArguments(s), false
}

// aetherKeysTimeoutOf is how long a run may take to say its keys are
// saved: longer when a carrier has to come up first.
func aetherKeysTimeoutOf(args []string) time.Duration {
	if containsStr(args, "--tor-reverse") || containsStr(args, "--psiphon-reverse") ||
		containsStr(args, "--tor-only") || containsStr(args, "--psiphon-only") {
		return aetherKeysCarrierTimeout
	}
	return aetherKeysTimeout
}

// aetherKeyRead reads the device and the addresses out of one key file.
// The secret fields beside them are never looked at: a triple-quoted
// block is skipped whole so nothing inside one can read as a field.
func aetherKeyRead(path string) (AetherKeyInfo, bool) {
	info := AetherKeyInfo{File: filepath.Base(path)}
	for _, f := range aetherKeyFiles {
		if f.name == info.File {
			info.Label = f.label
			break
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return info, false
	}
	defer f.Close()
	inBlock := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if inBlock {
			if aetherKeyMultiline.MatchString(line) {
				inBlock = false
			}
			continue
		}
		if m := aetherKeyField.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "device_id":
				info.DeviceID = m[2]
			case "ipv4":
				info.IPv4 = m[2]
			case "ipv6":
				info.IPv6 = m[2]
			}
			continue
		}
		// A value opening a block on its own line hides the lines after it.
		if aetherKeyMultiline.MatchString(line) {
			inBlock = true
		}
	}
	if info.DeviceID == "" {
		return info, false
	}
	info.Present = true
	return info, true
}

// aetherKeysRenewalDir is where a run gathers new keys, beside the folder
// holding the ones in use.
func aetherKeysRenewalDir() string {
	dir := filepath.Join(filepath.Dir(aetherWorkDir()), "aether-renewal")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// aetherKeysMarkReady records that every file the run was asked for is
// staged and reads as an identity. Only a marked renewal is settled.
func aetherKeysMarkReady(renewalDir string) error {
	return os.WriteFile(filepath.Join(renewalDir, aetherKeysReadyFile), []byte("ready\n"), 0o600)
}

// aetherKeysSettle moves a marked renewal over the keys in use. Nothing
// moves until every staged key file reads as an identity, so a truncated
// write can never replace a working key; a key of another kind is never
// touched, and a move that fails part way is rolled back. True when the
// new keys took their place.
func aetherKeysSettle(workDir, renewalDir string) bool {
	if _, err := os.Stat(filepath.Join(renewalDir, aetherKeysReadyFile)); err != nil {
		return false
	}
	var staged []string
	for _, f := range aetherKeyFiles {
		p := filepath.Join(renewalDir, f.name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if _, ok := aetherKeyRead(p); !ok {
			return false
		}
		staged = append(staged, f.name)
	}
	if len(staged) == 0 {
		return false
	}
	// Keep staged files until every replacement succeeds so failed installs can be retried.
	var moved, aside []string
	undo := func() {
		for _, name := range moved {
			_ = os.Remove(filepath.Join(workDir, name))
		}
		for _, name := range aside {
			_ = os.Rename(aetherKeyPreviousPath(workDir, name), filepath.Join(workDir, name))
		}
	}
	for _, name := range staged {
		live := filepath.Join(workDir, name)
		if _, err := os.Lstat(live); err == nil {
			if err := os.Rename(live, aetherKeyPreviousPath(workDir, name)); err != nil {
				undo()
				return false
			}
			aside = append(aside, name)
		}
		if err := aetherKeyCopy(filepath.Join(renewalDir, name), live); err != nil {
			undo()
			return false
		}
		moved = append(moved, name)
	}
	for _, name := range aside {
		_ = os.Remove(aetherKeyPreviousPath(workDir, name))
	}
	_ = os.RemoveAll(renewalDir)
	return true
}

// aetherKeyPreviousPath is where a key in use waits while its replacement
// is written. Beside it, so the move is one rename on one volume.
func aetherKeyPreviousPath(workDir, name string) string {
	return filepath.Join(workDir, name+".previous")
}

// aetherKeyCopy writes a staged identity into place. These files hold the
// device's private key, so the copy is owner-only like the file it replaces.
func aetherKeyCopy(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// aetherKeysList reads the identities the folder holds, settling a
// renewal that finished but could not move every file first.
func aetherKeysList(workDir, renewalDir string) []AetherKeyInfo {
	aetherKeysSettle(workDir, renewalDir)
	out := make([]AetherKeyInfo, 0, len(aetherKeyFiles))
	for _, f := range aetherKeyFiles {
		info, _ := aetherKeyRead(filepath.Join(workDir, f.name))
		info.File, info.Label = f.name, f.label
		out = append(out, info)
	}
	return out
}

// AetherKeysList reports the WARP identities NekoGo holds, one row per
// key file, present or not.
func (a *App) AetherKeysList() ([]AetherKeyInfo, error) {
	return aetherKeysList(aetherWorkDir(), aetherKeysRenewalDir()), nil
}

// AetherKeysProgress reports the live renewal progress.
func (a *App) AetherKeysProgress() (AetherKeysStatus, error) {
	a.aetherKeysStatusMu.Lock()
	defer a.aetherKeysStatusMu.Unlock()
	return a.aetherKeysStatus, nil
}

func (a *App) setAetherKeysStatus(s AetherKeysStatus) {
	a.aetherKeysStatusMu.Lock()
	a.aetherKeysStatus = s
	a.aetherKeysStatusMu.Unlock()
}

// AetherKeysPreview renders the registration line [s] builds, for the
// card's command box. The hand-written command is left out: the box
// compares against this to tell one from the other.
func (a *App) AetherKeysPreview(s aetherKeysSettings) string {
	return aetherCommandLine(buildAetherKeysArguments(s))
}

// SaveAetherKeysSettings persists the WARP keys card's settings.
func (a *App) SaveAetherKeysSettings(s aetherKeysSettings) error {
	if a.store == nil {
		return fmt.Errorf("config store is not configured")
	}
	return a.store.UpdateAetherKeys(func(aetherKeysSettings) aetherKeysSettings { return s })
}

// AetherCancelKeys aborts the renewal in flight, if any.
func (a *App) AetherCancelKeys() error {
	a.aetherKeysMu.Lock()
	cancel := a.aetherKeysCancel
	a.aetherKeysMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// aetherKeysRunning reports whether a renewal holds the one slot.
func (a *App) aetherKeysRunning() bool {
	a.aetherKeysMu.Lock()
	defer a.aetherKeysMu.Unlock()
	return a.aetherKeysCancel != nil
}

// AetherRenewKeys registers new WARP keys of the kind [s] asks for and
// puts them in place of the ones in use, once every file of that kind is
// there. Until then the working keys are untouched, so a failure, a
// cancel or the app being killed leaves them in use.
func (a *App) AetherRenewKeys(s aetherKeysSettings) ([]AetherKeyInfo, error) {
	if a.store == nil || a.aetherMgr == nil {
		return nil, fmt.Errorf("aether is not configured")
	}
	if err := aetherKeysProblem(s); err != nil {
		return nil, err
	}
	if a.aetherMgr.Running() {
		return nil, fmt.Errorf("disconnect the running Aether tunnel before getting new keys")
	}
	if a.aetherScanRunning() {
		return nil, fmt.Errorf("an Aether scan is running; wait for it to finish before getting new keys")
	}
	args, _ := aetherKeysRunArguments(s)
	kind, _ := aetherValueAfter(args, "--register")
	ctx, cancel, err := a.aetherKeysBegin(aetherKeysTimeoutOf(args))
	if err != nil {
		return nil, err
	}
	defer a.aetherKeysEnd(cancel)

	startedAt := time.Now().UTC().Format(time.RFC3339)
	a.setAetherKeysStatus(AetherKeysStatus{Running: true, Kind: kind, StartedAt: startedAt})
	workDir, renewalDir := aetherWorkDir(), aetherKeysRenewalDir()
	files := aetherKeysFilesOf(kind)
	err = a.aetherKeysRenew(ctx, s, workDir, renewalDir, files)
	status := AetherKeysStatus{Running: false, Kind: kind, StartedAt: startedAt}
	if err != nil {
		status.LastError = err.Error()
		status.LastErrorAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		status.LastOKAt = time.Now().UTC().Format(time.RFC3339)
	}
	a.setAetherKeysStatus(status)
	if err != nil {
		return nil, err
	}
	return aetherKeysList(workDir, renewalDir), nil
}

func (a *App) aetherKeysBegin(timeout time.Duration) (context.Context, context.CancelFunc, error) {
	a.aetherKeysMu.Lock()
	defer a.aetherKeysMu.Unlock()
	if a.aetherKeysCancel != nil {
		return nil, nil, fmt.Errorf("a WARP key run is already going")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	a.aetherKeysCancel = cancel
	return ctx, cancel, nil
}

func (a *App) aetherKeysEnd(cancel context.CancelFunc) {
	cancel()
	a.aetherKeysMu.Lock()
	a.aetherKeysCancel = nil
	a.aetherKeysMu.Unlock()
}

// aetherKeysRenew clears the renewal folder, runs the core into it, and
// settles the keys only once every one of [files] is there.
func (a *App) aetherKeysRenew(ctx context.Context, s aetherKeysSettings, workDir, renewalDir string, files []string) error {
	// A renewal that finished but could not move every file is settled
	// first; what an earlier run left behind is no key anyone uses.
	aetherKeysSettle(workDir, renewalDir)
	if err := os.RemoveAll(renewalDir); err != nil {
		return fmt.Errorf("clear the renewal folder: %w", err)
	}
	if err := os.MkdirAll(renewalDir, 0o755); err != nil {
		return fmt.Errorf("make the renewal folder: %w", err)
	}
	if err := a.aetherKeysRun(ctx, s, renewalDir); err != nil {
		return err
	}
	for _, name := range files {
		if _, ok := aetherKeyRead(filepath.Join(renewalDir, name)); !ok {
			return fmt.Errorf("the core did not write %s; the keys in use stay as they were", name)
		}
	}
	if err := aetherKeysMarkReady(renewalDir); err != nil {
		return fmt.Errorf("mark the new keys ready: %w", err)
	}
	if !aetherKeysSettle(workDir, renewalDir) {
		return fmt.Errorf("the new keys are ready but could not be moved into place; the keys in use stay as they were")
	}
	a.emitLog("aether-key", "info", "new WARP keys in place: "+strings.Join(files, ", "))
	return nil
}

// aetherKeysRun runs the core once into [dir] and waits for its word that
// the keys are saved, or for it to exit. The core exits on its own after
// registering; the word ends the wait earlier when a carrier it started
// keeps the process alive.
func (a *App) aetherKeysRun(ctx context.Context, s aetherKeysSettings, dir string) error {
	exe, err := a.aetherMgr.resolveAetherExe(a.store.GetSettings())
	if err != nil {
		return err
	}
	args, _ := aetherKeysRunArguments(s)
	// The success word is an info line; a quieter level would hide it.
	args = append(append([]string{}, args...), "--log-level", "info")

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	cmd := exec.CommandContext(runCtx, exe, args...)
	cmd.Dir = dir
	cmd.Env = aetherEnv(dir)
	cmd.WaitDelay = 5 * time.Second
	setHideWindow(cmd)

	found := make(chan *AetherScanResult, 1)
	writer := &aetherScanWriter{
		// The scan writer's signal is all this run needs: the keys are
		// read from the files afterwards, not from the line.
		match: func(line string) *AetherScanResult {
			if aetherKeysReadyLine(line) {
				return &AetherScanResult{}
			}
			return nil
		},
		log:   func(level, msg string) { a.emitLog("aether-key", level, msg) },
		found: found,
	}
	cmd.Stdout = writer
	cmd.Stderr = writer

	job := newProcJob()
	if err := cmd.Start(); err != nil {
		job.close()
		return fmt.Errorf("start the WARP key run: %w", err)
	}
	job.attach(cmd.Process.Pid)
	stopJob := context.AfterFunc(runCtx, job.kill)
	defer stopJob()
	defer job.close()

	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		job.kill()
		done <- err
	}()

	select {
	case <-found:
		runCancel()
		<-done
		return nil
	case err := <-done:
		writer.flush()
		select {
		case <-found:
			return nil
		default:
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return aetherKeysContextError(ctx)
		}
		if err != nil {
			return fmt.Errorf("the WARP key run ended without saving keys (%w); the keys in use stay as they were", err)
		}
		return fmt.Errorf("the WARP key run ended without saying its keys were saved; the keys in use stay as they were")
	case <-ctx.Done():
		<-done
		return aetherKeysContextError(ctx)
	}
}

func aetherKeysContextError(ctx context.Context) error {
	switch ctx.Err() {
	case context.Canceled:
		return fmt.Errorf("getting new WARP keys was cancelled; the keys in use stay as they were")
	case context.DeadlineExceeded:
		return fmt.Errorf("getting new WARP keys timed out; the keys in use stay as they were")
	default:
		return fmt.Errorf("the WARP key run ended without saving keys")
	}
}
