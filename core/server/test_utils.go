//go:build !darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"nekobox_core/internal"
	"nekobox_core/internal/boxbox"
	"net"
	"net/http"
	"sync"
	"time"

	"nekobox_core/speedtest"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

var testCtx context.Context
var cancelTests context.CancelFunc

// testMu guards the shared test scope (BUG-014). The Thrift server
// (TSimpleServer4) already serves each connection in its own goroutine,
// so a StopTest arriving on a FRESH connection (as the GUI sends it)
// runs concurrently with a long Test/SpeedTest — but StopTest used to
// reassign testCtx/cancelTests lock-free while handlers read the
// globals, racing and wedging cancellation. All access now goes
// through currentTestCtx/resetTestScope.
var testMu sync.Mutex

// currentTestCtx snapshots the live shared test scope under lock.
// Handlers take ONE snapshot per request so StopTest cleanly aborts
// the in-flight batch loops (they select on ctx.Done()).
func currentTestCtx() context.Context {
	testMu.Lock()
	defer testMu.Unlock()
	if testCtx == nil {
		testCtx, cancelTests = context.WithCancel(context.Background())
	}
	return testCtx
}

// resetTestScope cancels the in-flight tests and starts a fresh scope.
// This is the StopTest path.
func resetTestScope() {
	testMu.Lock()
	defer testMu.Unlock()
	if cancelTests != nil {
		cancelTests()
	}
	testCtx, cancelTests = context.WithCancel(context.Background())
}
var SpTQuerier SpeedTestResultQuerier
var URLReporter URLTestReporter
var CountryResults CountryTestResults

const URLTestTimeout = 3 * time.Second
const FetchServersTimeout = 8 * time.Second
const MaxConcurrentTests = 100

// maxOutboundTags caps the per-request tag list (BUG-015): unbounded
// tag lists fan out one goroutine each (wg.Add(len(outboundTags))) and
// append one reporter entry per tag per test, so adversarial input
// grows memory/CPU without bound. Requests above the cap are rejected
// by validateOutboundTags before any box is created or goroutine spans.
const maxOutboundTags = 256

// maxReporterResults bounds every test reporter buffer (BUG-015): past
// the cap the oldest entries are dropped, so loops without Query drain
// at a constant size instead of growing forever.
const maxReporterResults = 10000

// validateOutboundTags rejects adversarial per-request tag lists. The
// Test/IPTest/SpeedTest RPC handlers call it after tag resolution and
// before creating instances or fanning out goroutines.
func validateOutboundTags(tags []string) error {
	if len(tags) > maxOutboundTags {
		return fmt.Errorf("too many outbound tags: %d (max %d)", len(tags), maxOutboundTags)
	}
	return nil
}

// maxSimpleDownloadBytes caps the BUG-011 speed-test download at 32MB:
// the old code buffered the entire payload in RAM with no Close, so a
// large file grew the heap toward the 1.5GB watchdog on every run.
const maxSimpleDownloadBytes = 32 << 20

// clampMaxConcurrency maps out-of-range (including negative) RPC
// concurrency values onto the safe default. A negative value would
// otherwise reach make(chan struct{}, n) and panic the core (BUG-006).
func clampMaxConcurrency(mc int32) int32 {
	if mc <= 0 || mc > MaxConcurrentTests {
		return MaxConcurrentTests
	}
	return mc
}

type URLTestResult struct {
	Duration time.Duration
	Tag      string
	Error    error
}

type URLTestReporter struct {
	results []*URLTestResult
	mu      sync.Mutex
}

func (u *URLTestReporter) AddResult(result *URLTestResult) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.results = append(u.results, result)
	if len(u.results) > maxReporterResults {
		u.results = append([]*URLTestResult(nil), u.results[len(u.results)-maxReporterResults:]...)
	}
}

func (u *URLTestReporter) Results() []*URLTestResult {
	u.mu.Lock()
	defer u.mu.Unlock()
	res := u.results
	u.results = nil
	return res
}

type SpeedTestResult struct {
	Tag           string
	DlSpeed       string
	UlSpeed       string
	Latency       int32
	ServerName    string
	ServerCountry string
	Error         error
	Cancelled     bool
}

type SpeedTestResultQuerier struct {
	isRunning bool
	current   SpeedTestResult
	mu        sync.RWMutex
}

func (s *SpeedTestResultQuerier) Result() (SpeedTestResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current, s.isRunning
}

func (s *SpeedTestResultQuerier) storeResult(result *SpeedTestResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = *result
}

func (s *SpeedTestResultQuerier) setIsRunning(isRunning bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isRunning = isRunning
}

type CountryTestResults struct {
	results []*SpeedTestResult
	mu      sync.Mutex
}

