package contractsv1

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"math"
	"strings"
	"time"
)

const (
	RuntimeControlSchemaVersion      = "runtime.control.v1"
	RuntimeControlFrameSchemaVersion = "runtime.control.frame.v1"

	RuntimeControlFrameTypeResumeHello    = "resume_hello"
	RuntimeControlFrameTypeResumeAck      = "resume_ack"
	RuntimeControlFrameTypeResumeReady    = "resume_ready"
	RuntimeControlFrameTypeResumeComplete = "resume_complete"
	RuntimeControlFrameTypeEnvelope       = "envelope"

	RuntimeControlDirectionBackendToRuntime = "backend_to_runtime"
	RuntimeControlDirectionRuntimeToBackend = "runtime_to_backend"
	RuntimeControlCursorGenesisDigest       = "sha256:4f4de1293c07ebd69a11800878dd689311974c66f87a0582375b21c72bb787af"

	RuntimeControlTurnEffectReceived           = "received"
	RuntimeControlTurnEffectPromptIntent       = "prompt_invocation_intent"
	RuntimeControlTurnEffectActive             = "active"
	RuntimeControlTurnEffectTerminal           = "terminal"
	RuntimeControlResumeNoActiveTurn           = "none"
	RuntimeControlResumeContinueActive         = "continue_active"
	RuntimeControlResumeAlreadyTerminal        = "already_terminal"
	RuntimeControlResumeContinuityUnproven     = "continuity_unproven"
	RuntimeControlResumeRejectedStaleRuntime   = "stale_runtime"
	RuntimeControlResumeRejectedCursorConflict = "cursor_conflict"
	RuntimeControlResumeRejectedTurnConflict   = "active_turn_conflict"
	RuntimeControlResumeRejectedStaleAttempt   = "stream_attempt_stale"
	RuntimeControlResumeRejectedLeaseHeld      = "lease_held"
	RuntimeControlResumeRejectedProtocol       = "protocol_conflict"
)

// RuntimeControlFrame is the transport-level tagged union used during stream
// resume. Exactly one variant must be present. Ordinary envelopes retain their
// own schema and sequence semantics inside the envelope variant.
type RuntimeControlFrame struct {
	SchemaVersion  string                        `json:"schema_version"`
	FrameType      string                        `json:"frame_type"`
	ResumeHello    *RuntimeControlResumeHello    `json:"resume_hello"`
	ResumeAck      *RuntimeControlResumeAck      `json:"resume_ack"`
	ResumeReady    *RuntimeControlResumeReady    `json:"resume_ready"`
	ResumeComplete *RuntimeControlResumeComplete `json:"resume_complete"`
	Envelope       *RuntimeControlEnvelope       `json:"envelope"`
}

type RuntimeControlResumeHello struct {
	WorkerSessionID              WorkerSessionID           `json:"worker_session_id"`
	RuntimeInstanceID            RuntimeInstanceID         `json:"runtime_instance_id"`
	RuntimeGeneration            uint32                    `json:"runtime_generation"`
	ChannelEpoch                 uint64                    `json:"channel_epoch"`
	ShimProcessNonce             string                    `json:"shim_process_nonce"`
	StreamAttempt                uint64                    `json:"stream_attempt"`
	WorkerTXLastSequence         uint64                    `json:"worker_tx_last_sequence"`
	WorkerTXBackendAckedSequence uint64                    `json:"worker_tx_backend_acked_sequence"`
	WorkerTXCursorDigest         string                    `json:"worker_tx_cursor_digest"`
	WorkerRXLastSequence         uint64                    `json:"worker_rx_last_sequence"`
	WorkerRXAppliedSequence      uint64                    `json:"worker_rx_applied_sequence"`
	WorkerRXCursorDigest         string                    `json:"worker_rx_cursor_digest"`
	ActiveTurn                   *RuntimeControlActiveTurn `json:"active_turn"`
}

