package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

var (
	ErrRuntimeControlIdentityConflict      = errors.New("Runtime control identity conflict")
	ErrRuntimeControlCredentialConflict    = errors.New("Runtime control credential conflict")
	ErrRuntimeControlStaleOwner            = errors.New("Runtime control owner fence is stale")
	ErrRuntimeControlStreamAttemptStale    = errors.New("Runtime control stream attempt is stale")
	ErrRuntimeControlLeaseHeld             = errors.New("Runtime control stream lease is held")
	ErrRuntimeControlSequenceGap           = errors.New("Runtime control sequence gap")
	ErrRuntimeControlMessageConflict       = errors.New("Runtime control message conflict")
	ErrRuntimeControlCursorConflict        = errors.New("Runtime control cursor conflict")
	ErrRuntimeControlResumeSnapshotChanged = errors.New(
		"Runtime control resume replay changed after Claim",
	)
	ErrRuntimeControlPendingTurnSettlement = errors.New(
		"Runtime control admission is blocked by a pending Turn settlement",
	)
)

type RuntimeControlDirection string

const (
	RuntimeControlDirectionCommand RuntimeControlDirection = "command"
	RuntimeControlDirectionEvent   RuntimeControlDirection = "event"
)

type RuntimeControlEffectPhase string

const (
	RuntimeControlEffectReceived               RuntimeControlEffectPhase = "received"
	RuntimeControlEffectPromptInvocationIntent RuntimeControlEffectPhase = "prompt_invocation_intent"
	RuntimeControlEffectActive                 RuntimeControlEffectPhase = "active"
	RuntimeControlEffectTerminal               RuntimeControlEffectPhase = "terminal"
)

type RuntimeControlAuthority struct {
	WorkerSessionID   string
	RuntimeInstanceID string
	RuntimeGeneration uint64
	ChannelEpoch      uint64
	ProcessNonce      string
	StreamAttempt     uint64
	OwnerFence        uint64
}

type RuntimeControlChannelState struct {
	WorkerSessionID               string
	RuntimeInstanceID             string
	RuntimeGeneration             uint64
	ChannelEpoch                  uint64
	CredentialDigest              string
	ProcessNonce                  string
	StreamAttempt                 uint64
	OwnerID                       string
	OwnerFence                    uint64
	ResumeClaimRevision           uint64
	ResumeReadyDigest             string
	ResumeCompletedRevision       uint64
	ResumeCompletedAt             time.Time
	CommandCommittedSequence      uint64
	CommandCommittedDigest        string
	CommandAcknowledgedSequence   uint64
	CommandAppliedSequence        uint64
	EventCommittedSequence        uint64
	EventCommittedDigest          string
	EventAcknowledgedSequence     uint64
	EventAppliedSequence          uint64
	EventReplayCheckpointSequence uint64
	EventReplayCheckpointDigest   string
	EventReplayTargetSequence     uint64
	EventReplayTargetDigest       string
	ActiveEffectMessageID         string
	ActiveEffectAgentTurnID       string
	ActiveEffectCommandSequence   uint64
	ActiveEffectPhase             RuntimeControlEffectPhase
	ActiveEffectSnapshot          *contractsv1.RuntimeControlActiveTurn
	ReportedActiveTurn            *contractsv1.RuntimeControlActiveTurn
	TerminalProofPending          bool
	Status                        string
	FencedAt                      time.Time
	FenceRevision                 uint64
	FenceOwnerFence               uint64
	Revision                      uint64
}

type RuntimeControlMessage struct {
	Direction           RuntimeControlDirection
	Sequence            uint64
	MessageID           string
	IdempotencyKey      string
	Fingerprint         string
	CursorDigest        string
	Envelope            json.RawMessage
	Outcome             json.RawMessage
	EffectStatus        string
	CreatedAt           time.Time
	AcknowledgedAt      *time.Time
	ApplyIdempotencyKey string
	ApplyOutcomeDigest  string
	AppliedAt           *time.Time
	Duplicate           bool
}

type RuntimeControlReplayPlan struct {
	Channel              RuntimeControlChannelState
	PendingCommands      []RuntimeControlMessage
	PendingEvents        []RuntimeControlMessage
	EventReplayFrom      uint64
	EventReplayThrough   uint64
	ReportedActiveTurn   *contractsv1.RuntimeControlActiveTurn
	TerminalProofPending bool
	TurnConsumed         bool
}

