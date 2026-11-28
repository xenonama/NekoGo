//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const selfUpdateApplyFlag = "--apply-update"

type selfUpdateReady struct {
	Status string `json:"status"`
	PID    int    `json:"pid"`
}

type selfUpdateResult struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// These variables are narrow test seams. Production uses the real executable,
// the production mutex namespace, and the implementations below.
var (
	selfUpdateExecutable         = os.Executable
	selfUpdateReadyWait          = 30 * time.Second
	selfUpdateParentWait         = 5 * time.Minute
	selfUpdateMutexScope         = singleInstanceScope
	selfUpdateExpectedTargetName = "nekogo.exe"
	selfUpdateRelaunch           = relaunchSelfUpdateTarget
	selfUpdateReplaceFile        = replaceSelfUpdateFile
	selfUpdateWriteResult        = writeSelfUpdateResult
)

func init() {
	// main.go stays portable; Windows replaces the no-op dispatcher before run.
	runSelfUpdateAtStartup = runSelfUpdateHelper
}

// parseSelfUpdateArgs distinguishes ordinary GUI arguments from helper mode.
// Once --apply-update appears, exactly one job and no other argument is legal.
func parseSelfUpdateArgs(args []string) (job string, handled bool, err error) {
	found := false
	for _, arg := range args {
		if arg == selfUpdateApplyFlag || strings.HasPrefix(arg, selfUpdateApplyFlag+"=") {
			found = true
			break
		}
	}
	if !found {
		return "", false, nil
	}
	if len(args) == 2 && args[0] == selfUpdateApplyFlag {
		job = args[1]
	} else if len(args) == 1 && strings.HasPrefix(args[0], selfUpdateApplyFlag+"=") {
		job = strings.TrimPrefix(args[0], selfUpdateApplyFlag+"=")
	} else {
		return "", true, errors.New("--apply-update requires exactly one job and no other arguments")
	}
	if job == "" || strings.HasPrefix(job, selfUpdateApplyFlag) {
		return "", true, errors.New("--apply-update requires one non-empty job path")
	}
	return job, true, nil
}

func runSelfUpdateHelper(args []string) (bool, error) {
	job, handled, err := parseSelfUpdateArgs(args)
	if err != nil || !handled {
		return handled, err
	}
	return true, applySelfUpdateJob(job)
}

