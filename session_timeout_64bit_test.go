//go:build !386 && !arm

package solari

import (
	"math"
	"testing"
)

// The int32 seconds limit is only reachable where int is 64 bits; these constants
// would overflow int on 32-bit targets.
func TestSessionTimeoutSecondsInt32Boundary(t *testing.T) {
	got, err := sessionTimeoutSeconds(math.MaxInt32*1000, 0)
	if err != nil || got != math.MaxInt32 {
		t.Errorf("largest ms that fits int32 seconds: got (%d, %v), want (%d, nil)", got, err, math.MaxInt32)
	}
	if _, err := sessionTimeoutSeconds(math.MaxInt32*1000+1, 0); err == nil {
		t.Error("one ms past int32 seconds must be refused")
	}
	if _, err := sessionTimeoutSeconds(math.MaxInt, 0); err == nil {
		t.Error("max int must be refused, not overflow")
	}
}