type RuntimeControlActiveTurn struct {
	WorkerRunID          WorkerRunID    `json:"worker_run_id"`
	AgentSessionID       AgentSessionID `json:"agent_session_id"`
	AgentTurnID          AgentTurnID    `json:"agent_turn_id"`
	StartCommandSequence uint64         `json:"start_command_sequence"`
	StartMessageID       string         `json:"start_message_id"`
	StartIdempotencyKey  string         `json:"start_idempotency_key"`
	// StartEnvelopeDigest is RuntimeControlEnvelopeDigest for the exact
	// journaled agent_turn.start command, not a digest of only its prompt.
	StartEnvelopeDigest string `json:"start_envelope_digest"`
	Phase               string `json:"phase"`
	LastUpdateSequence  uint64 `json:"last_update_sequence"`
	// TerminalEnvelopeDigest is RuntimeControlEnvelopeDigest for the exact
	// journaled terminal event. It is empty until Phase is terminal.
	TerminalEnvelopeDigest string `json:"terminal_envelope_digest"`
}

type RuntimeControlResumeAck struct {
	Accepted                         bool                      `json:"accepted"`
	RejectionCode                    string                    `json:"rejection_code"`
	ServerConnectionID               string                    `json:"server_connection_id"`
	JournalRevision                  uint64                    `json:"journal_revision"`
	BackendRXCommittedWorkerSequence uint64                    `json:"backend_rx_committed_worker_sequence"`
	BackendRXAppliedWorkerSequence   uint64                    `json:"backend_rx_applied_worker_sequence"`
	BackendRXCursorDigest            string                    `json:"backend_rx_cursor_digest"`
	BackendTXLastCommandSequence     uint64                    `json:"backend_tx_last_command_sequence"`
	BackendTXWorkerAckedSequence     uint64                    `json:"backend_tx_worker_acked_sequence"`
	BackendTXWorkerAppliedSequence   uint64                    `json:"backend_tx_worker_applied_sequence"`
	BackendTXCursorDigest            string                    `json:"backend_tx_cursor_digest"`
	ReplayWorkerFromSequence         uint64                    `json:"replay_worker_from_sequence"`
	ReplayBackendFromSequence        uint64                    `json:"replay_backend_from_sequence"`
	ActiveTurnDisposition            string                    `json:"active_turn_disposition"`
	AuthoritativeStart               *RuntimeControlActiveTurn `json:"authoritative_start"`
}

type RuntimeControlResumeReady struct {
	ServerConnectionID              string                    `json:"server_connection_id"`
	AcknowledgedJournalRevision     uint64                    `json:"acknowledged_journal_revision"`
	WorkerTXReplayedThroughSequence uint64                    `json:"worker_tx_replayed_through_sequence"`
	WorkerTXCursorDigest            string                    `json:"worker_tx_cursor_digest"`
	WorkerRXReceivedThroughSequence uint64                    `json:"worker_rx_received_through_sequence"`
	WorkerRXAppliedSequence         uint64                    `json:"worker_rx_applied_sequence"`
	WorkerRXCursorDigest            string                    `json:"worker_rx_cursor_digest"`
	ActiveTurn                      *RuntimeControlActiveTurn `json:"active_turn"`
}

type RuntimeControlResumeComplete struct {
	ServerConnectionID               string                    `json:"server_connection_id"`
	JournalRevision                  uint64                    `json:"journal_revision"`
	BackendRXCommittedWorkerSequence uint64                    `json:"backend_rx_committed_worker_sequence"`
	BackendRXAppliedWorkerSequence   uint64                    `json:"backend_rx_applied_worker_sequence"`
	BackendRXCursorDigest            string                    `json:"backend_rx_cursor_digest"`
	BackendTXLastCommandSequence     uint64                    `json:"backend_tx_last_command_sequence"`
	BackendTXWorkerAckedSequence     uint64                    `json:"backend_tx_worker_acked_sequence"`
	BackendTXWorkerAppliedSequence   uint64                    `json:"backend_tx_worker_applied_sequence"`
	BackendTXCursorDigest            string                    `json:"backend_tx_cursor_digest"`
	ActiveTurnDisposition            string                    `json:"active_turn_disposition"`
	AuthoritativeStart               *RuntimeControlActiveTurn `json:"authoritative_start"`
}