func applySelfUpdateJob(job string) (err error) {
	plan, err := loadSelfUpdatePlan(job)
	if err != nil {
		return err
	}
	// The job is known to be a regular file at this point, so it is safe to
	// remove even when a later plan field fails validation.
	defer os.Remove(job)

	helperExe, err := selfUpdateExecutable()
	if err != nil {
		return fmt.Errorf("resolve self-update helper: %w", err)
	}
	// A running Windows image cannot normally delete itself, but making the
	// best-effort attempt is harmless and works on handles that share delete.
	defer os.Remove(helperExe)

	plan, err = normalizeSelfUpdatePlan(plan, job, helperExe, true, false)
	if err != nil {
		return err
	}

	var (
		parent        windows.Handle
		guard         *singleInstance
		replacement   string
		readyWritten  bool
		resultWritten bool
		replaced      bool
	)
	backup := plan.Target + ".bak"
	defer func() {
		if err != nil {
			result := selfUpdateResult{Status: "failed", Version: plan.Version, Error: err.Error()}
			if writeErr := selfUpdateWriteResult(plan.ResultPath, result); writeErr != nil {
				err = errors.Join(err, fmt.Errorf("write self-update failure result: %w", writeErr))
			} else {
				resultWritten = true
			}
		}
		if guard != nil {
			err = errors.Join(err, guard.Close())
		}
		if parent != 0 {
			err = errors.Join(err, windows.CloseHandle(parent))
		}
		if replacement != "" {
			_ = os.Remove(replacement)
		}
		if replaced {
			_ = os.Remove(backup)
		}
		_ = os.Remove(plan.Source)
		_ = os.Remove(job)
		if readyWritten {
			_ = os.Remove(plan.ReadyPath)
		}
		if resultWritten {
			_ = os.Remove(plan.ResultPath)
		}
	}()

	if err = verifySelfUpdateFile(plan.Source, plan.SourceSize, plan.SourceSHA256); err != nil {
		return fmt.Errorf("verify self-update source: %w", err)
	}
	// Stage on the target volume before readiness. ReplaceFileW can then perform
	// one atomic replacement even when the updater's download directory is on a
	// different volume.
	replacement, err = stageSelfUpdateSource(plan)
	if err != nil {
		return fmt.Errorf("stage self-update source: %w", err)
	}
	// Open the parent before publishing readiness. The parent cannot exit until
	// startSelfUpdateHelper returns, so this handle identifies the actual parent
	// rather than a later process that happens to reuse its PID.
	parent, err = openSelfUpdateParent(plan.ParentPID)
	if err != nil {
		return fmt.Errorf("open self-update parent %d: %w", plan.ParentPID, err)
	}
	if err = writeSelfUpdateAtomicJSON(plan.ReadyPath, selfUpdateReady{Status: "ready", PID: os.Getpid()}); err != nil {
		return fmt.Errorf("publish self-update readiness: %w", err)
	}
	readyWritten = true

	if err = waitSelfUpdateParent(parent, selfUpdateParentWait); err != nil {
		return fmt.Errorf("wait for self-update parent %d: %w", plan.ParentPID, err)
	}
	guard, err = acquireSingleInstance(selfUpdateMutexScope, nil)
	if err != nil {
		return fmt.Errorf("self-update single-instance barrier: %w", err)
	}
	if guard == nil {
		return errors.New("self-update single-instance barrier: another NekoGo instance is active")
	}
	if err = verifySelfUpdateFile(replacement, plan.SourceSize, plan.SourceSHA256); err != nil {
		return fmt.Errorf("reverify staged self-update source: %w", err)
	}
	if st, statErr := os.Lstat(plan.Target); statErr != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("self-update target changed before replacement: %v", statErr)
	}
	if _, statErr := os.Lstat(backup); statErr == nil {
		return fmt.Errorf("self-update backup already exists: %s", backup)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect self-update backup: %w", statErr)
	}

	if err = selfUpdateReplaceFile(replacement, plan.Target, backup); err != nil {
		rollbackErr := rollbackSelfUpdateTarget(plan.Target, backup)
		if rollbackErr != nil {
			return errors.Join(fmt.Errorf("replace self-update target: %w", err), fmt.Errorf("rollback self-update target: %w", rollbackErr))
		}
		return fmt.Errorf("replace self-update target: %w", err)
	}
	replaced = true
	if err = verifySelfUpdateFile(plan.Target, plan.SourceSize, plan.SourceSHA256); err != nil {
		// Preserve the backup if rollback itself fails; it is then the only
		// recovery copy and must not be removed by best-effort cleanup.
		replaced = false
		rollbackErr := rollbackSelfUpdateTarget(plan.Target, backup)
		if rollbackErr != nil {
			return errors.Join(fmt.Errorf("verify installed self-update: %w", err), fmt.Errorf("rollback self-update target: %w", rollbackErr))
		}
		return fmt.Errorf("verify installed self-update: %w", err)
	}

	// Completion is recorded only after the replacement and its verification.
	if err = selfUpdateWriteResult(plan.ResultPath, selfUpdateResult{Status: "succeeded", Version: plan.Version}); err != nil {
		return fmt.Errorf("write self-update result: %w", err)
	}
	resultWritten = true
	if err = guard.Close(); err != nil {
		return fmt.Errorf("release self-update single-instance barrier: %w", err)
	}
	guard = nil

	var args []string
	if plan.ConnectID != "" {
		args = []string{"--connect", plan.ConnectID}
	}
	if err = selfUpdateRelaunch(plan.Target, args); err != nil {
		return fmt.Errorf("relaunch updated NekoGo: %w", err)
	}
	return nil
}

