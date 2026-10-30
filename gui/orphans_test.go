package main

import (
	"testing"
)

// HOTFIX Bug 2: orphan-cleanup unit tests. All pure functions — no real
// process is listed or killed here.

func TestParseWmicCSV(t *testing.T) {
	out := "Node,ExecutablePath,ParentProcessId,ProcessId\r\n" +
		`HOST,"C:\Users\pc\Desktop\nekobox-main\gui\build\bin\xray.exe",1234,5678` + "\r\n" +
		`HOST,,1,999` + "\r\n"
	procs, err := parseWmicCSV(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 2 {
		t.Fatalf("got %d procs, want 2: %+v", len(procs), procs)
	}
	if procs[0].pid != 5678 || procs[0].ppid != 1234 || procs[0].name != "xray.exe" {
		t.Fatalf("row 0 = %+v", procs[0])
	}
	if procs[1].exePath != "" {
		t.Fatalf("row 1 exePath = %q, want empty", procs[1].exePath)
	}
}

func TestParsePowershellCSV(t *testing.T) {
	out := "\"ProcessId\",\"ParentProcessId\",\"ExecutablePath\"\n" +
		`"5678","1234","C:\\bin\\nekobox_core.exe"` + "\n"
	procs, err := parsePowershellCSV(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 1 {
		t.Fatalf("got %+v", procs)
	}
	if procs[0].pid != 5678 || procs[0].name != "nekobox_core.exe" {
		t.Fatalf("row = %+v", procs[0])
	}
}

func TestParseTasklistPIDs(t *testing.T) {
	out := "\"xray.exe\",\"5678\",\"Console\",\"1\",\"8,000 K\"\n" +
		"\"System\",\"4\",\"Services\",\"0\",\"100 K\"\n"
	pids, err := parseTasklistPIDs(out)
	if err != nil {
		t.Fatal(err)
	}
	if !pids[5678] || !pids[4] || pids[12345] {
		t.Fatalf("pids = %v", pids)
	}
}

func TestIsUnderDir(t *testing.T) {
	dir := `C:\a\bin`
	for _, p := range []string{
		`C:\a\bin\xray.exe`,
		`c:\A\BIN\XRAY.EXE`,
		`C:\a\bin`,
	} {
		if !isUnderDir(p, dir) {
			t.Errorf("isUnderDir(%q, %q) = false, want true", p, dir)
		}
	}
	for _, p := range []string{
		`C:\a\bin2\xray.exe`,
		`C:\other\xray.exe`,
		"",
	} {
		if isUnderDir(p, dir) {
			t.Errorf("isUnderDir(%q, %q) = true, want false", p, dir)
		}
	}
}

func TestOrphanCleanupSelectsOnlyStrays(t *testing.T) {
	dir := `C:\app\bin`
	self := 100
	alive := map[int]bool{100: true, 200: true} // us + another live GUI
	procs := []orphanProc{
		{pid: 101, ppid: 1, name: "xray.exe", exePath: dir + `\xray.exe`},          // orphan -> kill
		{pid: 102, ppid: 200, name: "xray.exe", exePath: dir + `\xray.exe`},        // other instance -> skip
		{pid: 103, ppid: 100, name: "nekobox_core.exe", exePath: dir + `\c.exe`},   // own child -> skip
		{pid: 100, ppid: 1, name: "xray.exe", exePath: dir + `\xray.exe`},          // self -> skip
		{pid: 104, ppid: 1, name: "xray.exe", exePath: `C:\other\xray.exe`},        // foreign path -> skip
		{pid: 105, ppid: 1, name: "xray.exe", exePath: ""},                         // unknown path -> skip
		{pid: 106, ppid: 1, name: "notepad.exe", exePath: dir + `\notepad.exe`},    // wrong image -> skip
		{pid: 107, ppid: 0, name: "nekobox_core.exe", exePath: dir + `\ncore.exe`}, // reparented -> kill
	}
	got := selectOrphans(procs, dir, self, alive)
	if len(got) != 2 {
		t.Fatalf("selected %+v, want PIDs 101 and 107", got)
	}
	if got[0].pid != 101 || got[1].pid != 107 {
		t.Fatalf("selected %+v, want PIDs 101 and 107", got)
	}
}