func DecodeRuntimeControlFrame(data []byte) (RuntimeControlFrame, error) {
	if err := requireRuntimeControlFrameFields(data); err != nil {
		return RuntimeControlFrame{}, err
	}
	return decodeStrict(data, RuntimeControlFrame.Validate)
}

func (frame RuntimeControlFrame) Validate() error {
	if frame.SchemaVersion != RuntimeControlFrameSchemaVersion {
		return fmt.Errorf("unsupported RuntimeControl frame schema %q", frame.SchemaVersion)
	}
	variants := 0
	if frame.ResumeHello != nil {
		variants++
	}
	if frame.ResumeAck != nil {
		variants++
	}
	if frame.ResumeReady != nil {
		variants++
	}
	if frame.ResumeComplete != nil {
		variants++
	}
	if frame.Envelope != nil {
		variants++
	}
	if variants != 1 {
		return fmt.Errorf("RuntimeControl frame must contain exactly one variant")
	}
	switch frame.FrameType {
	case RuntimeControlFrameTypeResumeHello:
		if frame.ResumeHello == nil {
			return fmt.Errorf("RuntimeControl resume_hello frame has the wrong variant")
		}
		return frame.ResumeHello.Validate()
	case RuntimeControlFrameTypeResumeAck:
		if frame.ResumeAck == nil {
			return fmt.Errorf("RuntimeControl resume_ack frame has the wrong variant")
		}
		return frame.ResumeAck.Validate()
	case RuntimeControlFrameTypeResumeReady:
		if frame.ResumeReady == nil {
			return fmt.Errorf("RuntimeControl resume_ready frame has the wrong variant")
		}
		return frame.ResumeReady.Validate()
	case RuntimeControlFrameTypeResumeComplete:
		if frame.ResumeComplete == nil {
			return fmt.Errorf("RuntimeControl resume_complete frame has the wrong variant")
		}
		return frame.ResumeComplete.Validate()
	case RuntimeControlFrameTypeEnvelope:
		if frame.Envelope == nil {
			return fmt.Errorf("RuntimeControl envelope frame has the wrong variant")
		}
		return frame.Envelope.Validate()
	default:
		return fmt.Errorf("unsupported RuntimeControl frame type %q", frame.FrameType)
	}
}

func (hello RuntimeControlResumeHello) Validate() error {
	for name, value := range map[string]string{
		"worker_session_id":   string(hello.WorkerSessionID),
		"runtime_instance_id": string(hello.RuntimeInstanceID),
		"shim_process_nonce":  hello.ShimProcessNonce,
	} {
		if err := requireRuntimeControlID(name, value); err != nil {
			return err
		}
	}
	if hello.RuntimeGeneration == 0 || hello.ChannelEpoch == 0 || hello.StreamAttempt == 0 {
		return fmt.Errorf("RuntimeControl resume identity and stream attempt must be positive")
	}
	if hello.WorkerTXBackendAckedSequence > hello.WorkerTXLastSequence {
		return fmt.Errorf("worker event ACK exceeds the sent cursor")
	}
	if hello.WorkerRXAppliedSequence > hello.WorkerRXLastSequence {
		return fmt.Errorf("worker command applied cursor exceeds the received cursor")
	}
	if err := requireSHA256("worker event cursor digest", hello.WorkerTXCursorDigest, true); err != nil {
		return err
	}
	if err := requireSHA256("worker command cursor digest", hello.WorkerRXCursorDigest, true); err != nil {
		return err
	}
	if hello.ActiveTurn != nil {
		return hello.ActiveTurn.Validate()
	}
	return nil
}