type RuntimeControlEventAcceptance struct {
	Message RuntimeControlMessage
	Channel RuntimeControlChannelState
}

type RuntimeControlEffectResult struct {
	Channel      RuntimeControlChannelState
	TurnConsumed bool
	Duplicate    bool
}

type BindRuntimeControlChannelCommand struct {
	WorkerSessionID   string
	RuntimeInstanceID string
	RuntimeGeneration uint64
	ChannelEpoch      uint64
	Credential        string
}

type BindRuntimeControlChannelRecord struct {
	Command          BindRuntimeControlChannelCommand
	CredentialDigest string
	BoundAt          time.Time
}

type ClaimRuntimeControlStreamCommand struct {
	WorkerSessionID                        string
	RuntimeInstanceID                      string
	RuntimeGeneration                      uint64
	ChannelEpoch                           uint64
	Credential                             string
	ProcessNonce                           string
	StreamAttempt                          uint64
	OwnerID                                string
	WorkerEventLastSequence                uint64
	WorkerEventBackendAcknowledgedSequence uint64
	WorkerEventCursorDigest                string
	WorkerCommandLastSequence              uint64
	WorkerCommandAppliedSequence           uint64
	WorkerCommandCursorDigest              string
	ActiveTurn                             *contractsv1.RuntimeControlActiveTurn
}

type ClaimRuntimeControlStreamRecord struct {
	Command          ClaimRuntimeControlStreamCommand
	CredentialDigest string
	ClaimedAt        time.Time
}

type ReserveRuntimeControlCommandCommand struct {
	Authority      RuntimeControlAuthority
	MessageID      string
	IdempotencyKey string
	Envelope       json.RawMessage
}

type ReserveRuntimeControlCommandRecord struct {
	Command         ReserveRuntimeControlCommandCommand
	LogicalEnvelope contractsv1.RuntimeControlEnvelope
	ReservedAt      time.Time
}

type AcknowledgeRuntimeControlCommandsCommand struct {
	Authority       RuntimeControlAuthority
	ThroughSequence uint64
}

type AcknowledgeRuntimeControlCommandsRecord struct {
	Command        AcknowledgeRuntimeControlCommandsCommand
	AcknowledgedAt time.Time
}

type AcceptRuntimeControlEventCommand struct {
	Authority      RuntimeControlAuthority
	Sequence       uint64
	MessageID      string
	IdempotencyKey string
	Envelope       json.RawMessage
}

type AcceptRuntimeControlEventRecord struct {
	Command         AcceptRuntimeControlEventCommand
	DecodedEnvelope contractsv1.RuntimeControlEnvelope
	Fingerprint     string
	AcceptedAt      time.Time
}

type ApplyRuntimeControlEventCommand struct {
	ObservationCapture *RuntimeObservationCapture `json:"-"`
	Authority          RuntimeControlAuthority
	Sequence           uint64
	MessageID          string
	IdempotencyKey     string
	Outcome            json.RawMessage
	TurnSettlement     *TurnSettlementApplyAuthority
}

type TurnSettlementApplyAuthority struct {
	SettlementID     string
	ExpectedRevision uint64
	LeaseOwnerID     string
	LeaseFence       uint64
}

type ApplyRuntimeControlEventRecord struct {
	Command           ApplyRuntimeControlEventCommand
	OutcomeDigest     string
	EffectDisposition RuntimeControlEffectDisposition
	AppliedAt         time.Time
}

type RuntimeControlEffectDisposition string

const (
	RuntimeControlEffectDispositionCommit RuntimeControlEffectDisposition = "commit"
	RuntimeControlEffectDispositionIgnore RuntimeControlEffectDisposition = "ignore"
)

type ReconcileRuntimeControlEffectCommand struct {
	Authority              RuntimeControlAuthority
	CommandSequence        uint64
	MessageID              string
	AgentTurnID            string
	Phase                  RuntimeControlEffectPhase
	TerminalEventSequence  uint64
	TerminalEnvelopeDigest string
	ActiveTurn             *contractsv1.RuntimeControlActiveTurn
}

type ReconcileRuntimeControlEffectRecord struct {
	Command      ReconcileRuntimeControlEffectCommand
	ReconciledAt time.Time
}

