//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// procJob is a Windows job object wrapping one engine spawn. Every
// descendant joins the job, so terminating it (or closing its last
// handle) ends the whole tree — psiphon-tunnel-core.exe included —
// even when the supervisor aether.exe has already exited or was
// killed. taskkill /T alone cannot do that once the parent link is
// gone, which is how a dead aether left a live Psiphon behind.
//
// KILL_ON_JOB_CLOSE also means a GUI crash drops its handle and the
// engines die with it instead of surviving as orphans.
type procJob struct {
	handle windows.Handle
}

// newProcJob creates a job that kills its members on last handle
// close. A nil return (unsupported/failed) degrades to the taskkill
// tree kill the managers already perform.
func newProcJob() *procJob {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(h)
		return nil
	}
	return &procJob{handle: h}
}

// attach puts the running child into the job. Best effort: a failure
// (e.g. the process already belongs to a non-breakaway job) leaves the
// taskkill path in charge.
func (j *procJob) attach(pid int) {
	if j == nil || pid <= 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(j.handle, h)
}

// kill terminates every process still in the job.
func (j *procJob) kill() {
	if j == nil {
		return
	}
	_ = windows.TerminateJobObject(j.handle, 1)
}

// close releases the handle; with KILL_ON_JOB_CLOSE any remaining
// member dies with it.
func (j *procJob) close() {
	if j == nil {
		return
	}
	_ = windows.CloseHandle(j.handle)
}
