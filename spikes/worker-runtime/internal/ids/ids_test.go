package ids

import (
	"regexp"
	"testing"
)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestNewReturnsUniqueUUIDShapedValues(t *testing.T) {
	first := New()
	second := New()

	if first == "" || second == "" {
		t.Fatal("New() returned an empty ID")
	}
	if !uuidShape.MatchString(first) || !uuidShape.MatchString(second) {
		t.Fatalf("New() returned non-UUID-shaped IDs: %q, %q", first, second)
	}
	if first == second {
		t.Fatalf("New() returned duplicate IDs: %q", first)
	}
}