type RuntimeControlJournal interface {
	BindRuntimeControlChannel(context.Context, BindRuntimeControlChannelRecord) (RuntimeControlChannelState, error)
	ClaimRuntimeControlStream(context.Context, ClaimRuntimeControlStreamRecord) (RuntimeControlReplayPlan, error)
	ReserveRuntimeControlCommand(context.Context, ReserveRuntimeControlCommandRecord) (RuntimeControlMessage, error)
	AcknowledgeRuntimeControlCommands(context.Context, AcknowledgeRuntimeControlCommandsRecord) (RuntimeControlChannelState, error)
	AcceptRuntimeControlEvent(context.Context, AcceptRuntimeControlEventRecord) (RuntimeControlEventAcceptance, error)
	ApplyRuntimeControlEvent(context.Context, ApplyRuntimeControlEventRecord) (RuntimeControlEventAcceptance, error)
	ReconcileRuntimeControlEffect(context.Context, ReconcileRuntimeControlEffectRecord) (RuntimeControlEffectResult, error)
	LoadRuntimeControlReplayPlan(context.Context, RuntimeControlAuthority) (RuntimeControlReplayPlan, error)
	DisconnectRuntimeControl(context.Context, RuntimeControlAuthority, time.Time) (RuntimeControlChannelState, error)
	FenceRuntimeControl(context.Context, FenceRuntimeControlChannelCommand, time.Time) (RuntimeControlChannelState, error)
	CompleteRuntimeControlResume(context.Context, CompleteRuntimeControlResumeRecord) (RuntimeControlChannelState, error)
}

type FenceRuntimeControlChannelCommand struct {
	WorkerSessionID   string
	RuntimeInstanceID string
	RuntimeGeneration uint64
	ChannelEpoch      uint64
	// OnlyIfNotConnected makes the durable channel row the linearization
	// authority for a ResumeComplete-vs-liveness-timeout race. A connected
	// channel is returned unchanged; resuming or disconnected channels are
	// fenced.
	OnlyIfNotConnected bool
	// ConnectedBy is the inclusive hard deadline for a connected channel to
	// win an OnlyIfNotConnected fence race.
	ConnectedBy time.Time
}

type CompleteRuntimeControlResumeCommand struct {
	Authority RuntimeControlAuthority
	Ready     contractsv1.RuntimeControlResumeReady
}

type CompleteRuntimeControlResumeRecord struct {
	Command     CompleteRuntimeControlResumeCommand
	ReadyDigest string
	CompletedAt time.Time
}

type RuntimeControlJournalService struct {
	Store RuntimeControlJournal
	Now   func() time.Time
}