func normalizeSelfUpdatePlan(plan selfUpdatePlan, job, helperExe string, sourceMustExist, allowSelfParent bool) (selfUpdatePlan, error) {
	if strings.TrimSpace(plan.Version) == "" {
		return plan, errors.New("self-update version is empty")
	}
	if plan.SourceSize <= 0 || plan.SourceSize == math.MaxInt64 {
		return plan, errors.New("self-update source size is invalid")
	}
	digest, err := hex.DecodeString(plan.SourceSHA256)
	if err != nil || len(digest) != sha256.Size {
		return plan, errors.New("self-update source SHA-256 is invalid")
	}
	plan.SourceSHA256 = strings.ToLower(plan.SourceSHA256)
	if plan.ParentPID <= 0 || uint64(plan.ParentPID) > math.MaxUint32 || (!allowSelfParent && plan.ParentPID == os.Getpid()) {
		return plan, fmt.Errorf("self-update parent PID %d is invalid", plan.ParentPID)
	}
	if strings.ContainsRune(plan.ConnectID, 0) {
		return plan, errors.New("self-update connect ID contains NUL")
	}

	paths := []struct {
		name string
		ptr  *string
	}{
		{"source", &plan.Source},
		{"target", &plan.Target},
		{"ready", &plan.ReadyPath},
		{"result", &plan.ResultPath},
	}
	if job != "" {
		paths = append(paths, struct {
			name string
			ptr  *string
		}{"job", &job})
	}
	if helperExe != "" {
		paths = append(paths, struct {
			name string
			ptr  *string
		}{"helper", &helperExe})
	}
	for _, item := range paths {
		path, err := selfUpdateAbsolutePath(*item.ptr)
		if err != nil {
			return plan, fmt.Errorf("self-update %s path: %w", item.name, err)
		}
		*item.ptr = path
	}
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			if selfUpdateSamePath(*paths[i].ptr, *paths[j].ptr) {
				return plan, fmt.Errorf("self-update %s and %s paths must differ", paths[i].name, paths[j].name)
			}
		}
	}
	if !strings.EqualFold(filepath.Base(plan.Target), selfUpdateExpectedTargetName) {
		return plan, fmt.Errorf("self-update target basename must be %s", selfUpdateExpectedTargetName)
	}
	if helperExe != "" {
		if !selfUpdateSamePath(filepath.Dir(helperExe), filepath.Dir(plan.Target)) {
			return plan, errors.New("self-update target is not a sibling of the helper image")
		}
		if err := requireSelfUpdateRegularFile(helperExe); err != nil {
			return plan, fmt.Errorf("self-update helper image: %w", err)
		}
	}
	if err := requireSelfUpdateRegularFile(plan.Target); err != nil {
		return plan, fmt.Errorf("self-update target: %w", err)
	}
	if sourceMustExist {
		if err := requireSelfUpdateRegularFile(plan.Source); err != nil {
			return plan, fmt.Errorf("self-update source: %w", err)
		}
	}
	for _, item := range []struct {
		name string
		path string
	}{{"ready", plan.ReadyPath}, {"result", plan.ResultPath}} {
		if _, err := os.Lstat(item.path); err == nil {
			return plan, fmt.Errorf("self-update %s path already exists", item.name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return plan, fmt.Errorf("inspect self-update %s path: %w", item.name, err)
		}
		if st, err := os.Stat(filepath.Dir(item.path)); err != nil || !st.IsDir() {
			return plan, fmt.Errorf("self-update %s directory: %v", item.name, err)
		}
	}
	if _, err := os.Lstat(plan.Target + ".bak"); err == nil {
		return plan, errors.New("self-update target .bak backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return plan, fmt.Errorf("inspect self-update target backup: %w", err)
	}
	return plan, nil
}

func selfUpdateAbsolutePath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) || !filepath.IsAbs(path) {
		return "", errors.New("must be an absolute path without NUL")
	}
	return filepath.Clean(path), nil
}

func selfUpdateSamePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func requireSelfUpdateRegularFile(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}

