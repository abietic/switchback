package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	app "github.com/abietic/switchback/internal/workerruntime/application"
)

// No filesystem, signature file, or remote I/O occurs under the pool lock.
func persistedCapacityTrust(raw []byte, digest string) (capacitysnapshot.Trust, error) {
	var trust capacitysnapshot.Trust
	if raw == nil && digest == "" {
		return trust, app.CapacityUninitialized
	}
	if len(raw) == 0 {
		return trust, app.CapacityEvidenceInvalid
	}
	if len(raw) > capacitysnapshot.MaxBytes || capacitysnapshot.Digest(raw) != digest {
		return trust, app.CapacityEvidenceInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&trust) != nil || capacitysnapshot.ValidateTrust(trust) != nil {
		return capacitysnapshot.Trust{}, app.CapacityEvidenceInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || bytes.Equal(bytes.TrimSpace(fields["admission_policy"]), []byte("null")) {
		return capacitysnapshot.Trust{}, app.CapacityEvidenceInvalid
	}
	canonical, err := json.Marshal(trust)
	if err != nil || !bytes.Equal(raw, canonical) {
		return capacitysnapshot.Trust{}, app.CapacityEvidenceInvalid
	}
	return trust, nil
}
func (h *CapacityTx) originalTrust(ctx context.Context) (capacitysnapshot.Trust, error) {
	var raw []byte
	var digest string
	if err := h.tx.QueryRow(ctx, `SELECT trust_bytes,COALESCE(trust_digest,'') FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&raw, &digest); err != nil {
		return capacitysnapshot.Trust{}, err
	}
	return persistedCapacityTrust(raw, digest)
}
func copyCapacityGuarantee(ref *app.CapacityGuaranteeRef) *app.CapacityGuaranteeRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}
func equalCapacityGuarantee(a, b *app.CapacityGuaranteeRef) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

// Only terminal settlement may use a legacy nil reference when BOTH original
// Trust bytes and digest are absent. The immutable snapshot is checked separately
// before release; admission/consume/replay never call this compatibility path.
func (h *CapacityTx) findSettlementReservation(ctx context.Context, where string, args ...any) (app.CapacityReservation, string, error) {
	r, digest, err := h.findReservation(ctx, where, args...)
	if errors.Is(err, app.CapacityUninitialized) {
		return r, digest, nil
	}
	return r, digest, err
}
func settlementGuaranteeMatches(raw []byte, original *app.CapacityGuaranteeRef) bool {
	var snapshot struct {
		Capacity *struct{ Reservation app.CapacityReservation } `json:"runtime_capacity"`
	}
	return json.Unmarshal(raw, &snapshot) == nil && snapshot.Capacity != nil && equalCapacityGuarantee(snapshot.Capacity.Reservation.Guarantee, original)
}
