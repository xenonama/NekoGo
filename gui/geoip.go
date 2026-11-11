package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// geoCountryRecord mirrors the GeoLite2-Country schema we read.
type geoCountryRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
}

// GeoIP wraps a MaxMind GeoLite2-Country reader. A missing DB is not an
// error — every lookup degrades to ("", "", nil).
type GeoIP struct {
	mu      sync.RWMutex
	db      *maxminddb.Reader
	dbPath  string
	lastErr error
	cfgPath string

	negMu    sync.Mutex
	negCache map[string]time.Time // negative DNS cache: host -> failure time
}

const negCacheTTL = 60 * time.Second
const dnsTimeout = 5 * time.Second

// NewGeoIP creates a detector. path is the settings override (empty =
// auto-resolve). Resolution is deferred to first use so tests and fresh
// installs never need the file present at construction.
func NewGeoIP(path string) *GeoIP {
	return &GeoIP{cfgPath: path, negCache: map[string]time.Time{}}
}

// Configure swaps the settings override path and drops any opened reader;
// the DB is re-resolved on next use.
func (g *GeoIP) Configure(path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cfgPath == path && g.db != nil {
		return
	}
	g.cfgPath = path
	g.closeLocked()
	g.dbPath = ""
	g.lastErr = nil
}

func (g *GeoIP) closeLocked() {
	if g.db != nil {
		_ = g.db.Close()
		g.db = nil
	}
}

// resolveDBPath tries, in order:
//  1. settings override (if non-empty and exists)
//  2. the most recently modified of the shipped/dev copies:
//     <exe dir>/bin/, <exe dir>/, <exe dir>/assets/, ./gui/assets/, ./assets/
func resolveDBPath(cfgPath string) string {
	if cfgPath != "" {
		if _, err := os.Stat(cfgPath); err == nil {
			return cfgPath
		}
	}
	exe, _ := os.Executable()
	candidates := []string{}
	if exe != "" {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, binDirName, "GeoLite2-Country.mmdb"),
			filepath.Join(dir, "GeoLite2-Country.mmdb"),
			filepath.Join(dir, "assets", "GeoLite2-Country.mmdb"),
		)
	}
	candidates = append(candidates,
		filepath.Join("gui", "assets", "GeoLite2-Country.mmdb"),
		filepath.Join("assets", "GeoLite2-Country.mmdb"),
	)
	return newestExisting(candidates)
}

// newestExisting returns the most recently modified regular file among
// candidates ("" when none exists). The shipped layouts have ONE copy in
// bin\ (next to the engines) and the updater rewrites that exact file, so
// the mtime rule only matters for legacy folders that still carry the old
// copy next to the exe: the freshest one wins instead of a fixed order.
func newestExisting(candidates []string) string {
	best := ""
	var bestTime time.Time
	for _, c := range candidates {
		fi, err := os.Stat(c)
		if err != nil || fi.IsDir() {
			continue
		}
		if best == "" || fi.ModTime().After(bestTime) {
			best, bestTime = c, fi.ModTime()
		}
	}
	return best
}

// ensure opens the DB on first use (and retries after a failure, so a
// file dropped in later is picked up without a restart).
func (g *GeoIP) ensure() {
	g.mu.RLock()
	if g.db != nil {
		g.mu.RUnlock()
		return
	}
	g.mu.RUnlock()

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.db != nil {
		return
	}
	path := resolveDBPath(g.cfgPath)
	if path == "" {
		g.lastErr = errGeoIPNoDB
		return
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		g.lastErr = err
		return
	}
	g.db = db
	g.dbPath = path
	g.lastErr = nil
}

// Lookup resolves an address (IP literal or hostname) to an ISO-3166
// alpha-2 country code + English name. Returns ("", "", nil) if the DB is
// missing, the address is private/loopback, or the country is unknown.
// Returns an error only for actual DB read failures.
func (g *GeoIP) Lookup(host string) (code, name string, err error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", nil
	}
	// IP literals: filter private space before touching the DB, so
	// loopback/private lookups never cost filesystem stats.
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return "", "", nil
		}
		return g.lookupIP(ip)
	}
	// Hostnames: check the DB first so a degraded detector never
	// pays for DNS it cannot use. The RLock is held for the whole
	// lookup (BUG-021): Configure/Reload take the write lock to close
	// the reader, so they block until in-flight lookups finish instead
	// of closing the DB under them. DNS resolution happens under the
	// lock too; it is bounded by dnsTimeout and never nests g.mu.
	g.ensure()
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.db == nil {
		return "", "", nil
	}
	ip, resErr := g.resolvePublicIP(host)
	if resErr != nil {
		return "", "", nil // unresolvable -> unknown, not a failure
	}
	if ip == nil {
		return "", "", nil
	}
	var rec geoCountryRecord
	if err := g.db.Lookup(ip, &rec); err != nil {
		return "", "", err
	}
	return splitRecord(rec)
}

