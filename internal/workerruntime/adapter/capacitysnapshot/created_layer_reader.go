package capacitysnapshot

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// CreatedLayerReader reads only signed envelopes and shares the immutable facts policy.
type CreatedLayerReader struct {
	directory string
	verifier  *CreatedLayerVerifier
}

func NewCreatedLayerReader(directory string, trust Trust) (*CreatedLayerReader, error) {
	verifier, err := NewCreatedLayerVerifier(trust)
	if err != nil {
		return nil, err
	}
	if _, err := NewReader(directory); err != nil {
		return nil, err
	}
	return &CreatedLayerReader{directory: directory, verifier: verifier}, nil
}

func (r *CreatedLayerReader) CheckCreatedLayer(ctx context.Context, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity) error {
	if ctx == nil {
		return fmt.Errorf("created-layer context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.directory == "" || r.verifier == nil {
		return fmt.Errorf("created-layer reader unconfigured")
	}
	if err := r.verifier.ValidateRequest(ctx, request, identity); err != nil {
		return err
	}
	// Only the complete actual CID can derive this fixed leaf. No caller-controlled path.
	raw, err := readBoundedFile(r.directory, "created-layer-"+identity.ContainerID+".json")
	if cancel := ctx.Err(); cancel != nil {
		return cancel
	}
	if err != nil {
		return fmt.Errorf("created-layer proof unavailable or unsafe")
	}
	err = r.verify(raw, request, identity, time.Now())
	if cancel := ctx.Err(); cancel != nil {
		return cancel
	}
	return err
}

func (r *CreatedLayerReader) verify(raw []byte, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity, now time.Time) error {
	var e CreatedLayerEnvelope
	if err := strictJSON(raw, &e); err != nil {
		return err
	}
	if e.Version != CreatedLayerVersion || e.KeyID != r.verifier.trust.KeyID || len(e.Signature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(r.verifier.trust.PublicKey), append([]byte(CreatedLayerSignatureDomain), e.Payload...), e.Signature) {
		return fmt.Errorf("created-layer signature invalid")
	}
	var f CreatedLayerFacts
	if err := strictJSON(e.Payload, &f); err != nil {
		return err
	}
	return r.verifier.verifyFacts(f, request, identity, now)
}
