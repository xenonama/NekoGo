//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	selfUpdateTestEnv        = "NEKOGO_SELF_UPDATE_HELPER"
	selfUpdateTestScopeEnv   = "NEKOGO_SELF_UPDATE_TEST_SCOPE"
	selfUpdateTestTargetEnv  = "NEKOGO_SELF_UPDATE_TEST_TARGET"
	selfUpdateTestParentWait = 5 * time.Second
)

// A copied test binary enters helper mode before TestMain, just like the
// production binary. The scope check makes accidental use of production
// kernel objects impossible.
func init() {
	if os.Getenv(selfUpdateTestEnv) != "1" {
		return
	}
	scope := os.Getenv(selfUpdateTestScopeEnv)
	if !strings.HasPrefix(scope, singleInstanceScope+".test.") {
		os.Exit(90)
	}
	selfUpdateMutexScope = scope
	selfUpdateExpectedTargetName = os.Getenv(selfUpdateTestTargetEnv)
	handled, err := runSelfUpdateHelper(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "self-update test helper:", err)
		os.Exit(1)
	}
	if !handled {
		os.Exit(91)
	}
	os.Exit(0)
}

func selfUpdateTestScope(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s.test.selfupdate.%s.%d.%d", singleInstanceScope, t.Name(), os.Getpid(), time.Now().UnixNano())
}

func selfUpdateTestSeams(t *testing.T) {
	t.Helper()
	oldExecutable := selfUpdateExecutable
	oldExit := selfUpdateExit
	oldReadyWait := selfUpdateReadyWait
	oldParentWait := selfUpdateParentWait
	oldScope := selfUpdateMutexScope
	oldTargetName := selfUpdateExpectedTargetName
	oldRelaunch := selfUpdateRelaunch
	oldReplace := selfUpdateReplaceFile
	oldWriteResult := selfUpdateWriteResult
	t.Cleanup(func() {
		selfUpdateExecutable = oldExecutable
		selfUpdateExit = oldExit
		selfUpdateReadyWait = oldReadyWait
		selfUpdateParentWait = oldParentWait
		selfUpdateMutexScope = oldScope
		selfUpdateExpectedTargetName = oldTargetName
		selfUpdateRelaunch = oldRelaunch
		selfUpdateReplaceFile = oldReplace
		selfUpdateWriteResult = oldWriteResult
	})
	selfUpdateReadyWait = 2 * time.Second
	selfUpdateParentWait = selfUpdateTestParentWait
	selfUpdateMutexScope = selfUpdateTestScope(t)
	selfUpdateExpectedTargetName = "nekogo.exe"
}

func selfUpdateDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func selfUpdateFixture(t *testing.T, sourceData, targetData []byte) (selfUpdatePlan, string, string) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "NeKoGo.ExE")
	helper := filepath.Join(dir, "nekogo-self-update-stage.exe")
	source := filepath.Join(dir, "downloaded update.exe")
	ready := filepath.Join(dir, "ready file.json")
	result := filepath.Join(dir, "result file.json")
	job := filepath.Join(dir, "job file.json")
	for path, data := range map[string][]byte{
		target: targetData,
		helper: []byte("test-owned staging image"),
		source: sourceData,
	} {
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	plan := selfUpdatePlan{
		Version:      "9.8.7-test",
		Source:       source,
		SourceSize:   int64(len(sourceData)),
		SourceSHA256: selfUpdateDigest(sourceData),
		Target:       target,
		ReadyPath:    ready,
		ResultPath:   result,
		ConnectID:    `profile "quoted" & structural`,
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return plan, helper, job
}

func selfUpdateExitedParent(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		selfUpdateTestEnv+"=",
		selfUpdateTestScopeEnv+"=",
		selfUpdateTestTargetEnv+"=",
		"NEKOGO_SINGLE_INSTANCE_HELPER=",
		"GO_WANT_HELPER_PROCESS=",
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("start test-owned parent: %v", err)
	}
	return cmd.Process.Pid
}

func selfUpdateWaitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(selfUpdateTestParentWait)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}

func selfUpdateAssertGone(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists (err=%v)", path, err)
		}
	}
}

