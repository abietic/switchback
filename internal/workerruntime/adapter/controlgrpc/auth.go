package controlgrpc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"google.golang.org/grpc/metadata"
)

type RuntimeIdentity struct {
	WorkerSessionID   string
	RuntimeInstanceID string
	RuntimeGeneration uint32
	ChannelEpoch      uint64
}

func (identity RuntimeIdentity) validate() error {
	if !validIdentity(identity.WorkerSessionID) || !validIdentity(identity.RuntimeInstanceID) ||
		identity.RuntimeGeneration == 0 || identity.ChannelEpoch == 0 {
		return ErrInvalidIdentity
	}
	return nil
}

type Credential struct {
	Identity RuntimeIdentity
	Token    string
}

func NewCredential(identity RuntimeIdentity) (Credential, error) {
	if err := identity.validate(); err != nil {
		return Credential{}, err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return Credential{}, fmt.Errorf("generate WorkerControl credential")
	}
	return Credential{
		Identity: identity,
		Token:    base64.RawURLEncoding.EncodeToString(random),
	}, nil
}

func (credential Credential) String() string {
	return fmt.Sprintf(
		"Credential{WorkerSessionID:%q RuntimeInstanceID:%q RuntimeGeneration:%d ChannelEpoch:%d Token:[REDACTED]}",
		credential.Identity.WorkerSessionID,
		credential.Identity.RuntimeInstanceID,
		credential.Identity.RuntimeGeneration,
		credential.Identity.ChannelEpoch,
	)
}

func (credential Credential) GoString() string { return credential.String() }

func bearerFromContext(ctx context.Context) (string, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", ErrUnauthenticated
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if !validToken(token) {
		return "", ErrUnauthenticated
	}
	return token, nil
}

func validToken(token string) bool {
	return token != "" && strings.TrimSpace(token) == token && !strings.ContainsAny(token, "\r\n\x00")
}

func validIdentity(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00/")
}

func secureEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