func (effect RuntimeControlActiveTurn) Validate() error {
	for name, value := range map[string]string{
		"worker_run_id":         string(effect.WorkerRunID),
		"agent_session_id":      string(effect.AgentSessionID),
		"agent_turn_id":         string(effect.AgentTurnID),
		"start_message_id":      effect.StartMessageID,
		"start_idempotency_key": effect.StartIdempotencyKey,
	} {
		if err := requireRuntimeControlID(name, value); err != nil {
			return err
		}
	}
	if effect.StartCommandSequence == 0 {
		return fmt.Errorf("start command sequence must be positive")
	}
	if err := requireSHA256("start envelope digest", effect.StartEnvelopeDigest, true); err != nil {
		return err
	}
	switch effect.Phase {
	case RuntimeControlTurnEffectReceived,
		RuntimeControlTurnEffectPromptIntent,
		RuntimeControlTurnEffectActive:
		if effect.TerminalEnvelopeDigest != "" {
			return fmt.Errorf("nonterminal active-Turn effect cannot carry terminal digest")
		}
	case RuntimeControlTurnEffectTerminal:
		if err := requireSHA256("terminal envelope digest", effect.TerminalEnvelopeDigest, true); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported active-Turn effect phase %q", effect.Phase)
	}
	return nil
}

func (ack RuntimeControlResumeAck) Validate() error {
	if !ack.Accepted {
		if !validRuntimeControlRejection(ack.RejectionCode) {
			return fmt.Errorf("RuntimeControl resume rejection code is invalid")
		}
		if ack.ServerConnectionID != "" || ack.JournalRevision != 0 ||
			ack.BackendRXCommittedWorkerSequence != 0 || ack.BackendRXAppliedWorkerSequence != 0 ||
			ack.BackendRXCursorDigest != "" ||
			ack.BackendTXLastCommandSequence != 0 || ack.BackendTXWorkerAckedSequence != 0 ||
			ack.BackendTXWorkerAppliedSequence != 0 ||
			ack.BackendTXCursorDigest != "" || ack.ReplayWorkerFromSequence != 0 ||
			ack.ReplayBackendFromSequence != 0 || ack.ActiveTurnDisposition != "" ||
			ack.AuthoritativeStart != nil {
			return fmt.Errorf("rejected RuntimeControl resume cannot carry accepted state")
		}
		return nil
	}
	if ack.RejectionCode != "" {
		return fmt.Errorf("accepted RuntimeControl resume cannot carry a rejection code")
	}
	if err := requireRuntimeControlID("server_connection_id", ack.ServerConnectionID); err != nil {
		return err
	}
	if ack.JournalRevision == 0 {
		return fmt.Errorf("accepted RuntimeControl resume requires journal revision")
	}
	if ack.BackendTXWorkerAckedSequence > ack.BackendTXLastCommandSequence {
		return fmt.Errorf("worker command ACK exceeds the command cursor")
	}
	if ack.BackendTXWorkerAppliedSequence > ack.BackendTXWorkerAckedSequence {
		return fmt.Errorf("worker command applied cursor exceeds its durable ACK")
	}
	if ack.BackendRXAppliedWorkerSequence > ack.BackendRXCommittedWorkerSequence {
		return fmt.Errorf("backend event applied cursor exceeds its committed cursor")
	}
	if err := requireSHA256("backend worker-event cursor digest", ack.BackendRXCursorDigest, true); err != nil {
		return err
	}
	if err := requireSHA256("backend command cursor digest", ack.BackendTXCursorDigest, true); err != nil {
		return err
	}
	if ack.BackendRXCommittedWorkerSequence == math.MaxUint64 ||
		ack.BackendTXWorkerAckedSequence == math.MaxUint64 {
		return fmt.Errorf("RuntimeControl resume sequence space is exhausted")
	}
	if ack.ReplayWorkerFromSequence != ack.BackendRXCommittedWorkerSequence+1 {
		return fmt.Errorf("worker replay cursor must follow the durably committed event cursor")
	}
	if ack.ReplayBackendFromSequence != ack.BackendTXWorkerAckedSequence+1 {
		return fmt.Errorf("backend replay cursor must follow the worker command ACK")
	}
	switch ack.ActiveTurnDisposition {
	case RuntimeControlResumeNoActiveTurn:
	case RuntimeControlResumeContinueActive, RuntimeControlResumeAlreadyTerminal:
	case RuntimeControlResumeContinuityUnproven:
	default:
		return fmt.Errorf("unsupported active-Turn resume disposition %q", ack.ActiveTurnDisposition)
	}
	return validateRuntimeControlDisposition(ack.ActiveTurnDisposition, ack.AuthoritativeStart)
}