func (c *CountryTestResults) AddResult(result *SpeedTestResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results = append(c.results, result)
	if len(c.results) > maxReporterResults {
		c.results = append([]*SpeedTestResult(nil), c.results[len(c.results)-maxReporterResults:]...)
	}
}

func (c *CountryTestResults) Results() []*SpeedTestResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := c.results
	c.results = nil
	return cp
}

// acquireSlot takes a limiter slot but gives up when ctx ends (BUG-016):
// the old blocking send hung until a slot freed (up to a full probe
// timeout each), delaying abort far beyond the test timeout.
func acquireSlot(ctx context.Context, limiter chan struct{}) error {
	select {
	case limiter <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func BatchURLTest(ctx context.Context, i *boxbox.Box, outboundTags []string, url string,
	maxConcurrency int, twice bool, timeout time.Duration) []*URLTestResult {
	if timeout <= 0 {
		timeout = URLTestTimeout
	}
	if maxConcurrency <= 0 || maxConcurrency > MaxConcurrentTests {
		maxConcurrency = MaxConcurrentTests
	}
	outbounds := service.FromContext[adapter.OutboundManager](i.Context())
	resMap := make(map[string]*URLTestResult)
	resAccess := sync.Mutex{}
	limiter := make(chan struct{}, maxConcurrency)

	wg := &sync.WaitGroup{}
	wg.Add(len(outboundTags))
	for _, tag := range outboundTags {
		select {
		case <-ctx.Done():
			wg.Done()
			resAccess.Lock()
			resMap[tag] = &URLTestResult{
				Duration: 0,
				Error:    errors.New("test aborted"),
			}
			resAccess.Unlock()
		default:
			time.Sleep(2 * time.Millisecond) // don't spawn goroutines too quickly
			if err := acquireSlot(ctx, limiter); err != nil {
				wg.Done()
				resAccess.Lock()
				resMap[tag] = &URLTestResult{
					Duration: 0,
					Error:    err,
				}
				resAccess.Unlock()
				continue
			}
			go func(t string) {
				defer wg.Done()
				outbound, found := outbounds.Outbound(t)
				if !found {
					resAccess.Lock()
					u := &URLTestResult{
						Tag:   t,
						Error: errors.New("no outbound with tag " + t + " found"),
					}
					resMap[t] = u
					URLReporter.AddResult(u)
					resAccess.Unlock()
					<-limiter
					return
				}
				client := &http.Client{
					Transport: &http.Transport{
						DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
							return outbound.DialContext(ctx, "tcp", metadata.ParseSocksaddr(addr))
						},
					},
					Timeout: timeout,
				}
				// to properly measure muxed configs, let's do the test twice
				if twice {
					req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
					if err == nil {
						resp, err := client.Do(req)
						if err == nil {
							resp.Body.Close()
						}
					}
				}
				duration, err := urlTest(ctx, client, url)
				resAccess.Lock()
				u := &URLTestResult{
					Duration: duration,
					Tag:      t,
					Error:    err,
				}
				resMap[t] = u
				URLReporter.AddResult(u)
				resAccess.Unlock()
				<-limiter
			}(tag)
		}
	}

	wg.Wait()
	res := make([]*URLTestResult, 0, len(outboundTags))
	for _, tag := range outboundTags {
		res = append(res, resMap[tag])
	}

	return res
}

func urlTest(ctx context.Context, client *http.Client, url string) (time.Duration, error) {
	begin := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return time.Since(begin), nil
}

func getNetDialer(dialer func(ctx context.Context, network string, destination metadata.Socksaddr) (net.Conn, error)) func(ctx context.Context, network string, address string) (net.Conn, error) {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		return dialer(ctx, network, metadata.ParseSocksaddr(address))
	}
}

