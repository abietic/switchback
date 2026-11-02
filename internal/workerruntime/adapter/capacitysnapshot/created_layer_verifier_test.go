package capacitysnapshot

import (
	"context"
	"errors"
	"testing"
)

func TestCreatedLayerVerifierPublicPolicyAndPinnedTrust(t *testing.T) {
	tr, _, f, req, id := createdFixture(t)
	v, err := NewCreatedLayerVerifier(tr)
	if err != nil {
		t.Fatal(err)
	}
	tr.PublicKey[0] ^= 255
	tr.Storage[0].ProjectID = 99
	tr.Core[0].CgroupPath = "/changed"
	tr.Coverage[0].Role = "changed"
	if err = v.ValidateRequest(context.Background(), req, id); err != nil {
		t.Fatal(err)
	}
	if err = v.VerifyCreatedLayerFacts(context.Background(), f, req, id); err != nil {
		t.Fatal(err)
	}
	f.Identity.EngineID = "other"
	if v.VerifyCreatedLayerFacts(context.Background(), f, req, id) == nil {
		t.Fatal("observed identity drift accepted")
	}
}

func TestCreatedLayerVerifierRejectsUnconfiguredAndCancellation(t *testing.T) {
	tr, _, f, req, id := createdFixture(t)
	var zero CreatedLayerVerifier
	if zero.ValidateRequest(context.Background(), req, id) == nil || zero.VerifyCreatedLayerFacts(context.Background(), f, req, id) == nil {
		t.Fatal("zero verifier accepted")
	}
	if _, err := NewCreatedLayerVerifier(Trust{}); err == nil {
		t.Fatal("empty trust accepted")
	}
	v, err := NewCreatedLayerVerifier(tr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(v.ValidateRequest(ctx, req, id), context.Canceled) || !errors.Is(v.VerifyCreatedLayerFacts(ctx, f, req, id), context.Canceled) {
		t.Fatal("context identity lost")
	}
	req.Generation = 0
	if v.ValidateRequest(context.Background(), req, id) == nil {
		t.Fatal("invalid request accepted")
	}
}