func (ready RuntimeControlResumeReady) Validate() error {
	if err := requireRuntimeControlID("server_connection_id", ready.ServerConnectionID); err != nil {
		return err
	}
	if ready.AcknowledgedJournalRevision == 0 {
		return fmt.Errorf("RuntimeControl resume ready requires the acknowledged journal revision")
	}
	if ready.WorkerRXAppliedSequence > ready.WorkerRXReceivedThroughSequence {
		return fmt.Errorf("worker command applied cursor exceeds the received cursor")
	}
	if err := requireSHA256("worker event cursor digest", ready.WorkerTXCursorDigest, true); err != nil {
		return err
	}
	if err := requireSHA256("worker command cursor digest", ready.WorkerRXCursorDigest, true); err != nil {
		return err
	}
	if ready.ActiveTurn != nil {
		return ready.ActiveTurn.Validate()
	}
	return nil
}

func (complete RuntimeControlResumeComplete) Validate() error {
	if err := requireRuntimeControlID("server_connection_id", complete.ServerConnectionID); err != nil {
		return err
	}
	if complete.JournalRevision == 0 {
		return fmt.Errorf("RuntimeControl resume complete requires journal revision")
	}
	if complete.BackendRXAppliedWorkerSequence > complete.BackendRXCommittedWorkerSequence {
		return fmt.Errorf("backend event applied cursor exceeds its committed cursor")
	}
	if complete.BackendTXWorkerAckedSequence > complete.BackendTXLastCommandSequence {
		return fmt.Errorf("worker command ACK exceeds the command cursor")
	}
	if complete.BackendTXWorkerAppliedSequence > complete.BackendTXWorkerAckedSequence {
		return fmt.Errorf("worker command applied cursor exceeds its durable ACK")
	}
	if err := requireSHA256("backend worker-event cursor digest", complete.BackendRXCursorDigest, true); err != nil {
		return err
	}
	if err := requireSHA256("backend command cursor digest", complete.BackendTXCursorDigest, true); err != nil {
		return err
	}
	return validateRuntimeControlDisposition(
		complete.ActiveTurnDisposition,
		complete.AuthoritativeStart,
	)
}

func validateRuntimeControlDisposition(
	disposition string,
	start *RuntimeControlActiveTurn,
) error {
	if start != nil {
		if err := start.Validate(); err != nil {
			return err
		}
	}
	switch disposition {
	case RuntimeControlResumeNoActiveTurn:
		if start != nil {
			return fmt.Errorf("resume without active Turn cannot carry start authority")
		}
	case RuntimeControlResumeContinueActive:
		if start == nil || start.Phase != RuntimeControlTurnEffectActive {
			return fmt.Errorf("continue_active requires an active start effect")
		}
	case RuntimeControlResumeAlreadyTerminal:
		if start == nil || start.Phase != RuntimeControlTurnEffectTerminal {
			return fmt.Errorf("already_terminal requires a terminal start effect")
		}
	case RuntimeControlResumeContinuityUnproven:
		if start != nil &&
			start.Phase != RuntimeControlTurnEffectReceived &&
			start.Phase != RuntimeControlTurnEffectPromptIntent &&
			start.Phase != RuntimeControlTurnEffectActive {
			return fmt.Errorf("continuity_unproven cannot carry a terminal effect")
		}
	default:
		return fmt.Errorf("unsupported active-Turn resume disposition %q", disposition)
	}
	return nil
}

func validRuntimeControlRejection(code string) bool {
	switch code {
	case RuntimeControlResumeRejectedStaleRuntime,
		RuntimeControlResumeRejectedCursorConflict,
		RuntimeControlResumeRejectedTurnConflict,
		RuntimeControlResumeRejectedStaleAttempt,
		RuntimeControlResumeRejectedLeaseHeld,
		RuntimeControlResumeRejectedProtocol:
		return true
	default:
		return false
	}
}

