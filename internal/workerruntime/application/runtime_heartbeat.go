package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	RuntimeSuspectAfter   = 45 * time.Second
	RuntimeUnhealthyAfter = 90 * time.Second
)

type RuntimeLivenessStatus string

const (
	RuntimeLivenessHealthy   RuntimeLivenessStatus = "healthy"
	RuntimeLivenessSuspect   RuntimeLivenessStatus = "suspect"
	RuntimeLivenessUnhealthy RuntimeLivenessStatus = "unhealthy"
)

type RuntimeLivenessStage string

const (
	RuntimeLivenessStageSuspect   RuntimeLivenessStage = "suspect"
	RuntimeLivenessStageUnhealthy RuntimeLivenessStage = "unhealthy"
)

type ObserveRuntimeHeartbeatCommand struct {
	WorkerSessionID   string
	RuntimeInstanceID string
	RuntimeGeneration uint64
	ChannelEpoch      uint64
	HeartbeatSequence uint64
	ControlSequence   uint64
	ReceivedAt        time.Time
}

type RecordRuntimeHeartbeat struct {
	Command            ObserveRuntimeHeartbeatCommand
	ScheduledCommandID string
	SuspectDueAt       time.Time
	UnhealthyDueAt     time.Time
}

type RuntimeHeartbeatState struct {
	WorkerSessionID     string
	RuntimeInstanceID   string
	RuntimeGeneration   uint64
	ChannelEpoch        uint64
	HeartbeatSequence   uint64
	ControlSequence     uint64
	LastReceivedAt      time.Time
	LivenessStatus      RuntimeLivenessStatus
	SuspectDueAt        time.Time
	UnhealthyDueAt      time.Time
	ObservationRevision uint64
	RuntimeRevision     uint64
	Duplicate           bool
	Stale               bool
}

type RuntimeHeartbeatStore interface {
	RecordRuntimeHeartbeat(context.Context, RecordRuntimeHeartbeat) (RuntimeHeartbeatState, error)
}

type EvaluateRuntimeLivenessCommand struct {
	TenantID                    string
	RuntimeInstanceID           string
	RuntimeGeneration           uint64
	ChannelEpoch                uint64
	HeartbeatSequence           uint64
	Stage                       RuntimeLivenessStage
	ExpectedObservationRevision uint64
	EvaluatedAt                 time.Time
}

type EvaluateRuntimeLivenessRecord struct {
	Command                 EvaluateRuntimeLivenessCommand
	UnhealthyCommandID      string
	UnhealthyIdempotencyKey string
}

type RuntimeLivenessEvaluation struct {
	Applied             bool
	LivenessStatus      RuntimeLivenessStatus
	ObservationRevision uint64
	RuntimeRevision     uint64
	OpenRecovery        *OpenRuntimeRecoveryCommand
}

type RuntimeLivenessStore interface {
	EvaluateRuntimeLiveness(context.Context, EvaluateRuntimeLivenessRecord) (RuntimeLivenessEvaluation, error)
}

type RuntimeLivenessService struct {
	Store RuntimeLivenessStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service RuntimeLivenessService) Evaluate(
	ctx context.Context,
	command EvaluateRuntimeLivenessCommand,
) (RuntimeLivenessEvaluation, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return RuntimeLivenessEvaluation{}, fmt.Errorf("Runtime liveness dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "runtime_instance_id": command.RuntimeInstanceID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return RuntimeLivenessEvaluation{}, err
		}
	}
	if command.RuntimeGeneration == 0 || command.ChannelEpoch == 0 ||
		command.HeartbeatSequence == 0 || command.ExpectedObservationRevision == 0 {
		return RuntimeLivenessEvaluation{}, fmt.Errorf("Runtime liveness identity and revision are required")
	}
	switch command.Stage {
	case RuntimeLivenessStageSuspect, RuntimeLivenessStageUnhealthy:
	default:
		return RuntimeLivenessEvaluation{}, fmt.Errorf("Runtime liveness stage %q is invalid", command.Stage)
	}
	command.EvaluatedAt = service.Now().UTC()
	nextID, err := service.IDs.NewID("scheduled-runtime-liveness")
	if err != nil {
		return RuntimeLivenessEvaluation{}, err
	}
	return service.Store.EvaluateRuntimeLiveness(ctx, EvaluateRuntimeLivenessRecord{
		Command: command, UnhealthyCommandID: nextID,
		UnhealthyIdempotencyKey: fmt.Sprintf(
			"heartbeat/%d/%d/unhealthy",
			command.RuntimeGeneration,
			command.HeartbeatSequence,
		),
	})
}

type RuntimeHeartbeatService struct {
	Store RuntimeHeartbeatStore
	IDs   AllocationIDGenerator
}

