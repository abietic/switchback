// Package service contains the in-memory orchestration shell for the spike.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"switchback.local/spikes/worker-runtime/backend/internal/control"
	"switchback.local/spikes/worker-runtime/backend/internal/langfuse"
	"switchback.local/spikes/worker-runtime/backend/internal/objectstore"
	"switchback.local/spikes/worker-runtime/backend/internal/runstate"
	"switchback.local/spikes/worker-runtime/backend/internal/runtime"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
	"switchback.local/spikes/worker-runtime/internal/ids"
	"switchback.local/spikes/worker-runtime/internal/redact"
)

var (
	ErrNotFound      = errors.New("runtime run not found")
	ErrConflict      = errors.New("runtime turn conflict")
	ErrUnauthorized  = errors.New("worker authentication failed")
	ErrForbidden     = errors.New("worker identity does not match request")
	ErrInvalid       = errors.New("invalid request")
	ErrNotConfigured = errors.New("runtime provisioning is not configured")
)

const (
	MaxRuntimeDiagnostics  = 128
	maxDiagnosticKindRunes = 64
	maxDiagnosticCodeRunes = 128
	maxDiagnosticTextRunes = 512
)

// MaterializedSession is intentionally an alias so a Task 7 provisioner can
// use runtime.MaterializedSession without an adapter for storage paths.
type MaterializedSession = runtime.MaterializedSession

// Materializer synchronously creates server-owned storage for an accepted session.
type Materializer interface {
	Materialize(context.Context, string) (MaterializedSession, error)
	Rollback(context.Context, string, MaterializedSession) error
}

// EvidenceStore is the small persistence boundary used by the upload handler.
type EvidenceStore interface {
	Put(objectstore.ObjectIdentity, []byte) (objectstore.StoredObject, error)
}

// schedulerHandoff is the package-internal capability passed only to trusted
// Task 6/7 scheduler implementations. It must never become an HTTP DTO.
type schedulerHandoff struct {
	workerSessionID    string
	workerRunID        string
	runtimeInstanceID  string
	runtimeGeneration  uint32
	turnID             string
	traceID            string
	goal               string
	acceptanceCriteria []string
	constraints        []string
	message            string
	workerToken        string
	materialized       MaterializedSession
}

func (h schedulerHandoff) String() string {
	identity := handoffIdentity(h)
	return fmt.Sprintf(
		"schedulerHandoff{WorkerSessionID:%q WorkerRunID:%q RuntimeInstanceID:%q RuntimeGeneration:%d Private:[REDACTED]}",
		identity.WorkerSessionID,
		identity.WorkerRunID,
		identity.RuntimeInstanceID,
		identity.RuntimeGeneration,
	)
}

func (h schedulerHandoff) GoString() string {
	return h.String()
}

// Scheduler owns asynchronous side effects. Task 6/7 will replace this shell
// with CommandBus and RuntimeProvisioner-backed dispatch.
type Scheduler interface {
	Initial(context.Context, schedulerHandoff) error
	Turn(context.Context, schedulerHandoff) error
	Cleanup(context.Context, schedulerHandoff) error
}

// ControlPlane is the narrow command/registration interface used by the
// service-package scheduler adapter.
type ControlPlane interface {
	RegisterCredential(control.Credential) error
	Revoke(control.Identity)
	WaitReady(context.Context, control.Identity) error
	Send(context.Context, control.Identity, *workercontrolv1.BackendEnvelope) error
}

// RuntimeProvisionRequest is passed only to trusted runtime provisioning code.
// It is intentionally separate from every public HTTP representation.
type RuntimeProvisionRequest struct {
	Identity     control.Identity
	WorkerToken  string
	Materialized MaterializedSession
}

func (r RuntimeProvisionRequest) String() string {
	return fmt.Sprintf(
		"RuntimeProvisionRequest{WorkerSessionID:%q WorkerRunID:%q RuntimeInstanceID:%q RuntimeGeneration:%d WorkerToken:[REDACTED] Materialized:[REDACTED]}",
		r.Identity.WorkerSessionID,
		r.Identity.WorkerRunID,
		r.Identity.RuntimeInstanceID,
		r.Identity.RuntimeGeneration,
	)
}

func (r RuntimeProvisionRequest) GoString() string {
	return r.String()
}

// RuntimeProvisioner owns runtime side effects. Task 7 supplies the Docker adapter.
type RuntimeProvisioner interface {
	Provision(context.Context, RuntimeProvisionRequest) error
	Cleanup(context.Context, control.Identity) error
}

type UnavailableRuntimeProvisioner struct{}

func (UnavailableRuntimeProvisioner) Provision(context.Context, RuntimeProvisionRequest) error {
	return ErrNotConfigured
}

func (UnavailableRuntimeProvisioner) Cleanup(context.Context, control.Identity) error {
	return ErrNotConfigured
}

type runtimeScheduler struct {
	control      ControlPlane
	provisioner  RuntimeProvisioner
	agentProfile agentprofile.Profile

	mu       sync.Mutex
	runtimes map[control.Identity]*scheduledRuntime
}

type scheduledRuntime struct {
	operationMu sync.Mutex

	registrationMu   sync.Mutex
	cleanupRequested atomic.Bool

	provisionAttempted bool
	fenceOnce          sync.Once
	stopOnce           sync.Once
	stopErr            error
}

// NewRuntimeScheduler constructs the trusted adapter that can read private
// scheduler handoffs without widening HTTP contracts.
func NewRuntimeScheduler(
	controlPlane ControlPlane,
	provisioner RuntimeProvisioner,
	profile agentprofile.Profile,
) (Scheduler, error) {
	if _, err := agentprofile.Parse(string(profile)); err != nil {
		return nil, err
	}
	if provisioner == nil {
		provisioner = UnavailableRuntimeProvisioner{}
	}
	return &runtimeScheduler{
		control:      controlPlane,
		provisioner:  provisioner,
		agentProfile: profile,
		runtimes:     make(map[control.Identity]*scheduledRuntime),
	}, nil
}

