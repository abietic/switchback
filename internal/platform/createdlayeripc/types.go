// Package createdlayeripc authenticates one finite created-layer observation over
// a fixed local socket. Authentication does not grant capacity or execution authority.
package createdlayeripc

import (
	"context"
	"crypto/rand"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"io"
	"math"
	"net"
	"regexp"
	"time"
)

const (
	CoreCgroupPath                  = "/switchback.slice/switchback-core.slice"
	ObserverCgroupPath              = CoreCgroupPath + "/switchback-created-layer-observer.service"
	QualificationObserverCgroupPath = CoreCgroupPath + "/switchback-qualification-observer.service"
	socketPath                      = "/run/switchback-created-layer/observer.sock"
	clientKeyPath                   = "/run/switchback-observer-auth/key"
	serverKeyPath                   = "/etc/switchback/created-layer-observer/key"
	version                         = "switchback.created_layer.ipc.v1"
	window                          = 30 * time.Second
	handshakeWindow                 = 5 * time.Second
)

// Error deliberately carries no external diagnostic strings or secrets.
type Error string

func (e Error) Error() string { return string(e) }

const (
	errInput       Error = "created_layer_ipc: invalid input"
	errWire        Error = "created_layer_ipc: invalid authenticated protocol"
	errNative      Error = "created_layer_ipc: native identity unavailable"
	errUnsupported Error = "created_layer_ipc: unsupported platform"
	errContext     Error = "created_layer_ipc: context unavailable"
)

type Binding struct {
	ManifestDigest  string `json:"manifest_digest"`
	ContainerID     string `json:"container_id"`
	RuntimeID       string `json:"runtime_id"`
	SessionID       string `json:"session_id"`
	RunID           string `json:"run_id"`
	Generation      uint64 `json:"generation"`
	ProfileID       string `json:"profile_id"`
	ProfileRevision uint64 `json:"profile_revision"`
	ProfileDigest   string `json:"profile_digest"`
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func (b Binding) valid(digest string) bool {
	return b.ManifestDigest == digest && digestPattern.MatchString(b.ManifestDigest) && hex64.MatchString(b.ContainerID) && idPattern.MatchString(b.RuntimeID) && idPattern.MatchString(b.SessionID) && idPattern.MatchString(b.RunID) && idPattern.MatchString(b.ProfileID) && digestPattern.MatchString(b.ProfileDigest) && b.Generation > 0 && b.Generation <= math.MaxInt64 && b.ProfileRevision > 0 && b.ProfileRevision <= math.MaxInt64
}
func copyKey(key []byte, digest string) ([]byte, error) {
	if len(key) != 32 || !digestPattern.MatchString(digest) {
		return nil, errInput
	}
	var n byte
	for _, b := range key {
		n |= b
	}
	if n == 0 {
		return nil, errInput
	}
	return append([]byte(nil), key...), nil
}
func checkContext(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return errContext
	}
	return nil
}

type Client struct {
	key     []byte
	digest  string
	dial    func(context.Context) (net.Conn, error)
	peer    func(net.Conn, bool) error
	now     func() time.Time
	entropy io.Reader
}
type Server struct {
	key       []byte
	digest    string
	listen    func(observerPlacement) (net.Listener, func(), error)
	placement observerPlacement
	peer      func(net.Conn, bool) error
	now       func() time.Time
	entropy   io.Reader
	collect   func(context.Context, Binding) capacityprobe.CreatedLayerCollection
}

func NewClient(key []byte, manifestDigest string) (*Client, error) {
	k, e := copyKey(key, manifestDigest)
	if e != nil {
		return nil, e
	}
	return &Client{key: k, digest: manifestDigest, dial: nativeDial, peer: nativePeer, now: time.Now, entropy: rand.Reader}, nil
}
func NewServer(key []byte, manifestDigest string) (*Server, error) {
	return newServer(key, manifestDigest, legacyObserverPlacement)
}

// NewQualificationServer accepts only the fixed qualification observer unit.
// It does not add that unit to the legacy constructor's placement policy.
func NewQualificationServer(key []byte, manifestDigest string) (*Server, error) {
	return newServer(key, manifestDigest, qualificationObserverPlacement)
}

func newServer(key []byte, manifestDigest string, policy observerPlacement) (*Server, error) {
	k, e := copyKey(key, manifestDigest)
	if e != nil {
		return nil, e
	}
	return &Server{key: k, digest: manifestDigest, listen: nativeListen, placement: policy, peer: nativePeer, now: time.Now, entropy: rand.Reader, collect: func(ctx context.Context, b Binding) capacityprobe.CreatedLayerCollection {
		return capacityprobe.CollectCreatedLayer(ctx, capacityprobe.CreatedLayerTarget{ContainerID: b.ContainerID}, capacityprobe.CreatedLayerSources{Get: capacityprobe.NewCreatedLayerEngineReader(), Physical: capacityprobe.ReadCreatedLayerPhysical})
	}}, nil
}
func ReadClientKey() ([]byte, error) { return nativeKey(clientKeyPath) }
func ReadServerKey() ([]byte, error) { return nativeKey(serverKeyPath) }
