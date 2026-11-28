package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// fileConfig is the on-disk JSON document: profiles + settings.
type fileConfig struct {
	Profiles []Profile `json:"profiles"`
	Settings Settings  `json:"settings"`
}

// legacyProcDoc reads only the old auxiliary-process entries, so a
// config saved before that subsystem was removed still contributes its
// custom aether.exe path to Settings.AetherPath.
type legacyProcDoc struct {
	Settings struct {
		Entries []struct {
			Kind    string `json:"kind"`
			ExePath string `json:"exePath"`
		} `json:"externals"`
	} `json:"settings"`
}

// migrateLegacyAetherPath copies the old "aether" auxiliary exe path
// into the dedicated setting. An explicit AetherPath always wins.
func migrateLegacyAetherPath(s *Settings, raw []byte) {
	if strings.TrimSpace(s.AetherPath) != "" {
		return
	}
	var doc legacyProcDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	for _, ext := range doc.Settings.Entries {
		if ext.Kind != "aether" {
			continue
		}
		s.AetherPath = strings.TrimSpace(ext.ExePath)
		return
	}
}

// migrateCorePathSplit separates the two engine binaries' settings. A
// config written before the split could point corePath at the official
// sing-box.exe (CoreEngine=upstream); that value moves to
// UpstreamCorePath so the custom core never resolves it, and corePath is
// cleared. In-memory only — persisted on the next save, like
// seedSNIDefaults.
func migrateCorePathSplit(s *Settings) {
	p := strings.TrimSpace(s.CorePath)
	if p == "" || !isOfficialSingBoxName(p) {
		return
	}
	if strings.TrimSpace(s.UpstreamCorePath) == "" {
		s.UpstreamCorePath = p
	}
	s.CorePath = ""
}

// migrateEmptyCoreOverrides drops stored section overrides that hold
// nothing ({} or []). mergeCoreOverrides skips those, so this is not what
// keeps a connect alive — it is about not leaving a lie in the file: the
// section editor reads a stored {} back as an override and shows it as
// though the user had configured something. In-memory only; persisted on
// the next save, like migrateCorePathSplit.
func migrateEmptyCoreOverrides(s *Settings) bool {
	changed := false
	for sec, raw := range s.CoreOverrides {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		if isEmptyOverride(v) {
			delete(s.CoreOverrides, sec)
			changed = true
		}
	}
	return changed
}

// migrateDNSServers drops saved DNS upstreams the current parser
// rejects. A value written before the plain-text shapes were accepted
// (or by an older, looser validator) would otherwise be persisted
// forever and fail every Connect with the same error, with no way for
// the user to see which field caused it. Reset means "system default",
// which always starts. Returns true when anything changed.
func migrateDNSServers(s *Settings) bool {
	changed := false
	for _, side := range []struct {
		name  string
		value *string
	}{{"DnsRemote", &s.DnsRemote}, {"DnsDirect", &s.DnsDirect}} {
		v := strings.TrimSpace(*side.value)
		*side.value = v
		if v == "" {
			continue
		}
		if err := validateDNSServer(v); err != nil {
			log.Printf("[config] invalid %s %q, resetting to empty", side.name, v)
			*side.value = ""
			changed = true
		}
	}
	return changed
}

// ConfigStore loads/saves profiles and settings as JSON. It replaces the
// old Qt/RocksDB datastore (src/nekobox/dataStore) which required C++.
type ConfigStore struct {
	mu                    sync.RWMutex
	dir                   string
	path                  string
	statePath             string
	profiles              []Profile
	settings              Settings
	statePresent          bool
	stateDirty            bool
	stateMigrationPending bool
	migrationState        configState
	stateDelay            time.Duration
	stateMaxDelay         time.Duration
	stateTimer            *time.Timer
	stateFirstDirty       time.Time
	stateNext             time.Time
	stateRetry            int
	stateFailureLogged    bool
}