func (service RuntimeHeartbeatService) Observe(
	ctx context.Context,
	command ObserveRuntimeHeartbeatCommand,
) (RuntimeHeartbeatState, error) {
	if service.Store == nil || service.IDs == nil {
		return RuntimeHeartbeatState{}, fmt.Errorf("Runtime heartbeat dependencies are required")
	}
	for name, value := range map[string]string{
		"worker_session_id":   command.WorkerSessionID,
		"runtime_instance_id": command.RuntimeInstanceID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return RuntimeHeartbeatState{}, err
		}
	}
	if command.RuntimeGeneration == 0 || command.ChannelEpoch == 0 ||
		command.HeartbeatSequence == 0 || command.ControlSequence == 0 || command.ReceivedAt.IsZero() {
		return RuntimeHeartbeatState{}, fmt.Errorf("Runtime heartbeat identity, sequences, and receive time are required")
	}
	scheduledCommandID, err := service.IDs.NewID("scheduled-runtime-liveness")
	if err != nil {
		return RuntimeHeartbeatState{}, err
	}
	receivedAt := command.ReceivedAt.UTC()
	command.ReceivedAt = receivedAt
	return service.Store.RecordRuntimeHeartbeat(ctx, RecordRuntimeHeartbeat{
		Command: command, ScheduledCommandID: scheduledCommandID,
		SuspectDueAt:   receivedAt.Add(RuntimeSuspectAfter),
		UnhealthyDueAt: receivedAt.Add(RuntimeUnhealthyAfter),
	})
}

type WorkerControlEventObserver interface {
	ObserveWorkerControlEvent(context.Context, WorkerControlEvent, time.Time) error
}

type RuntimeHeartbeatObserver struct {
	Service RuntimeHeartbeatService
}

// DecodeRuntimeHeartbeatEvent is the single strict decoder for both live
// delivery and durable-journal repair. The boolean is false only when the
// event is unrelated to Runtime heartbeat liveness.
func DecodeRuntimeHeartbeatEvent(
	event WorkerControlEvent,
	receivedAt time.Time,
) (ObserveRuntimeHeartbeatCommand, bool, error) {
	if event.Envelope.Kind != "heartbeat" &&
		event.Envelope.MessageType != "runtime.heartbeat" {
		return ObserveRuntimeHeartbeatCommand{}, false, nil
	}
	if event.Envelope.Kind != "heartbeat" ||
		event.Envelope.MessageType != "runtime.heartbeat" {
		return ObserveRuntimeHeartbeatCommand{}, true,
			fmt.Errorf(
				"Runtime heartbeat envelope kind and message type disagree",
			)
	}
	var payload struct {
		HeartbeatSequence uint64 `json:"heartbeat_sequence"`
	}
	payloadJSON := event.Payload
	if len(payloadJSON) == 0 {
		payloadJSON = event.Envelope.Payload
	}
	decoder := json.NewDecoder(bytes.NewReader(payloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return ObserveRuntimeHeartbeatCommand{}, true,
			fmt.Errorf("decode Runtime heartbeat payload")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF ||
		payload.HeartbeatSequence == 0 {
		return ObserveRuntimeHeartbeatCommand{}, true,
			fmt.Errorf("Runtime heartbeat payload is invalid")
	}
	return ObserveRuntimeHeartbeatCommand{
		WorkerSessionID: string(
			event.Envelope.WorkerSessionID,
		),
		RuntimeInstanceID: string(
			event.Envelope.RuntimeInstanceID,
		),
		RuntimeGeneration: uint64(
			event.Envelope.RuntimeGeneration,
		),
		ChannelEpoch:      event.Envelope.ChannelEpoch,
		HeartbeatSequence: payload.HeartbeatSequence,
		ControlSequence:   event.Envelope.Sequence,
		ReceivedAt:        receivedAt,
	}, true, nil
}

func (observer RuntimeHeartbeatObserver) ObserveWorkerControlEvent(
	ctx context.Context,
	event WorkerControlEvent,
	receivedAt time.Time,
) error {
	command, heartbeat, err := DecodeRuntimeHeartbeatEvent(
		event,
		receivedAt,
	)
	if err != nil {
		return err
	}
	if !heartbeat {
		return nil
	}
	_, err = observer.Service.Observe(ctx, command)
	return err
}

func ClassifyRuntimeLiveness(lastReceivedAt, now time.Time) RuntimeLivenessStatus {
	elapsed := now.Sub(lastReceivedAt)
	switch {
	case elapsed >= RuntimeUnhealthyAfter:
		return RuntimeLivenessUnhealthy
	case elapsed >= RuntimeSuspectAfter:
		return RuntimeLivenessSuspect
	default:
		return RuntimeLivenessHealthy
	}
}

var _ WorkerControlEventObserver = RuntimeHeartbeatObserver{}
