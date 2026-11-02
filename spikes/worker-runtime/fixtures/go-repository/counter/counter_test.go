package counter

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(3, 2); got != 5 {
		t.Fatalf("Add(3, 2) = %d, want 5", got)
	}
}