func loadSelfUpdatePlan(job string) (selfUpdatePlan, error) {
	if err := requireSelfUpdateRegularFile(job); err != nil {
		return selfUpdatePlan{}, fmt.Errorf("open self-update job: %w", err)
	}
	f, err := os.Open(job)
	if err != nil {
		return selfUpdatePlan{}, fmt.Errorf("open self-update job: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return selfUpdatePlan{}, fmt.Errorf("read self-update job: %w", err)
	}
	if len(raw) > 1<<20 {
		return selfUpdatePlan{}, errors.New("self-update job is too large")
	}
	var plan selfUpdatePlan
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return selfUpdatePlan{}, fmt.Errorf("decode self-update job: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return selfUpdatePlan{}, errors.New("self-update job contains trailing JSON")
	}
	return plan, nil
}

func verifySelfUpdateFile(path string, size int64, digest string) error {
	if err := requireSelfUpdateRegularFile(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() != size {
		return fmt.Errorf("size %d does not match expected %d", st.Size(), size)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("read %d bytes, expected %d", n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, digest) {
		return fmt.Errorf("SHA-256 %s does not match expected %s", got, digest)
	}
	return nil
}

func stageSelfUpdateSource(plan selfUpdatePlan) (path string, err error) {
	f, err := os.CreateTemp(filepath.Dir(plan.Target), "."+filepath.Base(plan.Target)+".new-*.exe")
	if err != nil {
		return "", err
	}
	path = f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err := copySelfUpdateFile(plan.Source, path); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return "", err
	}
	if err := verifySelfUpdateFile(path, plan.SourceSize, plan.SourceSHA256); err != nil {
		return "", err
	}
	return path, nil
}

func copySelfUpdateFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func openSelfUpdateParent(pid int) (windows.Handle, error) {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return 0, nil // already exited
	}
	if err != nil {
		return 0, err
	}
	return process, nil
}

func waitSelfUpdateParent(process windows.Handle, timeout time.Duration) error {
	if process == 0 {
		return nil
	}
	if timeout <= 0 || timeout > time.Hour {
		return errors.New("invalid self-update parent wait")
	}
	millis := uint32((timeout + time.Millisecond - 1) / time.Millisecond)
	status, err := windows.WaitForSingleObject(process, millis)
	if err != nil {
		return err
	}
	switch status {
	case windows.WAIT_OBJECT_0:
		return nil
	case uint32(windows.WAIT_TIMEOUT):
		return errors.New("timed out waiting for parent process exit")
	default:
		return fmt.Errorf("unexpected parent process wait status %#x", status)
	}
}

func writeSelfUpdateAtomicJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeSelfUpdateAtomicFile(path, raw)
}

func writeSelfUpdateAtomicFile(path string, raw []byte) (err error) {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("refusing to overwrite existing self-update status file")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".nekogo-self-update-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeSelfUpdateResult(path string, result selfUpdateResult) error {
	return writeSelfUpdateAtomicJSON(path, result)
}

// ReplaceFileW supplies the required atomic target replacement and .bak backup
// in one kernel operation. Source is staged on the target volume by the caller.
func replaceSelfUpdateFile(source, target, backup string) error {
	replaced, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	replacement, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	backupPath, err := windows.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")
	result, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(replaced)),
		uintptr(unsafe.Pointer(replacement)),
		uintptr(unsafe.Pointer(backupPath)),
		0,
		0,
		0,
	)
	if result == 0 {
		if callErr != nil {
			return fmt.Errorf("ReplaceFileW: %w", callErr)
		}
		return errors.New("ReplaceFileW failed")
	}
	return nil
}

func rollbackSelfUpdateTarget(target, backup string) error {
	if _, err := os.Lstat(backup); errors.Is(err, os.ErrNotExist) {
		if _, targetErr := os.Lstat(target); targetErr == nil {
			return nil // ReplaceFileW failed without changing the target
		}
		return errors.New("replacement failed without a restorable backup")
	} else if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(backup, target); err != nil {
		return err
	}
	return nil
}

