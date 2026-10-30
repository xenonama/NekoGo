package main

// Test stand-in for the official sing-box CLI. Copying the test binary
// to sing-box.exe and running `sing-box.exe run -c config.json` makes it
// bind the loopback port of the config's first inbound and block — which
// is exactly what UpstreamSingBoxManager.Start probes for readiness and
// what Process.Kill ends. No real sing-box binary is needed to prove
// WHICH engine a connect spawns.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeSingBoxListeners keeps the fake's listeners reachable so the
// runtime finalizer cannot close them while the child sleeps.
var fakeSingBoxListeners []net.Listener

// fakeSingBoxMain is the child-side implementation. args is os.Args.
func fakeSingBoxMain(args []string) {
	cfgPath := ""
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			cfgPath = args[i+1]
		}
	}
	if cfgPath == "" {
		os.Exit(2)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		os.Exit(2)
	}
	var doc struct {
		Inbounds []struct {
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		os.Exit(2)
	}
	for _, in := range doc.Inbounds {
		if in.ListenPort <= 0 {
			continue
		}
		host := in.Listen
		if host == "" || host == "::" {
			host = "127.0.0.1"
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(in.ListenPort)))
		if err != nil {
			os.Exit(3)
		}
		fakeSingBoxListeners = append(fakeSingBoxListeners, ln)
		break
	}
	for {
		time.Sleep(time.Hour)
	}
}

// isFakeSingBoxInvocation reports whether this process was started as the
// fake official CLI (the test binary copied under the CLI's file name).
func isFakeSingBoxInvocation() bool {
	base := filepath.Base(os.Args[0])
	if base != "sing-box.exe" && base != "sing-box" {
		return false
	}
	return len(os.Args) > 1 && os.Args[1] == "run"
}

// installFakeSingBox copies the running test binary into a temp dir under
// the official CLI's name and returns that path.
func installFakeSingBox(t *testing.T) string {
	t.Helper()
	name := "sing-box.exe"
	if runtime.GOOS != "windows" {
		name = "sing-box"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	dest := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	return dest
}
