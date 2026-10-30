// Command spoofmock is a stub sni-spoof.exe for the SpoofManager
// tests: it validates the `run -c <config>` surface, prints one
// [INFO] line, then emits [STATS] JSON snapshots until killed.
// This directory is testdata, so the normal `go build ./...`
// ignores it.
package main

import (
	"fmt"
	"os"
	"time"
)

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
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for range tick.C {
		n++
		fmt.Printf("[STATS] {\"type\":\"stats\",\"active\":%d,\"total\":%d,\"success\":%d,\"failed\":0,\"uptime\":%.1f,\"success_rate\":1.0,\"best_endpoint\":\"\",\"best_method\":\"\",\"methods\":[],\"up_bytes\":0,\"down_bytes\":0}\n",
			n%3, n, n, float64(n)/10.0)
	}
}