func relaunchSelfUpdateTarget(target string, args []string) error {
	cmd := exec.Command(target, args...)
	setHideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// startSelfUpdateHelper launches a copied image and does not return until that
// helper has validated the source and atomically published readiness. Until
// then the parent remains alive and any failure is fully reaped and cleaned.
func startSelfUpdateHelper(plan selfUpdatePlan) (*exec.Cmd, error) {
	if plan.ParentPID == 0 {
		plan.ParentPID = os.Getpid()
	}
	if plan.ParentPID != os.Getpid() {
		return nil, fmt.Errorf("self-update parent PID %d does not match caller %d", plan.ParentPID, os.Getpid())
	}
	helperExe, err := selfUpdateExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve current executable: %w", err)
	}
	plan, err = normalizeSelfUpdatePlan(plan, "", "", false, true)
	if err != nil {
		return nil, err
	}

	stage, err := os.CreateTemp(filepath.Dir(plan.Target), "."+filepath.Base(plan.Target)+".helper-*.exe")
	if err != nil {
		return nil, fmt.Errorf("create self-update staging executable: %w", err)
	}
	stagePath := stage.Name()
	if err := stage.Close(); err != nil {
		_ = os.Remove(stagePath)
		return nil, err
	}
	jobPath := ""
	cleanup := func() {
		_ = os.Remove(stagePath)
		if jobPath != "" {
			_ = os.Remove(jobPath)
		}
		_ = os.Remove(plan.ReadyPath)
		_ = os.Remove(plan.ResultPath)
	}
	fail := func(err error) (*exec.Cmd, error) {
		cleanup()
		return nil, err
	}
	if err := copySelfUpdateFile(helperExe, stagePath); err != nil {
		return fail(fmt.Errorf("copy self-update staging executable: %w", err))
	}
	if err := os.Chmod(stagePath, 0o700); err != nil {
		return fail(fmt.Errorf("set self-update staging executable mode: %w", err))
	}

	jobDir := filepath.Dir(plan.Source)
	if st, err := os.Stat(jobDir); err != nil || !st.IsDir() {
		jobDir = filepath.Dir(plan.Target)
	}
	job, err := os.CreateTemp(jobDir, ".nekogo-self-update-*.json")
	if err != nil {
		return fail(fmt.Errorf("create self-update job: %w", err))
	}
	jobPath = job.Name()
	if err := job.Chmod(0o600); err != nil {
		_ = job.Close()
		return fail(fmt.Errorf("set self-update job mode: %w", err))
	}
	if err := json.NewEncoder(job).Encode(plan); err != nil {
		_ = job.Close()
		return fail(fmt.Errorf("write self-update job: %w", err))
	}
	if err := job.Sync(); err != nil {
		_ = job.Close()
		return fail(fmt.Errorf("sync self-update job: %w", err))
	}
	if err := job.Close(); err != nil {
		return fail(fmt.Errorf("close self-update job: %w", err))
	}

	cmd := exec.Command(stagePath, selfUpdateApplyFlag, jobPath)
	setHideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return fail(fmt.Errorf("start self-update helper: %w", err))
	}
	ready, err := waitSelfUpdateReady(cmd, plan.ReadyPath, selfUpdateReadyWait)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // reap only the helper launched above
		return fail(fmt.Errorf("self-update helper readiness: %w", err))
	}
	if ready.PID != cmd.Process.Pid {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fail(errors.New("self-update helper readiness PID does not match"))
	}
	return cmd, nil
}

func waitSelfUpdateReady(cmd *exec.Cmd, path string, timeout time.Duration) (selfUpdateReady, error) {
	if timeout <= 0 || timeout > time.Hour {
		return selfUpdateReady{}, errors.New("invalid self-update readiness wait")
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := readSelfUpdateStatus(path)
		if err == nil {
			if len(raw) > 4096 {
				return selfUpdateReady{}, errors.New("readiness file is too large")
			}
			var ready selfUpdateReady
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&ready); err != nil {
				return ready, fmt.Errorf("decode readiness: %w", err)
			}
			var extra any
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				return ready, errors.New("readiness contains trailing JSON")
			}
			if ready.Status != "ready" || ready.PID != cmd.Process.Pid {
				return ready, errors.New("invalid readiness status or PID")
			}
			if selfUpdateProcessExited(cmd.Process.Pid) {
				return ready, errors.New("helper exited immediately after readiness")
			}
			return ready, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return selfUpdateReady{}, err
		}
		if selfUpdateProcessExited(cmd.Process.Pid) {
			return selfUpdateReady{}, errors.New("helper exited before readiness")
		}
		if !time.Now().Before(deadline) {
			return selfUpdateReady{}, errors.New("timed out waiting for helper readiness")
		}
		<-ticker.C
	}
}

func readSelfUpdateStatus(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 4097))
}

func selfUpdateProcessExited(pid int) bool {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true
	}
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	status, err := windows.WaitForSingleObject(process, 0)
	return err == nil && status == windows.WAIT_OBJECT_0
}