func requireRuntimeControlFrameFields(data []byte) error {
	frame, err := requireRuntimeControlObjectFields(
		data,
		"RuntimeControl frame",
		[]string{
			"schema_version",
			"frame_type",
			"resume_hello",
			"resume_ack",
			"resume_ready",
			"resume_complete",
			"envelope",
		},
	)
	if err != nil {
		return err
	}
	var frameType string
	if err := json.Unmarshal(frame["frame_type"], &frameType); err != nil {
		return fmt.Errorf("RuntimeControl frame_type must be a string")
	}
	switch frameType {
	case RuntimeControlFrameTypeResumeHello:
		hello, err := requireRuntimeControlObjectFields(
			frame["resume_hello"],
			"RuntimeControl resume hello",
			[]string{
				"worker_session_id",
				"runtime_instance_id",
				"runtime_generation",
				"channel_epoch",
				"shim_process_nonce",
				"stream_attempt",
				"worker_tx_last_sequence",
				"worker_tx_backend_acked_sequence",
				"worker_tx_cursor_digest",
				"worker_rx_last_sequence",
				"worker_rx_applied_sequence",
				"worker_rx_cursor_digest",
				"active_turn",
			},
		)
		if err != nil {
			return err
		}
		return requireRuntimeControlOptionalActiveTurn(hello["active_turn"])
	case RuntimeControlFrameTypeResumeAck:
		ack, err := requireRuntimeControlObjectFields(
			frame["resume_ack"],
			"RuntimeControl resume ack",
			[]string{
				"accepted",
				"rejection_code",
				"server_connection_id",
				"journal_revision",
				"backend_rx_committed_worker_sequence",
				"backend_rx_applied_worker_sequence",
				"backend_rx_cursor_digest",
				"backend_tx_last_command_sequence",
				"backend_tx_worker_acked_sequence",
				"backend_tx_worker_applied_sequence",
				"backend_tx_cursor_digest",
				"replay_worker_from_sequence",
				"replay_backend_from_sequence",
				"active_turn_disposition",
				"authoritative_start",
			},
		)
		if err != nil {
			return err
		}
		return requireRuntimeControlOptionalActiveTurn(ack["authoritative_start"])
	case RuntimeControlFrameTypeResumeReady:
		ready, err := requireRuntimeControlObjectFields(
			frame["resume_ready"],
			"RuntimeControl resume ready",
			[]string{
				"server_connection_id",
				"acknowledged_journal_revision",
				"worker_tx_replayed_through_sequence",
				"worker_tx_cursor_digest",
				"worker_rx_received_through_sequence",
				"worker_rx_applied_sequence",
				"worker_rx_cursor_digest",
				"active_turn",
			},
		)
		if err != nil {
			return err
		}
		return requireRuntimeControlOptionalActiveTurn(ready["active_turn"])
	case RuntimeControlFrameTypeResumeComplete:
		complete, err := requireRuntimeControlObjectFields(
			frame["resume_complete"],
			"RuntimeControl resume complete",
			[]string{
				"server_connection_id",
				"journal_revision",
				"backend_rx_committed_worker_sequence",
				"backend_rx_applied_worker_sequence",
				"backend_rx_cursor_digest",
				"backend_tx_last_command_sequence",
				"backend_tx_worker_acked_sequence",
				"backend_tx_worker_applied_sequence",
				"backend_tx_cursor_digest",
				"active_turn_disposition",
				"authoritative_start",
			},
		)
		if err != nil {
			return err
		}
		return requireRuntimeControlOptionalActiveTurn(complete["authoritative_start"])
	case RuntimeControlFrameTypeEnvelope:
		return requireRuntimeControlEnvelopeFields(frame["envelope"])
	default:
		// Typed validation reports the stable unsupported-type error.
		return nil
	}
}

