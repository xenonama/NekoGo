package main

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TrayCallbacks wires tray actions back into the App service. Callbacks run
// off the native UI thread, without controller locks. OnSelectProfile must
// SELECT only; connecting is exclusively OnConnect/OnReconnect's job. The
// service must still arbitrate connection races and publish the resulting state.
type TrayCallbacks struct {
	OnShow          func()
	OnConnect       func()
	OnReconnect     func()
	OnDisconnect    func() // Must cancel an in-flight connection when busy.
	OnSelectProfile func(id string)
	OnOpenProfiles  func() // Show/focus the window and open the full Proxies list.
	OnQuit          func()
}

func runTrayDisconnect(appSvc *App) {
	if err := appSvc.Disconnect(); err != nil {
		msg := fmt.Sprintf("[tray] ERROR disconnect: %v", err)
		appSvc.emitLog("tray", "error", msg)
		appSvc.noteError(msg)
	}
}

// TrayServer is one Profiles-submenu row. The name is retained for callers.
type TrayServer struct {
	ID   string
	Name string
}

// trayServerCap includes the selected profile, even when it is beyond the cap.
const trayServerCap = 25

// Keep this subset unchanged so existing App wiring and headless fakes work.
type trayController interface {
	Show()
	Hide()
	SetStatus(running bool, profileName, core string)
}

// trayNative keeps native publication testable without starting Wails. The
// fluent SetMenu return type is fixed by the beta.20 SystemTray API.
type trayNative interface {
	Show()
	Hide()
	SetLabel(string)
	SetTooltip(string)
	SetMenu(*application.Menu) *application.SystemTray
}

type trayConnectionState struct {
	running, busy            bool
	profileName, core        string
	canConnect, canReconnect bool
}

type trayAction uint8

const (
	trayShow trayAction = iota
	trayConnect
	trayReconnect
	trayDisconnect
	traySelectProfile
	trayOpenProfiles
	trayQuit
)

// TrayController owns the tray and its last published snapshot. mu serializes
// updates, visibility and action validation. Published menus are never mutated
// by us: Wails also writes radio checks on its native thread, outside our lock.
// Build detached replacements and install them through SystemTray.SetMenu.
type TrayController struct {
	mu      sync.RWMutex
	tray    trayNative
	logCh   chan LogEntry
	visible bool
	cb      TrayCallbacks

	menu                          *application.Menu
	servers                       *application.Menu
	statusItem, connectItem       *application.MenuItem
	reconnectItem, disconnectItem *application.MenuItem
	state                         trayConnectionState
	explicitState                 bool
	entries                       []TrayServer
	activeID                      string
}

// IsVisible reports the last Show/Hide state applied to the tray.
func (t *TrayController) IsVisible() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.visible
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

// SetupTray creates the tray before or after app.Run. In beta.20,
// SystemTray.New registers via runOrDeferToAppRun, covering both cases.
// Do NOT call tray.Run here: after startup that creates a duplicate icon.
func SetupTray(app *application.App, cb TrayCallbacks, logCh chan LogEntry) *TrayController {
	t := &TrayController{cb: cb, logCh: logCh}
	t.log("info", "[tray] creating system tray")
	native := app.SystemTray.New()
	t.tray = native
	// Windows gets the multi-size ICO (native 16x16 entry); elsewhere PNG.
	icon, iconKind := appIconPNG, "png"
	if runtime.GOOS == "windows" && len(appIconICO) > 0 {
		icon, iconKind = appIconICO, "ico"
	}
	if len(icon) > 0 {
		native.SetIcon(icon)
		t.log("info", fmt.Sprintf("[tray] set icon (%s, %d bytes)", iconKind, len(icon)))
	} else {
		t.log("error", "[tray] ERROR: embedded icon is empty, tray may show a fallback")
	}
	t.mu.Lock()
	t.rebuildLocked()
	t.visible = true
	t.mu.Unlock()
	native.OnClick(t.iconClick)
	t.log("info", "[tray] system tray created")
	return t
}

// Show/Hide preserve the icon and handlers, and serialize the native request
// with its recorded visibility (not merely the individual reads and writes).
func (t *TrayController) Show() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.visible {
		if t.tray != nil {
			t.tray.Show()
		}
		t.visible = true
	}
}

