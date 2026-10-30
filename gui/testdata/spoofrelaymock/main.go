// Command spoofrelaymock is a functional sni-spoof.exe stand-in for
// tests that need the ENGINE LEG of a spoof path to actually carry
// traffic: it accepts the same `run -c <config>` surface, listens on
// LISTEN_HOST:LISTEN_PORT and relays every accepted connection raw to
// the first ENDPOINTS entry. That is the transport contract the Go
// side relies on (the real engine adds WinDivert fake-ClientHello
// injection on the same path), so tests can run without admin rights
// or the WinDivert driver.
//
// Test hooks via environment:
//   - SPOOFRELAY_DELAY_MS > 0: sleep before binding, reproducing the
//     real engine's async startup (WinDivert handle opens + tokio bind
//     happen after process spawn).
//   - SPOOFRELAY_DELAY_MS = -1: print an [ERROR] line and exit right
//     away, reproducing a failed engine (e.g. WinDivert access denied).
//
// This directory is testdata, so the normal `go build ./...` ignores it.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

type endpoint struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

type config struct {
	ListenHost string     `json:"LISTEN_HOST"`
	ListenPort int        `json:"LISTEN_PORT"`
	Endpoints  []endpoint `json:"ENDPOINTS"`
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "run" || os.Args[2] != "-c" {
		fmt.Fprintln(os.Stderr, "usage:\n  sni-spoof.exe version\n  sni-spoof.exe run -c <config.json>\n  sni-spoof.exe help")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[3])
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] cannot load config %s: %v\n", os.Args[3], err)
		os.Exit(1)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] invalid config: %v\n", err)
		os.Exit(1)
	}
	if len(cfg.Endpoints) == 0 {
		fmt.Fprintln(os.Stderr, "[ERROR] no endpoints configured")
		os.Exit(1)
	}
	if d := os.Getenv("SPOOFRELAY_DELAY_MS"); d != "" {
		if ms, err := strconv.Atoi(d); err == nil {
			if ms < 0 {
				fmt.Println("[ERROR] mock engine cannot start (requested failure)")
				os.Exit(1)
			}
			if ms > 0 {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}
	}
	target := net.JoinHostPort(cfg.Endpoints[0].IP, strconv.Itoa(cfg.Endpoints[0].Port))
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.ListenHost, strconv.Itoa(cfg.ListenPort)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] listen: %v\n", err)
		os.Exit(1)
	}
	// Readiness line printed only after the listener is bound, like the
	// real CLI's "[INFO] engine RUNNING" (EngineHandle::start parity).
	fmt.Printf("[INFO] mock engine RUNNING (relay %s -> %s)\n", ln.Addr(), target)
	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] accept: %v\n", err)
			os.Exit(1)
		}
		go relay(conn, target)
	}
}

func relay(in net.Conn, target string) {
	defer in.Close()
	out, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer out.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(out, in); done <- struct{}{} }()
	go func() { _, _ = io.Copy(in, out); done <- struct{}{} }()
	<-done
}
