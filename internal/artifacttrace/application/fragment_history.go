package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

type HistoricalModelRef struct {
	MessageID      string
	Sequence       uint64
	EnvelopeDigest string
}
type HistoricalTurnRef struct {
	AgentTurnID      string
	ManifestChecksum string
}
type RequestHistoricalFragments struct {
	ActorID        string
	Scope          artifactdomain.ObservationScope
	Version        string
	Initial        HistoricalTurnRef
	Target         HistoricalTurnRef
	Models         []HistoricalModelRef
	IdempotencyKey string
}
type HistoricalFragmentSelection struct {
	RequestID     string
	RequestDigest string
}
type FragmentHistory interface {
	Request(context.Context, RequestHistoricalFragments) (HistoricalFragmentSelection, error)
}

var ErrHistoricalSelection = errors.New("historical_selection_invalid")

// CanonicalHistoricalRequest gives an exact set stable order. Duplicates are
// rejected, including two distinct fingerprints claiming one event identity.
func CanonicalHistoricalRequest(r RequestHistoricalFragments) ([]byte, string, error) {
	s := r.Scope
	if r.ActorID == "" || r.ActorID != s.UserID || s.TenantID == "" || s.WorkspaceID == "" || s.WorkerSessionID == "" || s.WorkerRunID == "" || r.Version != FragmentProjectionVersion || r.IdempotencyKey == "" || len(r.IdempotencyKey) > 256 || r.Initial.AgentTurnID == "" || r.Target.AgentTurnID == "" || !historyDigest(r.Initial.ManifestChecksum) || !historyDigest(r.Target.ManifestChecksum) || len(r.Models) > 1000 {
		return nil, "", ErrHistoricalSelection
	}
	if r.Initial.AgentTurnID == r.Target.AgentTurnID && r.Initial.ManifestChecksum != r.Target.ManifestChecksum {
		return nil, "", ErrHistoricalSelection
	}
	r.Models = append([]HistoricalModelRef{}, r.Models...)
	sort.Slice(r.Models, func(i, j int) bool {
		a, b := r.Models[i], r.Models[j]
		if a.MessageID != b.MessageID {
			return a.MessageID < b.MessageID
		}
		if a.Sequence != b.Sequence {
			return a.Sequence < b.Sequence
		}
		return a.EnvelopeDigest < b.EnvelopeDigest
	})
	seenID, seenSequence := map[string]bool{}, map[uint64]bool{}
	for _, m := range r.Models {
		if m.MessageID == "" || m.Sequence == 0 || !historyDigest(m.EnvelopeDigest) || seenID[m.MessageID] || seenSequence[m.Sequence] {
			return nil, "", ErrHistoricalSelection
		}
		seenID[m.MessageID] = true
		seenSequence[m.Sequence] = true
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, "", ErrHistoricalSelection
	}
	return raw, artifactdomain.Checksum(raw), nil
}
func historyDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	b, err := hex.DecodeString(s[7:])
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}

// Backend-only capture port. Request callers cannot supply source bytes,
// scanner results or attestations. Only the owner constructs these inputs from
// revalidated durable facts and verified objects.
type HistoricalObservationCapture struct {
	Frame      []byte
	Evidence   any
	Scope      ObservationScanScope
	Provenance ObservationProvenance
}
type HistoricalObservationPreparer interface {
	PrepareHistoricalObservation(context.Context, HistoricalObservationCapture) PreparedObservation
}
