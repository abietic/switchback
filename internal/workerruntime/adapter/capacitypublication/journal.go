package capacitypublication

import (
	"bytes"
	"encoding/json"

	"github.com/abietic/switchback/internal/platform/capacityjournal"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

const journalSize = capacityjournal.Size

func cloneTrust(t capacitysnapshot.Trust) (capacitysnapshot.Trust, error) {
	if len(t.Core) > 32 || len(t.Storage) > 32 || t.AdmissionPolicy == nil {
		return capacitysnapshot.Trust{}, ErrPublicationInvalid
	}
	t.PublicKey = bytes.Clone(t.PublicKey)
	t.Core = append([]capacitysnapshot.CoreDomain(nil), t.Core...)
	t.Storage = append([]capacitysnapshot.StorageDomain(nil), t.Storage...)
	t.Coverage = append([]capacitysnapshot.StorageCoverage(nil), t.Coverage...)
	p := *t.AdmissionPolicy
	t.AdmissionPolicy = &p
	if capacitysnapshot.ValidateTrust(t) != nil || (config.RuntimeCapacityGuarantee{ID: p.ID, Revision: p.Revision, Digest: p.Digest}).Validate() != nil {
		return capacitysnapshot.Trust{}, ErrPublicationInvalid
	}
	return t, nil
}
func trustBinding(t capacitysnapshot.Trust) [32]byte {
	raw, _ := json.Marshal(t)
	return capacityjournal.Binding(raw)
}
func encodeJournal(binding [32]byte, high int64) ([journalSize]byte, error) {
	b, e := capacityjournal.Encode(binding, high)
	if e != nil {
		return b, ErrPublicationStateUnavailable
	}
	return b, nil
}
func decodeJournal(raw []byte, binding [32]byte) (int64, error) {
	high, e := capacityjournal.Decode(raw, binding)
	if e != nil {
		return 0, ErrPublicationStateUnavailable
	}
	return high, nil
}