// NewConfigStore opens (or creates) the config directory and loads
// config.json if present. The SNI-NekoBox one-time migration (P3-8)
// runs only for the default location (dir == ""): explicit dirs
// (tests, portable mode) are never seeded from the old path.
func NewConfigStore(dir string) (*ConfigStore, error) {
	useDefault := dir == ""
	if useDefault {
		d, err := defaultConfigDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	// One-time migration from the pre-rename location (P3-8): if the new
	// config does not exist but %APPDATA%/SNI-NekoBox/config.json does,
	// copy it over (rewriting stale path references). The old file is
	// left in place; when both exist the new one wins silently.
	if useDefault {
		if oldDir, err := oldConfigDir(); err == nil && oldDir != dir {
			if _, err := migrateFromOldAppDataDir(dir, oldDir); err != nil {
				log.Printf("[config] migration from SNI-NekoBox failed: %v", err)
			}
		}
	}
	c := &ConfigStore{
		dir:           dir,
		path:          filepath.Join(dir, "config.json"),
		statePath:     filepath.Join(dir, "state.json"),
		stateDelay:    defaultStateWriteDelay,
		stateMaxDelay: defaultStateWriteMax,
	}
	c.settings = DefaultSettings()
	loaded := false
	if data, err := readConfigBytes(c.path); err == nil {
		loaded = true
		var fc fileConfig
		if err := json.Unmarshal(data, &fc); err != nil {
			return nil, fmt.Errorf("parse config.json: %w", err)
		}
		c.profiles = fc.Profiles
		// Keep every saved field. The old `MixedPort != 0` guard discarded
		// the WHOLE settings block when that one number was missing or zero
		// (feeds, core paths, SNI lists, window prefs) and the next save
		// persisted the loss; only the port itself is repaired here.
		c.settings = fc.Settings
		c.loadRuntimeStateLocked(fc)
		themeMigrated := c.settings.Theme == ""
		if themeMigrated {
			c.settings.Theme = DefaultSettings().Theme
		}
		if c.settings.MixedPort == 0 {
			c.settings.MixedPort = DefaultSettings().MixedPort
		}
		migrateLegacyAetherPath(&c.settings, data)
		migrateCorePathSplit(&c.settings)
		// One-time: a saved DNS upstream the parser rejects would fail
		// every Connect, so it is reset (and the reset written back, or
		// the same error line would print on every launch). Same for an
		// empty section override, which used to delete the rendered block.
		dnsMigrated := migrateDNSServers(&c.settings)
		overridesMigrated := migrateEmptyCoreOverrides(&c.settings)
		if (dnsMigrated || overridesMigrated || themeMigrated) && loaded {
			if err := c.saveConfigLocked(); err != nil {
				log.Printf("[config] settings migration: save failed: %v", err)
			}
		}
		if c.profiles == nil {
			c.profiles = []Profile{}
		}
		defaults := DefaultSettings().SNIConfig
		if c.settings.SNIConfig.Method == "" {
			c.settings.SNIConfig.Method = defaults.Method
		}
		if c.settings.SNIConfig.Fingerprint == "" {
			c.settings.SNIConfig.Fingerprint = defaults.Fingerprint
		}
		if c.settings.SNIConfig.QUICMode == "" {
			c.settings.SNIConfig.QUICMode = defaults.QUICMode
		}
		if c.settings.SNIConfig.IPMode == "" {
			c.settings.SNIConfig.IPMode = defaults.IPMode
		}
		// SNI-tab list migration: configs saved before the curated
		// defaults shipped have empty IP/SNI lists and no pin, which
		// left the SNI tab blank. Seed them in-memory (persisted on
		// the next save); user entries are never touched.
		seedSNIDefaults(&c.settings)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config.json: %w", err)
	} else {
		c.loadRuntimeStateLocked(fileConfig{Profiles: c.profiles, Settings: c.settings})
	}
	// SNI-spoof auto-detect migration (one-time): apply the import
	// rule (loopback + bare-domain engine-eligible profiles get the
	// spoof tag) to configs saved before the rule existed. IP
	// literals are left alone (never auto-tag). An
	// existing file is rewritten so the marker survives the restart
	// even when nothing was reverted — otherwise a later explicit
	// conversion (SNI tab / profile form) would be reverted too.
	// Fresh installs just carry the in-memory marker into their
	// first save. No lock needed: construction, no sharing yet.
	if !c.settings.SpoofAutoDetectReverted {
		// One-time import auto-detect: apply the new rule (loopback and
		// bare-domain engine-eligible profiles get the spoof tag).
		n := migrateSpoofProfiles(c.profiles)
		// Setting the marker is what makes this one-time. It used to be
		// declared but never written, so every launch re-tagged profiles
		// the user had deliberately switched back to the sing-box core.
		c.settings.SpoofAutoDetectReverted = true
		if loaded {
			if err := c.saveConfigLocked(); err != nil {
				log.Printf("[config] spoof auto-detect migration: save failed: %v", err)
			} else if n > 0 {
				log.Printf("[config] spoof auto-detect migration: %d profile(s) tagged spoof", n)
			}
		}
	}
	// TUN stack migration (one-time). NekoGo hardcoded the OS stack until
	// tunStack became a setting, and the first save after that wrote the
	// hardcode into config.json as though it had been chosen. The OS
	// stack terminates TCP inside Windows, which then needs an inbound
	// firewall allow for the core; sing-tun asks for that allow and
	// discards the error, so on a machine without the rule the tunnel
	// carries DNS and times out every TCP dial - it connects and nothing
	// loads. Move the stored value to the stack that needs nothing from
	// the firewall. The marker is what makes this one-time: after it,
	// System and Mixed are honored as deliberate choices.
	if !c.settings.TunOSStackMigrated {
		stored := c.settings.TunStack
		switch stored {
		case "system", "mixed":
			c.settings.TunStack = tunStackDefault
		}
		c.settings.TunOSStackMigrated = true
		if loaded {
			if err := c.saveConfigLocked(); err != nil {
				log.Printf("[config] tun stack migration: save failed: %v", err)
			} else if c.settings.TunStack != stored {
				log.Printf("[config] tun stack migration: stored tunStack %q needs the core allowed"+
					" through the firewall to carry TCP, using %q instead (Settings → Connection → TUN stack)",
					stored, tunStackDefault)
			}
		}
	}
	return c, nil
}

// migrateSpoofProfiles applies the import auto-detect rule to
// already-saved profiles: engine-eligible (spoofEligibilityIssue)
// loopback and bare-domain links get CoreSpoof. IP literals and
// ineligible profiles are untouched. Returns the number changed.
func migrateSpoofProfiles(profiles []Profile) int {
	n := 0
	for i := range profiles {
		p := &profiles[i]
		if p.Core != CoreSingBox {
			continue
		}
		if spoofEligibilityIssue(p) != nil {
			continue
		}
		// One-time import auto-detect rule (per config.json load): any
		// engine-eligible CTF that is loopback or a bare domain (like the
		// prompt's Test-CF) gets the spoof tag so it rides the engine.
		if isLoopbackAddress(p.Address) || isDomainAddress(p.Address) {
			p.Core = CoreSpoof
			n++
		}
	}
	return n
}

// defaultConfigDir returns %APPDATA%/NekoGo on Windows,
// ~/.config/nekogo elsewhere.
func defaultConfigDir() (string, error) {
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, "NekoGo"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "nekogo"), nil
}

