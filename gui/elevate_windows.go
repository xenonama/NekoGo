//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// Elevated relaunch (Windows): privileged driver operations require
// an elevated process. Instead of failing the Connect with
// "run as admin", NekoGo relaunches ITSELF elevated
// (UAC prompt) and the child resumes the pending connect, so one
// click connects. Only stdlib + LazyDLL (same pattern
// as refreshWinINET in sysproxy_windows.go).

// quoteElevateArgs joins argv for ShellExecute: args with whitespace
// are double-quoted (profile ids are generated hex, but quote anyway
// so a crafted id can never split the command line).
func quoteElevateArgs(args []string) string {
	q := make([]string, 0, len(args))
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"") {
			q = append(q, `"`+strings.ReplaceAll(a, `"`, ``)+`"`)
			continue
		}
		q = append(q, a)
	}
	return strings.Join(q, " ")
}

// relaunchElevated requests an elevated copy of the current exe with
// the given args via ShellExecute "runas". Nil means the UAC request
// was accepted — the caller must then stop its cores and exit
// promptly so two instances never hold the same ports. A declined
// UAC prompt (or any failure) returns an error and the caller keeps
// running unelevated. A variable (not a plain func) so tests can stub
// the spawn without a UAC prompt.
var relaunchElevated = func(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe path: %w", err)
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(quoteElevateArgs(args))
	dir, _ := syscall.UTF16PtrFromString("")
	proc := syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")
	ret, _, _ := proc.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(dir)),
		uintptr(1), // SW_SHOWNORMAL
	)
	// ShellExecute returns > 32 on success; anything at or below is
	// an error code (including a user-declined UAC prompt).
	if ret <= 32 {
		return fmt.Errorf("elevation request failed (code %d): approve the UAC prompt or right-click NekoGo and choose Run as administrator", ret)
	}
	return nil
}

// guiElevated reports whether THIS process runs elevated. A variable
// (not a direct isGuiElevated call) so tests can force either side
// without touching the process token.
var guiElevated = func() (bool, error) {
	return isGuiElevated()
}

// elevateExiter ends the unelevated parent after a successful
// elevated relaunch. A variable so tests can observe the relaunch
// without killing the test process.
var elevateExiter = func(code int) {
	os.Exit(code)
}
