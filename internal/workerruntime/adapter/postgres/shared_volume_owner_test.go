package postgres

import (
	"encoding/json"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"testing"
)

func TestSharedVolumePersistedOriginalOwnerExactSettlement(t *testing.T) {
	tr := guaranteeTrustFixture()
	old := copyCapacityGuarantee(tr.AdmissionPolicy)
	g := config.M1SharedVolumeCapacityGuarantee()
	tr.AdmissionPolicy = &app.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}
	raw, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := persistedCapacityTrust(raw, capacitysnapshot.Digest(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !equalCapacityGuarantee(got.AdmissionPolicy, tr.AdmissionPolicy) {
		t.Fatal("persisted owner lost exact new guarantee")
	}
	detached := copyCapacityGuarantee(got.AdmissionPolicy)
	detached.ID = "mutated"
	if !equalCapacityGuarantee(got.AdmissionPolicy, tr.AdmissionPolicy) {
		t.Fatal("copy mutated original owner")
	}
	snapshot, err := json.Marshal(map[string]any{"runtime_capacity": map[string]any{"Reservation": app.CapacityReservation{Guarantee: got.AdmissionPolicy}}})
	if err != nil {
		t.Fatal(err)
	}
	if !settlementGuaranteeMatches(snapshot, tr.AdmissionPolicy) || settlementGuaranteeMatches(snapshot, old) || settlementGuaranteeMatches(snapshot, nil) {
		t.Fatal("settlement crossed exact guarantee boundary")
	}
	for _, field := range []string{"id", "revision", "digest"} {
		other := copyCapacityGuarantee(tr.AdmissionPolicy)
		switch field {
		case "id":
			other.ID = "unknown"
		case "revision":
			other.Revision++
		case "digest":
			other.Digest = old.Digest
		}
		if equalCapacityGuarantee(other, tr.AdmissionPolicy) || settlementGuaranteeMatches(snapshot, other) {
			t.Fatal("mutated settlement matched", field)
		}
	}
	if _, err := persistedCapacityTrust(raw, capacitysnapshot.Digest([]byte("old persisted bytes"))); err == nil {
		t.Fatal("persisted trust digest drift accepted")
	}
}