// oldConfigDir returns the pre-rename config location
// (%APPDATA%/SNI-NekoBox on Windows, empty elsewhere).
func oldConfigDir() (string, error) {
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, "SNI-NekoBox"), nil
	}
	return "", fmt.Errorf("no legacy config dir on this platform")
}

// migrateFromOldAppDataDir copies oldDir/config.json to newDir/config.json
// when the new file is missing and the old one exists. Stale
// "SNI-NekoBox" path references inside are rewritten to "NekoGo".
// Returns true when a migration happened. The old file is kept.
func migrateFromOldAppDataDir(newDir, oldDir string) (bool, error) {
	newPath := filepath.Join(newDir, "config.json")
	oldPath := filepath.Join(oldDir, "config.json")
	if _, err := os.Stat(newPath); err == nil {
		return false, nil // new config wins silently
	}
	raw, err := os.ReadFile(oldPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	fixed := rewriteLegacyPaths(raw)
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(newPath, []byte(fixed), 0o644); err != nil {
		return false, err
	}
	log.Print("[config] migrated from SNI-NekoBox")
	return true, nil
}

// rewriteLegacyPaths renames the old app-data directory only in settings
// that hold filesystem paths. Credentials, profile names, URLs and raw
// outbound JSON may legitimately contain the same text and must survive
// byte-for-byte. Unparseable JSON is copied verbatim.
func rewriteLegacyPaths(raw []byte) []byte {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(doc["settings"], &settings); err != nil || settings == nil {
		return raw
	}
	changed := false
	for _, key := range []string{
		"corePath", "upstreamCorePath", "xrayPath", "mihomoPath",
		"aetherPath", "torPath", "logOutputPath", "geoIPDBPath",
	} {
		var value string
		if err := json.Unmarshal(settings[key], &value); err != nil {
			continue
		}
		fixed := rewriteLegacyPathValue(value)
		if fixed == value {
			continue
		}
		settings[key], _ = json.Marshal(fixed)
		changed = true
	}
	var externals []map[string]json.RawMessage
	if err := json.Unmarshal(settings["externals"], &externals); err == nil {
		for _, entry := range externals {
			var value string
			if err := json.Unmarshal(entry["exePath"], &value); err != nil {
				continue
			}
			fixed := rewriteLegacyPathValue(value)
			if fixed == value {
				continue
			}
			entry["exePath"], _ = json.Marshal(fixed)
			changed = true
		}
		if changed {
			settings["externals"], _ = json.Marshal(externals)
		}
	}
	if !changed {
		return raw
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return raw
	}
	doc["settings"] = settingsJSON
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

func rewriteLegacyPathValue(s string) string {
	if !strings.Contains(strings.ToLower(s), "sni-nekobox") {
		return s
	}
	if !strings.ContainsAny(s, `\/`) {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "SNI-NekoBox", "NekoGo"), "sni-nekobox", "nekogo")
}

// saveCount counts config.json writes. It is a test seam: the batch
// paths (country refresh) must not write when nothing changed, and
// that is only observable by counting the writes.
var saveCount atomic.Int64

func (c *ConfigStore) saveConfigLocked() error {
	if c.stateMigrationPending {
		if err := c.writeStateValueLocked(c.migrationState); err != nil {
			c.retryStateWriteLocked(err)
			return fmt.Errorf("persist legacy runtime state before rewriting config.json: %w", err)
		}
		c.stateMigrationPending = false
		c.migrationState = configState{}
		c.statePresent = true
		c.stateDirty = true
		c.stateRetry = 0
		c.stateFailureLogged = false
		c.scheduleStateWriteLocked()
	}
	profiles := make([]Profile, len(c.profiles))
	for i, p := range c.profiles {
		profiles[i] = durableProfile(p)
	}
	fc := fileConfig{Profiles: profiles, Settings: durableSettings(c.settings)}
	data, err := json.Marshal(fc)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(c.path, data); err != nil {
		return err
	}
	c.refreshBackupLocked(data)
	saveCount.Add(1)
	return nil
}

func (c *ConfigStore) saveLocked() error {
	return c.saveConfigLocked()
}

// configBackupEvery throttles the .bak refresh: saves happen per profile
// edit, and rewriting the backup every time doubles the IO for no gain.
// A test may lower it to force a write.
var configBackupEvery = time.Minute

// refreshBackupLocked keeps config.json.bak as the last known-good copy so
// a torn write (power loss mid-rename leaves a zero-length config.json)
// costs the user one minute of settings instead of every profile. The
// caller holds c.mu.
func (c *ConfigStore) refreshBackupLocked(data []byte) {
	bak := c.path + ".bak"
	if st, err := os.Stat(bak); err == nil && time.Since(st.ModTime()) < configBackupEvery {
		return
	}
	_ = writeJSONAtomic(bak, data)
}

// readConfigBytes returns the bytes to parse for [path], falling back to
// config.json.bak when the main file is unreadable JSON. A corrupt file is
// moved aside as config.json.corrupt and the backup copied into place, so
// the next save continues from a valid file. A missing main file is
// returned as-is (os.IsNotExist) for the caller's fresh-install path.
func readConfigBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probe fileConfig
	if json.Unmarshal(data, &probe) == nil {
		return data, nil
	}
	bak := path + ".bak"
	bdata, berr := os.ReadFile(bak)
	if berr != nil {
		return nil, fmt.Errorf("parse config.json: %w (no usable config.json.bak)", err)
	}
	var bprobe fileConfig
	if uerr := json.Unmarshal(bdata, &bprobe); uerr != nil {
		return nil, fmt.Errorf("parse config.json: %w (config.json.bak is corrupt too: %v)", err, uerr)
	}
	_ = os.Rename(path, path+".corrupt")
	if werr := os.WriteFile(path, bdata, 0o644); werr != nil {
		return nil, fmt.Errorf("restore config.json from backup: %w", werr)
	}
	log.Printf("[config] config.json was unreadable; restored from config.json.bak (the broken copy is config.json.corrupt)")
	return bdata, nil
}

