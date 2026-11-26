//go:build windows

package main

// The tray menu is a native Win32 popup: Windows draws it, not the
// WebView, so the dark theme in theme.css cannot reach it. What Windows
// does follow is the process's "preferred app mode", which the two
// uxtheme exports below set — the same calls every dark-mode Win32 app
// makes for its menus. Both are exported by ordinal only, which
// GetProcAddress resolves when it is handed a small integer instead of a
// name.

import (
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	// SetPreferredAppMode arguments, in uxtheme's own order.
	appModeForceDark  = 2
	appModeForceLight = 3

	// uxtheme's ordinals for SetPreferredAppMode and FlushMenuThemes.
	ordinalSetPreferredAppMode = 135
	ordinalFlushMenuThemes     = 136
)

var (
	uxthemeDLL         = windows.NewLazySystemDLL("uxtheme.dll")
	kernel32DLL        = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcAddress = kernel32DLL.NewProc("GetProcAddress")
)

// uxthemeOrdinal resolves one of the exports uxtheme publishes by
// ordinal only, reporting false when this Windows build has no such
// function.
func uxthemeOrdinal(ordinal uintptr) (uintptr, bool) {
	if err := uxthemeDLL.Load(); err != nil {
		return 0, false
	}
	addr, _, _ := procGetProcAddress.Call(uxthemeDLL.Handle(), ordinal)
	return addr, addr != 0
}

// applyTrayTheme makes the tray menu follow the app's theme: a dark menu
// for the dark theme, a light one for the light theme. Windows keeps the
// mode per process and FlushMenuThemes repaints menus that already
// exist, so switching themes needs no restart and no tray rebuild. The
// whole thing is best-effort: if a call is missing the menu simply stays
// the colour it always was, which must never fail a save or a startup.
func applyTrayTheme(theme string) {
	mode := uintptr(appModeForceLight)
	if theme == "dark" {
		mode = appModeForceDark
	}
	if addr, ok := uxthemeOrdinal(ordinalSetPreferredAppMode); ok {
		_, _, _ = syscall.SyscallN(addr, mode)
	}
	if addr, ok := uxthemeOrdinal(ordinalFlushMenuThemes); ok {
		_, _, _ = syscall.SyscallN(addr)
	}
}
