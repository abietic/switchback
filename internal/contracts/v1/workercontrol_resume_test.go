package contractsv1

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestRuntimeControlFrameValidatesResumeHelloAckAndEnvelopeVariants(t *testing.T) {
	hello := validRuntimeControlResumeHello()
	ack := validRuntimeControlResumeAck()
	ready := validRuntimeControlResumeReady()
	complete := validRuntimeControlResumeComplete()
	envelope := validRuntimeControlResumeEnvelope()

	tests := []struct {
		name  string
		frame RuntimeControlFrame
	}{
		{
			name: "resume hello",
			frame: RuntimeControlFrame{
				SchemaVersion: RuntimeControlFrameSchemaVersion,
				FrameType:     RuntimeControlFrameTypeResumeHello,
				ResumeHello:   &hello,
			},
		},
		{
			name: "resume ack",
			frame: RuntimeControlFrame{
				SchemaVersion: RuntimeControlFrameSchemaVersion,
				FrameType:     RuntimeControlFrameTypeResumeAck,
				ResumeAck:     &ack,
			},
		},
		{
			name: "envelope",
			frame: RuntimeControlFrame{
				SchemaVersion: RuntimeControlFrameSchemaVersion,
				FrameType:     RuntimeControlFrameTypeEnvelope,
				Envelope:      &envelope,
			},
		},
		{
			name: "resume ready",
			frame: RuntimeControlFrame{
				SchemaVersion: RuntimeControlFrameSchemaVersion,
				FrameType:     RuntimeControlFrameTypeResumeReady,
				ResumeReady:   &ready,
			},
		},
		{
			name: "resume complete",
			frame: RuntimeControlFrame{
				SchemaVersion:  RuntimeControlFrameSchemaVersion,
				FrameType:      RuntimeControlFrameTypeResumeComplete,
				ResumeComplete: &complete,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.frame.Validate(); err != nil {
				t.Fatalf("valid frame rejected: %v", err)
			}
			encoded, err := json.Marshal(test.frame)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeRuntimeControlFrame(encoded)
			if err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			if decoded.FrameType != test.frame.FrameType {
				t.Fatalf("frame type=%q, want %q", decoded.FrameType, test.frame.FrameType)
			}
		})
	}
}

func TestRuntimeControlFrameRejectsMissingMultipleAndMismatchedVariants(t *testing.T) {
	hello := validRuntimeControlResumeHello()
	ack := validRuntimeControlResumeAck()
	envelope := validRuntimeControlResumeEnvelope()
	tests := []struct {
		name  string
		frame RuntimeControlFrame
	}{
		{
			name: "missing variant",
			frame: RuntimeControlFrame{
				SchemaVersion: RuntimeControlFrameSchemaVersion,
				FrameType:     RuntimeControlFrameTypeResumeHello,
			},
		},
		{
			name: "multiple variants",
			frame: RuntimeControlFrame{
				SchemaVersion: RuntimeControlFrameSchemaVersion,
				FrameType:     RuntimeControlFrameTypeResumeHello,
				ResumeHello:   &hello,
				ResumeAck:     &ack,
			},
		},
		{
			name: "mismatched variant",
			frame: RuntimeControlFrame{
				SchemaVersion: RuntimeControlFrameSchemaVersion,
				FrameType:     RuntimeControlFrameTypeEnvelope,
				ResumeHello:   &hello,
			},
		},
		{
			name: "invalid nested envelope",
			frame: RuntimeControlFrame{
				SchemaVersion: RuntimeControlFrameSchemaVersion,
				FrameType:     RuntimeControlFrameTypeEnvelope,
				Envelope: func() *RuntimeControlEnvelope {
					envelope.Sequence = 0
					return &envelope
				}(),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.frame.Validate(); err == nil {
				t.Fatal("invalid frame was accepted")
			}
		})
	}
}