// Dir returns the config directory.
func (c *ConfigStore) Dir() string { return c.dir }

// --- profiles ---

// ListProfiles returns a copy of all profiles.
func (c *ConfigStore) ListProfiles() []Profile {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Profile, len(c.profiles))
	copy(out, c.profiles)
	return out
}

// GetProfile returns the profile with the given id.
func (c *ConfigStore) GetProfile(id string) (Profile, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// SaveProfile inserts or replaces a profile (assigns an id when empty).
func (c *ConfigStore) SaveProfile(p Profile) (Profile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.ID == "" {
		id, err := newID()
		if err != nil {
			return Profile{}, err
		}
		p.ID = id
	}
	for i, e := range c.profiles {
		if e.ID == p.ID {
			p = mergeProfileRuntime(p, e)
			previous := c.profiles[i]
			c.profiles[i] = p
			if err := c.saveLocked(); err != nil {
				c.profiles[i] = previous
				return p, err
			}
			if !profileRuntime(p).empty() {
				c.markStateChangedLocked()
			}
			return p, nil
		}
	}
	c.profiles = append(c.profiles, p)
	if err := c.saveLocked(); err != nil {
		c.profiles = c.profiles[:len(c.profiles)-1]
		return p, err
	}
	if !profileRuntime(p).empty() {
		c.markStateChangedLocked()
	}
	return p, nil
}

// SaveProfiles inserts or replaces a batch with one durable write.
func (c *ConfigStore) SaveProfiles(profiles []Profile) ([]Profile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(profiles) == 0 {
		return []Profile{}, nil
	}
	previous := c.profiles
	next := cloneSlice(c.profiles)
	index := make(map[string]int, len(next))
	for i := range next {
		index[next[i].ID] = i
	}
	saved := make([]Profile, len(profiles))
	hasRuntime := false
	for i, p := range profiles {
		if p.ID == "" {
			id, err := newID()
			if err != nil {
				return nil, err
			}
			p.ID = id
		}
		if at, ok := index[p.ID]; ok {
			p = mergeProfileRuntime(p, next[at])
			next[at] = p
		} else {
			index[p.ID] = len(next)
			next = append(next, p)
		}
		hasRuntime = hasRuntime || !profileRuntime(p).empty()
		saved[i] = p
	}
	c.profiles = next
	if err := c.saveLocked(); err != nil {
		c.profiles = previous
		return nil, err
	}
	if hasRuntime {
		c.markStateChangedLocked()
	}
	return saved, nil
}

// UpdateProfile replaces an existing profile in the in-memory store
// WITHOUT writing config.json.
//
// Batch callers (the country refresh, ClearCountries) update many
// profiles in a row; writing the file per profile re-marshals and
// rewrites the WHOLE config every time (measured: 1.9 ms + 220 KB per
// profile with 338 profiles). They call UpdateProfile for each change
// and SaveFile once when the batch is done.
//
// A profile whose ID is unknown is ignored (SaveProfile is the path
// that inserts).
func (c *ConfigStore) UpdateProfile(p Profile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.profiles {
		if e.ID == p.ID {
			c.profiles[i] = p
			return
		}
	}
}

// UpdateProfileCountry stamps ONLY the country fields on the stored row,
// under the lock (same BUG-043 contract as SetCloseToTray). The country
// refresh resolves addresses in parallel and a single DNS lookup can take
// seconds, so writing back a copy taken before the lookup would silently
// revert an edit the user saved in between — and SaveFile would persist
// the revert. In-memory only, like UpdateProfile. Reports whether the row
// existed.
func (c *ConfigStore) UpdateProfileCountry(id, code, name string, at *time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.profiles {
		if e.ID != id {
			continue
		}
		if e.CountryCode == code && e.CountryName == name && sameTime(e.CountryAt, at) {
			return true
		}
		c.profiles[i].CountryCode = code
		c.profiles[i].CountryName = name
		c.profiles[i].CountryAt = cloneTimePtr(at)
		c.markStateChangedLocked()
		return true
	}
	return false
}

// SaveFile writes the current in-memory state to config.json (one
// atomic write). Callers that used UpdateProfile must call it once
// when their batch is done.
func (c *ConfigStore) SaveFile() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.saveLocked(); err != nil {
		return err
	}
	c.markStateChangedLocked()
	return nil
}

