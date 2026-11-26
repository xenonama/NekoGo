//go:build windows

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

const singleInstanceScope = "NekoGo.SingleInstance.v1"
const singleInstanceParentWait = 30 * time.Second

var errSingleInstanceParentTimeout = errors.New("timed out waiting for elevation parent to exit")

// Wails beta.20's SingleInstance check happens in application.New, after our
// config and crash recovery. Its WM_COPYDATA notification also crosses UIPI.
// Use owned kernel objects instead; no window messages or commands are forwarded.
// A scope parameter keeps tests entirely outside the production namespace.
type singleInstance struct {
	release   chan struct{}
	done      chan struct{}
	ready     chan struct{}
	closeOnce sync.Once
	readyOnce sync.Once
	closed    atomic.Bool
	activate  func() // published by closing ready
	closeErr  error  // published by closing done
}

func acquireSingleInstance(scope string, args []string) (*singleInstance, error) {
	pid, err := parseWaitForParentArg(args)
	if err != nil {
		return nil, err
	}
	if pid != 0 {
		if err := waitForSingleInstanceParent(pid, singleInstanceParentWait); err != nil {
			return nil, err
		}
	}
	// --connect and --wait-for-parent NEVER grant ownership. Even a valid wait
	// only delays this same zero-timeout acquisition; a third launch can win.
	g := &singleInstance{
		release: make(chan struct{}),
		done:    make(chan struct{}),
		ready:   make(chan struct{}),
	}
	acquired := make(chan bool, 1)
	go func() {
		// Windows mutex ownership belongs to a native thread, not a goroutine.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		g.closeErr = g.own(scope, acquired)
		close(g.done)
	}()
	if !<-acquired {
		<-g.done
		return nil, g.closeErr // nil means the existing owner was signalled
	}
	return g, nil
}

// singleInstanceObjects uses the process token (not an impersonation token).
// SID + session are unchanged by same-user UAC and independent of exe path.
func singleInstanceObjects(scope string) (string, string, *windows.SecurityAttributes, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", "", nil, fmt.Errorf("single instance user: %w", err)
	}
	var session, size uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &size); err != nil {
		return "", "", nil, fmt.Errorf("single instance session: %w", err)
	}
	sid := user.User.Sid.String()
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", scope, sid, session)))
	base := fmt.Sprintf("Local\\NekoGo.SingleInstance.%x", identity)
	// Only these newly created objects get this descriptor. An explicit user
	// DACL works with both halves of a split UAC token; a medium label allows
	// a normal launch to signal an elevated owner's event. No global ACL,
	// registry, message-filter, or machine configuration changes are needed.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + sid + ")S:(ML;;NW;;;ME)")
	if err != nil {
		return "", "", nil, fmt.Errorf("single instance security: %w", err)
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	return base + ".mutex", base + ".activate", sa, nil
}

func (g *singleInstance) own(scope string, acquired chan<- bool) (err error) {
	reported := false
	defer func() {
		if !reported {
			acquired <- false
		}
	}()
	mutexName, eventName, sa, err := singleInstanceObjects(scope)
	if err != nil {
		return err
	}
	// Create/open the event BEFORE competing for the mutex. Thus any winner
	// already holds the event when a duplicate signals it, even before UI init.
	event, err := windows.CreateEventEx(sa, windows.StringToUTF16Ptr(eventName), 0, windows.SYNCHRONIZE|windows.EVENT_MODIFY_STATE)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("single instance event: %w", err)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(event)) }()
	mutex, err := windows.CreateMutexEx(sa, windows.StringToUTF16Ptr(mutexName), 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("single instance mutex: %w", err)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(mutex)) }()
	status, err := windows.WaitForSingleObject(mutex, 0)
	if err != nil {
		return fmt.Errorf("single instance acquisition: %w", err)
	}
	switch status {
	case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
		defer func() { err = errors.Join(err, windows.ReleaseMutex(mutex)) }()
	case uint32(windows.WAIT_TIMEOUT):
		if err := windows.SetEvent(event); err != nil {
			return fmt.Errorf("single instance activation: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("single instance unexpected mutex wait: %#x", status)
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return fmt.Errorf("single instance stop event: %w", err)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(stop)) }()
	monitorDone := make(chan error, 1)
	go func() { monitorDone <- g.monitor(event, stop) }()
	reported = true
	acquired <- true
	<-g.release
	// Handles remain alive until the waiter stops; release/close is performed
	// only here, on the same locked thread that acquired the mutex.
	stopErr := windows.SetEvent(stop)
	return errors.Join(stopErr, <-monitorDone)
}