func TestSelfUpdateArgsStrict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		job     string
		handled bool
		bad     bool
	}{
		{name: "normal startup", args: []string{"--connect", "profile"}},
		{name: "no args"},
		{name: "pair", args: []string{"--apply-update", `D:\tmp\job file.json`}, job: `D:\tmp\job file.json`, handled: true},
		{name: "equals", args: []string{`--apply-update=D:\tmp\job file.json`}, job: `D:\tmp\job file.json`, handled: true},
		{name: "missing", args: []string{"--apply-update"}, handled: true, bad: true},
		{name: "empty", args: []string{"--apply-update="}, handled: true, bad: true},
		{name: "duplicate", args: []string{"--apply-update", "a", "--apply-update=b"}, handled: true, bad: true},
		{name: "extra after", args: []string{"--apply-update", "a", "--connect", "b"}, handled: true, bad: true},
		{name: "extra before", args: []string{"--connect", "b", "--apply-update", "a"}, handled: true, bad: true},
		{name: "flag as value", args: []string{"--apply-update", "--apply-update=a"}, handled: true, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, handled, err := parseSelfUpdateArgs(tc.args)
			if handled != tc.handled || (err != nil) != tc.bad || job != tc.job {
				t.Fatalf("parse = (%q, %v, %v), want (%q, %v, error=%v)", job, handled, err, tc.job, tc.handled, tc.bad)
			}
		})
	}
}

