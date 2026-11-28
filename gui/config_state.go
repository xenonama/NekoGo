package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"time"
)

const (
	configStateVersion     = 1
	defaultStateWriteDelay = 500 * time.Millisecond
	defaultStateWriteMax   = 2 * time.Second
	defaultStateRetryDelay = 100 * time.Millisecond
	maxStateWriteRetries   = 4
)

type profileRuntimeState struct {
	LastLatency     int        `json:"lastLatency,omitempty"`
	LastTestedAt    *time.Time `json:"lastTestedAt,omitempty"`
	CountryCode     string     `json:"countryCode,omitempty"`
	CountryName     string     `json:"countryName,omitempty"`
	CountryAt       *time.Time `json:"countryAt,omitempty"`
	LastConnectedAt string     `json:"lastConnectedAt,omitempty"`
}

type sniIPRuntimeState struct {
	LastOK  bool   `json:"lastOk,omitempty"`
	LastMs  int    `json:"lastMs,omitempty"`
	LastAt  string `json:"lastAt,omitempty"`
	LastTLS string `json:"lastTls,omitempty"`
}

type sniSNIRuntimeState struct {
	LastOK  bool   `json:"lastOk,omitempty"`
	LastMs  int    `json:"lastMs,omitempty"`
	LastAt  string `json:"lastAt,omitempty"`
	LastTLS string `json:"lastTls,omitempty"`
	LastIP  string `json:"lastIp,omitempty"`
}

type configState struct {
	Version           int                            `json:"version"`
	Profiles          map[string]profileRuntimeState `json:"profiles,omitempty"`
	AetherScanResults *[]AetherScanResult            `json:"aetherScanResults,omitempty"`
	SNIIPs            map[string]sniIPRuntimeState   `json:"sniIps,omitempty"`
	SNISNIs           map[string]sniSNIRuntimeState  `json:"sniSnis,omitempty"`
}

