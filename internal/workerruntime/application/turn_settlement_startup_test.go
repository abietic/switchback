package application

import (
	"errors"
	"testing"
	"time"
)

func TestClassifyTurnSettlementForStartupRoutesDurableSources(t *testing.T) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	forceAfter := deadlineAt.Add(BackendDeadlineForceGrace)
	interrupt := backendDeadlineInterruptTestProof(deadlineAt)
	force := backendDeadlineForceStopTestProof(deadlineAt)
	writeFence, err := NewBackendDeadlineWriteFenceProof(
		7,
		8,
		force.DecidedAt,
		"container-1",
		force.DecidedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := NewBackendDeadlineCleanupProof(
		"container-1",
		"secret-volume-1",
		writeFence.ContainerAbsentObservedAt.Add(time.Second),
		writeFence.ContainerAbsentObservedAt.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	claimed := backendDeadlineTransitionSource(t, deadlineAt)
	interrupted := applyStartupSourceTransitions(
		t,
		claimed,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &interrupt,
		},
	)
	terminal := applyStartupSourceTransitions(
		t,
		claimed,
		BackendDeadlineSourceTransition{
			ToPhase:            BackendDeadlineSourceTerminalObserved,
			TerminalObservedAt: deadlineAt.Add(time.Second),
		},
	)
	forced := applyStartupSourceTransitions(
		t,
		claimed,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceForceStopIntent,
			ForceStop: force,
		},
	)
	fenced := applyStartupSourceTransitions(
		t,
		forced,
		BackendDeadlineSourceTransition{
			ToPhase:    BackendDeadlineSourceWriteFenced,
			WriteFence: &writeFence,
		},
	)
	cleaned := applyStartupSourceTransitions(
		t,
		fenced,
		BackendDeadlineSourceTransition{
			ToPhase: BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	runtimeEvent, err := NewRuntimeEventTurnSettlementSource(
		deadlineAt,
		"terminal-message-1",
		digestRuntimeControlBytes([]byte("terminal-message-1")),
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name                string
		source              TurnSettlementSource
		now                 time.Time
		wantAction          TurnSettlementStartupAction
		wantReconnect       bool
		wantNextAttemptTime time.Time
	}{
		{
			name:       "claimed deadline reconciles immediately",
			source:     claimed,
			now:        deadlineAt,
			wantAction: TurnSettlementStartupReconcileSource,
		},
		{
			name:                "interrupt before force boundary waits",
			source:              interrupted,
			now:                 forceAfter.Add(-time.Microsecond),
			wantAction:          TurnSettlementStartupWaitForceAfter,
			wantReconnect:       true,
			wantNextAttemptTime: forceAfter,
		},
		{
			name:       "interrupt at force boundary reconciles immediately",
			source:     interrupted,
			now:        forceAfter,
			wantAction: TurnSettlementStartupReconcileSource,
		},
		{
			name:       "terminal deadline reconciles terminal pipeline",
			source:     terminal,
			now:        forceAfter,
			wantAction: TurnSettlementStartupReconcileTerminal,
		},
		{
			name:       "force intent reconciles immediately",
			source:     forced,
			now:        forceAfter,
			wantAction: TurnSettlementStartupReconcileSource,
		},
		{
			name:       "write fence reconciles immediately",
			source:     fenced,
			now:        forceAfter,
			wantAction: TurnSettlementStartupReconcileSource,
		},
		{
			name:       "cleanup confirmation reconciles immediately",
			source:     cleaned,
			now:        forceAfter,
			wantAction: TurnSettlementStartupReconcileSource,
		},
		{
			name:       "Runtime event reconciles terminal pipeline",
			source:     runtimeEvent,
			now:        forceAfter,
			wantAction: TurnSettlementStartupReconcileTerminal,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			settlement := startupTestSettlement(test.source, forceAfter)
			disposition, err := ClassifyTurnSettlementForStartup(
				settlement,
				test.now.Add(999*time.Nanosecond),
			)
			if err != nil {
				t.Fatal(err)
			}
			if disposition.SettlementID != settlement.SettlementID ||
				disposition.Revision != settlement.Revision ||
				disposition.Phase != settlement.Phase ||
				disposition.Action != test.wantAction ||
				disposition.AllowExactReconnect != test.wantReconnect ||
				!disposition.NextAttemptAt.Equal(test.wantNextAttemptTime) {
				t.Fatalf("disposition=%+v", disposition)
			}
			if disposition.Lease.OwnerID != "foreign-startup-owner" ||
				disposition.Lease.Fence != 9 ||
				!disposition.Lease.ExpiresAt.Equal(
					forceAfter.Add(time.Minute),
				) ||
				!disposition.Lease.Active {
				t.Fatalf("lease disposition=%+v", disposition.Lease)
			}
		})
	}
}

