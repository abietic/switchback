package application

import (
	"bytes"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	"testing"
)

func TestFragmentHistoryCanonicalExactSelection(t *testing.T) {
	r := RequestHistoricalFragments{ActorID: "actor", Scope: artifactdomain.ObservationScope{TenantID: "tenant", UserID: "actor", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run"}, Version: FragmentProjectionVersion, Initial: HistoricalTurnRef{"initial", artifactdomain.Checksum([]byte("initial"))}, Target: HistoricalTurnRef{"target", artifactdomain.Checksum([]byte("target"))}, IdempotencyKey: "key", Models: []HistoricalModelRef{{"b", 2, artifactdomain.Checksum([]byte("b"))}, {"a", 1, artifactdomain.Checksum([]byte("a"))}}}
	a, d, err := CanonicalHistoricalRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Models[0], r.Models[1] = r.Models[1], r.Models[0]
	b, e, err := CanonicalHistoricalRequest(r)
	if err != nil || d != e || !bytes.Equal(a, b) {
		t.Fatal("set order changed request identity")
	}
	r.Models = r.Models[:1]
	_, e, err = CanonicalHistoricalRequest(r)
	if err != nil || d == e {
		t.Fatal("changed model set retained identity")
	}
	r.Models = append(r.Models, r.Models[0])
	if _, _, err := CanonicalHistoricalRequest(r); err == nil {
		t.Fatal("duplicate exact model accepted")
	}
}