// DeleteProfile removes a profile by id.
func (c *ConfigStore) DeleteProfile(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.profiles {
		if e.ID == id {
			previous := c.profiles
			next := cloneSlice(c.profiles)
			c.profiles = append(next[:i], next[i+1:]...)
			if err := c.saveLocked(); err != nil {
				c.profiles = previous
				return err
			}
			c.markStateChangedLocked()
			return nil
		}
	}
	return fmt.Errorf("profile %q not found", id)
}

// --- settings ---

// GetSettings returns a copy of the settings.
func (c *ConfigStore) GetSettings() Settings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.settings
	// TUN stack (CPU audit #1): resolve an empty/unknown value to the
	// effective default on read, so configs saved before the setting
	// existed (and backups that omit it) report "system" everywhere
	// without rewriting the file on disk.
	s.TunStack = normalizeTunStack(s.TunStack)
	// The rest of the TUN group, same read-time treatment: prefixes are
	// canonicalized (and junk dropped) so the rows show what will render,
	// and an unset auto-route reports the effective true rather than an
	// unchecked box that would turn routing off if saved back.
	normalizeTunSettings(&s)
	if s.TunAutoRoute == nil {
		v := true
		s.TunAutoRoute = &v
	}
	// Core engine: configs saved before the setting existed omit it.
	// Report the default ("custom", the Thrift/RPC core) instead of an
	// empty string so the settings select and the tray never read blank.
	if s.CoreEngine == "" {
		s.CoreEngine = "custom"
	}
	limit := defaultConsoleLogLimit
	if s.ConsoleLogLimit != nil && *s.ConsoleLogLimit >= 0 {
		limit = *s.ConsoleLogLimit
	}
	s.ConsoleLogLimit = &limit
	// SNI spoofing defaults to ON; configs written before the toggle
	// existed omit the field. Report the effective value so the UI and
	// every caller can treat it as a plain bool.
	if s.SpoofEnabled == nil {
		v := true
		s.SpoofEnabled = &v
	}
	// CoreOverrides is a map and every list below is a slice: the struct
	// copy above still aliases the live store, so a snapshot kept
	// mutating after it was handed out. That is not cosmetic —
	// TouchSubscription, RenameSubscription, SetSubscriptionEnabled, the
	// SNI scanner and UpdateAetherScanResults rewrite rows IN PLACE under
	// the lock, while a caller marshals the aliased array off-lock
	// (ListSubscriptions hands it straight to the Wails marshaller) or
	// iterates one snapshot across whole network fetches (the
	// subscription update loop). A torn value is garbage in the UI and a
	// concurrent map read/write is a fatal throw, so hand out detached
	// copies of every reference field, not just the map.
	if len(s.CoreOverrides) > 0 {
		m := make(map[string]json.RawMessage, len(s.CoreOverrides))
		for k, v := range s.CoreOverrides {
			m[k] = v
		}
		s.CoreOverrides = m
	}
	s.Subscriptions = cloneSlice(s.Subscriptions)
	s.AetherScanResults = cloneSlice(s.AetherScanResults)
	s.SNIIPs = cloneSlice(s.SNIIPs)
	s.SNISNIs = cloneSlice(s.SNISNIs)
	s.ProxyGroups = cloneSlice(s.ProxyGroups)
	s.CollapsedProxyGroups = cloneSlice(s.CollapsedProxyGroups)
	return s
}

// cloneSlice detaches one settings list from the store's own array.
// nil stays nil so an absent list keeps rendering as absent.
func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

