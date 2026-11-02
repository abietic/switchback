package control

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestAuthenticatorRejectsMissingOrWrongBearerToken(t *testing.T) {
	authenticator := NewAuthenticator()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	if err := authenticator.Register(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	tests := []struct {
		name          string
		authorization string
	}{
		{name: "missing"},
		{name: "wrong scheme", authorization: "Basic worker-token"},
		{name: "wrong token", authorization: "Bearer wrong-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if test.authorization != "" {
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", test.authorization))
			}
			if _, err := authenticator.Authenticate(ctx); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("Authenticate() error = %v, want ErrUnauthenticated", err)
			}
		})
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer worker-token"))
	got, err := authenticator.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got != identity {
		t.Fatalf("Authenticate() identity = %+v, want %+v", got, identity)
	}
}

func TestAuthenticatorRejectsRuntimeGenerationTwo(t *testing.T) {
	authenticator := NewAuthenticator()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 2,
	}
	if err := authenticator.Register(Credential{Identity: identity, Token: "worker-token"}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("Register() generation 2 error = %v, want ErrInvalidIdentity", err)
	}
}