func TestRuntimeControlResumeHelloRejectsInvalidIdentityCursorAndActiveEffect(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RuntimeControlResumeHello)
	}{
		{name: "missing process nonce", mutate: func(value *RuntimeControlResumeHello) {
			value.ShimProcessNonce = ""
		}},
		{name: "zero stream attempt", mutate: func(value *RuntimeControlResumeHello) {
			value.StreamAttempt = 0
		}},
		{name: "event ack above sent", mutate: func(value *RuntimeControlResumeHello) {
			value.WorkerTXBackendAckedSequence = value.WorkerTXLastSequence + 1
		}},
		{name: "command applied above received", mutate: func(value *RuntimeControlResumeHello) {
			value.WorkerRXAppliedSequence = value.WorkerRXLastSequence + 1
		}},
		{name: "invalid event cursor digest", mutate: func(value *RuntimeControlResumeHello) {
			value.WorkerTXCursorDigest = "sha256:not-a-digest"
		}},
		{name: "invalid active phase", mutate: func(value *RuntimeControlResumeHello) {
			value.ActiveTurn.Phase = "prompt_maybe_started"
		}},
		{name: "noncanonical runtime identity", mutate: func(value *RuntimeControlResumeHello) {
			value.RuntimeInstanceID = RuntimeInstanceID("runtime:instance")
		}},
		{name: "terminal phase without terminal digest", mutate: func(value *RuntimeControlResumeHello) {
			value.ActiveTurn.Phase = RuntimeControlTurnEffectTerminal
			value.ActiveTurn.TerminalEnvelopeDigest = ""
		}},
		{name: "nonterminal phase with terminal digest", mutate: func(value *RuntimeControlResumeHello) {
			value.ActiveTurn.TerminalEnvelopeDigest = controlTestDigest("unexpected-terminal")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validRuntimeControlResumeHello()
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid resume hello was accepted")
			}
		})
	}
}

func TestRuntimeControlResumeAckRequiresEitherAcceptedStateOrBoundedRejection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RuntimeControlResumeAck)
	}{
		{name: "accepted with rejection code", mutate: func(value *RuntimeControlResumeAck) {
			value.RejectionCode = "cursor_conflict"
		}},
		{name: "accepted without connection identity", mutate: func(value *RuntimeControlResumeAck) {
			value.ServerConnectionID = ""
		}},
		{name: "accepted ack above command high water", mutate: func(value *RuntimeControlResumeAck) {
			value.BackendTXWorkerAckedSequence = value.BackendTXLastCommandSequence + 1
		}},
		{name: "accepted applied command above ack", mutate: func(value *RuntimeControlResumeAck) {
			value.BackendTXWorkerAppliedSequence = value.BackendTXWorkerAckedSequence + 1
		}},
		{name: "accepted applied event above committed", mutate: func(value *RuntimeControlResumeAck) {
			value.BackendRXAppliedWorkerSequence = value.BackendRXCommittedWorkerSequence + 1
		}},
		{name: "accepted worker replay does not follow committed", mutate: func(value *RuntimeControlResumeAck) {
			value.ReplayWorkerFromSequence++
		}},
		{name: "accepted backend replay does not follow ack", mutate: func(value *RuntimeControlResumeAck) {
			value.ReplayBackendFromSequence++
		}},
		{name: "accepted exhausted worker event cursor", mutate: func(value *RuntimeControlResumeAck) {
			value.BackendRXCommittedWorkerSequence = math.MaxUint64
			value.ReplayWorkerFromSequence = 0
		}},
		{name: "accepted exhausted backend command cursor", mutate: func(value *RuntimeControlResumeAck) {
			value.BackendTXLastCommandSequence = math.MaxUint64
			value.BackendTXWorkerAckedSequence = math.MaxUint64
			value.BackendTXWorkerAppliedSequence = math.MaxUint64
			value.ReplayBackendFromSequence = 0
		}},
		{name: "accepted invalid disposition", mutate: func(value *RuntimeControlResumeAck) {
			value.ActiveTurnDisposition = "retry_prompt"
		}},
		{name: "continue active with terminal effect", mutate: func(value *RuntimeControlResumeAck) {
			value.AuthoritativeStart.Phase = RuntimeControlTurnEffectTerminal
			value.AuthoritativeStart.TerminalEnvelopeDigest = controlTestDigest("terminal")
		}},
		{name: "already terminal with active effect", mutate: func(value *RuntimeControlResumeAck) {
			value.ActiveTurnDisposition = RuntimeControlResumeAlreadyTerminal
		}},
		{name: "continuity unproven with terminal effect", mutate: func(value *RuntimeControlResumeAck) {
			value.ActiveTurnDisposition = RuntimeControlResumeContinuityUnproven
			value.AuthoritativeStart.Phase = RuntimeControlTurnEffectTerminal
			value.AuthoritativeStart.TerminalEnvelopeDigest = controlTestDigest("terminal")
		}},
		{name: "rejected without code", mutate: func(value *RuntimeControlResumeAck) {
			*value = RuntimeControlResumeAck{Accepted: false}
		}},
		{name: "rejected leaks accepted state", mutate: func(value *RuntimeControlResumeAck) {
			value.Accepted = false
			value.RejectionCode = "cursor_conflict"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validRuntimeControlResumeAck()
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid resume ack was accepted")
			}
		})
	}

	rejected := RuntimeControlResumeAck{
		Accepted:      false,
		RejectionCode: RuntimeControlResumeRejectedCursorConflict,
	}
	if err := rejected.Validate(); err != nil {
		t.Fatalf("bounded rejection was rejected: %v", err)
	}
	if err := (RuntimeControlResumeAck{
		Accepted:      false,
		RejectionCode: "unauthenticated",
	}).Validate(); err == nil {
		t.Fatal("transport authentication failure was accepted as an application resume rejection")
	}
}

