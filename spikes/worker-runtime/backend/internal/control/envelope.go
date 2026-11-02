package control

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
)

type PayloadKind string

const MaxWorkerEnvelopeBytes = 256 * 1024

const (
	PayloadKindInitializeSession PayloadKind = "initialize_session"
	PayloadKindStartAgentTurn    PayloadKind = "start_agent_turn"
	PayloadKindCancelAgentTurn   PayloadKind = "cancel_agent_turn"
	PayloadKindShutdownRuntime   PayloadKind = "shutdown_runtime"
	PayloadKindRuntimeReady      PayloadKind = "runtime_ready"
	PayloadKindAgentSessionReady PayloadKind = "agent_session_ready"
	PayloadKindTurnStarted       PayloadKind = "turn_started"
	PayloadKindACPUpdate         PayloadKind = "acp_update"
	PayloadKindModelObservation  PayloadKind = "model_observation"
	PayloadKindTurnCompleted     PayloadKind = "turn_completed"
	PayloadKindTurnFailed        PayloadKind = "turn_failed"
	PayloadKindRuntimeDiagnostic PayloadKind = "runtime_diagnostic"
)

type Cursor struct {
	LastInboundSequence uint64
	LastPeerSequence    uint64
}

func ValidateWorkerEnvelope(envelope *workercontrolv1.WorkerEnvelope, cursor Cursor) error {
	if envelope == nil {
		return fmt.Errorf("worker envelope is required")
	}
	if proto.Size(envelope) > MaxWorkerEnvelopeBytes {
		return fmt.Errorf("worker envelope exceeds %d byte limit", MaxWorkerEnvelopeBytes)
	}

	payloadKind, err := workerPayloadKind(envelope)
	if err != nil {
		return err
	}
	return ValidateInbound(envelope.GetMeta(), payloadKind, cursor)
}

func ValidateInbound(meta *workercontrolv1.EnvelopeMeta, payloadKind PayloadKind, cursor Cursor) error {
	if meta == nil {
		return fmt.Errorf("envelope meta is required")
	}
	if meta.GetMessageId() == "" {
		return fmt.Errorf("message ID is required")
	}
	if meta.GetSentAt() == nil {
		return fmt.Errorf("sent_at is required")
	}
	if err := meta.GetSentAt().CheckValid(); err != nil {
		return fmt.Errorf("sent_at is invalid: %w", err)
	}
	if meta.GetIdentity() == nil {
		return fmt.Errorf("identity is required")
	}

	identity := meta.GetIdentity()
	if identity.GetWorkerSessionId() == "" || identity.GetWorkerRunId() == "" || identity.GetRuntimeInstanceId() == "" {
		return fmt.Errorf("worker session, run, and runtime instance IDs are required")
	}
	if identity.GetRuntimeGeneration() != SpikeRuntimeGeneration {
		return fmt.Errorf("runtime generation must equal %d", SpikeRuntimeGeneration)
	}
	if requiresAgentTurn(payloadKind) && identity.GetAgentTurnId() == "" {
		return fmt.Errorf("agent turn ID is required for %s", payloadKind)
	}
	if meta.GetStreamSequence() == 0 || meta.GetStreamSequence() <= cursor.LastInboundSequence {
		return fmt.Errorf("stream sequence must be greater than the last inbound sequence")
	}
	if meta.GetAckSequence() > cursor.LastPeerSequence {
		return fmt.Errorf("ack sequence exceeds the last peer sequence")
	}
	return nil
}

func workerPayloadKind(envelope *workercontrolv1.WorkerEnvelope) (PayloadKind, error) {
	switch envelope.GetPayload().(type) {
	case *workercontrolv1.WorkerEnvelope_RuntimeReady:
		return PayloadKindRuntimeReady, nil
	case *workercontrolv1.WorkerEnvelope_AgentSessionReady:
		return PayloadKindAgentSessionReady, nil
	case *workercontrolv1.WorkerEnvelope_TurnStarted:
		return PayloadKindTurnStarted, nil
	case *workercontrolv1.WorkerEnvelope_AcpUpdate:
		return PayloadKindACPUpdate, nil
	case *workercontrolv1.WorkerEnvelope_ModelObservation:
		return PayloadKindModelObservation, nil
	case *workercontrolv1.WorkerEnvelope_TurnCompleted:
		return PayloadKindTurnCompleted, nil
	case *workercontrolv1.WorkerEnvelope_TurnFailed:
		return PayloadKindTurnFailed, nil
	case *workercontrolv1.WorkerEnvelope_RuntimeDiagnostic:
		return PayloadKindRuntimeDiagnostic, nil
	default:
		return "", fmt.Errorf("worker envelope payload is required")
	}
}

func requiresAgentTurn(payloadKind PayloadKind) bool {
	switch payloadKind {
	case PayloadKindStartAgentTurn,
		PayloadKindCancelAgentTurn,
		PayloadKindTurnStarted,
		PayloadKindACPUpdate,
		PayloadKindModelObservation,
		PayloadKindTurnCompleted,
		PayloadKindTurnFailed:
		return true
	default:
		return false
	}
}