func cloneTimePtr(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func profileRuntime(p Profile) profileRuntimeState {
	return profileRuntimeState{
		LastLatency:     p.LastLatency,
		LastTestedAt:    cloneTimePtr(p.LastTestedAt),
		CountryCode:     p.CountryCode,
		CountryName:     p.CountryName,
		CountryAt:       cloneTimePtr(p.CountryAt),
		LastConnectedAt: p.LastConnectedAt,
	}
}

func (r profileRuntimeState) apply(p *Profile) {
	p.LastLatency = r.LastLatency
	p.LastTestedAt = cloneTimePtr(r.LastTestedAt)
	p.CountryCode = r.CountryCode
	p.CountryName = r.CountryName
	p.CountryAt = cloneTimePtr(r.CountryAt)
	p.LastConnectedAt = r.LastConnectedAt
}

func (r profileRuntimeState) empty() bool {
	return r == (profileRuntimeState{})
}

func mergeProfileRuntime(incoming, previous Profile) Profile {
	old := profileRuntime(previous)
	next := profileRuntime(incoming)
	if next.LastLatency == 0 {
		next.LastLatency = old.LastLatency
	}
	if next.LastTestedAt == nil {
		next.LastTestedAt = old.LastTestedAt
	}
	if next.CountryCode == "" {
		next.CountryCode = old.CountryCode
	}
	if next.CountryName == "" {
		next.CountryName = old.CountryName
	}
	if next.CountryAt == nil {
		next.CountryAt = old.CountryAt
	}
	if next.LastConnectedAt == "" {
		next.LastConnectedAt = old.LastConnectedAt
	}
	next.apply(&incoming)
	return incoming
}

func durableProfile(p Profile) Profile {
	p.LastLatency = 0
	p.LastTestedAt = nil
	p.CountryCode = ""
	p.CountryName = ""
	p.CountryAt = nil
	p.LastConnectedAt = ""
	return p
}

func sniIPRuntime(e SNIIP) sniIPRuntimeState {
	return sniIPRuntimeState{LastOK: e.LastOK, LastMs: e.LastMs, LastAt: e.LastAt, LastTLS: e.LastTLS}
}

func (r sniIPRuntimeState) apply(e *SNIIP) {
	e.LastOK = r.LastOK
	e.LastMs = r.LastMs
	e.LastAt = r.LastAt
	e.LastTLS = r.LastTLS
}

func (r sniIPRuntimeState) empty() bool {
	return r == (sniIPRuntimeState{})
}

func sniSNIRuntime(e SNISNI) sniSNIRuntimeState {
	return sniSNIRuntimeState{LastOK: e.LastOK, LastMs: e.LastMs, LastAt: e.LastAt, LastTLS: e.LastTLS, LastIP: e.LastIP}
}

func (r sniSNIRuntimeState) apply(e *SNISNI) {
	e.LastOK = r.LastOK
	e.LastMs = r.LastMs
	e.LastAt = r.LastAt
	e.LastTLS = r.LastTLS
	e.LastIP = r.LastIP
}

func (r sniSNIRuntimeState) empty() bool {
	return r == (sniSNIRuntimeState{})
}

func durableSettings(s Settings) Settings {
	s.AetherScanResults = nil
	if s.SNIIPs != nil {
		ips := cloneSlice(s.SNIIPs)
		for i := range ips {
			ips[i].LastOK = false
			ips[i].LastMs = 0
			ips[i].LastAt = ""
			ips[i].LastTLS = ""
		}
		s.SNIIPs = ips
	}
	if s.SNISNIs != nil {
		snis := cloneSlice(s.SNISNIs)
		for i := range snis {
			snis[i].LastOK = false
			snis[i].LastMs = 0
			snis[i].LastAt = ""
			snis[i].LastTLS = ""
			snis[i].LastIP = ""
		}
		s.SNISNIs = snis
	}
	return s
}

func runtimeStateFrom(profiles []Profile, settings Settings) configState {
	state := configState{Version: configStateVersion}
	for _, p := range profiles {
		if p.ID == "" {
			continue
		}
		if r := profileRuntime(p); !r.empty() {
			if state.Profiles == nil {
				state.Profiles = make(map[string]profileRuntimeState)
			}
			state.Profiles[p.ID] = r
		}
	}
	if settings.AetherScanResults != nil {
		results := cloneSlice(settings.AetherScanResults)
		state.AetherScanResults = &results
	}
	for _, e := range settings.SNIIPs {
		if r := sniIPRuntime(e); !r.empty() {
			if state.SNIIPs == nil {
				state.SNIIPs = make(map[string]sniIPRuntimeState)
			}
			state.SNIIPs[e.IP] = r
		}
	}
	for _, e := range settings.SNISNIs {
		if r := sniSNIRuntime(e); !r.empty() {
			if state.SNISNIs == nil {
				state.SNISNIs = make(map[string]sniSNIRuntimeState)
			}
			state.SNISNIs[e.Host] = r
		}
	}
	return state
}

func (s configState) hasContent() bool {
	return len(s.Profiles) > 0 || s.AetherScanResults != nil || len(s.SNIIPs) > 0 || len(s.SNISNIs) > 0
}

func (c *ConfigStore) currentRuntimeStateLocked() configState {
	return runtimeStateFrom(c.profiles, c.settings)
}

func (c *ConfigStore) applyRuntimeStateLocked(state configState) {
	for i := range c.profiles {
		if r, ok := state.Profiles[c.profiles[i].ID]; ok {
			r.apply(&c.profiles[i])
		}
	}
	if state.AetherScanResults != nil {
		c.settings.AetherScanResults = cloneSlice(*state.AetherScanResults)
	}
	for i := range c.settings.SNIIPs {
		if r, ok := state.SNIIPs[c.settings.SNIIPs[i].IP]; ok {
			r.apply(&c.settings.SNIIPs[i])
		}
	}
	for i := range c.settings.SNISNIs {
		if r, ok := state.SNISNIs[c.settings.SNISNIs[i].Host]; ok {
			r.apply(&c.settings.SNISNIs[i])
		}
	}
}

func readConfigState(path string) (configState, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configState{}, false, nil
		}
		return configState{}, true, err
	}
	var state configState
	if err := json.Unmarshal(data, &state); err != nil {
		return configState{}, true, err
	}
	if state.Version != configStateVersion {
		return configState{}, true, fmt.Errorf("unsupported state version %d", state.Version)
	}
	return state, true, nil
}

func mergeConfigState(legacy, sidecar configState) configState {
	merged := legacy
	merged.Version = configStateVersion
	if sidecar.Profiles != nil {
		if merged.Profiles == nil {
			merged.Profiles = make(map[string]profileRuntimeState)
		}
		for id, r := range sidecar.Profiles {
			merged.Profiles[id] = r
		}
	}
	if sidecar.AetherScanResults != nil {
		results := cloneSlice(*sidecar.AetherScanResults)
		merged.AetherScanResults = &results
	}
	if sidecar.SNIIPs != nil {
		if merged.SNIIPs == nil {
			merged.SNIIPs = make(map[string]sniIPRuntimeState)
		}
		for ip, r := range sidecar.SNIIPs {
			merged.SNIIPs[ip] = r
		}
	}
	if sidecar.SNISNIs != nil {
		if merged.SNISNIs == nil {
			merged.SNISNIs = make(map[string]sniSNIRuntimeState)
		}
		for host, r := range sidecar.SNISNIs {
			merged.SNISNIs[host] = r
		}
	}
	return merged
}