func (s *runtimeScheduler) Initial(ctx context.Context, handoff schedulerHandoff) error {
	if s.control == nil {
		return fmt.Errorf("%w: worker control", ErrNotConfigured)
	}
	identity := handoffIdentity(handoff)
	lifecycle := s.lifecycle(identity)
	lifecycle.operationMu.Lock()
	defer lifecycle.operationMu.Unlock()
	if lifecycle.isCleanupRequested() {
		return runtimeCleanupRequestedError()
	}

	credential := control.Credential{Identity: identity, Token: handoff.workerToken}
	if err := s.registerCredential(lifecycle, credential); err != nil {
		return fmt.Errorf("register worker credential: %w", err)
	}
	if lifecycle.isCleanupRequested() {
		s.fence(lifecycle, identity)
		return runtimeCleanupRequestedError()
	}
	request := RuntimeProvisionRequest{
		Identity:     identity,
		WorkerToken:  handoff.workerToken,
		Materialized: handoff.materialized,
	}
	lifecycle.provisionAttempted = true
	if err := s.provisioner.Provision(ctx, request); err != nil {
		return s.rollback(ctx, lifecycle, identity, fmt.Errorf("provision runtime: %w", err))
	}
	if err := s.control.WaitReady(ctx, identity); err != nil {
		return s.rollback(ctx, lifecycle, identity, fmt.Errorf("wait for runtime ready: %w", err))
	}
	if lifecycle.isCleanupRequested() {
		return s.rollback(ctx, lifecycle, identity, runtimeCleanupRequestedError())
	}
	command := &workercontrolv1.BackendEnvelope{
		Payload: &workercontrolv1.BackendEnvelope_InitializeSession{
			InitializeSession: &workercontrolv1.InitializeSession{
				WorkingCopy:  runtime.WorkerWorkingCopyPath,
				AgentState:   runtime.WorkerAgentStatePath,
				TraceSpool:   runtime.WorkerTraceSpoolPath,
				AgentProfile: s.agentProfile.String(),
				TraceId:      handoff.traceID,
			},
		},
	}
	if err := s.control.Send(ctx, identity, command); err != nil {
		return s.rollback(ctx, lifecycle, identity, fmt.Errorf("send initialize session: %w", err))
	}
	return nil
}

func (s *runtimeScheduler) Turn(ctx context.Context, handoff schedulerHandoff) error {
	if s.control == nil {
		return fmt.Errorf("%w: worker control", ErrNotConfigured)
	}
	identity := handoffIdentity(handoff)
	lifecycle := s.lifecycle(identity)
	lifecycle.operationMu.Lock()
	defer lifecycle.operationMu.Unlock()
	if lifecycle.isCleanupRequested() {
		return runtimeCleanupRequestedError()
	}
	command := &workercontrolv1.BackendEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			Identity: &workercontrolv1.Identity{AgentTurnId: handoff.turnID},
		},
		Payload: &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: turnPrompt(handoff)},
		},
	}
	if err := s.control.Send(ctx, identity, command); err != nil {
		return s.rollback(ctx, lifecycle, identity, fmt.Errorf("send agent turn: %w", err))
	}
	return nil
}

func (s *runtimeScheduler) Cleanup(ctx context.Context, handoff schedulerHandoff) error {
	if s.control == nil {
		return fmt.Errorf("%w: worker control", ErrNotConfigured)
	}
	identity := handoffIdentity(handoff)
	lifecycle := s.lifecycle(identity)
	lifecycle.requestCleanup()
	s.fence(lifecycle, identity)

	lifecycle.operationMu.Lock()
	defer lifecycle.operationMu.Unlock()
	return s.stop(ctx, lifecycle, identity)
}

func (s *runtimeScheduler) lifecycle(identity control.Identity) *scheduledRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	lifecycle := s.runtimes[identity]
	if lifecycle == nil {
		lifecycle = &scheduledRuntime{}
		s.runtimes[identity] = lifecycle
	}
	return lifecycle
}

func (s *runtimeScheduler) registerCredential(lifecycle *scheduledRuntime, credential control.Credential) error {
	lifecycle.registrationMu.Lock()
	defer lifecycle.registrationMu.Unlock()
	if lifecycle.isCleanupRequested() {
		return runtimeCleanupRequestedError()
	}
	return s.control.RegisterCredential(credential)
}

func (s *runtimeScheduler) fence(lifecycle *scheduledRuntime, identity control.Identity) {
	lifecycle.fenceOnce.Do(func() {
		s.control.Revoke(identity)
	})
}

func (s *runtimeScheduler) stop(ctx context.Context, lifecycle *scheduledRuntime, identity control.Identity) error {
	if !lifecycle.provisionAttempted {
		return nil
	}
	lifecycle.stopOnce.Do(func() {
		lifecycle.stopErr = s.provisioner.Cleanup(ctx, identity)
	})
	return lifecycle.stopErr
}

func (s *runtimeScheduler) rollback(
	ctx context.Context,
	lifecycle *scheduledRuntime,
	identity control.Identity,
	original error,
) error {
	s.fence(lifecycle, identity)
	if err := s.stop(ctx, lifecycle, identity); err != nil {
		return errors.Join(original, fmt.Errorf("cleanup runtime: %w", err))
	}
	return original
}

func (r *scheduledRuntime) requestCleanup() {
	r.cleanupRequested.Store(true)
	r.registrationMu.Lock()
	r.registrationMu.Unlock()
}

func (r *scheduledRuntime) isCleanupRequested() bool {
	return r.cleanupRequested.Load()
}

func runtimeCleanupRequestedError() error {
	return fmt.Errorf("runtime cleanup requested: %w", control.ErrUnauthenticated)
}

func handoffIdentity(handoff schedulerHandoff) control.Identity {
	return control.Identity{
		WorkerSessionID:   handoff.workerSessionID,
		WorkerRunID:       handoff.workerRunID,
		RuntimeInstanceID: handoff.runtimeInstanceID,
		RuntimeGeneration: handoff.runtimeGeneration,
	}
}

func turnPrompt(handoff schedulerHandoff) string {
	if handoff.message != "" {
		return handoff.message
	}
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Goal: %s\nAcceptance criteria:\n", handoff.goal)
	for _, criterion := range handoff.acceptanceCriteria {
		fmt.Fprintf(&prompt, "- %s\n", criterion)
	}
	if len(handoff.constraints) > 0 {
		prompt.WriteString("Constraints:\n")
		for _, constraint := range handoff.constraints {
			fmt.Fprintf(&prompt, "- %s\n", constraint)
		}
	}
	return strings.TrimSpace(prompt.String())
}

// UnavailableScheduler is used by main until a real runtime provisioner exists.
type UnavailableScheduler struct{}

