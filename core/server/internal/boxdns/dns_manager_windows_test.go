//go:build windows

package boxdns

import (
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing/common/control"
)

// TestDnsManagerConcurrentAccess hammers SetSystemDNS (Thrift handler
// path) and HandleSystemDNS (interface-monitor callback path) from
// several goroutines with bogus interfaces. LUID lookups fail fast, so
// no real DNS state is touched; under -race this proves the mutex
// guards dnsIsSet/lastIfc.
func TestDnsManagerConcurrentAccess(t *testing.T) {
	d := &DnsManager{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ifc := &control.Interface{Index: 200000 + i, Name: "fake-ifc"}
			_ = d.SetSystemDNS(ifc, false)
			d.HandleSystemDNS(ifc, 0)
			_ = d.SetSystemDNS(ifc, true)
			d.HandleSystemDNS(ifc, 0)
		}(i)
	}
	wg.Wait()
}

// TestDnsManagerConcurrentClearAndMonitor (BUG-007): a clear racing the
// monitor callback must not apply a stale branch. One goroutine flips
// clear/set while others run the callback; under -race the whole
// snapshot-decision-apply sequence must stay atomic. A final callback on
// a known interface pins the resulting state deterministically.
func TestDnsManagerConcurrentClearAndMonitor(t *testing.T) {
	d := &DnsManager{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		clear := false
		for {
			select {
			case <-stop:
				return
			default:
				ifc := &control.Interface{Index: 200001, Name: "flip"}
				_ = d.SetSystemDNS(ifc, clear)
				clear = !clear
			}
		}
	}()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				select {
				case <-stop:
					return
				default:
					ifc := &control.Interface{Index: 200010 + i, Name: "mon"}
					d.HandleSystemDNS(ifc, 0)
				}
			}
		}(i)
	}
	// Let the hammer run briefly, then stop and settle deterministically.
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
	final := &control.Interface{Index: 200099, Name: "final"}
	_ = d.SetSystemDNS(final, false)
	d.HandleSystemDNS(final, 0)
	d.mu.Lock()
	defer d.mu.Unlock()
	// NOTE (BUG-027 interaction): the final Set runs on a bogus LUID,
	// so the apply fails and must NOT flip dnsIsSet (a failed apply
	// reporting itself applied is exactly what BUG-027 fixed). What
	// this test pins is BUG-007's property: the callback still records
	// the handled interface, so lastIfc is fresh, never stale.
	if d.dnsIsSet {
		t.Fatal("dnsIsSet true after failed Set on bogus ifc; want false (BUG-027)")
	}
	if d.lastIfc == nil || d.lastIfc.Index != final.Index {
		t.Fatalf("lastIfc = %+v, want index %d", d.lastIfc, final.Index)
	}
}

// TestHandleSystemDnsUpdatesLastIfc (BUG-008): each handled callback
// refreshes lastIfc to the current interface, so a default flap A->B->A
// never strands the override on the abandoned interface.
func TestHandleSystemDnsUpdatesLastIfc(t *testing.T) {
	d := &DnsManager{}
	ifA := &control.Interface{Index: 1, Name: "A"}
	ifB := &control.Interface{Index: 2, Name: "B"}
	d.HandleSystemDNS(ifA, 0)
	d.mu.Lock()
	if d.lastIfc == nil || d.lastIfc.Index != ifA.Index {
		d.mu.Unlock()
		t.Fatalf("after A: lastIfc = %+v, want index 1", d.lastIfc)
	}
	d.mu.Unlock()
	d.HandleSystemDNS(ifB, 0)
	d.mu.Lock()
	if d.lastIfc == nil || d.lastIfc.Index != ifB.Index {
		d.mu.Unlock()
		t.Fatalf("after B: lastIfc = %+v, want index 2", d.lastIfc)
	}
	d.mu.Unlock()
	d.HandleSystemDNS(ifA, 0)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastIfc == nil || d.lastIfc.Index != ifA.Index {
		t.Fatalf("after flap-back: lastIfc = %+v, want index 1", d.lastIfc)
	}
}

// TestSetSystemDNSReportsFailure — BUG-027: applying/clearing on a bogus
// interface must return an error (the old code always returned nil, so
// the UI reported success while the adapter was untouched). Bogus LUIDs
// fail fast without touching real adapters.
func TestSetSystemDNSReportsFailure(t *testing.T) {
	d := &DnsManager{}
	ifc := &control.Interface{Index: 200777, Name: "bogus"}
	if err := d.SetSystemDNS(ifc, false); err == nil {
		t.Fatal("SetSystemDNS(apply, bogus ifc) = nil, want LUID error")
	}
	if err := d.SetSystemDNS(ifc, true); err == nil {
		t.Fatal("SetSystemDNS(clear, bogus ifc) = nil, want LUID error")
	}
	// A failed apply must not flip the flag: truthfulness for IsSet and
	// for the Stop-time restore retry.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dnsIsSet {
		t.Fatal("dnsIsSet true after failed apply, want false")
	}
}
