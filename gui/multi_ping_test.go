package main

// Regression test for the Proxies "Test All" sweep: concurrent latency
// probes must run through independent throwaway cores. The old backend
// kept one ephemeral slot per manager, so every new probe cancelled the
// previous one and only the last row of a batch could succeed.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestConcurrentEphemeralURLTests(t *testing.T) {
	coreBin, xrayBin := testCoreBins(t)
	logs := make(chan LogEntry, 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "probe-ok")
	}))
	defer srv.Close()

	cm := NewCoreManager(NewSingBoxManager(coreBin, logs), NewXrayManager(xrayBin, "", logs))
	const workers = 4
	results := make([]*TestResult, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			core := CoreSingBox
			if i%2 == 1 {
				core = CoreXray
			}
			results[i], errs[i] = cm.EphemeralURLTest(
				directProfile(core), DefaultSettings(), srv.URL+"/", 15000, probeOwnerProxies)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent probes did not finish within 60s")
	}

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Errorf("probe %d: %v", i, errs[i])
			continue
		}
		if results[i] == nil || results[i].Error != "" || results[i].LatencyMs < 0 {
			t.Errorf("probe %d: bad result %+v", i, results[i])
		}
	}
}
