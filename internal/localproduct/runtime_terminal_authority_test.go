package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestApplyRuntimeRecoverySignalBeforeOpenOrdersDurableInboxFirst(t *testing.T) {
	t.Parallel()

	var calls []string
	opened, err := applyRuntimeRecoverySignalBeforeOpen(
		context.Background(),
		workerapp.WorkerControlEvent{},
		func(context.Context, workerapp.WorkerControlEvent) error {
			calls = append(calls, "apply_ignore")
			return nil
		},
		func(context.Context) (bool, error) {
			calls = append(calls, "open_recovery")
			return true, nil
		},
	)
	if err != nil || !opened {
		t.Fatalf("opened=%t error=%v", opened, err)
	}
	if want := []string{"apply_ignore", "open_recovery"}; !reflect.DeepEqual(
		calls,
		want,
	) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestRuntimeTerminalAuthorityDeadlineWinsOverACPEOFAtBoundary(
	t *testing.T,
) {
	t.Parallel()

	deadline := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	terminal, recovery, err := terminalResultFromRuntimeSettlementEvent(
		&workerapp.WorkerControlEvent{
			Envelope: contractsv1.RuntimeControlEnvelope{
				MessageType: "agent_turn.terminal",
			},
			Payload: json.RawMessage(
				`{"update_kind":"error","update":{"code":"acp_eof"}}`,
			),
		},
		workerapp.TurnSettlement{AcceptedAt: deadline},
		pendingTurn{DeadlineAt: deadline},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovery ||
		terminal.reason != "agent_turn_deadline" ||
		!terminal.timedOut ||
		terminal.sourceKind != workerapp.TurnOutcomeSourceBackendDeadline ||
		!terminal.occurredAt.Equal(deadline) {
		t.Fatalf(
			"terminal=%+v recovery=%t",
			terminal,
			recovery,
		)
	}
}

func TestApplyRuntimeRecoverySignalBeforeOpenDoesNotOpenAfterApplyFailure(
	t *testing.T,
) {
	t.Parallel()

	applyErr := errors.New("injected inbox Apply failure")
	openCalled := false
	opened, err := applyRuntimeRecoverySignalBeforeOpen(
		context.Background(),
		workerapp.WorkerControlEvent{},
		func(context.Context, workerapp.WorkerControlEvent) error {
			return applyErr
		},
		func(context.Context) (bool, error) {
			openCalled = true
			return true, nil
		},
	)
	if !errors.Is(err, applyErr) || opened || openCalled {
		t.Fatalf(
			"opened=%t open_called=%t error=%v",
			opened,
			openCalled,
			err,
		)
	}
}