func TestRuntimeControlResumeAckAllowsActiveAuthorityForPendingTerminalReplay(t *testing.T) {
	value := validRuntimeControlResumeAck()
	value.ActiveTurnDisposition = RuntimeControlResumeContinuityUnproven
	if err := value.Validate(); err != nil {
		t.Fatalf("pending terminal replay authority was rejected: %v", err)
	}
}

func TestDecodeRuntimeControlFrameRejectsUnknownFields(t *testing.T) {
	hello := validRuntimeControlResumeHello()
	frame := RuntimeControlFrame{
		SchemaVersion: RuntimeControlFrameSchemaVersion,
		FrameType:     RuntimeControlFrameTypeResumeHello,
		ResumeHello:   &hello,
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	encoded = []byte(strings.TrimSuffix(string(encoded), "}") + `,"client_override":"forbidden"}`)
	if _, err := DecodeRuntimeControlFrame(encoded); err == nil {
		t.Fatal("unknown frame field was accepted")
	}
}

func TestDecodeRuntimeControlFrameRejectsOmittedRequiredFields(t *testing.T) {
	hello := validRuntimeControlResumeHello()
	frame := RuntimeControlFrame{
		SchemaVersion: RuntimeControlFrameSchemaVersion,
		FrameType:     RuntimeControlFrameTypeResumeHello,
		ResumeHello:   &hello,
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "resume_ack")
	encoded, _ = json.Marshal(document)
	if _, err := DecodeRuntimeControlFrame(encoded); err == nil {
		t.Fatal("omitted top-level variant field was accepted")
	}

	document = map[string]any{
		"schema_version": RuntimeControlFrameSchemaVersion,
		"frame_type":     RuntimeControlFrameTypeResumeAck,
		"resume_hello":   nil,
		"resume_ack": map[string]any{
			"accepted":       false,
			"rejection_code": RuntimeControlResumeRejectedCursorConflict,
		},
		"resume_ready":    nil,
		"resume_complete": nil,
		"envelope":        nil,
	}
	encoded, _ = json.Marshal(document)
	if _, err := DecodeRuntimeControlFrame(encoded); err == nil {
		t.Fatal("rejected ack with omitted zero-valued fields was accepted")
	}
}

func TestDecodeRuntimeControlEnvelopeRejectsOmittedZeroValuedAckSequence(t *testing.T) {
	envelope := validRuntimeControlResumeEnvelope()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "ack_sequence")
	encoded, _ = json.Marshal(document)
	if _, err := DecodeRuntimeControlEnvelope(encoded); err == nil {
		t.Fatal("omitted zero-valued ack_sequence was accepted")
	}
}