func (UnavailableScheduler) Initial(context.Context, schedulerHandoff) error { return ErrNotConfigured }
func (UnavailableScheduler) Turn(context.Context, schedulerHandoff) error    { return ErrNotConfigured }
func (UnavailableScheduler) Cleanup(context.Context, schedulerHandoff) error { return ErrNotConfigured }

// Options keeps construction explicit and provides deterministic seams for tests.
type Options struct {
	RootContext       context.Context
	Materializer      Materializer
	ObjectStore       EvidenceStore
	Scheduler         Scheduler
	NewID             func() string
	NewToken          func() (string, error)
	LangfuseProjector LangfuseProjector
}

// Coordinator owns in-memory accepted runs. It never exposes internal pointers.
type Coordinator struct {
	root         context.Context
	materializer Materializer
	objectStore  EvidenceStore
	scheduler    Scheduler
	newID        func() string
	newToken     func() (string, error)
	projector    LangfuseProjector

	mu        sync.RWMutex
	byRun     map[string]*managedRun
	bySession map[string]*managedRun
}

type managedRun struct {
	store              *runstate.Store
	goal               string
	acceptanceCriteria []string
	constraints        []string
	pendingTurns       map[string]struct{}
	token              workerToken
	materialized       MaterializedSession
	evidence           map[evidenceSlotKey]*evidenceSlot
	diagnostics        []RuntimeDiagnosticSnapshot
	projection         projectionState
	cleanupSent        bool
}

type storedEvidence struct {
	turnID string
	kind   objectstore.EvidenceKind
	object objectstore.StoredObject
	trees  sharedevidence.TreePair
}

type evidenceSlotKey struct {
	turnID string
	kind   objectstore.EvidenceKind
}

type evidenceFingerprint struct {
	digest [sha256.Size]byte
	bytes  int64
	trees  sharedevidence.TreePair
}

type evidenceSlot struct {
	fingerprint evidenceFingerprint
	ready       chan struct{}
	complete    bool
	evidence    storedEvidence
}

type workerToken struct {
	value             string
	workerSessionID   string
	workerRunID       string
	runtimeInstanceID string
	runtimeGeneration uint32
}

func (t workerToken) String() string {
	return fmt.Sprintf(
		"workerToken{WorkerSessionID:%q WorkerRunID:%q RuntimeInstanceID:%q RuntimeGeneration:%d Value:[REDACTED]}",
		t.workerSessionID,
		t.workerRunID,
		t.runtimeInstanceID,
		t.runtimeGeneration,
	)
}

func (t workerToken) GoString() string {
	return t.String()
}

// CreateRunInput is the validated public contract accepted by the coordinator.
type CreateRunInput struct {
	Goal               string
	AcceptanceCriteria []string
	Constraints        []string
}

// CreatedRun identifies an accepted run without exposing its worker credential.
type CreatedRun struct {
	WorkerSessionID string
	WorkerRunID     string
	TurnID          string
}

// CreatedTurn identifies a later accepted turn.
type CreatedTurn struct {
	WorkerRunID string
	TurnID      string
}

// TurnSnapshot is the public, immutable turn representation.
type TurnSnapshot struct {
	ID string `json:"id"`
}

// RunSnapshot is deliberately free of object paths, credentials and internal pointers.
type RunSnapshot struct {
	WorkerSessionID       string                      `json:"worker_session_id"`
	WorkerRunID           string                      `json:"worker_run_id"`
	RuntimeInstanceID     string                      `json:"runtime_instance_id"`
	RuntimeGeneration     uint32                      `json:"runtime_generation"`
	AgentSessionID        string                      `json:"agent_session_id,omitempty"`
	TraceID               string                      `json:"trace_id"`
	Goal                  string                      `json:"goal"`
	AcceptanceCriteria    []string                    `json:"acceptance_criteria"`
	Constraints           []string                    `json:"constraints,omitempty"`
	State                 string                      `json:"state"`
	ActiveTurnID          string                      `json:"active_turn_id,omitempty"`
	Turns                 []TurnSnapshot              `json:"turns"`
	ExecutionOutcome      string                      `json:"execution_outcome"`
	SpikeAcceptance       string                      `json:"spike_acceptance"`
	Revision              uint64                      `json:"revision"`
	ModelObservationCount int                         `json:"model_observation_count"`
	Diagnostics           []RuntimeDiagnosticSnapshot `json:"diagnostics,omitempty"`
}

