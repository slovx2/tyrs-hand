package fixture

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewID(t *testing.T) {
	if _, err := uuid.Parse(NewID()); err != nil {
		t.Fatalf("invalid UUID: %v", err)
	}
}