func requireRuntimeControlOptionalActiveTurn(raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	_, err := requireRuntimeControlObjectFields(
		raw,
		"RuntimeControl active Turn",
		[]string{
			"worker_run_id",
			"agent_session_id",
			"agent_turn_id",
			"start_command_sequence",
			"start_message_id",
			"start_idempotency_key",
			"start_envelope_digest",
			"phase",
			"last_update_sequence",
			"terminal_envelope_digest",
		},
	)
	return err
}

func requireRuntimeControlEnvelopeFields(data []byte) error {
	_, err := requireRuntimeControlObjectFields(
		data,
		"RuntimeControl envelope",
		[]string{
			"schema_version",
			"message_id",
			"kind",
			"worker_session_id",
			"worker_run_id",
			"runtime_instance_id",
			"runtime_generation",
			"agent_session_id",
			"agent_turn_id",
			"channel_epoch",
			"sequence",
			"ack_sequence",
			"idempotency_key",
			"message_type",
			"payload",
			"sent_at",
		},
	)
	return err
}

func requireRuntimeControlObjectFields(
	data []byte,
	name string,
	required []string,
) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", name)
	}
	for _, field := range required {
		if _, ok := object[field]; !ok {
			return nil, fmt.Errorf("%s field %q is required", name, field)
		}
	}
	return object, nil
}

func requireRuntimeControlID(name, value string) error {
	if len(value) == 0 || len(value) > 160 || !isRuntimeControlAlphaNumeric(value[0]) {
		return fmt.Errorf("%s contains invalid identity characters", name)
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !isRuntimeControlAlphaNumeric(character) &&
			character != '.' && character != '_' && character != '-' {
			return fmt.Errorf("%s contains invalid identity characters", name)
		}
	}
	return nil
}

func isRuntimeControlAlphaNumeric(character byte) bool {
	return character >= 'A' && character <= 'Z' ||
		character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9'
}

type RuntimeControlEnvelope struct {
	SchemaVersion     string            `json:"schema_version"`
	MessageID         string            `json:"message_id"`
	Kind              string            `json:"kind"`
	WorkerSessionID   WorkerSessionID   `json:"worker_session_id"`
	WorkerRunID       WorkerRunID       `json:"worker_run_id"`
	RuntimeInstanceID RuntimeInstanceID `json:"runtime_instance_id"`
	RuntimeGeneration uint32            `json:"runtime_generation"`
	AgentSessionID    AgentSessionID    `json:"agent_session_id"`
	AgentTurnID       AgentTurnID       `json:"agent_turn_id"`
	ChannelEpoch      uint64            `json:"channel_epoch"`
	Sequence          uint64            `json:"sequence"`
	AckSequence       uint64            `json:"ack_sequence"`
	IdempotencyKey    string            `json:"idempotency_key"`
	MessageType       string            `json:"message_type"`
	Payload           json.RawMessage   `json:"payload"`
	SentAt            time.Time         `json:"sent_at"`
}

func DecodeRuntimeControlEnvelope(data []byte) (RuntimeControlEnvelope, error) {
	if err := requireRuntimeControlEnvelopeFields(data); err != nil {
		return RuntimeControlEnvelope{}, err
	}
	return decodeStrict(data, RuntimeControlEnvelope.Validate)
}