func BatchSpeedTest(ctx context.Context, i *boxbox.Box, outboundTags []string, testDl, testUl bool, simpleDL bool, simpleAddress string, timeout time.Duration, countryOnly bool, countryConcurrency int32) []*SpeedTestResult {
	outbounds := service.FromContext[adapter.OutboundManager](i.Context())
	results := make([]*SpeedTestResult, 0)
	var queuer chan struct{}
	wg := &sync.WaitGroup{}
	if countryOnly {
		if countryConcurrency <= 0 {
			countryConcurrency = 5
		}
		queuer = make(chan struct{}, countryConcurrency)
	}

loop_tags:
	for _, tag := range outboundTags {
		select {
		case <-ctx.Done():
			break loop_tags
		default:
		}
		res := new(SpeedTestResult)
		res.Tag = tag
		results = append(results, res)

		outbound, exists := outbounds.Outbound(tag)
		if !exists {
			res.Error = errors.New("no outbound with tag " + tag + " found")
			continue
		}

		var err error
		if countryOnly {
			queuer <- struct{}{}
			wg.Add(1)
			go func(res *SpeedTestResult, outbound adapter.Outbound) {
				defer func() { <-queuer }()
				defer wg.Done()
				err := countryTest(ctx, getNetDialer(outbound.DialContext), res)
				if err != nil && !errors.Is(err, context.Canceled) {
					res.Error = err
					fmt.Println("Failed to countryTest with err:", err)
				}
				CountryResults.AddResult(res)
			}(res, outbound)
			continue
		}
		if simpleDL {
			err = simpleDownloadTest(ctx, getNetDialer(outbound.DialContext), res, simpleAddress, timeout)
		} else {
			err = speedTestWithDialer(ctx, getNetDialer(outbound.DialContext), res, testDl, testUl, timeout)
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			res.Error = err
			fmt.Println("Failed to speedtest with err:", err)
		}
		if !testDl && !simpleDL {
			res.DlSpeed = ""
		}
		if !testUl {
			res.UlSpeed = ""
		}
	}
	wg.Wait()

	return results
}

func simpleDownloadTest(ctx context.Context, dialer func(ctx context.Context, network string, address string) (net.Conn, error), res *SpeedTestResult, testURL string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = URLTestTimeout
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
				return dialer(ctx, network, addr)
			},
		},
		Timeout: timeout,
	}

	res.ServerName = "N/A"
	res.ServerCountry = "N/A"

	buf := bytes.NewBuffer(make([]byte, 0, 8*(1<<20)))
	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return err
	}

	done := make(chan struct{})
	var start time.Time
	var latency int32

	go func() {
		defer close(done)
		reqStart := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			res.Error = err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			res.Error = fmt.Errorf("unexpected HTTP status: %s", resp.Status)
			return
		}
		latency = int32(time.Since(reqStart).Milliseconds())
		start = time.Now()
		if _, err := io.Copy(buf, io.LimitReader(resp.Body, maxSimpleDownloadBytes)); err != nil {
			res.Error = err
			return
		}
	}()

	ticker := time.NewTicker(time.Millisecond * 50)
	defer ticker.Stop()

	SpTQuerier.setIsRunning(true)
	defer SpTQuerier.setIsRunning(false)

	for {
		select {
		case <-done:
			if res.Error != nil {
				return res.Error
			}
			res.DlSpeed = internal.BrateToStr(internal.CalculateBRate(float64(buf.Len()), start))
			res.Latency = latency
			SpTQuerier.storeResult(res)
			return nil
		case <-ctx.Done():
			res.Cancelled = true
			return ctx.Err()
		case <-ticker.C:
			res.DlSpeed = internal.BrateToStr(internal.CalculateBRate(float64(buf.Len()), start))
			res.Latency = latency
			SpTQuerier.storeResult(res)
		}
	}
}

func getSpeedtestServer(ctx context.Context, dialer func(ctx context.Context, network string, address string) (net.Conn, error)) (*speedtest.Server, error) {
	clt := speedtest.New(speedtest.WithUserConfig(&speedtest.UserConfig{
		DialContextFunc: dialer,
		PingMode:        speedtest.HTTP,
		MaxConnections:  8,
	}))
	fetchCtx, cancel := context.WithTimeout(ctx, FetchServersTimeout)
	defer cancel()
	srv, err := clt.FetchServerListContext(fetchCtx)
	if err != nil {
		return nil, err
	}
	srv, err = srv.FindServer(nil)
	if err != nil {
		return nil, err
	}

	if srv.Len() == 0 {
		return nil, errors.New("no server found for speedTest")
	}

	return srv[0], nil
}

func countryTest(ctx context.Context, dialer func(ctx context.Context, network string, address string) (net.Conn, error), res *SpeedTestResult) error {
	srv, err := getSpeedtestServer(ctx, dialer)
	if err != nil {
		return err
	}
	res.ServerName = srv.Name
	res.ServerCountry = srv.Country
	res.Latency = int32(srv.Latency.Milliseconds())
	return nil
}