func TestSelfUpdateReplacementSuccessAndRollback(t *testing.T) {
	for _, failReplacement := range []bool{false, true} {
		name := "success"
		if failReplacement {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			selfUpdateTestSeams(t)
			oldTarget := []byte("old nekogo image")
			newSource := []byte("new nekogo image")
			plan, helper, job := selfUpdateFixture(t, newSource, oldTarget)
			plan.ParentPID = selfUpdateExitedParent(t)
			raw, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(job, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			selfUpdateExecutable = func() (string, error) { return helper, nil }

			var relaunchPath string
			var relaunchArgs []string
			selfUpdateRelaunch = func(path string, args []string) error {
				relaunchPath = path
				relaunchArgs = append([]string(nil), args...)
				return nil
			}
			var statuses []string
			selfUpdateWriteResult = func(path string, result selfUpdateResult) error {
				statuses = append(statuses, result.Status)
				if result.Status == "succeeded" {
					got, err := os.ReadFile(plan.Target)
					if err != nil || !bytes.Equal(got, newSource) {
						return errors.New("success result was written before replacement")
					}
					if _, err := os.Lstat(plan.Target + ".bak"); err != nil {
						return errors.New("success result was written without a rollback backup")
					}
				}
				return writeSelfUpdateAtomicJSON(path, result)
			}
			if failReplacement {
				selfUpdateReplaceFile = func(_, target, backup string) error {
					if err := os.Rename(target, backup); err != nil {
						return err
					}
					return errors.New("injected replacement failure")
				}
			}

			handled, err := runSelfUpdateHelper([]string{"--apply-update", job})
			if !handled {
				t.Fatal("helper arguments were not handled")
			}
			backup := plan.Target + ".bak"
			if failReplacement {
				if err == nil || !strings.Contains(err.Error(), "injected replacement failure") {
					t.Fatalf("replacement error = %v", err)
				}
				got, readErr := os.ReadFile(plan.Target)
				if readErr != nil || !bytes.Equal(got, oldTarget) {
					t.Fatalf("rollback target = %q, %v", got, readErr)
				}
				if relaunchPath != "" {
					t.Fatal("relaunched after failed replacement")
				}
				if len(statuses) != 1 || statuses[0] != "failed" {
					t.Fatalf("result statuses = %v", statuses)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				got, readErr := os.ReadFile(plan.Target)
				if readErr != nil || !bytes.Equal(got, newSource) {
					t.Fatalf("installed target = %q, %v", got, readErr)
				}
				if relaunchPath != plan.Target {
					t.Fatalf("relaunch path = %q, want %q", relaunchPath, plan.Target)
				}
				wantArgs := []string{"--connect", plan.ConnectID}
				if fmt.Sprint(relaunchArgs) != fmt.Sprint(wantArgs) {
					t.Fatalf("relaunch args = %#v, want %#v", relaunchArgs, wantArgs)
				}
				if len(statuses) != 1 || statuses[0] != "succeeded" {
					t.Fatalf("result statuses = %v", statuses)
				}
			}
			selfUpdateAssertGone(t, backup, plan.Source, plan.ReadyPath, plan.ResultPath, helper, job)
			entries, err := os.ReadDir(filepath.Dir(plan.Target))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != filepath.Base(plan.Target) {
				t.Fatalf("temporary files remain: %v", entries)
			}
		})
	}
}

func TestSelfUpdateHelperWaitsForParentAndSharesExclusion(t *testing.T) {
	selfUpdateTestSeams(t)
	oldTarget := []byte("old nekogo image")
	plan, helper, job := selfUpdateFixture(t, []byte("new nekogo image"), oldTarget)
	parent := startSingleInstanceHelper(t, selfUpdateTestScope(t)+".parent", "linger")
	parent.expect(t, "LINGER")
	plan.ParentPID = parent.cmd.Process.Pid
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	selfUpdateExecutable = func() (string, error) { return helper, nil }
	relaunched := false
	selfUpdateRelaunch = func(string, []string) error {
		relaunched = true
		return nil
	}

	done := make(chan error, 1)
	go func() {
		handled, err := runSelfUpdateHelper([]string{"--apply-update", job})
		if !handled && err == nil {
			err = errors.New("helper mode not handled")
		}
		done <- err
	}()
	selfUpdateWaitFile(t, plan.ReadyPath)
	got, err := os.ReadFile(plan.Target)
	if err != nil || !bytes.Equal(got, oldTarget) {
		t.Fatalf("target changed before parent exit: %q, %v", got, err)
	}
	select {
	case err := <-done:
		t.Fatalf("helper finished while parent was alive: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// The helper must still be waiting on the real parent handle, not holding
	// the production barrier early. This test-owned owner then proves the
	// helper uses that same barrier after the parent exits.
	guard, err := acquireSingleInstance(selfUpdateMutexScope, nil)
	if err != nil || guard == nil {
		t.Fatalf("acquire while helper waits for parent: guard=%v err=%v", guard, err)
	}
	parent.send(t, "exit")
	parent.wait(t, 0)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("helper replaced the target despite single-instance exclusion")
		}
	case <-time.After(selfUpdateTestParentWait):
		t.Fatal("helper did not finish after parent exit")
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(plan.Target)
	if err != nil || !bytes.Equal(got, oldTarget) {
		t.Fatalf("excluded helper changed target: %q, %v", got, err)
	}
	if relaunched {
		t.Fatal("excluded helper relaunched the target")
	}
	selfUpdateAssertGone(t, plan.Source, plan.ReadyPath, plan.ResultPath, helper, job)
}

func TestStartSelfUpdateHelperSuccessIsStructuralAndHidden(t *testing.T) {
	selfUpdateTestSeams(t)
	t.Setenv(selfUpdateTestEnv, "1")
	t.Setenv(selfUpdateTestScopeEnv, selfUpdateMutexScope)
	t.Setenv(selfUpdateTestTargetEnv, selfUpdateExpectedTargetName)
	dir := t.TempDir()
	target := filepath.Join(dir, "nekogo.exe")
	oldTarget := []byte("old target")
	if err := os.WriteFile(target, oldTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "downloaded update.exe")
	newSource := []byte("new source")
	if err := os.WriteFile(source, newSource, 0o600); err != nil {
		t.Fatal(err)
	}
	plan := selfUpdatePlan{
		Version:      "9.8.7-test",
		Source:       source,
		SourceSize:   int64(len(newSource)),
		SourceSHA256: selfUpdateDigest(newSource),
		Target:       target,
		ReadyPath:    filepath.Join(dir, "ready.json"),
		ResultPath:   filepath.Join(dir, "result.json"),
		ParentPID:    os.Getpid(),
	}
	cmd, err := startSelfUpdateHelper(plan)
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Process == nil {
		t.Fatal("start returned no live helper command")
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if filepath.Dir(cmd.Path) != filepath.Dir(target) || cmd.Path == target {
		t.Fatalf("staging path %q is not a unique target sibling", cmd.Path)
	}
	wantArgs := []string{cmd.Path, "--apply-update"}
	if len(cmd.Args) < 3 || fmt.Sprint(cmd.Args[:2]) != fmt.Sprint(wantArgs) {
		t.Fatalf("helper argv = %#v", cmd.Args)
	}
	job := cmd.Args[2]
	if !filepath.IsAbs(job) || !strings.HasSuffix(job, ".json") {
		t.Fatalf("job path = %q", job)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("helper window is not hidden")
	}
	var ready selfUpdateReady
	if err := json.Unmarshal(selfUpdateWaitFile(t, plan.ReadyPath), &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || ready.PID != cmd.Process.Pid {
		t.Fatalf("readiness = %+v, helper pid = %d", ready, cmd.Process.Pid)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, oldTarget) {
		t.Fatalf("target changed before parent exit: %q, %v", got, err)
	}
	selfUpdateAssertGone(t, plan.ResultPath)
}

func TestStartSelfUpdateHelperPreReadinessFailureDoesNotStrandParent(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		name := "missing source"
		if tampered {
			name = "tampered source"
		}
		t.Run(name, func(t *testing.T) {
			selfUpdateTestSeams(t)
			t.Setenv(selfUpdateTestEnv, "1")
			t.Setenv(selfUpdateTestScopeEnv, selfUpdateMutexScope)
			t.Setenv(selfUpdateTestTargetEnv, selfUpdateExpectedTargetName)
			dir := t.TempDir()
			target := filepath.Join(dir, "nekogo.exe")
			oldTarget := []byte("old target")
			if err := os.WriteFile(target, oldTarget, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(dir, "downloaded update.exe")
			original := []byte("original-source")
			if tampered {
				if err := os.WriteFile(source, []byte("tampered-source"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			plan := selfUpdatePlan{
				Version:      "9.8.7-test",
				Source:       source,
				SourceSize:   int64(len(original)),
				SourceSHA256: selfUpdateDigest(original),
				Target:       target,
				ReadyPath:    filepath.Join(dir, "ready.json"),
				ResultPath:   filepath.Join(dir, "result.json"),
				ParentPID:    os.Getpid(),
			}
			started := time.Now()
			cmd, err := startSelfUpdateHelper(plan)
			if err == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("start succeeded without valid source readiness")
			}
			if cmd != nil {
				t.Fatal("start returned a command on pre-readiness failure")
			}
			if elapsed := time.Since(started); elapsed > selfUpdateTestParentWait {
				t.Fatalf("start failure took %v", elapsed)
			}
			got, readErr := os.ReadFile(target)
			if readErr != nil || !bytes.Equal(got, oldTarget) {
				t.Fatalf("target changed on failed start: %q, %v", got, readErr)
			}
			selfUpdateAssertGone(t, plan.ReadyPath, plan.ResultPath)
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, entry := range entries {
				if entry.Name() != filepath.Base(target) && !(tampered && entry.Name() == filepath.Base(source)) {
					t.Errorf("start failure stranded %s", entry.Name())
				}
			}
		})
	}
}

func TestSelfUpdateDispatchPrecedesNormalStartup(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	dispatch := strings.Index(text, "runSelfUpdateAtStartup(os.Args[1:])")
	guard := strings.Index(text, "acquireSingleInstance(singleInstanceScope, os.Args[1:])")
	if dispatch < 0 || guard < 0 || dispatch > guard {
		t.Fatal("self-update helper dispatch does not precede single-instance acquisition")
	}
	for _, operation := range []string{"NewConfigStore(", "NewApp(", "cleanupOrphanCores(", "cleanupStaleSysproxy(", "application.New("} {
		if at := strings.Index(text, operation); at >= 0 && at < dispatch {
			t.Errorf("%s precedes helper dispatch", operation)
		}
	}
}

func TestSelfUpdateParentPIDValidation(t *testing.T) {
	selfUpdateTestSeams(t)
	plan, helper, job := selfUpdateFixture(t, []byte("new"), []byte("old"))
	plan.ParentPID = os.Getpid()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	selfUpdateExecutable = func() (string, error) { return helper, nil }
	handled, err := runSelfUpdateHelper([]string{"--apply-update", job})
	if !handled || err == nil || !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("self-parent plan = handled %v, err %v", handled, err)
	}
	selfUpdateAssertGone(t, plan.ReadyPath, plan.ResultPath)
}
