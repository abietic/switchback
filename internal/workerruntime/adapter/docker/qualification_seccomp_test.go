package docker

import "testing"

func TestQualificationUsesExactProductionWorkerSeccomp(t *testing.T) {
	want, err := buildWorkerSeccomp(embeddedWorkerSeccompBaseline)
	if err != nil {
		t.Fatal(err)
	}
	got, err := QualificationWorkerSeccomp()
	if err != nil || got != want {
		t.Fatalf("qualification profile differs: %v", err)
	}
}