func (envelope RuntimeControlEnvelope) Validate() error {
	if envelope.SchemaVersion != RuntimeControlSchemaVersion {
		return fmt.Errorf("unsupported RuntimeControl schema %q", envelope.SchemaVersion)
	}
	if envelope.Kind != "command" && envelope.Kind != "event" && envelope.Kind != "ack" && envelope.Kind != "heartbeat" {
		return fmt.Errorf("unsupported RuntimeControl kind %q", envelope.Kind)
	}
	for name, value := range map[string]string{
		"message_id": envelope.MessageID, "idempotency_key": envelope.IdempotencyKey,
		"message_type": envelope.MessageType,
	} {
		if err := requireRuntimeControlID(name, value); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{
		"worker_session_id": string(envelope.WorkerSessionID), "worker_run_id": string(envelope.WorkerRunID),
		"runtime_instance_id": string(envelope.RuntimeInstanceID), "agent_session_id": string(envelope.AgentSessionID),
		"agent_turn_id": string(envelope.AgentTurnID),
	} {
		if err := requireRuntimeControlID(name, value); err != nil {
			return err
		}
	}
	if envelope.RuntimeGeneration == 0 || envelope.ChannelEpoch == 0 || envelope.Sequence == 0 {
		return fmt.Errorf("runtime generation, channel epoch, and sequence must be positive")
	}
	if err := requireJSONObject("payload", envelope.Payload); err != nil {
		return err
	}
	if envelope.SentAt.IsZero() {
		return fmt.Errorf("sent_at is required")
	}
	return nil
}

// RuntimeControlEnvelopeDigest hashes a canonical, length-prefixed encoding of
// every envelope field. JSON objects are decoded with json.Number and encoded
// without insignificant whitespace; Go's encoding/json lexical map-key order
// is the v1 canonical object-key order. The digest deliberately includes
// sequence, cumulative ACK, payload and sent_at so replay must retain the exact
// committed logical frame.
func RuntimeControlEnvelopeDigest(envelope RuntimeControlEnvelope) (string, error) {
	if err := envelope.Validate(); err != nil {
		return "", err
	}
	payload, err := canonicalRuntimeControlJSON(envelope.Payload)
	if err != nil {
		return "", fmt.Errorf("canonicalize RuntimeControl payload: %w", err)
	}
	digest := sha256.New()
	writeRuntimeControlField(digest, []byte("switchback.runtime-control.envelope.v1"))
	for _, value := range []string{
		envelope.SchemaVersion,
		envelope.MessageID,
		envelope.Kind,
		string(envelope.WorkerSessionID),
		string(envelope.WorkerRunID),
		string(envelope.RuntimeInstanceID),
		string(envelope.AgentSessionID),
		string(envelope.AgentTurnID),
		envelope.IdempotencyKey,
		envelope.MessageType,
		envelope.SentAt.UTC().Format(time.RFC3339Nano),
	} {
		writeRuntimeControlField(digest, []byte(value))
	}
	for _, value := range []uint64{
		uint64(envelope.RuntimeGeneration),
		envelope.ChannelEpoch,
		envelope.Sequence,
		envelope.AckSequence,
	} {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], value)
		writeRuntimeControlField(digest, encoded[:])
	}
	writeRuntimeControlField(digest, payload)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// AdvanceRuntimeControlCursor extends the versioned cursor hash chain with one
// exact envelope digest. The zero-message cursor is
// RuntimeControlCursorGenesisDigest. Direction is domain separated.
func AdvanceRuntimeControlCursor(
	previous string,
	direction string,
	envelope RuntimeControlEnvelope,
) (string, error) {
	if err := requireSHA256("previous RuntimeControl cursor digest", previous, true); err != nil {
		return "", err
	}
	if direction != RuntimeControlDirectionBackendToRuntime &&
		direction != RuntimeControlDirectionRuntimeToBackend {
		return "", fmt.Errorf("RuntimeControl cursor direction %q is invalid", direction)
	}
	envelopeDigest, err := RuntimeControlEnvelopeDigest(envelope)
	if err != nil {
		return "", err
	}
	previousBytes, _ := hex.DecodeString(strings.TrimPrefix(previous, "sha256:"))
	envelopeBytes, _ := hex.DecodeString(strings.TrimPrefix(envelopeDigest, "sha256:"))
	digest := sha256.New()
	writeRuntimeControlField(digest, []byte("switchback.runtime-control.cursor.v1"))
	writeRuntimeControlField(digest, []byte(direction))
	writeRuntimeControlField(digest, previousBytes)
	var sequence [8]byte
	binary.BigEndian.PutUint64(sequence[:], envelope.Sequence)
	writeRuntimeControlField(digest, sequence[:])
	writeRuntimeControlField(digest, envelopeBytes)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func writeRuntimeControlField(target hash.Hash, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = target.Write(size[:])
	_, _ = target.Write(value)
}

func canonicalRuntimeControlJSON(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("payload contains trailing JSON")
	}
	return json.Marshal(value)
}