// SaveSettings replaces and persists the settings.
//
// Subscriptions, AetherScanResults and CoreOverrides are backend-owned:
// the feed APIs (AddSubscription, TouchSubscription, DeleteSubscription,
// ...), the Aether scanner and the Advanced config editors rewrite them
// in place, while the frontend's settings snapshot is loaded once at
// startup and posted back wholesale. Replacing them from that snapshot
// silently deleted a feed, a scan result or a saved core override that
// was added after the snapshot — the profiles stayed, the data vanished
// on the next restart. Carry the live values over; the backup restore
// path uses restoreAll, which still replaces everything.
//
// The SNI lists are shared rather than backend-owned, so they are merged:
// the tab's entry set wins, the scanner's stamps are carried over.
func (c *ConfigStore) SaveSettings(s Settings) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.Subscriptions = c.settings.Subscriptions
	s.AetherScanResults = c.settings.AetherScanResults
	s.CoreOverrides = c.settings.CoreOverrides
	s.SNIConfig = c.settings.SNIConfig
	s.SNIIPs = mergeSNIIPStamps(s.SNIIPs, c.settings.SNIIPs)
	s.SNISNIs = mergeSNIStamps(s.SNISNIs, c.settings.SNISNIs)
	if s.ConsoleLogLimit != nil {
		limit := *s.ConsoleLogLimit
		s.ConsoleLogLimit = &limit
	}
	previous := c.settings
	c.settings = s
	if err := c.saveLocked(); err != nil {
		c.settings = previous
		return err
	}
	if !settingsRuntimeEqual(previous, s) {
		c.markStateChangedLocked()
	}
	return nil
}

// mergeSNIIPStamps keeps the live scanner stamps on the entries the tab
// posts. The tab owns the entry set, Source and Selected; the scanner owns
// LastOK/LastMs/LastAt/LastTLS and writes them through UpdateSNI, including
// on a cancelled run. A snapshot taken before that write would erase it.
func mergeSNIIPStamps(incoming, live []SNIIP) []SNIIP {
	if len(incoming) == 0 || len(live) == 0 {
		return incoming
	}
	by := make(map[string]SNIIP, len(live))
	for _, e := range live {
		by[e.IP] = e
	}
	for i := range incoming {
		if old, ok := by[incoming[i].IP]; ok {
			incoming[i].LastOK, incoming[i].LastMs = old.LastOK, old.LastMs
			incoming[i].LastAt, incoming[i].LastTLS = old.LastAt, old.LastTLS
		}
	}
	return incoming
}

// mergeSNIStamps is mergeSNIIPStamps for the decoy-SNI list, which also
// carries Plan A's resolved address.
func mergeSNIStamps(incoming, live []SNISNI) []SNISNI {
	if len(incoming) == 0 || len(live) == 0 {
		return incoming
	}
	by := make(map[string]SNISNI, len(live))
	for _, e := range live {
		by[e.Host] = e
	}
	for i := range incoming {
		if old, ok := by[incoming[i].Host]; ok {
			incoming[i].LastOK, incoming[i].LastMs = old.LastOK, old.LastMs
			incoming[i].LastAt, incoming[i].LastTLS = old.LastAt, old.LastTLS
			incoming[i].LastIP = old.LastIP
		}
	}
	return incoming
}

// UpdateCoreOverrides applies fn to the live override map under the
// store lock and persists when fn reports a change. The Advanced
// editors used to mutate the map handed out by GetSettings and then
// save the whole struct, which raced background saves (a concurrent
// map read/write is a fatal runtime throw) and let a stale snapshot
// drop an override.
func (c *ConfigStore) UpdateCoreOverrides(fn func(map[string]json.RawMessage) bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.CoreOverrides == nil {
		c.settings.CoreOverrides = map[string]json.RawMessage{}
	}
	if !fn(c.settings.CoreOverrides) {
		return nil
	}
	return c.saveLocked()
}

// updateProfileFields applies fn to one stored row under the lock and
// persists it. It is the single-row counterpart of UpdateProfileCountry:
// callers that hold a profile snapshot across a network probe (latency
// test, GeoIP lookup, multi-second connect) must not write the whole
// snapshot back — that reverts any edit saved meanwhile.
func (c *ConfigStore) updateProfileFields(id string, fn func(*Profile)) (Profile, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.profiles {
		if c.profiles[i].ID != id {
			continue
		}
		previous := c.profiles[i]
		fn(&c.profiles[i])
		durableChanged := !profilesDurableEqual(previous, c.profiles[i])
		runtimeChanged := !reflect.DeepEqual(profileRuntime(previous), profileRuntime(c.profiles[i]))
		if !durableChanged && !runtimeChanged {
			return c.profiles[i], true, nil
		}
		if durableChanged {
			if err := c.saveLocked(); err != nil {
				c.profiles[i] = previous
				return c.profiles[i], true, err
			}
		}
		if runtimeChanged {
			c.markStateChangedLocked()
		}
		return c.profiles[i], true, nil
	}
	return Profile{}, false, nil
}

