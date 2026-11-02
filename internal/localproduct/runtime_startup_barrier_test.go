package localproduct

import (
	"context"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type startupSettlementCatalogStub struct {
	settlements []workerapp.TurnSettlement
}

func (stub *startupSettlementCatalogStub) ListPendingTurnSettlementsForStartup(
	_ context.Context,
	_ workerapp.ListPendingTurnSettlementsForStartupRecord,
) ([]workerapp.TurnSettlement, error) {
	return append([]workerapp.TurnSettlement(nil), stub.settlements...), nil
}

func TestSettlementStartupBarrierReconcilesImmediatelyRunnableWorkBeforeRestore(
	t *testing.T,
) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	catalog := &startupSettlementCatalogStub{
		settlements: []workerapp.TurnSettlement{
			startupBarrierDeadlineSettlement(
				t,
				now,
				workerapp.BackendDeadlineSourceClaimed,
			),
		},
	}
	var sourceCalls, terminalCalls int
	quarantine, err := (settlementStartupBarrier{
		Catalog:  catalog,
		TenantID: "tenant-local",
		OwnerID:  "startup-owner",
		Now:      func() time.Time { return now },
		ReconcileSource: func(context.Context, string) (bool, error) {
			sourceCalls++
			catalog.settlements = nil
			return true, nil
		},
		ReconcileTerminal: func(context.Context, string) (bool, error) {
			terminalCalls++
			return true, nil
		},
	}).Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sourceCalls != 1 || terminalCalls != 0 ||
		len(quarantine.byWorkerSession) != 0 {
		t.Fatalf(
			"source calls=%d terminal calls=%d quarantine=%+v",
			sourceCalls,
			terminalCalls,
			quarantine,
		)
	}
}

func TestSettlementStartupBarrierAllowsOnlyExactInterruptedRuntimeReconnect(
	t *testing.T,
) {
	deadlineAt := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	now := deadlineAt.Add(5 * time.Second)
	settlement := startupBarrierDeadlineSettlement(
		t,
		deadlineAt,
		workerapp.BackendDeadlineSourceInterruptQueued,
	)
	catalog := &startupSettlementCatalogStub{
		settlements: []workerapp.TurnSettlement{settlement},
	}
	var reconcileCalls int
	quarantine, err := (settlementStartupBarrier{
		Catalog:  catalog,
		TenantID: "tenant-local",
		OwnerID:  "startup-owner",
		Now:      func() time.Time { return now },
		ReconcileSource: func(context.Context, string) (bool, error) {
			reconcileCalls++
			return true, nil
		},
		ReconcileTerminal: func(context.Context, string) (bool, error) {
			reconcileCalls++
			return true, nil
		},
	}).Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reconcileCalls != 0 {
		t.Fatalf("future interrupt reconciliation calls=%d", reconcileCalls)
	}

	exact := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID:     "session-1",
			WorkerRunID:         "run-1",
			AgentSessionID:      "agent-session-1",
			RuntimeInstanceID:   "runtime-1",
			RuntimeGeneration:   7,
			ControlChannelEpoch: 9,
		},
		TurnID: "turn-1",
	}
	prepared, allowed := quarantine.PrepareRuntimeCandidate(exact)
	if !allowed ||
		!prepared.SettlementReconnectUntil.Equal(
			deadlineAt.Add(workerapp.BackendDeadlineForceGrace),
		) {
		t.Fatalf("exact candidate allowed=%t candidate=%+v", allowed, prepared)
	}
	stale := exact
	stale.Restore.RuntimeGeneration++
	if _, allowed := quarantine.PrepareRuntimeCandidate(stale); allowed {
		t.Fatal("stale Runtime generation was allowed to reconnect")
	}
}