// lookupIP runs a DB lookup for an already-vetted public IP literal,
// holding the RLock for the whole read (BUG-021, same as Lookup).
func (g *GeoIP) lookupIP(ip net.IP) (string, string, error) {
	g.ensure()
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.db == nil {
		return "", "", nil
	}
	var rec geoCountryRecord
	if err := g.db.Lookup(ip, &rec); err != nil {
		return "", "", err
	}
	return splitRecord(rec)
}

// splitRecord extracts the upper-cased code + English name, mapping
// unknown or malformed countries to ("", "", nil). A corrupt/customized
// DB can return a non-ISO string (e.g. "GOOGLE"); only a two-letter
// alpha-2 code is accepted so garbage never reaches the UI.
func splitRecord(rec geoCountryRecord) (string, string, error) {
	code := strings.ToUpper(strings.TrimSpace(rec.Country.ISOCode))
	if !validCountryCode(code) {
		return "", "", nil
	}
	return code, rec.Country.Names["en"], nil
}

// validCountryCode reports whether code is an ISO-3166 alpha-2 shape:
// exactly two ASCII letters (already upper-cased by the caller).
func validCountryCode(code string) bool {
	if len(code) != 2 {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 'A' || code[i] > 'Z' {
			return false
		}
	}
	return true
}

// isPublicIP rejects loopback, private, link-local and multicast space:
// no country exists for those.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if ip.IsPrivate() {
		return false
	}
	return true
}

// resolvePublicIP resolves a hostname with a 5s cap and returns the first
// public IPv4, else the first public IPv6. Negative results are cached for
// 60s to avoid hammering DNS.
func (g *GeoIP) resolvePublicIP(host string) (net.IP, error) {
	g.negMu.Lock()
	if t, ok := g.negCache[host]; ok && time.Since(t) < negCacheTTL {
		g.negMu.Unlock()
		return nil, fmt.Errorf("recent DNS failure for %q", host)
	}
	g.negMu.Unlock()

	type result struct {
		ips []net.IP
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ips, err := net.LookupIP(host)
		ch <- result{ips, err}
	}()
	var ips []net.IP
	select {
	case r := <-ch:
		if r.err != nil {
			g.rememberNegative(host)
			return nil, r.err
		}
		ips = r.ips
	case <-time.After(dnsTimeout):
		g.rememberNegative(host)
		return nil, fmt.Errorf("DNS timeout for %q", host)
	}
	var firstV6 net.IP
	for _, ip := range ips {
		if !isPublicIP(ip) {
			continue
		}
		if ip.To4() != nil {
			return ip, nil
		}
		if firstV6 == nil {
			firstV6 = ip
		}
	}
	if firstV6 == nil {
		g.rememberNegative(host)
	}
	return firstV6, nil
}

func (g *GeoIP) rememberNegative(host string) {
	g.negMu.Lock()
	g.negCache[host] = time.Now()
	// Opportunistic prune so the map cannot grow unboundedly.
	for h, t := range g.negCache {
		if time.Since(t) >= negCacheTTL {
			delete(g.negCache, h)
		}
	}
	g.negMu.Unlock()
}

// Reload drops the opened reader and re-resolves (used after the updater
// swapped the file, or the user dropped in a new DB). A still-missing DB is
// not an error. An existing file that will not open is: the detector keeps
// answering "unknown country" from then on with nothing left to retry, so
// the caller that just replaced it has to hear about it.
func (g *GeoIP) Reload() error {
	g.mu.Lock()
	g.closeLocked()
	g.dbPath = ""
	g.lastErr = nil
	g.mu.Unlock()
	g.ensure()
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.db == nil && !errors.Is(g.lastErr, errGeoIPNoDB) {
		return g.lastErr
	}
	return nil
}

// errGeoIPNoDB marks the benign absence of a database, as opposed to one
// that exists and cannot be opened.
var errGeoIPNoDB = errors.New("GeoLite2-Country.mmdb not found")

// validateMMDBBytes reports whether data is a database the reader can
// actually open. The updater fetches GeoIP copies that publish no sha256,
// so this is the only check standing between a truncated download, an HTML
// error page or an LFS pointer and the file every country lookup reads.
func validateMMDBBytes(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty database")
	}
	db, err := maxminddb.FromBytes(data)
	if err != nil {
		return fmt.Errorf("not an openable MaxMind database: %w", err)
	}
	return db.Close()
}

// Available reports whether a usable DB is loaded.
func (g *GeoIP) Available() bool {
	g.ensure()
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.db != nil
}

// DBPath returns the resolved path (may be empty).
func (g *GeoIP) DBPath() string {
	g.ensure()
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.dbPath
}

// LastErr returns the last resolution/open error, if any.
func (g *GeoIP) LastErr() error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.lastErr
}
