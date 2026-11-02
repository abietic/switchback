package control

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
)

func TestValidateEnvelope(t *testing.T) {
	validMeta := func() *workercontrolv1.EnvelopeMeta {
		return &workercontrolv1.EnvelopeMeta{
			MessageId:      "message-1",
			StreamSequence: 1,
			SentAt:         timestamppb.Now(),
			Identity: &workercontrolv1.Identity{
				WorkerSessionId:   "session-1",
				WorkerRunId:       "run-1",
				RuntimeInstanceId: "runtime-1",
				RuntimeGeneration: 1,
			},
		}
	}

	tests := []struct {
		name        string
		meta        *workercontrolv1.EnvelopeMeta
		payloadKind PayloadKind
		cursor      Cursor
		wantErr     bool
	}{
		{
			name:        "missing identity",
			meta:        &workercontrolv1.EnvelopeMeta{MessageId: "message-1", StreamSequence: 1},
			payloadKind: PayloadKindRuntimeReady,
			wantErr:     true,
		},
		{
			name: "missing sent at",
			meta: func() *workercontrolv1.EnvelopeMeta {
				meta := validMeta()
				meta.SentAt = nil
				return meta
			}(),
			payloadKind: PayloadKindRuntimeReady,
			wantErr:     true,
		},
		{
			name: "invalid sent at",
			meta: func() *workercontrolv1.EnvelopeMeta {
				meta := validMeta()
				meta.SentAt = &timestamppb.Timestamp{Seconds: 253402300800}
				return meta
			}(),
			payloadKind: PayloadKindRuntimeReady,
			wantErr:     true,
		},
		{
			name: "generation zero",
			meta: func() *workercontrolv1.EnvelopeMeta {
				meta := validMeta()
				meta.Identity.RuntimeGeneration = 0
				return meta
			}(),
			payloadKind: PayloadKindRuntimeReady,
			wantErr:     true,
		},
		{
			name: "generation two",
			meta: func() *workercontrolv1.EnvelopeMeta {
				meta := validMeta()
				meta.Identity.RuntimeGeneration = 2
				return meta
			}(),
			payloadKind: PayloadKindRuntimeReady,
			wantErr:     true,
		},
		{
			name:        "turn command without agent turn ID",
			meta:        validMeta(),
			payloadKind: PayloadKindStartAgentTurn,
			wantErr:     true,
		},
		{
			name:        "duplicate stream sequence",
			meta:        validMeta(),
			payloadKind: PayloadKindRuntimeReady,
			cursor:      Cursor{LastInboundSequence: 1},
			wantErr:     true,
		},
		{
			name: "lower stream sequence",
			meta: func() *workercontrolv1.EnvelopeMeta {
				meta := validMeta()
				meta.StreamSequence = 4
				return meta
			}(),
			payloadKind: PayloadKindRuntimeReady,
			cursor:      Cursor{LastInboundSequence: 5},
			wantErr:     true,
		},
		{
			name: "ACK beyond last peer sequence",
			meta: func() *workercontrolv1.EnvelopeMeta {
				meta := validMeta()
				meta.AckSequence = 11
				return meta
			}(),
			payloadKind: PayloadKindRuntimeReady,
			cursor:      Cursor{LastPeerSequence: 10},
			wantErr:     true,
		},
		{
			name:        "valid runtime ready",
			meta:        validMeta(),
			payloadKind: PayloadKindRuntimeReady,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInbound(tt.meta, tt.payloadKind, tt.cursor)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateInbound() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestValidateWorkerEnvelopeSize(t *testing.T) {
	exactlyAtLimit := workerEnvelopeWithWireSize(t, MaxWorkerEnvelopeBytes)
	if got := proto.Size(exactlyAtLimit); got != MaxWorkerEnvelopeBytes {
		t.Fatalf("proto.Size(exactlyAtLimit) = %d, want %d", got, MaxWorkerEnvelopeBytes)
	}
	if err := ValidateWorkerEnvelope(exactlyAtLimit, Cursor{}); err != nil {
		t.Fatalf("ValidateWorkerEnvelope() error = %v, want nil", err)
	}

	overLimit := proto.Clone(exactlyAtLimit).(*workercontrolv1.WorkerEnvelope)
	overLimit.GetRuntimeDiagnostic().Detail += "x"
	if got := proto.Size(overLimit); got != MaxWorkerEnvelopeBytes+1 {
		t.Fatalf("proto.Size(overLimit) = %d, want %d", got, MaxWorkerEnvelopeBytes+1)
	}
	if err := ValidateWorkerEnvelope(overLimit, Cursor{}); err == nil {
		t.Fatal("ValidateWorkerEnvelope() error = nil, want size limit error")
	}
}

func workerEnvelopeWithWireSize(t *testing.T, wantSize int) *workercontrolv1.WorkerEnvelope {
	t.Helper()

	envelope := &workercontrolv1.WorkerEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			MessageId:      "message-1",
			StreamSequence: 1,
			SentAt:         timestamppb.Now(),
			Identity: &workercontrolv1.Identity{
				WorkerSessionId:   "session-1",
				WorkerRunId:       "run-1",
				RuntimeInstanceId: "runtime-1",
				RuntimeGeneration: 1,
			},
		},
		Payload: &workercontrolv1.WorkerEnvelope_RuntimeDiagnostic{
			RuntimeDiagnostic: &workercontrolv1.RuntimeDiagnostic{},
		},
	}

	diagnostic := envelope.GetRuntimeDiagnostic()
	diagnostic.Detail = strings.Repeat("x", wantSize)
	diagnostic.Detail = strings.Repeat("x", wantSize-(proto.Size(envelope)-wantSize))
	return envelope
}
