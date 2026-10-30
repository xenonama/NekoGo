package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var distFS embed.FS

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	// GC tuning: the GUI idles at ~35 MB RSS (measured) with a 1 s
	// poll, so the default GOGC=100 collects more often than it needs
	// to. A relaxed target trades a little RSS for less GC CPU, and the
	// soft memory limit keeps a runaway allocation from growing
	// without bound (512 MB is ~9x the measured private footprint).
	debug.SetGCPercent(200)
	debug.SetMemoryLimit(512 << 20)

	logCh := make(chan LogEntry, 512)

	store, err := NewConfigStore("")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	appSvc := NewApp(store, logCh)

	// HOTFIX (zombie xray): a crashed session leaves xray.exe /
	// nekobox_core.exe holding the ports and blocking the next Connect.
	// Reap strays before the first Connect is possible. Conservative by
	// construction (bin-dir + dead-parent checks inside); never fails
	// startup — failures degrade to a [startup] log line.
	//
	// The scan spawns wmic + tasklist (~420 ms measured), so it runs in
	// the background: the window appears immediately. Connect waits for
	// the scan (bounded, see App.waitOrphanScan), so a stray core can
	// still never hold a port into the first Connect.
	// HOTFIX (zombie xray): a crashed session leaves xray.exe /
	// nekobox_core.exe holding the ports and blocking the next Connect.
	// Reap strays before the first Connect is possible. Conservative by
	// construction (bin-dir + dead-parent checks inside); never fails
	// startup — failures degrade to a [startup] log line.
	//
	// The scan spawns wmic + tasklist (~420 ms measured), so it runs in
	// the background: the window appears immediately. Connect waits for
	// the scan (bounded, see App.waitOrphanScan), so a stray core can
	// still never hold a port into the first Connect.
	orphanScanDone := make(chan struct{})
	appSvc.setOrphanScanDone(orphanScanDone)
	if exe, err := os.Executable(); err == nil {
		go func() {
			defer close(orphanScanDone)
			cleanupOrphanCores(logCh, filepath.Dir(exe))
		}()
	} else {
		close(orphanScanDone)
	}

	// Crash recovery for the GUI-side system proxy: if our marker file
	// exists, a previous session died with the OS proxy set. Clear it
	// best-effort before any Connect. Only acts when the marker exists.
	cleanupStaleSysproxy()

	sub, err := fs.Sub(distFS, "frontend/dist")
	if err != nil {
		return fmt.Errorf("frontend dist: %w", err)
	}

	app := application.New(application.Options{
		Name:        "NekoGo",
		Description: "NekoGo — a NekoBox fork with sing-box + Xray cores and a Wails3 GUI",
		Icon:        appIconPNG,
		Services: []application.Service{
			application.NewService(appSvc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(sub),
		},
		OnShutdown: func() {
			appSvc.Shutdown()
		},
	})
	appSvc.AttachWailsApp(app)

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "NekoGo",
		Width:     1280,
		Height:    720,
		MinWidth:  940,
		MinHeight: 600,
		URL:       "/",
	})

	// CASE A (beta.20): RegisterHook runs synchronously before the
	// framework's internal destroy-listener; event.Cancel() aborts the
	// close so the window can hide to tray instead. On Windows the OS
	// already swallows WM_CLOSE (w32 return 0) and only emits
	// Common.WindowClosing, so Cancel() is sufficient — no native
	// veto needed.
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		appSvc.emitLog("tray", "info", "[tray] window close intercepted")
		if appSvc.ShouldHideOnClose() {
			appSvc.emitLog("tray", "info", "[tray] closeToTray=true -> hiding")
			event.Cancel()
			window.Hide()
		} else {
			appSvc.emitLog("tray", "info", "[tray] closeToTray=false -> quitting")
		}
	})

	buildTray := func() trayController {
		return SetupTray(app, TrayCallbacks{
			OnShow: func() {
				window.Show()
				window.Focus()
				appSvc.emitLog("tray", "info", "[tray] window shown and focused")
			},
			OnConnect: func() {
				// A Connect is already probing (auto-connect or window):
				// skip instead of queueing a second Start behind it whose
				// stop would kill the just-established core (INT BUG-001).
				if appSvc.IsConnectBusy() {
					appSvc.emitLog("tray", "info", "[tray] connect skipped: connect already in flight")
					return
				}
				id := store.GetSettings().ActiveProfileID
				if id == "" {
					if profiles := store.ListProfiles(); len(profiles) > 0 {
						id = profiles[0].ID
					}
				}
				if id != "" {
					if err := appSvc.Connect(id); err != nil {
						msg := fmt.Sprintf("[tray] ERROR connect: %v", err)
						appSvc.emitLog("tray", "error", msg)
						appSvc.noteError(msg)
					}
				}
			},
			OnDisconnect: func() {
				// Disconnect during a probe aborts it promptly via the
				// CoreManager epoch (no lock pile-up); still honor the
				// busy flag so a double-click cannot interleave two stops.
				if appSvc.IsConnectBusy() {
					appSvc.emitLog("tray", "info", "[tray] disconnect skipped: connect in flight, use Stop once settled")
					return
				}
				if err := appSvc.Disconnect(); err != nil {
					msg := fmt.Sprintf("[tray] ERROR disconnect: %v", err)
					appSvc.emitLog("tray", "error", msg)
					appSvc.noteError(msg)
				}
			},
			OnSelectProfile: func(id string) {
				// Servers-submenu pick: same guarded path as Connect,
				// but for the chosen id (persists selection first so
				// restart/auto-connect agree with the tray).
				if appSvc.IsConnectBusy() {
					appSvc.emitLog("tray", "info", "[tray] select skipped: connect already in flight")
					return
				}
				if _, ok := store.GetProfile(id); !ok {
					appSvc.emitLog("tray", "error", fmt.Sprintf("[tray] ERROR select: profile %q vanished", id))
					return
				}
				if err := store.SetActiveProfileID(id); err != nil {
					appSvc.emitLog("tray", "error", fmt.Sprintf("[tray] ERROR select persist: %v", err))
					return
				}
				if err := appSvc.Connect(id); err != nil {
					msg := fmt.Sprintf("[tray] ERROR connect: %v", err)
					appSvc.emitLog("tray", "error", msg)
					appSvc.noteError(msg)
				}
			},
			OnQuit: func() {
				appSvc.Shutdown()
				app.Quit()
			},
		}, logCh)
	}
	// No create-then-hide flash: with closeToTray=false the tray is
	// never built, so no icon appears at all on a fresh launch.
	appSvc.attachTray(setupTrayIfEnabled(appSvc, buildTray))
	appSvc.setTrayBuilder(buildTray)
	// Fill the Servers submenu once at startup (refreshTray keeps it
	// in sync on every connect/disconnect afterwards).
	appSvc.refreshTray()

	// Elevated-child resume: the unelevated parent relaunched us with
	// `--connect <id>` (see ensureElevatedForConnect/RelaunchAsAdmin)
	// so the pending connect continues here, elevated. Takes
	// precedence over the AutoConnect setting below; an unknown id
	// is logged, never fatal to startup.
	if connectID := parseConnectArg(os.Args[1:]); connectID != "" {
		if _, ok := store.GetProfile(connectID); !ok {
			appSvc.emitLog("gui", "error", fmt.Sprintf("[startup] ERROR elevated resume: profile %q not found", connectID))
		} else if err := appSvc.Connect(connectID); err != nil {
			msg := fmt.Sprintf("[startup] ERROR elevated resume connect: %v", err)
			appSvc.emitLog("gui", "error", msg)
			appSvc.noteError(msg)
		}
	} else if settings := store.GetSettings(); settings.AutoConnect && settings.ActiveProfileID != "" {
		if err := appSvc.Connect(settings.ActiveProfileID); err != nil {
			msg := fmt.Sprintf("[startup] ERROR auto-connect: %v", err)
			appSvc.emitLog("gui", "error", msg)
			appSvc.noteError(msg)
		}
	}

	return app.Run()
}
