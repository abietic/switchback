package control

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/grpc/metadata"
)

var (
	ErrUnauthenticated  = errors.New("worker authentication failed")
	ErrInvalidIdentity  = errors.New("invalid runtime identity")
	ErrCredentialExists = errors.New("runtime credential already exists")
)

const SpikeRuntimeGeneration uint32 = 1

// Identity is the immutable runtime identity bound to one worker credential.
// Agent turn identity is intentionally excluded because a runtime serves both
// turns in the M1 lifecycle.
type Identity struct {
	WorkerSessionID   string
	WorkerRunID       string
	RuntimeInstanceID string
	RuntimeGeneration uint32
}

func (i Identity) validate() error {
	if strings.TrimSpace(i.WorkerSessionID) == "" ||
		strings.TrimSpace(i.WorkerRunID) == "" ||
		strings.TrimSpace(i.RuntimeInstanceID) == "" ||
		i.RuntimeGeneration != SpikeRuntimeGeneration {
		return ErrInvalidIdentity
	}
	return nil
}

// Credential binds one scoped bearer token to one exact runtime identity.
type Credential struct {
	Identity Identity
	Token    string
}

func (c Credential) String() string {
	return fmt.Sprintf(
		"Credential{WorkerSessionID:%q WorkerRunID:%q RuntimeInstanceID:%q RuntimeGeneration:%d Token:[REDACTED]}",
		c.Identity.WorkerSessionID,
		c.Identity.WorkerRunID,
		c.Identity.RuntimeInstanceID,
		c.Identity.RuntimeGeneration,
	)
}

func (c Credential) GoString() string {
	return c.String()
}

// Authenticator owns scoped worker credentials without exposing lookup state.
type Authenticator struct {
	mu          sync.RWMutex
	credentials map[Identity]string
}

func NewAuthenticator() *Authenticator {
	return &Authenticator{credentials: make(map[Identity]string)}
}

func (a *Authenticator) Register(credential Credential) error {
	if err := credential.Identity.validate(); err != nil {
		return err
	}
	if credential.Token == "" {
		return fmt.Errorf("%w: empty token", ErrUnauthenticated)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if token, exists := a.credentials[credential.Identity]; exists {
		if secureEqual(token, credential.Token) {
			return nil
		}
		return ErrCredentialExists
	}
	for identity, token := range a.credentials {
		if secureEqual(token, credential.Token) && identity != credential.Identity {
			return ErrCredentialExists
		}
	}
	a.credentials[credential.Identity] = credential.Token
	return nil
}

func (a *Authenticator) Revoke(identity Identity) {
	a.mu.Lock()
	delete(a.credentials, identity)
	a.mu.Unlock()
}

func (a *Authenticator) Contains(identity Identity) bool {
	a.mu.RLock()
	_, ok := a.credentials[identity]
	a.mu.RUnlock()
	return ok
}

func (a *Authenticator) Authenticate(ctx context.Context) (Identity, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return Identity{}, ErrUnauthenticated
	}
	presented := strings.TrimPrefix(values[0], "Bearer ")
	if presented == "" {
		return Identity{}, ErrUnauthenticated
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	for identity, token := range a.credentials {
		if secureEqual(token, presented) {
			return identity, nil
		}
	}
	return Identity{}, ErrUnauthenticated
}

func secureEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