// SetActiveProfileID persists the last used profile id.
func (c *ConfigStore) SetActiveProfileID(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.settings.ActiveProfileID
	c.settings.ActiveProfileID = id
	if err := c.saveLocked(); err != nil {
		c.settings.ActiveProfileID = previous
		return err
	}
	return nil
}

// SetActiveProfileIDIfUnchanged persists id only while the selection still
// matches the value observed before a long-running Connect started.
func (c *ConfigStore) SetActiveProfileIDIfUnchanged(id, expected string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.ActiveProfileID != expected {
		return false, nil
	}
	previous := c.settings.ActiveProfileID
	c.settings.ActiveProfileID = id
	if err := c.saveLocked(); err != nil {
		c.settings.ActiveProfileID = previous
		return true, err
	}
	return true, nil
}

// restoreAll atomically replaces settings and profiles with one
// locked write (Phase F import/reset): callers fully validate first,
// so a failure never leaves a half-replaced store.
func (c *ConfigStore) restoreAll(s Settings, profiles []Profile) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previousSettings := c.settings
	previousProfiles := c.profiles
	c.settings = s
	c.profiles = append([]Profile(nil), profiles...)
	if c.profiles == nil {
		c.profiles = []Profile{}
	}
	if err := c.saveLocked(); err != nil {
		c.settings = previousSettings
		c.profiles = previousProfiles
		return err
	}
	c.markStateChangedLocked()
	return nil
}

// SetCloseToTray persists the close-to-tray toggle atomically (BUG-043):
// a single-field RMW under the store lock, so it cannot clobber a
// concurrent subscription add/touch that only rewrites Subscriptions.
func (c *ConfigStore) SetCloseToTray(enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.CloseToTray = enabled
	return c.saveLocked()
}

// SetStartOnBoot persists the launch-at-login toggle atomically
// (same BUG-043 contract as SetCloseToTray).
func (c *ConfigStore) SetStartOnBoot(enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.StartOnBoot = enabled
	return c.saveLocked()
}

// UpdateSNI applies fn to the SNI-spoof settings slice (SNIConfig,
// SNISelectedIP/SNI, SNIIPs/SNISNIs) atomically: a locked RMW like
// AddSubscription, so a concurrent SaveSettings cannot clobber the
// scanner or the SNI tab (same BUG-043 contract). SaveSettings
// persists every other field verbatim, so plain full-struct saves
// already round-trip the SNI fields.
func (c *ConfigStore) UpdateSNI(fn func(*Settings)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.settings
	previous.SNIIPs = cloneSlice(c.settings.SNIIPs)
	previous.SNISNIs = cloneSlice(c.settings.SNISNIs)
	fn(&c.settings)
	durableChanged := !settingsDurableEqual(previous, c.settings)
	runtimeChanged := !settingsRuntimeEqual(previous, c.settings)
	if durableChanged {
		if err := c.saveLocked(); err != nil {
			c.settings = previous
			return err
		}
	}
	if runtimeChanged {
		c.markStateChangedLocked()
	}
	return nil
}

// UpdateAetherScanResults applies fn to the persisted scan results
// atomically: a locked RMW like UpdateSNI, so a scan finishing while
// the settings tab saves cannot drop either update.
func (c *ConfigStore) UpdateAetherScanResults(fn func([]AetherScanResult) []AetherScanResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := cloneSlice(c.settings.AetherScanResults)
	c.settings.AetherScanResults = fn(c.settings.AetherScanResults)
	if reflect.DeepEqual(previous, c.settings.AetherScanResults) {
		return nil
	}
	c.markStateChangedLocked()
	return nil
}

// UpdateAetherKeys applies fn to the persisted WARP-key renewal settings
// atomically, like UpdateAetherScanResults: a renewal saving while the
// settings tab saves cannot drop either update.
func (c *ConfigStore) UpdateAetherKeys(fn func(aetherKeysSettings) aetherKeysSettings) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.AetherKeys = fn(c.settings.AetherKeys)
	return c.saveLocked()
}

// AddSubscription records a subscription URL atomically (BUG-043): the
// read-modify-write holds the store lock throughout, so a concurrent
// SaveSettings/SetCloseToTray cannot interleave between the read and
// the write and drop either update. Reports whether the URL was new.
func (c *ConfigStore) AddSubscription(sub Subscription) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.settings.Subscriptions {
		if e.URL == sub.URL {
			return false, nil
		}
	}
	c.settings.Subscriptions = append(c.settings.Subscriptions, sub)
	return true, c.saveLocked()
}

// TouchSubscription refreshes one subscription's UpdatedAt and its
// provider usage (quota/expiry) atomically (BUG-043): same
// locked-RMW contract as AddSubscription.
func (c *ConfigStore) TouchSubscription(url string, at string, info SubUserInfo) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.settings.Subscriptions {
		if e.URL == url {
			c.settings.Subscriptions[i].UpdatedAt = at
			c.settings.Subscriptions[i].UsageUpload = info.Upload
			c.settings.Subscriptions[i].UsageDownload = info.Download
			c.settings.Subscriptions[i].UsageTotal = info.Total
			c.settings.Subscriptions[i].UsageExpire = info.Expire
			return true, c.saveLocked()
		}
	}
	return false, nil
}