func TestRuntimeControlCursorDigestUsesVersionedHashChain(t *testing.T) {
	if RuntimeControlCursorGenesisDigest !=
		"sha256:4f4de1293c07ebd69a11800878dd689311974c66f87a0582375b21c72bb787af" {
		t.Fatalf("genesis digest=%q", RuntimeControlCursorGenesisDigest)
	}
	envelope := validRuntimeControlResumeEnvelope()
	first, err := AdvanceRuntimeControlCursor(
		RuntimeControlCursorGenesisDigest,
		RuntimeControlDirectionRuntimeToBackend,
		envelope,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first == RuntimeControlCursorGenesisDigest {
		t.Fatal("cursor did not advance")
	}
	if first != "sha256:72702d4973fee2591f8599be418475891600ce03c072c2879a34a1a74bb76d93" {
		t.Fatalf("first cursor digest=%q", first)
	}
	duplicate, err := AdvanceRuntimeControlCursor(
		RuntimeControlCursorGenesisDigest,
		RuntimeControlDirectionRuntimeToBackend,
		envelope,
	)
	if err != nil || duplicate != first {
		t.Fatalf("deterministic cursor=%q error=%v, want %q", duplicate, err, first)
	}
	otherDirection, err := AdvanceRuntimeControlCursor(
		RuntimeControlCursorGenesisDigest,
		RuntimeControlDirectionBackendToRuntime,
		envelope,
	)
	if err != nil {
		t.Fatal(err)
	}
	if otherDirection == first {
		t.Fatal("cursor digest does not domain-separate direction")
	}
	envelope.MessageID = "runtime-event-2"
	changed, err := AdvanceRuntimeControlCursor(
		RuntimeControlCursorGenesisDigest,
		RuntimeControlDirectionRuntimeToBackend,
		envelope,
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("cursor digest ignored envelope identity")
	}
}

func TestRuntimeControlFrameJSONSchemaValidatesDeclaredWireSemantics(t *testing.T) {
	schema := compileRuntimeControlFrameSchema(t)
	assertValid := func(t *testing.T, frame RuntimeControlFrame) {
		t.Helper()
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
		if err != nil {
			t.Fatalf("decode JSON instance: %v", err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("schema rejected valid frame: %v", err)
		}
	}
	assertInvalid := func(t *testing.T, frame RuntimeControlFrame) {
		t.Helper()
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
		if err != nil {
			t.Fatalf("decode JSON instance: %v", err)
		}
		if err := schema.Validate(instance); err == nil {
			t.Fatal("schema accepted invalid frame")
		}
	}

	t.Run("valid resume ack", func(t *testing.T) {
		ack := validRuntimeControlResumeAck()
		assertValid(t, RuntimeControlFrame{
			SchemaVersion: RuntimeControlFrameSchemaVersion,
			FrameType:     RuntimeControlFrameTypeResumeAck,
			ResumeAck:     &ack,
		})
	})
	t.Run("valid pending terminal replay authority", func(t *testing.T) {
		ack := validRuntimeControlResumeAck()
		ack.ActiveTurnDisposition = RuntimeControlResumeContinuityUnproven
		assertValid(t, RuntimeControlFrame{
			SchemaVersion: RuntimeControlFrameSchemaVersion,
			FrameType:     RuntimeControlFrameTypeResumeAck,
			ResumeAck:     &ack,
		})
	})
	t.Run("disposition and active phase disagree", func(t *testing.T) {
		ack := validRuntimeControlResumeAck()
		ack.AuthoritativeStart.Phase = RuntimeControlTurnEffectTerminal
		ack.AuthoritativeStart.TerminalEnvelopeDigest = controlTestDigest("terminal")
		assertInvalid(t, RuntimeControlFrame{
			SchemaVersion: RuntimeControlFrameSchemaVersion,
			FrameType:     RuntimeControlFrameTypeResumeAck,
			ResumeAck:     &ack,
		})
	})
	t.Run("runtime identity is noncanonical", func(t *testing.T) {
		hello := validRuntimeControlResumeHello()
		hello.RuntimeInstanceID = RuntimeInstanceID("runtime:instance")
		assertInvalid(t, RuntimeControlFrame{
			SchemaVersion: RuntimeControlFrameSchemaVersion,
			FrameType:     RuntimeControlFrameTypeResumeHello,
			ResumeHello:   &hello,
		})
	})
}

func compileRuntimeControlFrameSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{
		"runtime-control-envelope.schema.json",
		"runtime-control-frame.schema.json",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "schema", "v1", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		location := "https://switchback.local/schema/v1/" + name
		if err := compiler.AddResource(location, document); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	schema, err := compiler.Compile(
		"https://switchback.local/schema/v1/runtime-control-frame.schema.json",
	)
	if err != nil {
		t.Fatalf("compile RuntimeControl frame schema: %v", err)
	}
	return schema
}

func validRuntimeControlResumeHello() RuntimeControlResumeHello {
	return RuntimeControlResumeHello{
		WorkerSessionID:              WorkerSessionID("worker-session-1"),
		RuntimeInstanceID:            RuntimeInstanceID("runtime-instance-1"),
		RuntimeGeneration:            1,
		ChannelEpoch:                 7,
		ShimProcessNonce:             "shim-process-1",
		StreamAttempt:                2,
		WorkerTXLastSequence:         5,
		WorkerTXBackendAckedSequence: 4,
		WorkerTXCursorDigest:         controlTestDigest("worker-tx"),
		WorkerRXLastSequence:         3,
		WorkerRXAppliedSequence:      3,
		WorkerRXCursorDigest:         controlTestDigest("worker-rx"),
		ActiveTurn: &RuntimeControlActiveTurn{
			WorkerRunID:          WorkerRunID("worker-run-1"),
			AgentSessionID:       AgentSessionID("agent-session-1"),
			AgentTurnID:          AgentTurnID("agent-turn-1"),
			StartCommandSequence: 3,
			StartMessageID:       "start-agent-turn-1",
			StartIdempotencyKey:  "start-agent-turn-1",
			StartEnvelopeDigest:  controlTestStartEnvelopeDigest(),
			Phase:                RuntimeControlTurnEffectActive,
			LastUpdateSequence:   8,
		},
	}
}

func validRuntimeControlResumeAck() RuntimeControlResumeAck {
	active := validRuntimeControlResumeHello().ActiveTurn
	return RuntimeControlResumeAck{
		Accepted:                         true,
		ServerConnectionID:               "backend-connection-2",
		JournalRevision:                  11,
		BackendRXCommittedWorkerSequence: 5,
		BackendRXAppliedWorkerSequence:   4,
		BackendRXCursorDigest:            controlTestDigest("worker-tx"),
		BackendTXLastCommandSequence:     3,
		BackendTXWorkerAckedSequence:     3,
		BackendTXWorkerAppliedSequence:   3,
		BackendTXCursorDigest:            controlTestDigest("worker-rx"),
		ReplayWorkerFromSequence:         6,
		ReplayBackendFromSequence:        4,
		ActiveTurnDisposition:            RuntimeControlResumeContinueActive,
		AuthoritativeStart:               active,
	}
}

func validRuntimeControlResumeReady() RuntimeControlResumeReady {
	hello := validRuntimeControlResumeHello()
	return RuntimeControlResumeReady{
		ServerConnectionID:              "backend-connection-2",
		AcknowledgedJournalRevision:     11,
		WorkerTXReplayedThroughSequence: hello.WorkerTXLastSequence,
		WorkerTXCursorDigest:            hello.WorkerTXCursorDigest,
		WorkerRXReceivedThroughSequence: hello.WorkerRXLastSequence,
		WorkerRXAppliedSequence:         hello.WorkerRXAppliedSequence,
		WorkerRXCursorDigest:            hello.WorkerRXCursorDigest,
		ActiveTurn:                      hello.ActiveTurn,
	}
}

func validRuntimeControlResumeComplete() RuntimeControlResumeComplete {
	ack := validRuntimeControlResumeAck()
	return RuntimeControlResumeComplete{
		ServerConnectionID:               ack.ServerConnectionID,
		JournalRevision:                  ack.JournalRevision + 1,
		BackendRXCommittedWorkerSequence: ack.BackendRXCommittedWorkerSequence,
		BackendRXAppliedWorkerSequence:   ack.BackendRXAppliedWorkerSequence,
		BackendRXCursorDigest:            ack.BackendRXCursorDigest,
		BackendTXLastCommandSequence:     ack.BackendTXLastCommandSequence,
		BackendTXWorkerAckedSequence:     ack.BackendTXWorkerAckedSequence,
		BackendTXWorkerAppliedSequence:   ack.BackendTXWorkerAppliedSequence,
		BackendTXCursorDigest:            ack.BackendTXCursorDigest,
		ActiveTurnDisposition:            ack.ActiveTurnDisposition,
		AuthoritativeStart:               ack.AuthoritativeStart,
	}
}

func validRuntimeControlResumeEnvelope() RuntimeControlEnvelope {
	return RuntimeControlEnvelope{
		SchemaVersion:     RuntimeControlSchemaVersion,
		MessageID:         "runtime-event-1",
		Kind:              "event",
		WorkerSessionID:   WorkerSessionID("worker-session-1"),
		WorkerRunID:       WorkerRunID("worker-run-1"),
		RuntimeInstanceID: RuntimeInstanceID("runtime-instance-1"),
		RuntimeGeneration: 1,
		AgentSessionID:    AgentSessionID("agent-session-1"),
		AgentTurnID:       AgentTurnID("agent-turn-1"),
		ChannelEpoch:      7,
		Sequence:          1,
		AckSequence:       0,
		IdempotencyKey:    "runtime-event-1",
		MessageType:       "runtime.ready",
		Payload:           json.RawMessage(`{"ready":true}`),
		SentAt:            controlTestTime(),
	}
}

func controlTestDigest(seed string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(seed)))
}

func controlTestStartEnvelopeDigest() string {
	envelope := validRuntimeControlResumeEnvelope()
	envelope.Kind = "command"
	envelope.MessageID = "start-agent-turn-1"
	envelope.IdempotencyKey = "start-agent-turn-1"
	envelope.MessageType = "agent_turn.start"
	envelope.Sequence = 3
	envelope.Payload = json.RawMessage(`{"prompt":"test"}`)
	digest, err := RuntimeControlEnvelopeDigest(envelope)
	if err != nil {
		panic(err)
	}
	return digest
}

func controlTestTime() time.Time {
	return time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
}
