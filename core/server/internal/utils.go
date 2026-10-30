package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"nekobox_core/internal/boxbox"
	"nekobox_core/internal/process"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"net"

	"github.com/sagernet/sing-box/common/settings"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/metadata"
)

const (
	Gb = 1000 * Mb
	Mb = 1000 * Kb
	Kb = 1000
)

var ruleset_cachedir string

var BoxInstance *boxbox.Box
var ExtraProcess *process.Process
var NeedUnsetDNS bool

var SystemProxyController settings.SystemProxy
var SystemProxyAddr string
var SystemProxyPort uint16
var SystemProxySupportSOCKS bool

// newSystemProxy constructs the OS proxy controller. It is a variable
// (not a direct call) so unit tests can stub construction failures
// (BUG-003). The wrapper keeps the concrete per-OS return type out of
// our signature.
var newSystemProxy = func(ctx context.Context, addr metadata.Socksaddr, supportSOCKS bool, bypass []string) (settings.SystemProxy, error) {
	return settings.NewSystemProxy(ctx, addr, supportSOCKS, bypass)
}

var InstanceCancel context.CancelFunc
var Debug bool

func IsSystemProxyEnabled() bool {
	return SystemProxyController != nil && SystemProxyController.IsEnabled()
}

func ResetSystemProxy() error {
	if IsSystemProxyEnabled() {
		return SystemProxyController.Disable()
	}
	return nil
}

func SetSystemProxy(ctx context.Context, serverAddr string, serverPort uint16, supportSOCKS bool) error {
	if BoxInstance == nil {
		log.Print("[SetSystemProxy] warning: no running instance; refusing to report success")
		return fmt.Errorf("no running instance")
	}
	if serverAddr == SystemProxyAddr && serverPort == SystemProxyPort && supportSOCKS == SystemProxySupportSOCKS {
		if IsSystemProxyEnabled() {
			return nil
		}
	} else {
		ResetSystemProxy()
	}
	SystemProxyAddr = serverAddr
	SystemProxyPort = serverPort
	SystemProxySupportSOCKS = supportSOCKS
	addr := metadata.ParseSocksaddrHostPort(serverAddr, serverPort)
	ctl, err := newSystemProxy(ctx, addr, supportSOCKS, nil)
	if err != nil {
		return err
	}
	if ctl == nil {
		return errors.New("system proxy controller unavailable")
	}
	SystemProxyController = ctl
	return SystemProxyController.Enable()
}

func SetRulesetCachedir(v string) bool {
	ruleset_cachedir = v
	return true
}

func BoxCreateHttpClient(instance *boxbox.Box) *http.Client {
	if instance == nil {
		return &http.Client{}
	}
	outbound := instance.Outbound().Default()
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
				return outbound.DialContext(ctx, "tcp", metadata.ParseSocksaddr(addr))
			},
		},
	}
	return client
}

func DownloadFile(originalURL, targetPath string, use_default_outbound bool) error {
	// Create all necessary directories for the target path
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directories: %v", err)
	}
	// Open the file for writing
	outFile, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer outFile.Close()
	// Download the file
	var client *http.Client

	if use_default_outbound {
		goto def_cli
	} else {
		if BoxInstance == nil {
			goto def_cli
		} else {
			client = BoxCreateHttpClient(BoxInstance)
			goto skip_def_cli
		}
	}
def_cli:

	client = &http.Client{}
skip_def_cli:

	parsedURL, err := url.Parse(originalURL)
	if err != nil {
		return fmt.Errorf("error parsing URL: %v", err)
	}

	// Extract credentials (if available)
	var username, password string
	if parsedURL.User != nil {
		username = parsedURL.User.Username()
		password, _ = parsedURL.User.Password()
		// Remove the credentials from the URL (we don't need them in the URL anymore)
		parsedURL.User = nil
	}

	// Rebuild the cleaned URL without credentials
	cleanedURL := parsedURL.String()

	// Create a new GET request with the cleaned URL
	req, err := http.NewRequest("GET", cleanedURL, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}

	// If credentials were found, set Basic Authentication header
	if username != "" && password != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download file: %v", err)
	}
	defer resp.Body.Close()
	// Copy the downloaded content to the file
	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save file: %v", err)
	}
	return nil
}