func (g *singleInstance) monitor(event, stop windows.Handle) error {
	// The auto-reset event retains ONE activation during all startup work.
	// Do not consume it before WindowRuntimeReady: repeated early launches
	// coalesce in the kernel, with no polling or unbounded callback queue.
	select {
	case <-g.release:
		return nil
	case <-g.ready:
	}
	for {
		status, err := windows.WaitForMultipleObjects([]windows.Handle{stop, event}, false, windows.INFINITE)
		if err != nil {
			// Retain the mutex even on monitor failure (fail closed).
			fmt.Fprintln(os.Stderr, "single instance activation monitor:", err)
			return err
		}
		switch status {
		case windows.WAIT_OBJECT_0:
			return nil
		case windows.WAIT_OBJECT_0 + 1:
			if !g.closed.Load() {
				g.activate()
			}
		default:
			return fmt.Errorf("single instance unexpected event wait: %#x", status)
		}
	}
}

func (g *singleInstance) onWindowReady(activate func()) {
	g.readyOnce.Do(func() {
		g.activate = activate
		close(g.ready)
	})
}

func (g *singleInstance) watchWindow(window *application.WebviewWindow) {
	window.RegisterHook(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		g.onWindowReady(func() {
			application.InvokeAsync(func() {
				if !g.closed.Load() {
					window.Show()
					window.UnMinimise()
					window.Focus()
				}
			})
		})
	})
}

func (g *singleInstance) Close() error {
	g.closeOnce.Do(func() {
		g.closed.Store(true)
		close(g.release)
	})
	<-g.done
	return g.closeErr
}

func parseWaitForParentArg(args []string) (uint32, error) {
	var pid uint32
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var value string
		switch {
		case arg == "--wait-for-parent":
			i++
			if i == len(args) {
				return 0, errors.New("--wait-for-parent requires a PID")
			}
			value = args[i]
		case strings.HasPrefix(arg, "--wait-for-parent="):
			value = strings.TrimPrefix(arg, "--wait-for-parent=")
		default:
			continue
		}
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || parsed == 0 || parsed == uint64(os.Getpid()) || pid != 0 {
			return 0, errors.New("--wait-for-parent requires one positive PID other than this process")
		}
		pid = uint32(parsed)
	}
	return pid, nil
}

func waitForSingleInstanceParent(pid uint32, timeout time.Duration) error {
	if pid == 0 || pid == uint32(os.Getpid()) || timeout <= 0 || timeout > time.Hour {
		return errors.New("invalid single instance parent wait")
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil // parent already exited; still acquire the mutex normally
	}
	if err != nil {
		return fmt.Errorf("open elevation parent: %w", err)
	}
	defer windows.CloseHandle(process)
	millis := uint32((timeout + time.Millisecond - 1) / time.Millisecond)
	status, err := windows.WaitForSingleObject(process, millis)
	if err != nil {
		return fmt.Errorf("wait for elevation parent: %w", err)
	}
	switch status {
	case windows.WAIT_OBJECT_0:
		return nil
	case uint32(windows.WAIT_TIMEOUT):
		return errSingleInstanceParentTimeout
	default:
		return fmt.Errorf("unexpected elevation parent wait: %#x", status)
	}
}