func (service RuntimeControlJournalService) Bind(
	ctx context.Context,
	command BindRuntimeControlChannelCommand,
) (RuntimeControlChannelState, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlChannelState{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlIdentity(
		command.WorkerSessionID,
		command.RuntimeInstanceID,
		command.RuntimeGeneration,
		command.ChannelEpoch,
	); err != nil {
		return RuntimeControlChannelState{}, err
	}
	digest, err := digestRuntimeControlCredential(command.Credential)
	if err != nil {
		return RuntimeControlChannelState{}, err
	}
	command.Credential = ""
	return service.Store.BindRuntimeControlChannel(ctx, BindRuntimeControlChannelRecord{
		Command: command, CredentialDigest: digest,
		BoundAt: normalizePostgresTimestamp(service.Now()),
	})
}

func (service RuntimeControlJournalService) Claim(
	ctx context.Context,
	command ClaimRuntimeControlStreamCommand,
) (RuntimeControlReplayPlan, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlReplayPlan{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlIdentity(
		command.WorkerSessionID,
		command.RuntimeInstanceID,
		command.RuntimeGeneration,
		command.ChannelEpoch,
	); err != nil {
		return RuntimeControlReplayPlan{}, err
	}
	for name, value := range map[string]string{
		"process_nonce": command.ProcessNonce,
		"owner_id":      command.OwnerID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return RuntimeControlReplayPlan{}, err
		}
	}
	if command.StreamAttempt == 0 {
		return RuntimeControlReplayPlan{}, fmt.Errorf("stream_attempt must be positive")
	}
	if command.WorkerEventBackendAcknowledgedSequence > command.WorkerEventLastSequence {
		return RuntimeControlReplayPlan{}, fmt.Errorf("worker event acknowledged cursor exceeds its last sequence")
	}
	if command.WorkerCommandAppliedSequence > command.WorkerCommandLastSequence {
		return RuntimeControlReplayPlan{}, fmt.Errorf("worker command applied cursor exceeds its last sequence")
	}
	if err := validateRuntimeControlCursorDigest(command.WorkerEventCursorDigest); err != nil {
		return RuntimeControlReplayPlan{}, fmt.Errorf("worker event cursor: %w", err)
	}
	if err := validateRuntimeControlCursorDigest(command.WorkerCommandCursorDigest); err != nil {
		return RuntimeControlReplayPlan{}, fmt.Errorf("worker command cursor: %w", err)
	}
	if command.ActiveTurn != nil {
		if err := command.ActiveTurn.Validate(); err != nil {
			return RuntimeControlReplayPlan{}, fmt.Errorf("active Turn proof: %w", err)
		}
	}
	digest, err := digestRuntimeControlCredential(command.Credential)
	if err != nil {
		return RuntimeControlReplayPlan{}, err
	}
	command.Credential = ""
	return service.Store.ClaimRuntimeControlStream(ctx, ClaimRuntimeControlStreamRecord{
		Command: command, CredentialDigest: digest,
		ClaimedAt: normalizePostgresTimestamp(service.Now()),
	})
}

func (service RuntimeControlJournalService) ReserveCommand(
	ctx context.Context,
	command ReserveRuntimeControlCommandCommand,
) (RuntimeControlMessage, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlMessage{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlAuthority(command.Authority); err != nil {
		return RuntimeControlMessage{}, err
	}
	envelope, err := decodeRuntimeControlCommandTemplate(
		command.MessageID,
		command.IdempotencyKey,
		command.Envelope,
	)
	if err != nil {
		return RuntimeControlMessage{}, err
	}
	if !runtimeControlEnvelopeMatchesAuthority(envelope, command.Authority) {
		return RuntimeControlMessage{}, ErrRuntimeControlIdentityConflict
	}
	return service.Store.ReserveRuntimeControlCommand(ctx, ReserveRuntimeControlCommandRecord{
		Command: command, LogicalEnvelope: envelope,
		ReservedAt: normalizePostgresTimestamp(service.Now()),
	})
}

func decodeRuntimeControlCommandTemplate(
	messageID string,
	idempotencyKey string,
	encoded json.RawMessage,
) (contractsv1.RuntimeControlEnvelope, error) {
	if err := validateRuntimeControlMessageIdentity(
		messageID,
		idempotencyKey,
	); err != nil {
		return contractsv1.RuntimeControlEnvelope{}, err
	}
	var envelope contractsv1.RuntimeControlEnvelope
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return contractsv1.RuntimeControlEnvelope{},
			fmt.Errorf("decode Runtime control command template: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return contractsv1.RuntimeControlEnvelope{},
			fmt.Errorf(
				"Runtime control command template must contain one object",
			)
	}
	if envelope.Kind != "command" && envelope.Kind != "ack" {
		return contractsv1.RuntimeControlEnvelope{},
			fmt.Errorf(
				"Runtime control outbound journal requires command or ACK envelope",
			)
	}
	if envelope.Sequence != 0 || envelope.AckSequence != 0 ||
		!envelope.SentAt.IsZero() {
		return contractsv1.RuntimeControlEnvelope{},
			fmt.Errorf(
				"Runtime control command transport fields must be unset before reservation",
			)
	}
	if envelope.MessageID != messageID ||
		envelope.IdempotencyKey != idempotencyKey {
		return contractsv1.RuntimeControlEnvelope{},
			fmt.Errorf(
				"Runtime control command identity disagrees with its envelope",
			)
	}
	return envelope, nil
}

func (service RuntimeControlJournalService) AcknowledgeCommands(
	ctx context.Context,
	command AcknowledgeRuntimeControlCommandsCommand,
) (RuntimeControlChannelState, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlChannelState{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlAuthority(command.Authority); err != nil {
		return RuntimeControlChannelState{}, err
	}
	return service.Store.AcknowledgeRuntimeControlCommands(ctx, AcknowledgeRuntimeControlCommandsRecord{
		Command: command, AcknowledgedAt: normalizePostgresTimestamp(service.Now()),
	})
}

func (service RuntimeControlJournalService) AcceptEvent(
	ctx context.Context,
	command AcceptRuntimeControlEventCommand,
) (RuntimeControlEventAcceptance, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlEventAcceptance{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlAuthority(command.Authority); err != nil {
		return RuntimeControlEventAcceptance{}, err
	}
	if command.Sequence == 0 {
		return RuntimeControlEventAcceptance{}, fmt.Errorf("event sequence must be positive")
	}
	if err := validateRuntimeControlMessageIdentity(command.MessageID, command.IdempotencyKey); err != nil {
		return RuntimeControlEventAcceptance{}, err
	}
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(command.Envelope)
	if err != nil {
		return RuntimeControlEventAcceptance{}, fmt.Errorf("decode Runtime control event envelope: %w", err)
	}
	if envelope.Kind != "event" && envelope.Kind != "heartbeat" && envelope.Kind != "ack" {
		return RuntimeControlEventAcceptance{}, fmt.Errorf(
			"Runtime control event journal requires event, heartbeat, or ACK envelope",
		)
	}
	if envelope.Sequence != command.Sequence ||
		envelope.MessageID != command.MessageID ||
		envelope.IdempotencyKey != command.IdempotencyKey {
		return RuntimeControlEventAcceptance{}, fmt.Errorf(
			"Runtime control event identity disagrees with its envelope",
		)
	}
	if !runtimeControlEnvelopeMatchesAuthority(envelope, command.Authority) {
		return RuntimeControlEventAcceptance{}, ErrRuntimeControlIdentityConflict
	}
	fingerprint, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil {
		return RuntimeControlEventAcceptance{}, err
	}
	return service.Store.AcceptRuntimeControlEvent(ctx, AcceptRuntimeControlEventRecord{
		Command: command, DecodedEnvelope: envelope, Fingerprint: fingerprint,
		AcceptedAt: normalizePostgresTimestamp(service.Now()),
	})
}

func (service RuntimeControlJournalService) ReconcileEffect(
	ctx context.Context,
	command ReconcileRuntimeControlEffectCommand,
) (RuntimeControlEffectResult, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlEffectResult{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlAuthority(command.Authority); err != nil {
		return RuntimeControlEffectResult{}, err
	}
	if command.CommandSequence == 0 {
		return RuntimeControlEffectResult{}, fmt.Errorf("command sequence must be positive")
	}
	for name, value := range map[string]string{
		"message_id":    command.MessageID,
		"agent_turn_id": command.AgentTurnID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return RuntimeControlEffectResult{}, err
		}
	}
	if !command.Phase.valid() {
		return RuntimeControlEffectResult{}, fmt.Errorf("Runtime control effect phase %q is invalid", command.Phase)
	}
	if command.ActiveTurn == nil {
		return RuntimeControlEffectResult{}, fmt.Errorf("Runtime control effect requires exact active-Turn proof")
	}
	if err := command.ActiveTurn.Validate(); err != nil {
		return RuntimeControlEffectResult{}, fmt.Errorf("Runtime control effect proof: %w", err)
	}
	if command.ActiveTurn.StartCommandSequence != command.CommandSequence ||
		command.ActiveTurn.StartMessageID != command.MessageID ||
		string(command.ActiveTurn.AgentTurnID) != command.AgentTurnID ||
		command.ActiveTurn.Phase != string(command.Phase) {
		return RuntimeControlEffectResult{}, fmt.Errorf(
			"Runtime control effect proof disagrees with command identity",
		)
	}
	if command.Phase == RuntimeControlEffectTerminal {
		if command.TerminalEventSequence == 0 {
			return RuntimeControlEffectResult{}, fmt.Errorf("terminal event sequence must be positive")
		}
		if err := validateRuntimeControlCursorDigest(command.TerminalEnvelopeDigest); err != nil {
			return RuntimeControlEffectResult{}, fmt.Errorf("terminal envelope digest: %w", err)
		}
		if command.ActiveTurn.LastUpdateSequence != command.TerminalEventSequence ||
			command.ActiveTurn.TerminalEnvelopeDigest != command.TerminalEnvelopeDigest {
			return RuntimeControlEffectResult{}, fmt.Errorf(
				"terminal effect proof disagrees with terminal event",
			)
		}
	} else if command.TerminalEventSequence != 0 || command.TerminalEnvelopeDigest != "" {
		return RuntimeControlEffectResult{}, fmt.Errorf(
			"nonterminal effect cannot carry terminal event proof",
		)
	}
	return service.Store.ReconcileRuntimeControlEffect(ctx, ReconcileRuntimeControlEffectRecord{
		Command: command, ReconciledAt: normalizePostgresTimestamp(service.Now()),
	})
}

func (service RuntimeControlJournalService) ApplyEvent(
	ctx context.Context,
	command ApplyRuntimeControlEventCommand,
) (RuntimeControlEventAcceptance, error) {
	if command.ObservationCapture != nil {
		capture := command.ObservationCapture.Clone()
		command.ObservationCapture = &capture
	}
	if service.Store == nil || service.Now == nil {
		return RuntimeControlEventAcceptance{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlAuthority(command.Authority); err != nil {
		return RuntimeControlEventAcceptance{}, err
	}
	if command.Sequence == 0 {
		return RuntimeControlEventAcceptance{}, fmt.Errorf("event sequence must be positive")
	}
	if err := validateRuntimeControlMessageIdentity(command.MessageID, command.IdempotencyKey); err != nil {
		return RuntimeControlEventAcceptance{}, err
	}
	if err := validateRuntimeControlJSON("outcome", command.Outcome, true); err != nil {
		return RuntimeControlEventAcceptance{}, err
	}
	if command.TurnSettlement != nil {
		if err := validateCanonical(
			"settlement_id",
			command.TurnSettlement.SettlementID,
		); err != nil {
			return RuntimeControlEventAcceptance{}, err
		}
		if err := validateCanonical(
			"lease_owner_id",
			command.TurnSettlement.LeaseOwnerID,
		); err != nil {
			return RuntimeControlEventAcceptance{}, err
		}
		if command.TurnSettlement.ExpectedRevision == 0 ||
			command.TurnSettlement.LeaseFence == 0 {
			return RuntimeControlEventAcceptance{}, fmt.Errorf(
				"terminal Turn settlement Apply requires exact revision and lease fence",
			)
		}
	}
	var outcome struct {
		EffectDisposition RuntimeControlEffectDisposition `json:"effect_disposition"`
	}
	if err := json.Unmarshal(command.Outcome, &outcome); err != nil {
		return RuntimeControlEventAcceptance{}, fmt.Errorf("decode Runtime control event outcome: %w", err)
	}
	switch outcome.EffectDisposition {
	case "", RuntimeControlEffectDispositionCommit, RuntimeControlEffectDispositionIgnore:
	default:
		return RuntimeControlEventAcceptance{}, fmt.Errorf(
			"Runtime control effect disposition %q is invalid",
			outcome.EffectDisposition,
		)
	}
	return service.Store.ApplyRuntimeControlEvent(ctx, ApplyRuntimeControlEventRecord{
		Command: command, OutcomeDigest: digestRuntimeControlBytes(command.Outcome),
		EffectDisposition: outcome.EffectDisposition,
		AppliedAt:         normalizePostgresTimestamp(service.Now()),
	})
}

func (service RuntimeControlJournalService) Disconnect(
	ctx context.Context,
	authority RuntimeControlAuthority,
) (RuntimeControlChannelState, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlChannelState{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlAuthority(authority); err != nil {
		return RuntimeControlChannelState{}, err
	}
	return service.Store.DisconnectRuntimeControl(
		ctx,
		authority,
		normalizePostgresTimestamp(service.Now()),
	)
}

func (service RuntimeControlJournalService) Fence(
	ctx context.Context,
	command FenceRuntimeControlChannelCommand,
) (RuntimeControlChannelState, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlChannelState{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlIdentity(
		command.WorkerSessionID,
		command.RuntimeInstanceID,
		command.RuntimeGeneration,
		command.ChannelEpoch,
	); err != nil {
		return RuntimeControlChannelState{}, err
	}
	if command.OnlyIfNotConnected {
		if command.ConnectedBy.IsZero() {
			return RuntimeControlChannelState{}, fmt.Errorf(
				"conditional Runtime control fence deadline is required",
			)
		}
		command.ConnectedBy = normalizePostgresTimestamp(command.ConnectedBy)
	} else if !command.ConnectedBy.IsZero() {
		return RuntimeControlChannelState{}, fmt.Errorf(
			"unconditional Runtime control fence cannot carry a deadline",
		)
	}
	return service.Store.FenceRuntimeControl(
		ctx,
		command,
		normalizePostgresTimestamp(service.Now()),
	)
}

func (service RuntimeControlJournalService) CompleteResume(
	ctx context.Context,
	command CompleteRuntimeControlResumeCommand,
) (RuntimeControlChannelState, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeControlChannelState{}, fmt.Errorf("Runtime control journal dependencies are required")
	}
	if err := validateRuntimeControlAuthority(command.Authority); err != nil {
		return RuntimeControlChannelState{}, err
	}
	if err := command.Ready.Validate(); err != nil {
		return RuntimeControlChannelState{}, fmt.Errorf("Runtime control resume ready: %w", err)
	}
	ready, err := json.Marshal(command.Ready)
	if err != nil {
		return RuntimeControlChannelState{}, fmt.Errorf("marshal Runtime control resume ready: %w", err)
	}
	return service.Store.CompleteRuntimeControlResume(ctx, CompleteRuntimeControlResumeRecord{
		Command: command, ReadyDigest: digestRuntimeControlBytes(ready),
		CompletedAt: normalizePostgresTimestamp(service.Now()),
	})
}

func (service RuntimeControlJournalService) ReplayPlan(
	ctx context.Context,
	authority RuntimeControlAuthority,
) (RuntimeControlReplayPlan, error) {
	if service.Store == nil {
		return RuntimeControlReplayPlan{}, fmt.Errorf("Runtime control journal store is required")
	}
	if err := validateRuntimeControlAuthority(authority); err != nil {
		return RuntimeControlReplayPlan{}, err
	}
	return service.Store.LoadRuntimeControlReplayPlan(ctx, authority)
}

func (phase RuntimeControlEffectPhase) valid() bool {
	switch phase {
	case RuntimeControlEffectReceived,
		RuntimeControlEffectPromptInvocationIntent,
		RuntimeControlEffectActive,
		RuntimeControlEffectTerminal:
		return true
	default:
		return false
	}
}

func validateRuntimeControlIdentity(
	workerSessionID string,
	runtimeInstanceID string,
	runtimeGeneration uint64,
	channelEpoch uint64,
) error {
	if err := validateCanonical("worker_session_id", workerSessionID); err != nil {
		return err
	}
	if err := validateCanonical("runtime_instance_id", runtimeInstanceID); err != nil {
		return err
	}
	if runtimeGeneration == 0 || channelEpoch == 0 {
		return fmt.Errorf("Runtime control generation and ChannelEpoch must be positive")
	}
	return nil
}

func validateRuntimeControlAuthority(authority RuntimeControlAuthority) error {
	if err := validateRuntimeControlIdentity(
		authority.WorkerSessionID,
		authority.RuntimeInstanceID,
		authority.RuntimeGeneration,
		authority.ChannelEpoch,
	); err != nil {
		return err
	}
	if err := validateCanonical("process_nonce", authority.ProcessNonce); err != nil {
		return err
	}
	if authority.StreamAttempt == 0 || authority.OwnerFence == 0 {
		return fmt.Errorf("Runtime control stream attempt and owner fence must be positive")
	}
	return nil
}

func validateRuntimeControlMessageIdentity(messageID, idempotencyKey string) error {
	if err := validateCanonical("message_id", messageID); err != nil {
		return err
	}
	return validateCanonical("idempotency_key", idempotencyKey)
}

func runtimeControlEnvelopeMatchesAuthority(
	envelope contractsv1.RuntimeControlEnvelope,
	authority RuntimeControlAuthority,
) bool {
	return string(envelope.WorkerSessionID) == authority.WorkerSessionID &&
		string(envelope.RuntimeInstanceID) == authority.RuntimeInstanceID &&
		uint64(envelope.RuntimeGeneration) == authority.RuntimeGeneration &&
		envelope.ChannelEpoch == authority.ChannelEpoch
}

func validateRuntimeControlCursorDigest(value string) error {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("cursor digest must use sha256")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:")); err != nil {
		return fmt.Errorf("cursor digest must use lowercase hexadecimal sha256")
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("cursor digest must use lowercase hexadecimal sha256")
	}
	return nil
}

func validateRuntimeControlJSON(name string, value json.RawMessage, required bool) error {
	if len(value) == 0 {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%s must contain one JSON object", name)
	}
	return nil
}

func digestRuntimeControlCredential(credential string) (string, error) {
	if credential == "" || strings.TrimSpace(credential) != credential ||
		strings.ContainsAny(credential, "\r\n\x00") {
		return "", fmt.Errorf("Runtime control credential must be canonical")
	}
	return digestRuntimeControlBytes([]byte(credential)), nil
}

func digestRuntimeControlBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