func urlToPath(url string) (string, error) {
	url = strings.Replace(url, ":/", "/", 1)
	url = strings.ReplaceAll(url, "_", "_0_")
	url = strings.ReplaceAll(url, ":", "_1_")
	url = strings.ReplaceAll(url, "@", "_2_")
	url = strings.ReplaceAll(url, "?", "_3_")
	url = strings.ReplaceAll(url, "=", "_4_")
	url = strings.ReplaceAll(url, "&", "_5_")
	url = strings.ReplaceAll(url, "\"", "_6_")
	url = strings.ReplaceAll(url, "'", "_7_")
	url = strings.ReplaceAll(url, "*", "_8_")
	url = filepath.Clean(filepath.FromSlash(url))
	resolved := filepath.Clean(filepath.Join(ruleset_cachedir, url))
	// Reject `..` escapes: the resolved path must stay inside the
	// ruleset cache dir.
	base := filepath.Clean(ruleset_cachedir)
	if base != "" && base != "." {
		if resolved != base && !strings.HasPrefix(resolved, base+string(os.PathSeparator)) {
			return "", fmt.Errorf("url escapes ruleset cache dir: %q", url)
		}
	}
	return resolved, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// CacheHttpBool downloads url into the ruleset cache dir (when missing)
// and reports the on-disk path. A download failure is returned as an
// error — it must never kill the core (the old log.Fatalf took down the
// whole process from an RPC-reachable path on transient network errors).
func CacheHttpBool(url string, use_default_outbound bool, s *bool) (string, error) {
	if url == "" {
		if s != nil {
			*s = false
		}
		return "", nil
	}
	path, err := urlToPath(url)
	if err != nil {
		return "", err
	}
	if !fileExists(path) {
		log.Printf("Downloading %s %s", url, func() string {
			if use_default_outbound {
				return "with proxy"
			}
			return "without proxy"
		}())
		if err := DownloadFile(url, path, use_default_outbound); err != nil {
			return "", fmt.Errorf("download %s: %w", url, err)
		}
		if s != nil {
			*s = true
		}
	} else if s != nil {
		log.Printf("Cached %s", url)
		*s = false
	}
	return path, nil
}

func CacheHttp(url string, use_default_outbound bool) (string, error) {
	return CacheHttpBool(url, use_default_outbound, nil)
}

func cacheRuleSet(url string, format string, tag badoption.Listable[string]) (option.RuleSet, error) {
	var ruleset option.RuleSet
	ruleset.Tag = tag
	ruleset.Type = C.RuleSetTypeLocal
	ruleset.Format = format
	path, err := CacheHttp(url, false)
	if err != nil {
		return ruleset, err
	}
	ruleset.LocalOptions.Path = path
	return ruleset, nil
}

func ClearRulesets() {
	paths := []string{
		"ftps",
		"ftp",
		"http",
		"https",
	}

	for _, path := range paths {
		os.RemoveAll(filepath.Clean(filepath.Join(ruleset_cachedir, path)))
	}
}

func ModifyRulesets(opt *option.Options) error {
	if ruleset_cachedir == "" {
		return nil
	}
	if opt.Route != nil {
		for u, i := range opt.Route.RuleSet {
			if i.Type == C.RuleSetTypeRemote {
				url := i.RemoteOptions.URL
				cached, err := cacheRuleSet(url, i.Format, i.Tag)
				if err != nil {
					return err
				}
				opt.Route.RuleSet[u] = cached
			}
		}
	}
	return nil
}

func GetRulesetCachedir() string {
	return ruleset_cachedir
}

func BrateToStr(brate float64) string {
	brate *= 8
	if brate >= Gb {
		return fmt.Sprintf("%.2f%s", brate/Gb, "Gbps")
	}
	if brate >= Mb {
		return fmt.Sprintf("%.2f%s", brate/Mb, "Mbps")
	}
	return fmt.Sprintf("%.2f%s", brate/Kb, "Kbps")
}

func CalculateBRate(bytes float64, startTime time.Time) float64 {
	elapsed := time.Since(startTime).Seconds()
	return bytes / elapsed
}
