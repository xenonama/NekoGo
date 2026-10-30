package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TrayCallbacks wires tray menu actions back into the App service.
type TrayCallbacks struct {
	OnShow          func()
	OnConnect       func()
	OnDisconnect    func()
	OnSelectProfile func(id string)
	OnQuit          func()
}

// TrayServer is one Servers-submenu row (Throne parity: tray picker
// with the active profile checked).
type TrayServer struct {
	ID   string
	Name string
}

// trayServerCap bounds the Servers submenu (menus stay usable with
// hundreds of profiles in the store).
const trayServerCap = 25

// trayController is the Show/Hide/SetStatus subset of TrayController,
// extracted so visibility toggling is unit-testable headless.
type trayController interface {
	Show()
	Hide()
	SetStatus(running bool, profileName, core string)
}

// TrayController owns the system-tray icon, label and menu.
//
// BUG-042: visible is guarded by mu (tray callbacks and SetCloseToTray
// race under -race) and actually read via IsVisible instead of being a
// write-only field; native tray state remains authoritative for the OS,
// this only tracks what we last asked for.
type TrayController struct {
	mu      sync.RWMutex
	tray    *application.SystemTray
	logCh   chan LogEntry
	visible bool
	cb      TrayCallbacks
	servers *application.Menu
}

// IsVisible reports the last Show/Hide state applied to the tray.
func (t *TrayController) IsVisible() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.visible
}

func (t *TrayController) setVisible(v bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.visible = v
}

func (t *TrayController) log(level, msg string) {
	if t.logCh == nil {
		return
	}
	select {
	case t.logCh <- LogEntry{Level: level, Source: "tray", Message: msg, Time: time.Now().Format(time.RFC3339)}:
	default:
		// Never block the tray callbacks on a full channel.
	}
}

// SetupTray creates the tray with Connect/Disconnect/Show/Quit items.
//
// NOTE (beta.20 lifecycle): app.SystemTray.New() registers the tray via
// runOrDeferToAppRun, which covers BOTH paths — deferred to startup when
// called before app.Run(), and immediate when called later (e.g. the
// tray rebuild on a runtime closeToTray flip). Do NOT call tray.Run()
// here: pre-Run it is a silent no-op, but post-Run it would execute a
// SECOND full run, creating a duplicate native icon while leaking the
// first impl (stale icons, dead menus, blank slots).
func SetupTray(app *application.App, cb TrayCallbacks, logCh chan LogEntry) *TrayController {
	t := &TrayController{logCh: logCh}
	t.log("info", "[tray] creating system tray")
	tray := app.SystemTray.New()
	tray.SetLabel("NekoGo: stopped")
	t.log("info", "[tray] set label ok")
	// Windows gets the multi-size ICO (native 16x16 entry); everywhere
	// else the PNG. Either way the bytes must be valid image data —
	// an empty icon means a blank notification-area slot.
	icon := appIconPNG
	iconKind := "png"
	if runtime.GOOS == "windows" && len(appIconICO) > 0 {
		icon = appIconICO
		iconKind = "ico"
	}
	if len(icon) > 0 {
		tray.SetIcon(icon)
		t.log("info", fmt.Sprintf("[tray] set icon (%s, %d bytes)", iconKind, len(icon)))
	} else {
		t.log("error", "[tray] ERROR: embedded icon is empty, tray may show a fallback")
	}
	menu := application.NewMenu()
	items := 0
	menu.Add("Show NekoGo").OnClick(func(*application.Context) {
		t.log("info", "[tray] tray menu: show")
		if cb.OnShow != nil {
			cb.OnShow()
		}
	})
	items++
	menu.Add("Connect").OnClick(func(*application.Context) {
		t.log("info", "[tray] callback: connect")
		if cb.OnConnect != nil {
			cb.OnConnect()
		}
	})
	items++
	menu.Add("Disconnect").OnClick(func(*application.Context) {
		t.log("info", "[tray] callback: disconnect")
		if cb.OnDisconnect != nil {
			cb.OnDisconnect()
		}
	})
	items++
	t.cb = cb
	t.servers = menu.AddSubmenu("Servers")
	items++
	menu.Add("Quit").OnClick(func(*application.Context) {
		t.log("info", "[tray] tray menu: quit")
		if cb.OnQuit != nil {
			cb.OnQuit()
		}
	})
	items++
	tray.SetMenu(menu)
	t.log("info", fmt.Sprintf("[tray] set menu (%d items)", items))
	tray.OnClick(func() {
		t.log("info", "[tray] callback: click")
		if cb.OnShow != nil {
			cb.OnShow()
		}
	})
	// No explicit tray.Run(): New() above already ran or deferred it.
	t.log("info", "[tray] system tray created")
	t.tray = tray
	t.setVisible(true)
	return t
}

// Show re-displays a hidden tray icon. Cheap on Windows (single
// NIM_MODIFY clearing NIS_HIDDEN); icon, menu and handlers are kept.
func (t *TrayController) Show() {
	if t.IsVisible() {
		return
	}
	if t.tray == nil {
		t.setVisible(true)
		return
	}
	t.tray.Show()
	t.setVisible(true)
}

// Hide removes the tray icon from the notification area without
// destroying it, so Show() can bring it back with everything intact.
func (t *TrayController) Hide() {
	if !t.IsVisible() {
		return
	}
	if t.tray == nil {
		t.setVisible(false)
		return
	}
	t.tray.Hide()
	t.setVisible(false)
}

// SetStatus refreshes the tray label from connection state.
func (t *TrayController) SetStatus(running bool, profileName, core string) {
	if t.tray == nil {
		return
	}
	if running {
		t.tray.SetLabel(fmt.Sprintf("NekoGo: %s (%s)", profileName, core))
	} else {
		t.tray.SetLabel("NekoGo: stopped")
	}
	t.log("info", "[tray] set label ok")
}

// RefreshServers rebuilds the Servers submenu (capped): the active
// profile renders checked. Called with the submenu handle under mu;
// a nil submenu (unit-test controller) is a no-op. Long names are
// truncated so one remark cannot stretch the menu.
func (t *TrayController) RefreshServers(entries []TrayServer, activeID string) {
	t.mu.Lock()
	servers, cb := t.servers, t.cb
	t.mu.Unlock()
	if servers == nil {
		return
	}
	servers.Clear()
	n := 0
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		label := e.Name
		if label == "" {
			label = e.ID
		}
		if r := []rune(label); len(r) > 48 {
			label = string(r[:47]) + "…"
		}
		id := e.ID
		servers.AddRadio(label, id == activeID).OnClick(func(*application.Context) {
			t.log("info", fmt.Sprintf("[tray] callback: select %s", id))
			if cb.OnSelectProfile != nil {
				cb.OnSelectProfile(id)
			}
		})
		if n++; n >= trayServerCap {
			break
		}
	}
	if n == 0 {
		servers.Add("No servers").SetEnabled(false)
	}
	t.log("info", fmt.Sprintf("[tray] servers menu rebuilt (%d rows)", n))
}
