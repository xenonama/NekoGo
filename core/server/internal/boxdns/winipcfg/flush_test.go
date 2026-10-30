package winipcfg

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestFlushIPAddressesNoMatchIsNil — BUG-028 smoke: flushing a LUID that
// owns no addresses deletes nothing and reports success. The Delete-error
// propagation itself mirrors FlushRoutes line-for-line (verified by
// review + vet); a destructive Delete-failure repro needs a real adapter
// with an undeletable address, which this sandbox has not.
func TestFlushIPAddressesNoMatchIsNil(t *testing.T) {
	var zero LUID
	if err := zero.FlushIPAddresses(windows.AF_INET); err != nil {
		t.Fatalf("FlushIPAddresses(zero LUID) = %v, want nil", err)
	}
}