func TestClassifyTurnSettlementForStartupFailsClosed(t *testing.T) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	validSource := backendDeadlineTransitionSource(t, deadlineAt)
	tests := []struct {
		name    string
		mutate  func(*TurnSettlement)
		now     time.Time
		wantErr error
	}{
		{
			name: "completed settlement",
			mutate: func(settlement *TurnSettlement) {
				settlement.Phase = TurnSettlementCompleted
			},
			now:     deadlineAt,
			wantErr: ErrTurnSettlementPhaseConflict,
		},
		{
			name: "unknown source kind",
			mutate: func(settlement *TurnSettlement) {
				settlement.Source.Kind = TurnSettlementSourceKind("recovery")
			},
			now:     deadlineAt,
			wantErr: ErrTurnSettlementSourceConflict,
		},
		{
			name: "unknown settlement phase",
			mutate: func(settlement *TurnSettlement) {
				settlement.Phase = TurnSettlementPhase("future_phase")
			},
			now:     deadlineAt,
			wantErr: ErrTurnSettlementConflict,
		},
		{
			name: "partial lease",
			mutate: func(settlement *TurnSettlement) {
				settlement.LeaseFence = 0
			},
			now:     deadlineAt,
			wantErr: ErrTurnSettlementConflict,
		},
		{
			name: "tampered source state",
			mutate: func(settlement *TurnSettlement) {
				settlement.Source.State = []byte(
					`{"phase":"unknown","schema_version":2}`,
				)
			},
			now:     deadlineAt,
			wantErr: ErrTurnSettlementSourceConflict,
		},
		{
			name:    "missing classification time",
			now:     time.Time{},
			wantErr: ErrTurnSettlementConflict,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			settlement := startupTestSettlement(validSource, deadlineAt)
			if test.mutate != nil {
				test.mutate(&settlement)
			}
			if _, err := ClassifyTurnSettlementForStartup(
				settlement,
				test.now,
			); !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v want=%v", err, test.wantErr)
			}
		})
	}
}

func TestListPendingTurnSettlementsForStartupRecordRequiresCanonicalTenant(
	t *testing.T,
) {
	t.Parallel()

	for _, tenantID := range []string{"", " tenant-local", "tenant-local "} {
		record := ListPendingTurnSettlementsForStartupRecord{
			TenantID: tenantID,
		}
		if !errors.Is(record.Validate(), ErrTurnSettlementConflict) {
			t.Fatalf("tenant %q was accepted", tenantID)
		}
	}
	if err := (ListPendingTurnSettlementsForStartupRecord{
		TenantID: "tenant-local",
	}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func startupTestSettlement(
	source TurnSettlementSource,
	now time.Time,
) TurnSettlement {
	return TurnSettlement{
		SettlementID:    "turn-settlement/turn-1",
		Phase:           TurnSettlementAccepted,
		Revision:        4,
		Source:          source,
		LeaseOwnerID:    "foreign-startup-owner",
		LeaseFence:      9,
		LeaseExpiresAt:  now.Add(time.Minute),
		AcceptedAt:      now.Add(-time.Minute),
		UpdatedAt:       now,
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentSessionID:  "agent-session-1",
		AgentTurnID:     "turn-1",
	}
}

func applyStartupSourceTransitions(
	t *testing.T,
	source TurnSettlementSource,
	transitions ...BackendDeadlineSourceTransition,
) TurnSettlementSource {
	t.Helper()
	var err error
	for _, transition := range transitions {
		source, err = ApplyBackendDeadlineSourceTransition(source, transition)
		if err != nil {
			t.Fatal(err)
		}
	}
	return source
}
