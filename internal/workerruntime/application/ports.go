package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

type Clock interface {
	Now() time.Time
}

type RuntimeProvisionRequest struct {
	SourcePlanDigest         string
	WorkerSessionID          string
	WorkerRunID              string
	RuntimeInstanceID        string
	Generation               uint64
	ResourceProfile          ProfileRef
	AgentProfile             ProfileRef
	NetworkProfile           ProfileRef
	NetworkEnforcementStatus string
	Storage                  SessionStorage
	Secrets                  RuntimeSecretResources
}

type ProfileRef struct {
	ID       string
	Revision uint64
	Digest   string
}

type ProvisionedRuntime struct {
	RuntimeInstanceID string
	ContainerID       string
	Generation        uint64
	StartedAt         time.Time
	// PhysicalEffectUnknown forbids no-CID release/discard after an issued effect.
	PhysicalEffectUnknown bool
}

type RuntimeInspection struct {
	Exists      bool
	Running     bool
	ContainerID string
	Generation  uint64
	ExitCode    *int
	ObservedAt  time.Time
}

// CreatedRuntimeIdentity is the actual Engine create result for this invocation.
type CreatedRuntimeIdentity struct {
	ContainerID       string
	RuntimeInstanceID string
	WorkerSessionID   string
	Generation        uint64
}

// RuntimePreStartCheck is required per call; it never travels in a TaskSpec/context.
type RuntimePreStartCheck func(context.Context, CreatedRuntimeIdentity) error

type InitialCreatedLayerChecker interface {
	CheckCreatedLayer(context.Context, RuntimeProvisionRequest, CreatedRuntimeIdentity) error
}

// RejectRuntimePreStart keeps callers without initial physical proof fail closed.
func RejectRuntimePreStart(context.Context, CreatedRuntimeIdentity) error {
	return fmt.Errorf("initial created-layer physical proof is unavailable")
}

type UnavailableInitialCreatedLayerChecker struct{}

func (UnavailableInitialCreatedLayerChecker) CheckCreatedLayer(ctx context.Context, _ RuntimeProvisionRequest, identity CreatedRuntimeIdentity) error {
	return RejectRuntimePreStart(ctx, identity)
}

type RuntimeProvisioner interface {
	Provision(context.Context, RuntimeProvisionRequest, RuntimePreStartCheck) (ProvisionedRuntime, error)
	Inspect(context.Context, string) (RuntimeInspection, error)
}

type RuntimeCleanupRequest struct {
	RuntimeInstanceID   string
	ExpectedContainerID string
	Generation          uint64
}

type RuntimeCleanupObservation struct {
	Confirmed          bool
	ContainerExists    bool
	SecretVolumeExists bool
	ContainerRef       string
	SecretVolumeRef    string
	ObservedAt         time.Time
}

type RuntimeCleaner interface {
	Cleanup(context.Context, RuntimeCleanupRequest) (RuntimeCleanupObservation, error)
}

type WorkerControlCommand struct {
	Envelope contractsv1.RuntimeControlEnvelope
	Payload  json.RawMessage
}

type WorkerControlEvent struct {
	Envelope contractsv1.RuntimeControlEnvelope
	Payload  json.RawMessage
	Delivery WorkerControlEventDelivery
}

// WorkerControlEventDelivery advances the durable event inbox only after the
// application has committed the event's real business effect. Once either
// Apply method is invoked, the delivery owns retry even when it returns an
// error; callers release only when they fail before attempting Apply.
type WorkerControlEventDelivery interface {
	Apply(context.Context, json.RawMessage) error
	// ReleaseForRetry relinquishes an in-memory delivery lease when the
	// business transaction did not commit, allowing the same durable inbox
	// message to be delivered again without advancing its applied cursor.
	ReleaseForRetry()
}

type TurnSettlementWorkerControlEventDelivery interface {
	WorkerControlEventDelivery
	ApplyTurnSettlement(
		context.Context,
		json.RawMessage,
		TurnSettlementApplyAuthority,
	) error
}

type WorkerControl interface {
	Send(context.Context, WorkerControlCommand) error
	Events(context.Context, string, uint64) (<-chan WorkerControlEvent, error)
	Fence(context.Context, string, uint64, string) error
}

type AgentInitializeRequest struct {
	WorkerSessionID string
	RuntimeID       string
	Generation      uint64
	ProtocolVersion string
}

type AgentCapabilities struct {
	ProtocolVersion string
	ResumeSession   bool
	LoadSession     bool
	Prompt          bool
	Cancel          bool
	Raw             json.RawMessage
}

type AgentSessionRequest struct {
	WorkerSessionID string
	AgentSessionID  string
	WorkingCopyPath string
	LoadExisting    bool
}

type AgentPromptRequest struct {
	AgentSessionID string
	AgentTurnID    string
	Content        string
}

type AgentUpdate struct {
	Kind    string
	Payload json.RawMessage
}

type AgentAdapter interface {
	Initialize(context.Context, AgentInitializeRequest) (AgentCapabilities, error)
	Authenticate(context.Context, string) error
	OpenSession(context.Context, AgentSessionRequest) (string, error)
	Prompt(context.Context, AgentPromptRequest) (<-chan AgentUpdate, error)
	Cancel(context.Context, string, string) error
	Close(context.Context) error
}

type SessionStorage struct {
	BackendRoot string
	EngineRoot  string
	WorkingCopy StorageMount
	AgentState  StorageMount
	TraceSpool  StorageMount
}

type StorageMount struct {
	BackendPath  string
	EngineSource string
	WorkerPath   string
}

type RuntimeSecretResources struct {
	BackendRoot     string
	EngineRoot      string
	Bootstrap       StorageMount
	ModelCredential StorageMount
}

type ObjectIdentity struct {
	TenantID        string
	UserID          string
	WorkspaceID     string
	WorkerSessionID string
	WorkerRunID     string
	AgentTurnID     string
	Kind            string
}

type TemporaryObject struct {
	UploadID string
	Size     int64
	Checksum string
}

type StoredObject struct {
	Key      string
	Size     int64
	Checksum string
}

// ErrObjectIntegrity identifies a proven checksum/size mismatch or immutable-key
// conflict. ObjectStore implementations must not wrap I/O or cancellation errors
// with it: inability to read bytes does not establish corruption.
var ErrObjectIntegrity = errors.New("ObjectStore integrity violation")

type ObjectStore interface {
	PutTemporary(context.Context, ObjectIdentity, io.Reader) (TemporaryObject, error)
	Finalize(context.Context, ObjectIdentity, TemporaryObject) (StoredObject, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Verify(context.Context, string, string) error
}

type ModelObservation struct {
	ObservationID   string
	WorkerSessionID string
	WorkerRunID     string
	AgentTurnID     string
	RuntimeID       string
	Generation      uint64
	Model           string
	Route           string
	Input           json.RawMessage
	Output          json.RawMessage
	InputTokens     int64
	OutputTokens    int64
	StartedAt       time.Time
	EndedAt         time.Time
	Status          string
}

type ModelObservationSink interface {
	Record(context.Context, ModelObservation) error
}