// RuntimeDiagnosticSnapshot is bounded, derived control-plane context. Full
// authoritative content remains in uploaded JSONL.
type RuntimeDiagnosticSnapshot struct {
	Kind   string `json:"kind"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// EvidenceAuthorization is an opaque, short-lived capability returned only
// after worker authentication and exact path authorization.
type EvidenceAuthorization struct {
	coordinator       *Coordinator
	managed           *managedRun
	workerSessionID   string
	workerRunID       string
	runtimeInstanceID string
	runtimeGeneration uint32
	turnID            string
	workerToken       string
}

func (a EvidenceAuthorization) String() string {
	return fmt.Sprintf(
		"EvidenceAuthorization{WorkerSessionID:%q WorkerRunID:%q RuntimeInstanceID:%q RuntimeGeneration:%d TurnID:%q Private:[REDACTED]}",
		a.workerSessionID,
		a.workerRunID,
		a.runtimeInstanceID,
		a.runtimeGeneration,
		a.turnID,
	)
}

func (a EvidenceAuthorization) GoString() string {
	return a.String()
}

// NewCoordinator constructs the in-memory shell with safe defaults for optional seams.
func NewCoordinator(options Options) *Coordinator {
	root := options.RootContext
	if root == nil {
		root = context.Background()
	}
	scheduler := options.Scheduler
	if scheduler == nil {
		scheduler = UnavailableScheduler{}
	}
	newID := options.NewID
	if newID == nil {
		newID = ids.New
	}
	newToken := options.NewToken
	if newToken == nil {
		newToken = randomToken
	}
	return &Coordinator{
		root:         root,
		materializer: options.Materializer,
		objectStore:  options.ObjectStore,
		scheduler:    scheduler,
		newID:        newID,
		newToken:     newToken,
		projector:    options.LangfuseProjector,
		byRun:        make(map[string]*managedRun),
		bySession:    make(map[string]*managedRun),
	}
}

// CreateRun synchronously allocates identities and storage, then detaches all
// background command ownership from the HTTP request context.
func (c *Coordinator) CreateRun(_ context.Context, input CreateRunInput) (CreatedRun, error) {
	if err := validateCreateRun(input); err != nil {
		return CreatedRun{}, err
	}
	if c.materializer == nil {
		return CreatedRun{}, fmt.Errorf("%w: materializer", ErrNotConfigured)
	}

	created := CreatedRun{WorkerSessionID: c.newID(), WorkerRunID: c.newID(), TurnID: c.newID()}
	runtimeID := c.newID()
	traceID := c.newID()
	if err := validateGeneratedIDs(created, runtimeID, traceID); err != nil {
		return CreatedRun{}, err
	}
	token, err := c.newToken()
	if err != nil {
		return CreatedRun{}, fmt.Errorf("create worker token: %w", err)
	}
	if token == "" {
		return CreatedRun{}, fmt.Errorf("%w: empty worker token", ErrInvalid)
	}
	materialized, err := c.materializer.Materialize(c.root, created.WorkerSessionID)
	if err != nil {
		return CreatedRun{}, fmt.Errorf("materialize session: %w", err)
	}
	store, err := runstate.NewStore(runstate.Run{
		WorkerSessionID:   created.WorkerSessionID,
		WorkerRunID:       created.WorkerRunID,
		RuntimeInstanceID: runtimeID,
		RuntimeGeneration: 1,
		TraceID:           traceID,
		State:             runstate.StateProvisioning,
		ExecutionOutcome:  runstate.ExecutionPending,
		SpikeAcceptance:   runstate.AcceptancePending,
	})
	if err != nil {
		return CreatedRun{}, c.rollbackMaterialization(created.WorkerSessionID, materialized, fmt.Errorf("create run state: %w", err))
	}
	managed := &managedRun{
		store:              store,
		goal:               strings.TrimSpace(input.Goal),
		acceptanceCriteria: append([]string(nil), input.AcceptanceCriteria...),
		constraints:        append([]string(nil), input.Constraints...),
		pendingTurns:       map[string]struct{}{created.TurnID: {}},
		evidence:           make(map[evidenceSlotKey]*evidenceSlot),
		projection:         projectionState{turns: make(map[string]*projectionTurn)},
		token: workerToken{
			value:             token,
			workerSessionID:   created.WorkerSessionID,
			workerRunID:       created.WorkerRunID,
			runtimeInstanceID: runtimeID,
			runtimeGeneration: 1,
		},
		materialized: materialized,
	}
	c.mu.Lock()
	if _, exists := c.byRun[created.WorkerRunID]; exists {
		c.mu.Unlock()
		return CreatedRun{}, c.rollbackMaterialization(created.WorkerSessionID, materialized, fmt.Errorf("%w: duplicate generated run ID", ErrConflict))
	}
	if _, exists := c.bySession[created.WorkerSessionID]; exists {
		c.mu.Unlock()
		return CreatedRun{}, c.rollbackMaterialization(created.WorkerSessionID, materialized, fmt.Errorf("%w: duplicate generated session ID", ErrConflict))
	}
	c.byRun[created.WorkerRunID] = managed
	c.bySession[created.WorkerSessionID] = managed
	c.mu.Unlock()

	work := c.scheduledWork(managed, created.TurnID, "")
	go func() { _ = c.scheduler.Initial(c.root, work) }()
	return created, nil
}

// CreateTurn accepts one later turn only after the exact session is idle and
// has no active or already-accepted pending turn.
func (c *Coordinator) CreateTurn(_ context.Context, sessionID, message string) (CreatedTurn, error) {
	if strings.TrimSpace(message) == "" {
		return CreatedTurn{}, fmt.Errorf("%w: message is required", ErrInvalid)
	}
	c.mu.Lock()
	managed := c.bySession[sessionID]
	if managed == nil {
		c.mu.Unlock()
		return CreatedTurn{}, ErrNotFound
	}
	run := managed.store.Snapshot()
	if run.State != runstate.StateIdle || run.ActiveTurnID != "" || len(managed.pendingTurns) != 0 {
		c.mu.Unlock()
		return CreatedTurn{}, ErrConflict
	}
	turnID := c.newID()
	if turnID == "" {
		c.mu.Unlock()
		return CreatedTurn{}, fmt.Errorf("%w: empty generated turn ID", ErrInvalid)
	}
	managed.pendingTurns[turnID] = struct{}{}
	work := c.scheduledWork(managed, turnID, message)
	c.mu.Unlock()

	go func() { _ = c.scheduler.Turn(c.root, work) }()
	return CreatedTurn{WorkerRunID: run.WorkerRunID, TurnID: turnID}, nil
}

// Snapshot returns a detached public representation of one run.
func (c *Coordinator) Snapshot(runID string) (RunSnapshot, error) {
	c.mu.RLock()
	managed := c.byRun[runID]
	if managed == nil {
		c.mu.RUnlock()
		return RunSnapshot{}, ErrNotFound
	}
	run := managed.store.Snapshot()
	goal := managed.goal
	acceptanceCriteria := append([]string(nil), managed.acceptanceCriteria...)
	constraints := append([]string(nil), managed.constraints...)
	modelObservationCount := 0
	for _, turn := range managed.projection.turns {
		modelObservationCount += len(turn.observations)
	}
	diagnostics := append([]RuntimeDiagnosticSnapshot(nil), managed.diagnostics...)
	c.mu.RUnlock()

	turns := make([]TurnSnapshot, 0, len(run.Turns))
	for _, turn := range run.Turns {
		turns = append(turns, TurnSnapshot{ID: turn.ID})
	}
	return RunSnapshot{
		WorkerSessionID:       run.WorkerSessionID,
		WorkerRunID:           run.WorkerRunID,
		RuntimeInstanceID:     run.RuntimeInstanceID,
		RuntimeGeneration:     run.RuntimeGeneration,
		AgentSessionID:        run.AgentSessionID,
		TraceID:               run.TraceID,
		Goal:                  goal,
		AcceptanceCriteria:    acceptanceCriteria,
		Constraints:           constraints,
		State:                 string(run.State),
		ActiveTurnID:          run.ActiveTurnID,
		Turns:                 turns,
		ExecutionOutcome:      string(run.ExecutionOutcome),
		SpikeAcceptance:       string(run.SpikeAcceptance),
		Revision:              run.Revision,
		ModelObservationCount: modelObservationCount,
		Diagnostics:           diagnostics,
	}, nil
}

// Cleanup schedules exactly one root-owned cleanup command for a session.
func (c *Coordinator) Cleanup(_ context.Context, sessionID string) error {
	c.mu.Lock()
	managed := c.bySession[sessionID]
	if managed == nil {
		c.mu.Unlock()
		return ErrNotFound
	}
	if managed.cleanupSent {
		c.mu.Unlock()
		return nil
	}
	managed.cleanupSent = true
	work := c.scheduledWork(managed, "", "")
	c.mu.Unlock()

	go func() { _ = c.scheduler.Cleanup(c.root, work) }()
	return nil
}

// AuthorizeEvidenceUpload authenticates before the HTTP boundary reads a body.
// Unknown sessions and invalid credentials intentionally share one error.
func (c *Coordinator) AuthorizeEvidenceUpload(sessionID, runID, turnID, presentedToken string) (EvidenceAuthorization, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	managed := c.bySession[sessionID]
	if managed == nil {
		return EvidenceAuthorization{}, ErrUnauthorized
	}
	run := managed.store.Snapshot()
	if !tokenMatches(managed.token, run, presentedToken) {
		return EvidenceAuthorization{}, ErrUnauthorized
	}
	if runID != run.WorkerRunID || !isKnownTurn(managed, run, turnID) || managed.cleanupSent {
		return EvidenceAuthorization{}, ErrForbidden
	}
	return EvidenceAuthorization{
		coordinator:       c,
		managed:           managed,
		workerSessionID:   run.WorkerSessionID,
		workerRunID:       run.WorkerRunID,
		runtimeInstanceID: run.RuntimeInstanceID,
		runtimeGeneration: run.RuntimeGeneration,
		turnID:            turnID,
		workerToken:       managed.token.value,
	}, nil
}

// StoreEvidence redacts the authorized dynamic token and performs persistence
// without the Coordinator lock, then revalidates before retaining the ref.
func (c *Coordinator) StoreEvidence(ctx context.Context, authorization EvidenceAuthorization, kind objectstore.EvidenceKind, payload []byte) (objectstore.StoredObject, error) {
	return c.StoreEvidenceWithTrees(
		ctx,
		authorization,
		kind,
		payload,
		sharedevidence.TreePair{},
	)
}

// StoreEvidenceWithTrees retains the exact Worker snapshots alongside the
// Backend-returned StoredObject ref. The HTTP upload path always uses this form.
func (c *Coordinator) StoreEvidenceWithTrees(
	ctx context.Context,
	authorization EvidenceAuthorization,
	kind objectstore.EvidenceKind,
	payload []byte,
	trees sharedevidence.TreePair,
) (objectstore.StoredObject, error) {
	if authorization.coordinator != c || authorization.managed == nil {
		return objectstore.StoredObject{}, ErrUnauthorized
	}
	if !validTreePair(trees) {
		return objectstore.StoredObject{}, fmt.Errorf("%w: invalid Git tree pair", ErrInvalid)
	}
	if c.objectStore == nil {
		return objectstore.StoredObject{}, fmt.Errorf("%w: evidence store", ErrNotConfigured)
	}
	dynamicRedactor := redact.New(redact.Secrets{WorkerToken: authorization.workerToken})
	redactedPayload := []byte(dynamicRedactor.Redact(string(payload)))
	fingerprint := evidenceFingerprint{
		digest: sha256.Sum256(redactedPayload),
		bytes:  int64(len(redactedPayload)),
		trees:  trees,
	}
	key := evidenceSlotKey{turnID: authorization.turnID, kind: kind}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		managed, err := c.revalidateEvidenceAuthorizationLocked(authorization)
		if err != nil {
			c.mu.Unlock()
			return objectstore.StoredObject{}, err
		}
		if existing := managed.evidence[key]; existing != nil {
			if existing.fingerprint != fingerprint {
				c.mu.Unlock()
				return objectstore.StoredObject{}, fmt.Errorf(
					"%w: evidence slot %q for turn %q already has different payload or trees",
					ErrConflict,
					kind,
					authorization.turnID,
				)
			}
			if existing.complete {
				stored := existing.evidence.object
				c.mu.Unlock()
				return stored, nil
			}
			ready := existing.ready
			c.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return objectstore.StoredObject{}, fmt.Errorf("wait for evidence slot: %w", ctx.Err())
			}
		}
		slot := &evidenceSlot{
			fingerprint: fingerprint,
			ready:       make(chan struct{}),
		}
		managed.evidence[key] = slot
		c.mu.Unlock()

		stored, putErr := c.objectStore.Put(objectstore.ObjectIdentity{
			WorkerSessionID: authorization.workerSessionID,
			WorkerRunID:     authorization.workerRunID,
			AgentTurnID:     authorization.turnID,
			Kind:            kind,
		}, redactedPayload)

		c.mu.Lock()
		managed, revalidateErr := c.revalidateEvidenceAuthorizationLocked(authorization)
		if putErr != nil || revalidateErr != nil {
			c.releaseEvidenceSlotLocked(authorization.managed, key, slot)
			c.mu.Unlock()
			if putErr != nil {
				return objectstore.StoredObject{}, fmt.Errorf("store evidence: %w", putErr)
			}
			return objectstore.StoredObject{}, revalidateErr
		}
		if managed.evidence[key] != slot {
			select {
			case <-slot.ready:
			default:
				close(slot.ready)
			}
			c.mu.Unlock()
			return objectstore.StoredObject{}, fmt.Errorf("%w: evidence reservation was replaced", ErrConflict)
		}
		slot.evidence = storedEvidence{
			turnID: authorization.turnID,
			kind:   kind,
			object: stored,
			trees:  trees,
		}
		slot.complete = true
		close(slot.ready)
		c.mu.Unlock()
		return stored, nil
	}
}

func (c *Coordinator) revalidateEvidenceAuthorizationLocked(
	authorization EvidenceAuthorization,
) (*managedRun, error) {
	managed := c.bySession[authorization.workerSessionID]
	if managed == nil || managed != authorization.managed {
		return nil, ErrUnauthorized
	}
	run := managed.store.Snapshot()
	if !tokenMatches(managed.token, run, authorization.workerToken) {
		return nil, ErrUnauthorized
	}
	if run.WorkerRunID != authorization.workerRunID ||
		run.RuntimeInstanceID != authorization.runtimeInstanceID ||
		run.RuntimeGeneration != authorization.runtimeGeneration ||
		!isKnownTurn(managed, run, authorization.turnID) ||
		managed.cleanupSent {
		return nil, ErrForbidden
	}
	return managed, nil
}

func (c *Coordinator) releaseEvidenceSlotLocked(
	managed *managedRun,
	key evidenceSlotKey,
	slot *evidenceSlot,
) {
	if managed != nil && managed.evidence[key] == slot {
		delete(managed.evidence, key)
		close(slot.ready)
	}
}

// HandleWorkerEvent is the persistence seam used by WorkerControl. The caller
// advances its receive cursor only after this method succeeds.
func (c *Coordinator) HandleWorkerEvent(
	ctx context.Context,
	identity control.Identity,
	event *workercontrolv1.WorkerEnvelope,
) (control.EventDisposition, error) {
	if event == nil || event.GetMeta() == nil {
		return control.EventDispositionInvalid, fmt.Errorf("%w: worker event metadata is required", ErrInvalid)
	}

	var scheduled *schedulerHandoff
	var projectionWork *langfuseProjectionWork
	var projectionErr error
	c.mu.Lock()
	managed := c.byRun[identity.WorkerRunID]
	if managed == nil {
		c.mu.Unlock()
		return control.EventDispositionInvalid, ErrNotFound
	}
	run := managed.store.Snapshot()
	if run.WorkerSessionID != identity.WorkerSessionID ||
		run.RuntimeInstanceID != identity.RuntimeInstanceID ||
		run.RuntimeGeneration != identity.RuntimeGeneration {
		c.mu.Unlock()
		return control.EventDispositionInvalid, ErrForbidden
	}
	payloadKind := workerEventKind(event)
	if managed.cleanupSent || isTerminalRun(run.State) {
		appendDiagnostic(managed, RuntimeDiagnosticSnapshot{
			Kind:   payloadKind,
			Code:   "late_worker_event",
			Detail: workerEventDetail(event),
		})
		c.mu.Unlock()
		return control.EventDispositionDiagnosticOnly, nil
	}

	var err error
	switch payload := event.GetPayload().(type) {
	case *workercontrolv1.WorkerEnvelope_RuntimeReady:
		_, err = managed.store.Advance(run.Revision, runstate.StateConnecting)
	case *workercontrolv1.WorkerEnvelope_AgentSessionReady:
		run, err = managed.store.BindAgentSession(run.Revision, payload.AgentSessionReady.GetAgentSessionId())
		if err == nil {
			run, err = managed.store.Advance(run.Revision, runstate.StateReady)
		}
		if err == nil {
			if turnID := onlyPendingTurn(managed); turnID != "" {
				work := c.scheduledWork(managed, turnID, "")
				scheduled = &work
			}
		}
	case *workercontrolv1.WorkerEnvelope_TurnStarted:
		turnID := event.GetMeta().GetIdentity().GetAgentTurnId()
		if _, pending := managed.pendingTurns[turnID]; !pending {
			err = fmt.Errorf("%w: turn %q is not pending", ErrConflict, turnID)
			break
		}
		var startedAt time.Time
		startedAt, err = validTimestamp(payload.TurnStarted.GetStartedAt())
		if err != nil {
			err = fmt.Errorf("%w: invalid TurnStarted timestamp", ErrInvalid)
			break
		}
		_, err = managed.store.StartTurn(run.Revision, turnID)
		if err == nil {
			delete(managed.pendingTurns, turnID)
			recordProjectionTurnStarted(managed, turnID, startedAt)
		}
	case *workercontrolv1.WorkerEnvelope_AcpUpdate:
		appendDiagnostic(managed, RuntimeDiagnosticSnapshot{
			Kind:   payloadKind,
			Code:   payload.AcpUpdate.GetKind(),
			Detail: "derived ACP update",
		})
	case *workercontrolv1.WorkerEnvelope_ModelObservation:
		turnID := event.GetMeta().GetIdentity().GetAgentTurnId()
		if run.State != runstate.StateTurnRunning || run.ActiveTurnID != turnID {
			err = fmt.Errorf("%w: turn %q is not active", ErrConflict, turnID)
			break
		}
		var fact langfuse.ModelObservationFact
		fact, err = modelObservationFact(payload.ModelObservation)
		if err != nil {
			break
		}
		if err = recordModelObservation(managed, turnID, fact); err != nil {
			break
		}
		appendDiagnostic(managed, RuntimeDiagnosticSnapshot{
			Kind:   payloadKind,
			Code:   payload.ModelObservation.GetStatus(),
			Detail: payload.ModelObservation.GetModel(),
		})
	case *workercontrolv1.WorkerEnvelope_RuntimeDiagnostic:
		appendDiagnostic(managed, RuntimeDiagnosticSnapshot{
			Kind:   payloadKind,
			Code:   payload.RuntimeDiagnostic.GetCode(),
			Detail: payload.RuntimeDiagnostic.GetDetail(),
		})
	case *workercontrolv1.WorkerEnvelope_TurnCompleted:
		turnID := event.GetMeta().GetIdentity().GetAgentTurnId()
		if run.State != runstate.StateTurnRunning || run.ActiveTurnID != turnID {
			err = fmt.Errorf("%w: turn %q is not active", ErrConflict, turnID)
			break
		}
		if payload.TurnCompleted.GetAgentSessionId() != run.AgentSessionID {
			err = fmt.Errorf("%w: agent session does not match active run", ErrForbidden)
			break
		}
		var endedAt time.Time
		endedAt, err = validTimestamp(event.GetMeta().GetSentAt())
		if err != nil {
			err = fmt.Errorf("%w: invalid TurnCompleted timestamp", ErrInvalid)
			break
		}
		if err = validateEvidencePointers(managed, turnID, payload.TurnCompleted.GetEvidence(), true); err != nil {
			break
		}
		if err = c.writeTurnManifest(managed, run, turnID, payload.TurnCompleted.GetEvidence()); err != nil {
			break
		}
		run, err = managed.store.CompleteTurn(run.Revision, turnID)
		if err == nil {
			markProjectionTurnCompleted(managed, turnID, endedAt)
		}
		if err == nil && run.State == runstate.StateCompleted {
			run, err = managed.store.SetExecutionOutcome(run.Revision, runstate.ExecutionCompleted)
		}
		if err == nil && run.State == runstate.StateCompleted && c.projector != nil {
			projectionWork, projectionErr = prepareLangfuseProjection(managed, run, c.projector)
		}
	case *workercontrolv1.WorkerEnvelope_TurnFailed:
		turnID := event.GetMeta().GetIdentity().GetAgentTurnId()
		if run.State != runstate.StateTurnRunning || run.ActiveTurnID != turnID {
			err = fmt.Errorf("%w: turn %q is not active", ErrConflict, turnID)
			break
		}
		if err = validateEvidencePointers(managed, turnID, payload.TurnFailed.GetPartialEvidence(), false); err != nil {
			break
		}
		run, err = managed.store.SetExecutionOutcome(run.Revision, runstate.ExecutionFailed)
		if err == nil {
			_, err = managed.store.Advance(run.Revision, runstate.StateFailed)
		}
		if err == nil {
			appendDiagnostic(managed, RuntimeDiagnosticSnapshot{
				Kind:   payloadKind,
				Code:   payload.TurnFailed.GetCode(),
				Detail: payload.TurnFailed.GetDetail(),
			})
		}
	default:
		err = fmt.Errorf("%w: unsupported worker event", ErrInvalid)
	}
	c.mu.Unlock()
	if err != nil {
		return control.EventDispositionInvalid, err
	}
	if scheduled != nil {
		go func(work schedulerHandoff) { _ = c.scheduler.Turn(c.root, work) }(*scheduled)
	}
	if projectionWork != nil {
		go c.runLangfuseProjection(projectionWork, projectionErr)
	}
	return control.EventDispositionAuthoritative, nil
}

// Diagnostics returns detached bounded derived diagnostics for review/testing.
func (c *Coordinator) Diagnostics(runID string) ([]RuntimeDiagnosticSnapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	managed := c.byRun[runID]
	if managed == nil {
		return nil, ErrNotFound
	}
	return append([]RuntimeDiagnosticSnapshot(nil), managed.diagnostics...), nil
}

func (c *Coordinator) scheduledWork(managed *managedRun, turnID, message string) schedulerHandoff {
	run := managed.store.Snapshot()
	return schedulerHandoff{
		workerSessionID:    run.WorkerSessionID,
		workerRunID:        run.WorkerRunID,
		runtimeInstanceID:  run.RuntimeInstanceID,
		runtimeGeneration:  run.RuntimeGeneration,
		turnID:             turnID,
		traceID:            run.TraceID,
		goal:               managed.goal,
		acceptanceCriteria: append([]string(nil), managed.acceptanceCriteria...),
		constraints:        append([]string(nil), managed.constraints...),
		message:            strings.TrimSpace(message),
		workerToken:        managed.token.value,
		materialized:       managed.materialized,
	}
}

func (c *Coordinator) rollbackMaterialization(sessionID string, materialized MaterializedSession, original error) error {
	if err := c.materializer.Rollback(c.root, sessionID, materialized); err != nil {
		return errors.Join(original, fmt.Errorf("rollback materialized session: %w", err))
	}
	return original
}

func validateCreateRun(input CreateRunInput) error {
	if strings.TrimSpace(input.Goal) == "" {
		return fmt.Errorf("%w: goal is required", ErrInvalid)
	}
	for _, criterion := range input.AcceptanceCriteria {
		if strings.TrimSpace(criterion) != "" {
			return nil
		}
	}
	return fmt.Errorf("%w: at least one acceptance criterion is required", ErrInvalid)
}

func validateGeneratedIDs(created CreatedRun, runtimeID, traceID string) error {
	for _, value := range []string{created.WorkerSessionID, created.WorkerRunID, created.TurnID, runtimeID, traceID} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: generated ID is empty", ErrInvalid)
		}
	}
	return nil
}

func isKnownTurn(managed *managedRun, run runstate.Run, turnID string) bool {
	if turnID == "" {
		return false
	}
	if run.ActiveTurnID == turnID {
		return true
	}
	if _, ok := managed.pendingTurns[turnID]; ok {
		return true
	}
	for _, turn := range run.Turns {
		if turn.ID == turnID {
			return true
		}
	}
	return false
}

func onlyPendingTurn(managed *managedRun) string {
	if len(managed.pendingTurns) != 1 {
		return ""
	}
	for turnID := range managed.pendingTurns {
		return turnID
	}
	return ""
}

func validateEvidencePointers(
	managed *managedRun,
	turnID string,
	pointers []*workercontrolv1.EvidencePointer,
	requireComplete bool,
) error {
	required := map[objectstore.EvidenceKind]bool{
		objectstore.EvidenceRawACP: false,
		objectstore.EvidenceTrace:  false,
		objectstore.EvidenceReport: false,
		objectstore.EvidencePatch:  false,
	}
	if requireComplete && len(pointers) != len(required) {
		return fmt.Errorf("%w: turn completion requires exactly four evidence refs", ErrInvalid)
	}
	for _, pointer := range pointers {
		if pointer == nil {
			return fmt.Errorf("%w: nil evidence ref", ErrInvalid)
		}
		kind, err := objectstore.ParseUploadEvidenceKind(pointer.GetKind())
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if required[kind] {
			return fmt.Errorf("%w: duplicate evidence kind %q", ErrInvalid, kind)
		}
		slot := managed.evidence[evidenceSlotKey{turnID: turnID, kind: kind}]
		if slot == nil ||
			!slot.complete ||
			slot.evidence.object.Path != pointer.GetObjectPath() ||
			slot.evidence.object.SHA256 != pointer.GetSha256() ||
			slot.evidence.object.Bytes != pointer.GetSizeBytes() {
			return fmt.Errorf("%w: evidence ref %q does not match an uploaded object", ErrInvalid, kind)
		}
		required[kind] = true
	}
	if requireComplete {
		for kind, present := range required {
			if !present {
				return fmt.Errorf("%w: missing evidence kind %q", ErrInvalid, kind)
			}
		}
	}
	return nil
}

func (c *Coordinator) writeTurnManifest(
	managed *managedRun,
	run runstate.Run,
	turnID string,
	pointers []*workercontrolv1.EvidencePointer,
) error {
	if c.objectStore == nil {
		return fmt.Errorf("%w: evidence store", ErrNotConfigured)
	}
	objects, trees, err := authoritativeObjects(managed, turnID, pointers)
	if err != nil {
		return err
	}
	manifest := sharedevidence.TurnManifest{
		SchemaVersion:     sharedevidence.TurnManifestSchemaVersion,
		WorkerSessionID:   run.WorkerSessionID,
		WorkerRunID:       run.WorkerRunID,
		RuntimeInstanceID: run.RuntimeInstanceID,
		RuntimeGeneration: run.RuntimeGeneration,
		AgentSessionID:    run.AgentSessionID,
		AgentTurnID:       turnID,
		TraceID:           run.TraceID,
		BeforeTree:        trees.BeforeTree,
		AfterTree:         trees.AfterTree,
		Objects:           objects,
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode turn manifest: %w", err)
	}
	if _, err := c.objectStore.Put(objectstore.ObjectIdentity{
		WorkerSessionID: run.WorkerSessionID,
		WorkerRunID:     run.WorkerRunID,
		AgentTurnID:     "",
		Kind:            objectstore.EvidenceManifest,
	}, payload); err != nil {
		return fmt.Errorf("store turn manifest: %w", err)
	}
	return nil
}

func authoritativeObjects(
	managed *managedRun,
	turnID string,
	pointers []*workercontrolv1.EvidencePointer,
) ([]sharedevidence.ObjectRef, sharedevidence.TreePair, error) {
	byKind := make(map[objectstore.EvidenceKind]storedEvidence, len(pointers))
	for _, pointer := range pointers {
		kind, err := objectstore.ParseUploadEvidenceKind(pointer.GetKind())
		if err != nil {
			return nil, sharedevidence.TreePair{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		slot := managed.evidence[evidenceSlotKey{turnID: turnID, kind: kind}]
		if slot != nil &&
			slot.complete &&
			slot.evidence.object.Path == pointer.GetObjectPath() &&
			slot.evidence.object.SHA256 == pointer.GetSha256() &&
			slot.evidence.object.Bytes == pointer.GetSizeBytes() {
			byKind[kind] = slot.evidence
		}
	}
	order := []objectstore.EvidenceKind{
		objectstore.EvidenceReport,
		objectstore.EvidenceRawACP,
		objectstore.EvidenceTrace,
		objectstore.EvidencePatch,
	}
	objects := make([]sharedevidence.ObjectRef, 0, len(order))
	var trees sharedevidence.TreePair
	for index, kind := range order {
		uploaded, ok := byKind[kind]
		if !ok {
			return nil, sharedevidence.TreePair{}, fmt.Errorf("%w: missing verified evidence kind %q", ErrInvalid, kind)
		}
		if index == 0 {
			trees = uploaded.trees
		} else if uploaded.trees != trees {
			return nil, sharedevidence.TreePair{}, fmt.Errorf("%w: evidence tree pairs do not match", ErrInvalid)
		}
		objects = append(objects, sharedevidence.ObjectRef{
			Kind:   string(kind),
			Path:   uploaded.object.Path,
			SHA256: uploaded.object.SHA256,
			Bytes:  uploaded.object.Bytes,
		})
	}
	return objects, trees, nil
}

func validTreePair(trees sharedevidence.TreePair) bool {
	if trees.BeforeTree == "" && trees.AfterTree == "" {
		return true
	}
	return validGitObjectID(trees.BeforeTree) && validGitObjectID(trees.AfterTree)
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func appendDiagnostic(managed *managedRun, diagnostic RuntimeDiagnosticSnapshot) {
	diagnostic.Kind = truncateRunes(diagnostic.Kind, maxDiagnosticKindRunes)
	diagnostic.Code = truncateRunes(diagnostic.Code, maxDiagnosticCodeRunes)
	diagnostic.Detail = truncateRunes(diagnostic.Detail, maxDiagnosticTextRunes)
	if len(managed.diagnostics) == MaxRuntimeDiagnostics {
		copy(managed.diagnostics, managed.diagnostics[1:])
		managed.diagnostics[len(managed.diagnostics)-1] = diagnostic
		return
	}
	managed.diagnostics = append(managed.diagnostics, diagnostic)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func workerEventKind(event *workercontrolv1.WorkerEnvelope) string {
	switch event.GetPayload().(type) {
	case *workercontrolv1.WorkerEnvelope_RuntimeReady:
		return string(control.PayloadKindRuntimeReady)
	case *workercontrolv1.WorkerEnvelope_AgentSessionReady:
		return string(control.PayloadKindAgentSessionReady)
	case *workercontrolv1.WorkerEnvelope_TurnStarted:
		return string(control.PayloadKindTurnStarted)
	case *workercontrolv1.WorkerEnvelope_AcpUpdate:
		return string(control.PayloadKindACPUpdate)
	case *workercontrolv1.WorkerEnvelope_ModelObservation:
		return string(control.PayloadKindModelObservation)
	case *workercontrolv1.WorkerEnvelope_TurnCompleted:
		return string(control.PayloadKindTurnCompleted)
	case *workercontrolv1.WorkerEnvelope_TurnFailed:
		return string(control.PayloadKindTurnFailed)
	case *workercontrolv1.WorkerEnvelope_RuntimeDiagnostic:
		return string(control.PayloadKindRuntimeDiagnostic)
	default:
		return "unknown"
	}
}

func workerEventDetail(event *workercontrolv1.WorkerEnvelope) string {
	switch payload := event.GetPayload().(type) {
	case *workercontrolv1.WorkerEnvelope_RuntimeDiagnostic:
		return payload.RuntimeDiagnostic.GetCode()
	case *workercontrolv1.WorkerEnvelope_AcpUpdate:
		return payload.AcpUpdate.GetKind()
	case *workercontrolv1.WorkerEnvelope_ModelObservation:
		return payload.ModelObservation.GetModelObservationId()
	case *workercontrolv1.WorkerEnvelope_TurnCompleted:
		return "turn_completed"
	case *workercontrolv1.WorkerEnvelope_TurnFailed:
		return payload.TurnFailed.GetCode()
	default:
		return workerEventKind(event)
	}
}

func isTerminalRun(state runstate.State) bool {
	switch state {
	case runstate.StateCompleted, runstate.StateFailed, runstate.StateDiagnosticHold, runstate.StateShuttingDown:
		return true
	default:
		return false
	}
}

func tokenMatches(record workerToken, run runstate.Run, presented string) bool {
	if presented == "" || record.value == "" {
		return false
	}
	if record.workerSessionID != run.WorkerSessionID ||
		record.workerRunID != run.WorkerRunID ||
		record.runtimeInstanceID != run.RuntimeInstanceID ||
		record.runtimeGeneration != run.RuntimeGeneration {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(record.value), []byte(presented)) == 1
}

// IsConflict and IsNotFound keep HTTP error mapping independent of messages.
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
