package fixture

import (
	"runtime"
	"testing"
)

func TestReady(t *testing.T) {
	if !Ready() || runtime.Version() != "go1.26.5" {
		t.Fatalf("unexpected runtime: %s", runtime.Version())
	}
}
