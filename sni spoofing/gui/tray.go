package main

import (
	"fmt"
	"log"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// FIX(tray-rewrite): single tray owner. This file is the implementation
// that main.go / sni_engine.go call into:
//
//   - exactly one native instance, created on the first Ensure (pre-Run
//     SetIcon seeds the PNG bytes the native run() consumes, so the shell
//     registration always carries app.png);
//   - later toggles only Show/Hide that same instance (never re-create,
//     which is what used to reset/lose the project icon);
//   - all mutations serialized under mu; a generation counter makes
//     overlapping Ensure calls deterministic (callers also re-apply the
//     desired state after App.Run starts, covering async native ordering).
//
// The window handle is captured once via SetWindow (stable across Hide),
// never dynamically looked up.

// TrayOptions is reserved for future tray knobs (tooltip override, menu
// extras). The 4th NewTrayManager arg accepts nil.
type TrayOptions struct {
	Tooltip string
}

// TrayManager owns the native tray icon lifecycle.
type TrayManager struct {
	mu      sync.Mutex
	app     *application.App
	engine  *EngineService
	icon    []byte
	tooltip string

	win     application.Window
	tray    *application.SystemTray
	enabled bool
	ran     bool
	dead    bool
	gen     uint64
}

// NewTrayManager builds the manager. Nothing native happens here; the
// icon instance is created on the first Ensure call.
func NewTrayManager(app *application.App, engine *EngineService, icon []byte, opts *TrayOptions) *TrayManager {
	tooltip := "SNI Spoofer"
	if opts != nil && opts.Tooltip != "" {
		tooltip = opts.Tooltip
	}
	return &TrayManager{
		app:     app,
		engine:  engine,
		icon:    append([]byte(nil), icon...),
		tooltip: tooltip,
	}
}

// SetWindow captures the stable window handle (call once after creating
// the main window; never dynamically looked up afterwards).
func (m *TrayManager) SetWindow(w application.Window) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.win = w
	if m.tray != nil && w != nil {
		m.tray.AttachWindow(w)
	}
}

// window returns the stored handle for EngineService fallbacks.
func (m *TrayManager) window() application.Window {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.win
}

// Enabled reports the desired tray state (not the native visibility).
func (m *TrayManager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled && !m.dead
}

// IsActive reports whether a live native instance exists right now.
func (m *TrayManager) IsActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tray != nil && m.ran && !m.dead
}

// Ensure creates the single instance on first call, then shows/hides it
// to match enabled. Never re-creates (keeps the seeded project icon).
func (m *TrayManager) Ensure(enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	if m.dead {
		return fmt.Errorf("tray manager shut down")
	}
	m.enabled = enabled
	if m.tray == nil {
		if len(m.icon) == 0 {
			return fmt.Errorf("tray icon empty")
		}
		if m.app == nil || m.app.SystemTray == nil {
			return fmt.Errorf("app not ready")
		}
		t := m.app.SystemTray.New()
		t.SetLabel("SNI Spoofer")
		// Pre-Run seed: the bytes native run() consumes.
		t.SetIcon(m.icon)
		t.SetTooltip(m.tooltip)
		t.SetMenu(m.buildMenu())
		t.OnClick(func() { m.restoreWindow() })
		if m.win != nil {
			t.AttachWindow(m.win)
		}
		t.Run()
		m.tray = t
		m.ran = true
	}
	if enabled {
		m.tray.Show()
	} else {
		// NOTE: Show/Hide are no-ops before the native Run processes;
		// callers re-apply Ensure after ApplicationStarted (plus a delayed
		// re-apply) so a start-disabled tray never pops in visibly.
		m.tray.Hide()
	}
	return nil
}

// Shutdown removes the icon; further Ensure calls fail closed.
func (m *TrayManager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dead = true
	m.enabled = false
	if m.tray != nil {
		m.tray.Hide()
		m.tray.Destroy()
		m.tray = nil
	}
	m.ran = false
}

// log sinks manager logs to the Go logger. It deliberately does NOT route
// through trayLogInfo (which calls back into m.log when the manager
// exists — that would recurse).
func (m *TrayManager) log(level, msg string) {
	log.Printf("[tray][%s] %s", level, msg)
}

// defaultTrayLog is the pre-manager sink (startup paths before NewTrayManager,
// config-parse errors). Same format as TrayManager.log.
func defaultTrayLog(level, msg string) {
	log.Printf("[tray][%s] %s", level, msg)
}

func (m *TrayManager) buildMenu() *application.Menu {
	menu := application.NewMenu()
	show := menu.Add("Show")
	show.OnClick(func(*application.Context) { m.restoreWindow() })
	menu.AddSeparator()
	exit := menu.Add("Exit")
	exit.OnClick(func(*application.Context) { requestQuit(m.appRef()) })
	return menu
}

func (m *TrayManager) appRef() *application.App {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.app != nil {
		return m.app
	}
	return application.Get()
}

// restoreWindow brings the captured window back (click-to-restore).
func (m *TrayManager) restoreWindow() {
	w := m.window()
	if w == nil {
		return
	}
	if s, ok := w.(interface{ Show() application.Window }); ok {
		s.Show()
	}
}
