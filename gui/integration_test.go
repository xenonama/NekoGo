package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertCoreProcess(t *testing.T, name string) {
	t.Helper()
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name+".exe", "/NH").Output()
	if err != nil {
		t.Fatalf("tasklist: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(out)), name+".exe") {
		t.Fatalf("core process %s.exe not found running", name)
	}
}

// TestIntegration exercises both cores through the real App service:
// save profile -> Connect (spawns core process) -> GetStats ->
// ListConnections -> Disconnect (stops process). No remote server is
// needed: sing-box uses a direct-only config, Xray starts with an
// unreachable outbound (process + API must still come up).
func TestIntegration(t *testing.T) {
	if os.Getenv("NEKOBOX_INTEGRATION") == "" {
		t.Skip("set NEKOBOX_INTEGRATION=1 to run core spawn tests")
	}
	dir := t.TempDir()
	logCh := make(chan LogEntry, 512)
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, logCh)

	binDir, err := filepath.Abs("build/bin")
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.CorePath = filepath.Join(binDir, "nekobox_core.exe")
	settings.XrayPath = filepath.Join(binDir, "xray.exe")
	settings.MixedPort = 18080
	settings.XrayAPIAddress = "127.0.0.1:18085"
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	app.singbox.corePath = settings.CorePath
	app.xray.xrayPath = settings.XrayPath

	t.Run("singbox-direct", func(t *testing.T) {
		p, err := app.SaveProfile(Profile{
			Name: "it-direct", Core: CoreSingBox, Protocol: "direct",
			RawOutbound: `{"type":"direct","tag":"proxy"}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Connect(p.ID); err != nil {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(2 * time.Second)
		st, err := app.GetStats()
		if err != nil {
			for _, e := range app.RecentLogs(50) {
				t.Logf("core-log [%s] %s", e.Source, e.Message)
			}
			t.Fatalf("stats: %v", err)
		}
		if !st.Running || st.Core != "sing-box" {
			t.Fatalf("unexpected stats: %+v", st)
		}
		cl, err := app.ListConnections()
		if err != nil {
			t.Fatalf("connections: %v", err)
		}
		t.Logf("sing-box stats=%+v conns=%d truncated=%v", st, len(cl.Connections), cl.Truncated)
		assertCoreProcess(t, "nekobox_core")
		if err := app.Disconnect(); err != nil {
			t.Fatalf("disconnect: %v", err)
		}
	})

	t.Run("xray-unreachable", func(t *testing.T) {
		p, err := app.SaveProfile(Profile{
			Name: "it-xray", Core: CoreXray, Protocol: "vless",
			Address: "127.0.0.1", Port: 19999,
			IDOrPassword: "11111111-2222-4333-8444-555555555555",
			Network:      "tcp",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Connect(p.ID); err != nil {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(2 * time.Second)
		st, err := app.GetStats()
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if !st.Running || st.Core != "xray" {
			t.Fatalf("unexpected stats: %+v", st)
		}
		t.Logf("xray stats=%+v", st)
		assertCoreProcess(t, "xray")
		if err := app.Disconnect(); err != nil {
			t.Fatalf("disconnect: %v", err)
		}
	})
}

func drainLogs(t *testing.T, ch chan LogEntry) {
	t.Helper()
	for {
		select {
		case e := <-ch:
			t.Logf("core-log [%s] %s", e.Source, e.Message)
		default:
			return
		}
	}
}
