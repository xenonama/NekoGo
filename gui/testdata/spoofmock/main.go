// Command spoofmock is a stub sni-spoof.exe for the SpoofManager
// tests: it validates the `run -c <config>` surface, prints one
// [INFO] line, then emits [STATS] JSON snapshots until killed.
// This directory is testdata, so the normal `go build ./...`
// ignores it.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// stopMarker is what the manager test waits for. It may only ever be
// printed after a "stop" line arrives on stdin, which makes it a real
// proof that the host's stop request reached the engine.
const stopMarker = "[INFO] mock graceful stop"

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
	fmt.Printf("[INFO] mock engine RUNNING (%d config bytes)\n", len(data))
	// The stop channel, mirroring cli/src/main.rs: a Go parent on Windows
	// cannot deliver any signal to its child except TerminateProcess, so
	// SpoofManager.Stop writes "stop" here and the engine exits by itself.
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "stop" {
				fmt.Println(stopMarker)
				os.Exit(0)
			}
		}
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for range tick.C {
		n++
		// methods mirrors core/src/stats.rs Ranked exactly: one row that has
		// been timed (latency_ms a number) and one that has not (null), both
		// carrying the score auto ranks on.
		fmt.Printf("[STATS] {\"type\":\"stats\",\"active\":%d,\"total\":%d,\"success\":%d,\"failed\":0,\"uptime\":%.1f,\"success_rate\":1.0,\"best_endpoint\":\"\",\"best_method\":\"split_seq\",\"methods\":[{\"key\":\"split_seq\",\"ok\":%d,\"fail\":0,\"rate\":1.0,\"latency_ms\":42.5,\"score\":0.912},{\"key\":\"hello_split\",\"ok\":%d,\"fail\":0,\"rate\":1.0,\"latency_ms\":null,\"score\":0.8}],\"up_bytes\":0,\"down_bytes\":0}\n",
			n%3, n, n, float64(n)/10.0, n, n)
	}
}