func (c *ConfigStore) loadRuntimeStateLocked(fc fileConfig) {
	legacy := runtimeStateFrom(fc.Profiles, fc.Settings)
	sidecar, exists, err := readConfigState(c.statePath)
	if err != nil {
		log.Printf("[config] ignoring unreadable state.json: %v", err)
	}
	merged := mergeConfigState(legacy, sidecar)
	c.statePresent = exists || legacy.hasContent()
	c.applyRuntimeStateLocked(merged)
	if legacy.hasContent() {
		c.stateDirty = true
		c.stateMigrationPending = true
		c.migrationState = merged
		c.stateFirstDirty = time.Now()
		if err := c.flushStateLocked(); err != nil {
			c.retryStateWriteLocked(err)
		}
	}
}

func writeJSONAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (c *ConfigStore) writeStateLocked() error {
	return c.writeStateValueLocked(c.currentRuntimeStateLocked())
}

func (c *ConfigStore) writeStateValueLocked(state configState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeJSONAtomic(c.statePath, data)
}

func (c *ConfigStore) markStateChangedLocked() {
	c.stateDirty = true
	c.stateRetry = 0
	c.stateFailureLogged = false
	c.scheduleStateWriteLocked()
}

func (c *ConfigStore) scheduleStateWriteLocked() {
	if !c.stateDirty && !c.statePresent {
		return
	}
	now := time.Now()
	if c.stateFirstDirty.IsZero() {
		c.stateFirstDirty = now
	}
	next := now.Add(c.stateDelay)
	if c.stateMaxDelay > 0 {
		deadline := c.stateFirstDirty.Add(c.stateMaxDelay)
		if deadline.Before(next) {
			next = deadline
		}
	}
	if !next.After(now) {
		next = now.Add(time.Millisecond)
	}
	c.stateNext = next
	if c.stateTimer != nil {
		c.stateTimer.Stop()
	}
	c.stateTimer = time.AfterFunc(next.Sub(now), c.stateTimerFired)
}

func (c *ConfigStore) armStateTimerLocked(delay time.Duration) {
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	if c.stateTimer != nil {
		c.stateTimer.Stop()
	}
	c.stateTimer = time.AfterFunc(delay, c.stateTimerFired)
}

func (c *ConfigStore) retryStateWriteLocked(err error) {
	if !c.stateFailureLogged {
		log.Printf("[config] saving state.json failed: %v; retrying up to %d times", err, maxStateWriteRetries)
		c.stateFailureLogged = true
	}
	c.stateNext = time.Time{}
	if c.stateRetry >= maxStateWriteRetries {
		c.stateTimer = nil
		return
	}
	c.stateRetry++
	delay := defaultStateRetryDelay << (c.stateRetry - 1)
	c.armStateTimerLocked(delay)
}

func (c *ConfigStore) stateTimerFired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.stateDirty {
		c.stateTimer = nil
		return
	}
	if !c.stateNext.IsZero() && time.Now().Before(c.stateNext) {
		c.armStateTimerLocked(time.Until(c.stateNext))
		return
	}
	c.stateTimer = nil
	if err := c.flushStateLocked(); err != nil {
		c.retryStateWriteLocked(err)
	}
}

func (c *ConfigStore) flushStateLocked() error {
	if !c.stateDirty {
		return nil
	}
	state := c.currentRuntimeStateLocked()
	if !state.hasContent() && !c.statePresent {
		c.stateDirty = false
		c.stateMigrationPending = false
		c.migrationState = configState{}
		c.stateFirstDirty = time.Time{}
		c.stateNext = time.Time{}
		c.stateRetry = 0
		c.stateFailureLogged = false
		return nil
	}
	if err := c.writeStateLocked(); err != nil {
		return err
	}
	c.statePresent = true
	c.stateDirty = false
	c.stateMigrationPending = false
	c.migrationState = configState{}
	c.stateFirstDirty = time.Time{}
	c.stateNext = time.Time{}
	c.stateRetry = 0
	c.stateFailureLogged = false
	return nil
}

// Flush writes pending runtime state immediately.
func (c *ConfigStore) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stateTimer != nil {
		c.stateTimer.Stop()
		c.stateTimer = nil
	}
	c.stateNext = time.Time{}
	if err := c.flushStateLocked(); err != nil {
		c.retryStateWriteLocked(err)
		return err
	}
	return nil
}

func profilesDurableEqual(a, b Profile) bool {
	return reflect.DeepEqual(durableProfile(a), durableProfile(b))
}

func settingsDurableEqual(a, b Settings) bool {
	return reflect.DeepEqual(durableSettings(a), durableSettings(b))
}

func settingsRuntimeEqual(a, b Settings) bool {
	return reflect.DeepEqual(runtimeStateFrom(nil, a), runtimeStateFrom(nil, b))
}
