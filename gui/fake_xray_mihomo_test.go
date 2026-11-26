package main

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

var fakeXrayListeners []net.Listener
var fakeMihomoListeners []net.Listener

func fakeXrayMain(args []string) {
	cfgPath := ""
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
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
			Listen string `json:"listen"`
			Port   int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		os.Exit(2)
	}
	for _, inbound := range doc.Inbounds {
		if inbound.Port <= 0 {
			continue
		}
		host := inbound.Listen
		if host == "" || host == "::" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(inbound.Port)))
		if err != nil {
			os.Exit(3)
		}
		fakeXrayListeners = append(fakeXrayListeners, ln)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func fakeMihomoMain(args []string) {
	cfgPath := ""
	for i, arg := range args {
		if arg == "-f" && i+1 < len(args) {
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
		MixedPort          int    `json:"mixed-port"`
		ExternalController string `json:"external-controller"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		os.Exit(2)
	}
	for _, addr := range []string{
		fmt.Sprintf("127.0.0.1:%d", doc.MixedPort),
		doc.ExternalController,
	} {
		_, port, err := net.SplitHostPort(addr)
		if err != nil || port == "0" {
			os.Exit(2)
		}
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			os.Exit(3)
		}
		fakeMihomoListeners = append(fakeMihomoListeners, ln)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func isFakeXrayInvocation() bool {
	base := filepath.Base(os.Args[0])
	return (base == "xray.exe" || base == "xray") && len(os.Args) > 1 && os.Args[1] == "-c"
}

func isFakeMihomoInvocation() bool {
	base := filepath.Base(os.Args[0])
	return (base == "mihomo.exe" || base == "mihomo" || base == "mihomo-windows-amd64.exe") && len(os.Args) > 1
}

func installFakeNamedCore(t *testing.T, name string) string {
	t.Helper()
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
		t.Fatalf("write fake core: %v", err)
	}
	return dest
}

func installFakeXray(t *testing.T) string {
	t.Helper()
	name := "xray.exe"
	if runtime.GOOS != "windows" {
		name = "xray"
	}
	return installFakeNamedCore(t, name)
}

func installFakeMihomo(t *testing.T) string {
	t.Helper()
	name := "mihomo.exe"
	if runtime.GOOS != "windows" {
		name = "mihomo"
	}
	return installFakeNamedCore(t, name)
}