func TestSettlementStartupBarrierQuarantinesForeignTerminalOwnerWithoutBlockingStartup(
	t *testing.T,
) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	settlement := startupBarrierRuntimeEventSettlement(t, now)
	settlement.LeaseOwnerID = "foreign-owner"
	settlement.LeaseFence = 4
	settlement.LeaseExpiresAt = now.Add(time.Minute)
	catalog := &startupSettlementCatalogStub{
		settlements: []workerapp.TurnSettlement{settlement},
	}
	var reconcileCalls int
	quarantine, err := (settlementStartupBarrier{
		Catalog:  catalog,
		TenantID: "tenant-local",
		OwnerID:  "startup-owner",
		Now:      func() time.Time { return now },
		ReconcileSource: func(context.Context, string) (bool, error) {
			reconcileCalls++
			return true, nil
		},
		ReconcileTerminal: func(context.Context, string) (bool, error) {
			reconcileCalls++
			return true, nil
		},
	}).Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reconcileCalls != 0 {
		t.Fatalf("foreign lease reconciliation calls=%d", reconcileCalls)
	}
	candidate := startupRuntimeCandidate{
		Restore: liveRuntimeRestoreRequest{
			WorkerSessionID:     "session-1",
			WorkerRunID:         "run-1",
			AgentSessionID:      "agent-session-1",
			RuntimeInstanceID:   "runtime-1",
			RuntimeGeneration:   7,
			ControlChannelEpoch: 9,
		},
		TurnID: "turn-1",
	}
	if _, allowed := quarantine.PrepareRuntimeCandidate(candidate); allowed {
		t.Fatal("terminal settlement owner allowed ordinary Runtime restore")
	}
}

func startupBarrierDeadlineSettlement(
	t *testing.T,
	deadlineAt time.Time,
	phase workerapp.BackendDeadlineSourcePhase,
) workerapp.TurnSettlement {
	t.Helper()
	record, err := workerapp.NewBackendDeadlineClaimRecord(
		workerapp.ClaimBackendDeadlineCommand{
			TenantID:          "tenant-local",
			WorkerSessionID:   "session-1",
			WorkerRunID:       "run-1",
			AgentSessionID:    "agent-session-1",
			AgentTurnID:       "turn-1",
			RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 7,
			ChannelEpoch:      9,
			DeadlineAt:        deadlineAt,
			SourceRef:         "scheduled-command-1",
		},
		deadlineAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	source := record.Source
	if phase == workerapp.BackendDeadlineSourceInterruptQueued {
		source, err = workerapp.ApplyBackendDeadlineSourceTransition(
			source,
			workerapp.BackendDeadlineSourceTransition{
				ToPhase: workerapp.BackendDeadlineSourceInterruptQueued,
				Interrupt: &workerapp.BackendDeadlineInterruptProof{
					MessageID:      "interrupt-message-1",
					IdempotencyKey: "interrupt-idempotency-1",
					Sequence:       2,
					EnvelopeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					CursorDigest:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					JournaledAt:    deadlineAt.Add(2 * time.Second),
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	} else if phase != workerapp.BackendDeadlineSourceClaimed {
		t.Fatalf("unsupported test phase %s", phase)
	}
	return workerapp.TurnSettlement{
		SettlementID:    record.SettlementID,
		TenantID:        "tenant-local",
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentSessionID:  "agent-session-1",
		AgentTurnID:     "turn-1",
		Source:          source,
		Phase:           workerapp.TurnSettlementAccepted,
		Revision:        1,
		AcceptedAt:      deadlineAt.Add(time.Second),
		UpdatedAt:       deadlineAt.Add(time.Second),
	}
}

func startupBarrierRuntimeEventSettlement(
	t *testing.T,
	occurredAt time.Time,
) workerapp.TurnSettlement {
	t.Helper()
	source, err := workerapp.NewRuntimeEventTurnSettlementSource(
		occurredAt,
		"terminal-message-1",
		"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	)
	if err != nil {
		t.Fatal(err)
	}
	return workerapp.TurnSettlement{
		SettlementID:          "turn-settlement/turn-1",
		TenantID:              "tenant-local",
		WorkerSessionID:       "session-1",
		WorkerRunID:           "run-1",
		AgentSessionID:        "agent-session-1",
		AgentTurnID:           "turn-1",
		RuntimeInstanceID:     "runtime-1",
		RuntimeGeneration:     7,
		ChannelEpoch:          9,
		TerminalEventSequence: 3,
		TerminalMessageID:     "terminal-message-1",
		Source:                source,
		Phase:                 workerapp.TurnSettlementAccepted,
		Revision:              1,
		AcceptedAt:            occurredAt,
		UpdatedAt:             occurredAt,
	}
}