// DeleteSubscription removes a recorded feed by URL (locked RMW,
// BUG-043 contract). Already-imported profiles are intentionally left
// untouched — they are independent rows in the Proxies tab.
func (c *ConfigStore) DeleteSubscription(url string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.settings.Subscriptions {
		if e.URL == url {
			c.settings.Subscriptions = append(c.settings.Subscriptions[:i], c.settings.Subscriptions[i+1:]...)
			return true, c.saveLocked()
		}
	}
	return false, nil
}

// RenameSubscription sets a feed's display name (locked RMW, BUG-043
// contract). An empty name is rejected so every feed stays renderable.
func (c *ConfigStore) RenameSubscription(url, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("subscription name required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.settings.Subscriptions {
		if e.URL == url {
			c.settings.Subscriptions[i].Name = name
			return true, c.saveLocked()
		}
	}
	return false, nil
}

// SetSubscriptionEnabled pauses/resumes one feed atomically (locked
// RMW, BUG-043 contract). nil means enabled, so enabling clears the
// field to keep the file unchanged for legacy records.
func (c *ConfigStore) SetSubscriptionEnabled(url string, enabled bool) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.settings.Subscriptions {
		if e.URL == url {
			if enabled {
				c.settings.Subscriptions[i].Enabled = nil
			} else {
				off := false
				c.settings.Subscriptions[i].Enabled = &off
			}
			return true, c.saveLocked()
		}
	}
	return false, nil
}

// profileFromSubscription matches one profile to a feed: precise on
// SubURL, plus a group-name fallback for profiles imported before
// SubURL existed (their group is the feed's name, kept in sync by
// renames).
func profileFromSubscription(p *Profile, subURL, group string) bool {
	if subURL != "" && p.SubURL == subURL {
		return true
	}
	return p.SubURL == "" && group != "" && p.Group == group
}

// DeleteProfilesBySource removes every profile imported from one
// subscription and returns the count (locked RMW, BUG-043 contract).
// ActiveProfileID cleanup stays with the caller: App owns the core
// status needed to refuse deleting a connected profile.
func (c *ConfigStore) DeleteProfilesBySource(subURL, group string) (int, error) {
	if subURL == "" && group == "" {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.profiles
	next := cloneSlice(c.profiles)
	kept := next[:0]
	n := 0
	for i := range next {
		if profileFromSubscription(&next[i], subURL, group) {
			n++
			continue
		}
		kept = append(kept, next[i])
	}
	if n == 0 {
		return 0, nil
	}
	c.profiles = kept
	if err := c.saveLocked(); err != nil {
		c.profiles = previous
		return 0, err
	}
	c.markStateChangedLocked()
	return n, nil
}

// RepointSubscriptionProfiles updates matching live rows under one lock so
// an intervening edit or deletion cannot be overwritten by a stale batch.
func (c *ConfigStore) RepointSubscriptionProfiles(oldURL, newURL string) (int, error) {
	if oldURL == "" || newURL == "" || oldURL == newURL {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.profiles
	next := cloneSlice(c.profiles)
	n := 0
	for i := range next {
		if next[i].SubURL == oldURL {
			next[i].SubURL = newURL
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	c.profiles = next
	if err := c.saveLocked(); err != nil {
		c.profiles = previous
		return 0, err
	}
	return n, nil
}

// RenameFeedProfiles re-files the profiles one feed owns after its
// display name changes, and returns how many moved.
//
// The feed's own profiles are found by SubURL, which is exact. Matching on
// the old group name alone also catches a second feed the user gave the
// same name — one provider handing out two links is ordinary — and every
// hand-picked profile filed in a remark group that happens to share it, so
// a rename dragged all of them into the renamed feed's group. Profiles
// imported before SubURL existed carry no feed pointer at all, so the group
// name is the only evidence they have; that fallback stands down when the
// name is also a group the user created.
//
// A profile the user moved out of the feed group on purpose keeps the group
// it was moved to: only rows still carrying the old name follow.
func (c *ConfigStore) RenameFeedProfiles(subURL, oldName, newName string) (int, error) {
	if subURL == "" || oldName == "" || newName == "" || oldName == newName {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	legacy := !slices.Contains(c.settings.ProxyGroups, oldName)
	n := 0
	for i := range c.profiles {
		p := &c.profiles[i]
		if p.Group != oldName {
			continue
		}
		if p.SubURL == subURL || (legacy && p.SubURL == "") {
			p.Group = newName
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, c.saveLocked()
}

// randRead is crypto/rand.Read as a variable so tests can inject a
// CSPRNG failure (BUG-029).
var randRead = rand.Read

// newID mints a random profile id. A CSPRNG failure is returned as an
// error (BUG-029) — the old code panicked, crashing the whole GUI on
// the save path.
func newID() (string, error) {
	var b [8]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
