package main

// App-level autostart: the Settings toggle's single code path.
// OS state first, then the atomic persist; a persist failure
// best-effort reverts the OS write so stored state matches reality
// (same discipline as the external flag revert).

// GetAutoRun reports the ACTUAL OS autostart state by reading the
// Run key (ground truth: a manually deleted value reads false even
// when the setting says true).
func (a *App) GetAutoRun() (bool, error) {
	return autorunEnabled()
}

// SetStartOnBoot applies launch-at-login to the OS and persists the
// setting. Bound for the Settings toggle (replaces the old
// stored-only row that never touched the OS).
func (a *App) SetStartOnBoot(enabled bool) error {
	prev, _ := autorunEnabled()
	var err error
	if enabled {
		err = autorunSet()
	} else {
		err = autorunClear()
	}
	if err != nil {
		return err
	}
	if perr := a.store.SetStartOnBoot(enabled); perr != nil {
		// Persist failed: revert the OS write so the two agree.
		if prev {
			_ = autorunSet()
		} else {
			_ = autorunClear()
		}
		return perr
	}
	a.emitLog("gui", "info", map[bool]string{
		true:  "autostart enabled (Run key set)",
		false: "autostart disabled (Run key cleared)",
	}[enabled])
	return nil
}
