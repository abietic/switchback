package postgres

import (
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"testing"
)

func TestRuntimeControlTransportRevisionFinitePairs(t *testing.T) {
	for _, tc := range []struct {
		kind, messageType string
		want              int64
	}{
		{"heartbeat", "runtime.heartbeat", 0}, {"ack", "runtime.control.ack", 0},
		{"event", "runtime.heartbeat", 1}, {"command", "runtime.control.ack", 1},
		{"ack", "runtime.heartbeat", 1}, {"heartbeat", "runtime.control.ack", 1},
		{"ack", "runtime.unknown.ack", 1}, {"heartbeat", "runtime.unknown", 1},
		{"command", "runtime.inspect", 1}, {"event", "runtime.update", 1},
		{"event", "agent_turn.terminal", 1}, {"command", "agent_turn.start", 1},
		{"Heartbeat", "runtime.heartbeat", 1}, {"heartbeat", "runtime.heartbeat ", 1}, {"", "", 1},
	} {
		t.Run(tc.kind+"/"+tc.messageType, func(t *testing.T) {
			if got := runtimeControlLifecycleRevisionDelta(contractsv1.RuntimeControlEnvelope{Kind: tc.kind, MessageType: tc.messageType}); got != tc.want {
				t.Fatalf("revision delta=%d want %d", got, tc.want)
			}
		})
	}
}