func speedTestWithDialer(ctx context.Context, dialer func(ctx context.Context, network string, address string) (net.Conn, error), res *SpeedTestResult, testDl, testUl bool, timeout time.Duration) error {
	srv, err := getSpeedtestServer(ctx, dialer)
	if err != nil {
		return err
	}
	res.ServerName = srv.Name
	res.ServerCountry = srv.Country

	done := make(chan struct{})

	SpTQuerier.setIsRunning(true)
	defer SpTQuerier.setIsRunning(false)

	go func() {
		defer func() { close(done) }()
		if testDl {
			timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			err = srv.DownloadTestContext(timeoutCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				res.Error = err
				return
			}
		}
		if testUl {
			timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			err = srv.UploadTestContext(timeoutCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				res.Error = err
				return
			}
		}
	}()

	ticker := time.NewTicker(time.Millisecond * 50)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			res.DlSpeed = internal.BrateToStr(float64(srv.DLSpeed))
			res.UlSpeed = internal.BrateToStr(float64(srv.ULSpeed))
			res.Latency = int32(srv.Latency.Milliseconds())
			SpTQuerier.storeResult(res)
			return nil
		case <-ctx.Done():
			res.Cancelled = true
			return ctx.Err()
		case <-ticker.C:
			res.DlSpeed = internal.BrateToStr(srv.Context.GetEWMADownloadRate())
			res.UlSpeed = internal.BrateToStr(srv.Context.GetEWMAUploadRate())
			res.Latency = int32(srv.Latency.Milliseconds())
			SpTQuerier.storeResult(res)
		}
	}
}

type IPInfo struct {
	IP          string `json:"ip"`
	CountryCode string `json:"country_code"`
}

var IPReporter IPTestReporter

const IPTestTimeout = 3 * time.Second
const ipInfoAPI = "https://api.ip2location.io/"

type IPTestResult struct {
	Result IPInfo
	Tag    string
	Error  error
}

type IPTestReporter struct {
	results []*IPTestResult
	mu      sync.Mutex
}

func (u *IPTestReporter) AddResult(result *IPTestResult) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.results = append(u.results, result)
	if len(u.results) > maxReporterResults {
		u.results = append([]*IPTestResult(nil), u.results[len(u.results)-maxReporterResults:]...)
	}
}

func (u *IPTestReporter) Results() []*IPTestResult {
	u.mu.Lock()
	defer u.mu.Unlock()
	res := u.results
	u.results = nil
	return res
}

func BatchIPTest(ctx context.Context, i *boxbox.Box, outboundTags []string, maxConcurrency int, timeout time.Duration) []*IPTestResult {
	if timeout <= 0 {
		timeout = IPTestTimeout
	}
	if maxConcurrency <= 0 || maxConcurrency > MaxConcurrentTests {
		maxConcurrency = MaxConcurrentTests
	}
	outbounds := service.FromContext[adapter.OutboundManager](i.Context())
	resMap := make(map[string]*IPTestResult)
	resAccess := sync.Mutex{}
	limiter := make(chan struct{}, maxConcurrency)

	wg := &sync.WaitGroup{}
	wg.Add(len(outboundTags))
	for _, tag := range outboundTags {
		select {
		case <-ctx.Done():
			wg.Done()
			resAccess.Lock()
			resMap[tag] = &IPTestResult{
				Error: errors.New("test aborted"),
			}
			resAccess.Unlock()
		default:
			time.Sleep(2 * time.Millisecond) // don't spawn goroutines too quickly
			if err := acquireSlot(ctx, limiter); err != nil {
				wg.Done()
				resAccess.Lock()
				resMap[tag] = &IPTestResult{
					Error: err,
				}
				resAccess.Unlock()
				continue
			}
			go func(t string) {
				defer wg.Done()
				outbound, found := outbounds.Outbound(t)
				if !found {
					resAccess.Lock()
					u := &IPTestResult{
						Tag:   t,
						Error: errors.New("no outbound with tag " + t + " found"),
					}
					resMap[t] = u
					IPReporter.AddResult(u)
					resAccess.Unlock()
					<-limiter
					return
				}
				client := &http.Client{
					Transport: &http.Transport{
						DialContext: func(_ context.Context, network string, addr string) (net.Conn, error) {
							return outbound.DialContext(ctx, "tcp", metadata.ParseSocksaddr(addr))
						},
					},
					Timeout: timeout,
				}
				resp, err := ipTest(ctx, client)
				resAccess.Lock()
				u := &IPTestResult{
					Result: resp,
					Tag:    t,
					Error:  err,
				}
				resMap[t] = u
				IPReporter.AddResult(u)
				resAccess.Unlock()
				<-limiter
			}(tag)
		}
	}

	wg.Wait()
	res := make([]*IPTestResult, 0, len(outboundTags))
	for _, tag := range outboundTags {
		res = append(res, resMap[tag])
	}

	return res
}

func ipTest(ctx context.Context, client *http.Client) (IPInfo, error) {
	var res IPInfo
	req, err := http.NewRequestWithContext(ctx, "GET", ipInfoAPI, nil)
	if err != nil {
		return res, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return res, err
	}
	err = json.Unmarshal(data, &res)
	if err != nil {
		return res, err
	}
	return res, nil
}