func (t *TrayController) Hide() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.visible {
		if t.tray != nil {
			t.tray.Hide()
		}
		t.visible = false
	}
}

// SetConnectionState publishes explicit service state. canConnect means a
// usable selected/fallback profile exists; canReconnect must include the
// reconnectMode != "off" policy. running/busy gates are enforced here too.
// RefreshServers supplies the independent profile-selection snapshot.
func (t *TrayController) SetConnectionState(running, busy bool, profileName, core string, canConnect, canReconnect bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.explicitState = true
	t.setStateLocked(trayConnectionState{running, busy, profileName, core, canConnect, canReconnect})
}

// SetStatus is the compatibility wrapper. Legacy callers cannot describe
// busy/reconnect policy, so reconnect stays disabled until explicitly supplied.
// Once the explicit API is used, this wrapper preserves its busy/policy flags.
func (t *TrayController) SetStatus(running bool, profileName, core string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.state
	s.running, s.profileName, s.core = running, profileName, core
	if !t.explicitState {
		s.canConnect = len(t.entries) > 0
	}
	t.setStateLocked(s)
}

func (t *TrayController) setStateLocked(s trayConnectionState) {
	if t.menu != nil && t.state == s {
		return
	}
	t.state = s
	t.rebuildLocked()
}

// RefreshServers copies the full profile snapshot. Callers may reuse their
// slice, and changes beyond the cap still update the overflow count.
func (t *TrayController) RefreshServers(entries []TrayServer, activeID string) {
	valid := make([]TrayServer, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.ID != "" && !seen[entry.ID] {
			valid = append(valid, entry)
			seen[entry.ID] = true
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.menu != nil && t.activeID == activeID && slices.Equal(t.entries, valid) {
		return
	}
	t.entries, t.activeID = valid, activeID
	if !t.explicitState {
		t.state.canConnect = len(valid) > 0
	}
	t.rebuildLocked()
}

func trayText(text string) string {
	text = strings.ReplaceAll(text, "\x00", " ")
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 48 {
		text = string(r[:47]) + "…"
	}
	return text
}

func (s trayConnectionState) status() string {
	status := "Stopped"
	switch {
	case s.busy && s.running:
		status = "Reconnecting"
	case s.busy:
		status = "Connecting"
	case s.running:
		status = "Connected"
	}
	if s.running || s.busy {
		if name := trayText(s.profileName); name != "" {
			status += " — " + name
		}
		if core := trayText(s.core); core != "" {
			status += " (" + core + ")"
		}
	}
	return status
}

func (t *TrayController) rebuildLocked() {
	old := t.menu
	menu := application.NewMenu()
	menu.Add("Show NekoGo").SetBitmap(trayShowIcon).OnClick(t.menuClick(trayShow, ""))
	t.statusItem = menu.Add("Status: " + t.state.status()).SetEnabled(false)
	menu.AddSeparator()
	t.connectItem = menu.Add("Connect selected").SetBitmap(trayConnectIcon).
		SetEnabled(!t.state.running && !t.state.busy && t.state.canConnect).
		OnClick(t.menuClick(trayConnect, ""))
	t.reconnectItem = menu.Add("Reconnect").SetBitmap(trayReconnectIcon).
		SetEnabled(t.state.running && !t.state.busy && t.state.canReconnect).
		OnClick(t.menuClick(trayReconnect, ""))
	disconnectLabel := "Disconnect"
	if t.state.busy {
		disconnectLabel = "Cancel connection"
	}
	t.disconnectItem = menu.Add(disconnectLabel).SetBitmap(trayDisconnectIcon).
		SetEnabled(t.state.running || t.state.busy).
		OnClick(t.menuClick(trayDisconnect, ""))
	menu.AddSeparator()
	t.servers = menu.AddSubmenu("Profiles")
	if item := menu.FindByLabel("Profiles"); item != nil {
		item.SetBitmap(trayProfilesIcon)
	}
	rows := slices.Clone(t.entries[:min(len(t.entries), trayServerCap)])
	for _, entry := range t.entries[len(rows):] {
		if entry.ID == t.activeID {
			rows[len(rows)-1] = entry
			break
		}
	}
	for _, entry := range rows {
		label := trayText(entry.Name)
		if label == "" {
			label = trayText(entry.ID)
		}
		t.servers.AddRadio(label, entry.ID == t.activeID).SetEnabled(!t.state.busy).
			OnClick(t.menuClick(traySelectProfile, entry.ID))
	}
	if len(rows) == 0 {
		t.servers.Add("No profiles").SetEnabled(false)
	}
	hidden := len(t.entries) - len(rows)
	if hidden > 0 {
		t.servers.AddSeparator()
		t.servers.Add(fmt.Sprintf("Showing %d of %d profiles (%d more)", len(rows), len(t.entries), hidden)).SetEnabled(false)
		t.servers.Add("Open Proxies").SetBitmap(trayProfilesIcon).OnClick(t.menuClick(trayOpenProfiles, ""))
	}
	menu.AddSeparator()
	menu.Add("Quit NekoGo").SetBitmap(trayQuitIcon).OnClick(t.menuClick(trayQuit, ""))
	t.menu = menu
	if t.tray != nil {
		label := "NekoGo: " + t.state.status()
		t.tray.SetLabel(label)
		t.tray.SetTooltip(label) // Windows does not display SystemTray.SetLabel.
		// beta.20: SetMenu marshals to the UI thread and refreshes the native
		// tray menu (Win32 popup / macOS menu / Linux DBus layout). Merely
		// Clear/Add or submenu.Update does not update the Windows tray popup.
		t.tray.SetMenu(menu)
	}
	// SetMenu released the old native popup. Clear only the Go registrations;
	// Destroy would try to destroy native items belonging to that old popup.
	if old != nil {
		if profiles := old.FindByLabel("Profiles"); profiles != nil {
			profiles.GetSubmenu().Clear()
		}
		old.Clear()
	}
	t.log("info", "[tray] set label ok")
	t.log("info", "[tray] set menu (10 items)")
	t.log("info", fmt.Sprintf("[tray] profiles menu rebuilt (%d rows, %d hidden)", len(rows), hidden))
}

// beta.20 MenuItem.handleClick already spawns a goroutine, but Windows tray
// icon clicks run directly in wndProc. Keep both adapters nonblocking even
// when called directly: neither waits for mu nor executes service work on UI.
func (t *TrayController) menuClick(action trayAction, id string) func(*application.Context) {
	return func(*application.Context) { go t.invoke(action, id) }
}

func (t *TrayController) iconClick() {
	go func() {
		t.log("info", "[tray] callback: click")
		t.invoke(trayShow, "")
	}()
}

// invoke validates against the latest snapshot, not the snapshot at menu-open
// time. Never hold mu across callbacks: they may synchronously publish state.
// It returns whether an action was dispatched, for headless tests as well.
func (t *TrayController) invoke(action trayAction, id string) bool {
	t.mu.RLock()
	var callback func()
	var message string
	s := t.state
	switch action {
	case trayShow:
		callback, message = t.cb.OnShow, "[tray] tray menu: show"
	case trayConnect:
		if !s.running && !s.busy && s.canConnect {
			callback, message = t.cb.OnConnect, "[tray] callback: connect"
		}
	case trayReconnect:
		if s.running && !s.busy && s.canReconnect {
			callback, message = t.cb.OnReconnect, "[tray] callback: reconnect"
		}
	case trayDisconnect:
		if s.running || s.busy {
			callback, message = t.cb.OnDisconnect, "[tray] callback: disconnect"
		}
	case traySelectProfile:
		if !s.busy && t.cb.OnSelectProfile != nil {
			for _, entry := range t.entries {
				if entry.ID == id {
					selectProfile := t.cb.OnSelectProfile
					callback = func() { selectProfile(id) }
					message = fmt.Sprintf("[tray] callback: select %s", id)
					break
				}
			}
		}
	case trayOpenProfiles:
		callback, message = t.cb.OnOpenProfiles, "[tray] tray menu: open profiles"
	case trayQuit:
		callback, message = t.cb.OnQuit, "[tray] tray menu: quit"
	}
	t.mu.RUnlock()
	if callback == nil {
		return false
	}
	t.log("info", message)
	callback()
	return true
}
