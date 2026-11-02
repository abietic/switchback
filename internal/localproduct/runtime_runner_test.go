package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeControlCompositionUsesPostgresJournal(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC) }
	service := newRuntimeControlJournalService(nil, now)
	if _, ok := service.Store.(*workerpostgres.RuntimeControlJournal); !ok {
		t.Fatalf("Runtime control journal store = %T, want PostgreSQL", service.Store)
	}
	if got := service.Now(); !got.Equal(now()) {
		t.Fatalf("Runtime control journal clock = %v, want %v", got, now())
	}
}

func TestActiveTurnSupervisorsAllowsSameTurnAfterCompletedCleanup(t *testing.T) {
	supervisors := &activeTurnSupervisors{}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	started, err := supervisors.start("agent-turn-1", func() {
		close(firstStarted)
		<-releaseFirst
	})
	if err != nil || !started {
		t.Fatalf("start first supervisor = (%t, %v), want (true, nil)", started, err)
	}
	<-firstStarted

	duplicateRan := make(chan struct{}, 1)
	started, err = supervisors.start("agent-turn-1", func() {
		duplicateRan <- struct{}{}
	})
	if err != nil || started {
		t.Fatalf("start duplicate supervisor = (%t, %v), want (false, nil)", started, err)
	}
	select {
	case <-duplicateRan:
		t.Fatal("duplicate supervisor ran before completed cleanup")
	default:
	}

	close(releaseFirst)
	cleanupCompleted := make(chan struct{})
	go func() {
		supervisors.group.Wait()
		close(cleanupCompleted)
	}()
	select {
	case <-cleanupCompleted:
	case <-time.After(time.Second):
		t.Fatal("completed supervisor was not cleaned up")
	}

	restarted := make(chan struct{})
	started, err = supervisors.start("agent-turn-1", func() {
		close(restarted)
	})
	if err != nil || !started {
		t.Fatalf("restart completed supervisor = (%t, %v), want (true, nil)", started, err)
	}
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("restarted supervisor did not run")
	}
	supervisors.stopAndWait()
}

func TestActiveTurnSupervisorsRejectsStartsAfterStopAndWait(t *testing.T) {
	supervisors := &activeTurnSupervisors{}
	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	started, err := supervisors.start("agent-turn-1", func() {
		close(activeStarted)
		<-releaseActive
	})
	if err != nil || !started {
		t.Fatalf("start active supervisor = (%t, %v), want (true, nil)", started, err)
	}
	<-activeStarted

	stopped := make(chan struct{})
	go func() {
		supervisors.stopAndWait()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stopAndWait returned before the active supervisor completed")
	default:
	}
	close(releaseActive)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stopAndWait did not observe active supervisor completion")
	}

	rejectedRan := make(chan struct{}, 1)
	started, err = supervisors.start("agent-turn-2", func() {
		rejectedRan <- struct{}{}
	})
	if err != nil || started {
		t.Fatalf("start after stopAndWait = (%t, %v), want (false, nil)", started, err)
	}
	select {
	case <-rejectedRan:
		t.Fatal("supervisor ran after stopAndWait")
	default:
	}
}

func TestStartupSafetyPollerDrainsDeadlinesAndFirstHeartbeatWatchdogs(
	t *testing.T,
) {
	runner := &runtimeRunner{
		configuration: Config{},
		scheduled: platformpostgres.ScheduledCommandPoller{
			LeaseOwner: "steady-state",
		},
	}
	runner.configuration.Profile.TenantID = "tenant-local"
	poller := runner.startupDeadlineScheduledCommandPoller()
	if poller.TenantID != "tenant-local" ||
		poller.LeaseOwner != "steady-state-startup-deadline" ||
		len(poller.CommandTypes) != 2 ||
		poller.CommandTypes[0] !=
			workerpostgres.CommandAgentTurnDeadline ||
		poller.CommandTypes[1] !=
			workerpostgres.CommandRuntimeFirstHeartbeatEvaluate {
		t.Fatalf("startup safety poller=%+v", poller)
	}
}

func TestProcessWorkerControlEventAppliesOnlyAfterBusinessSuccess(t *testing.T) {
	processingErr := errors.New("business transaction failed")
	failed := &recordingWorkerControlDelivery{}
	event := workerapp.WorkerControlEvent{Delivery: failed}
	err := processWorkerControlEvent(
		context.Background(),
		event,
		"business_committed",
		func() error { return processingErr },
	)
	if !errors.Is(err, processingErr) || failed.calls != 0 || failed.retryCalls != 1 {
		t.Fatalf(
			"failed handler error=%v delivery calls=%d retry calls=%d",
			err,
			failed.calls,
			failed.retryCalls,
		)
	}

	succeeded := &recordingWorkerControlDelivery{}
	event.Delivery = succeeded
	err = processWorkerControlEvent(
		context.Background(),
		event,
		"business_committed",
		func() error { return nil },
	)
	if err != nil || succeeded.calls != 1 ||
		string(succeeded.outcome) != `{"status":"business_committed"}` {
		t.Fatalf(
			"successful handler error=%v calls=%d outcome=%s",
			err,
			succeeded.calls,
			succeeded.outcome,
		)
	}
}

func TestProcessWorkerControlEventDoesNotHideDeliveryFailure(t *testing.T) {
	deliveryErr := errors.New("journal apply failed")
	delivery := &recordingWorkerControlDelivery{err: deliveryErr}
	err := processWorkerControlEvent(
		context.Background(),
		workerapp.WorkerControlEvent{Delivery: delivery},
		"business_committed",
		func() error { return nil },
	)
	if !errors.Is(err, deliveryErr) || delivery.calls != 1 {
		t.Fatalf("delivery failure error=%v calls=%d", err, delivery.calls)
	}
}

func TestProcessWorkerControlEffectEventCarriesExplicitDisposition(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      string
		disposition string
		want        string
	}{
		{
			name:        "commit",
			status:      "effect_committed",
			disposition: "commit",
			want:        `{"status":"effect_committed","effect_disposition":"commit"}`,
		},
		{
			name:        "ignore",
			status:      "effect_ignored",
			disposition: "ignore",
			want:        `{"status":"effect_ignored","effect_disposition":"ignore"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			delivery := &recordingWorkerControlDelivery{}
			err := processWorkerControlEffectEvent(
				context.Background(),
				workerapp.WorkerControlEvent{Delivery: delivery},
				test.status,
				test.disposition,
				func() error { return nil },
			)
			if err != nil || delivery.calls != 1 || string(delivery.outcome) != test.want {
				t.Fatalf(
					"effect outcome error=%v calls=%d outcome=%s",
					err,
					delivery.calls,
					delivery.outcome,
				)
			}
		})
	}
}

func TestTerminalAndIgnoredEffectDeliveryDeclareReconciliationDisposition(t *testing.T) {
	terminalDelivery := &recordingWorkerControlDelivery{}
	terminal := workerapp.WorkerControlEvent{
		Envelope: contractsv1.RuntimeControlEnvelope{MessageType: "agent_turn.terminal"},
		Delivery: terminalDelivery,
	}
	if err := applyTerminalWorkerControlEvent(context.Background(), &terminal); err != nil {
		t.Fatal(err)
	}
	if got := string(terminalDelivery.outcome); got !=
		`{"status":"agent_turn_terminal_committed","effect_disposition":"commit"}` {
		t.Fatalf("terminal outcome=%s", got)
	}

	ignoredDelivery := &recordingWorkerControlDelivery{}
	ignored := workerapp.WorkerControlEvent{
		Envelope: contractsv1.RuntimeControlEnvelope{MessageType: "agent_turn.effect"},
		Delivery: ignoredDelivery,
	}
	if err := ignoreWorkerControlEvent(
		context.Background(),
		ignored,
		"ignored_unrelated_turn",
	); err != nil {
		t.Fatal(err)
	}
	if got := string(ignoredDelivery.outcome); got !=
		`{"status":"ignored_unrelated_turn","effect_disposition":"ignore"}` {
		t.Fatalf("ignored effect outcome=%s", got)
	}
}

func TestWaitWorkerEventFailsClosedWithoutApplyingUnexpectedBusinessEvent(t *testing.T) {
	delivery := &recordingWorkerControlDelivery{}
	events := make(chan workerapp.WorkerControlEvent, 1)
	events <- workerapp.WorkerControlEvent{
		Envelope: contractsv1.RuntimeControlEnvelope{MessageType: "agent.update"},
		Delivery: delivery,
	}
	_, err := waitWorkerEvent(
		context.Background(),
		events,
		time.Second,
		func(event workerapp.WorkerControlEvent) bool {
			return event.Envelope.MessageType == "runtime.ready"
		},
	)
	if err == nil || !strings.Contains(err.Error(), "unexpected") || delivery.calls != 0 {
		t.Fatalf("unexpected event error=%v delivery calls=%d", err, delivery.calls)
	}
}

type recordingWorkerControlDelivery struct {
	calls      int
	retryCalls int
	outcome    json.RawMessage
	err        error
}

func (delivery *recordingWorkerControlDelivery) Apply(
	_ context.Context,
	outcome json.RawMessage,
) error {
	delivery.calls++
	delivery.outcome = append(json.RawMessage(nil), outcome...)
	return delivery.err
}

func (delivery *recordingWorkerControlDelivery) ReleaseForRetry() {
	delivery.retryCalls++
}

func TestLocalScheduledDeadlineRequiresDurableSourceClaimHandler(
	t *testing.T,
) {
	handler := localScheduledCommandHandler{}
	err := handler.HandleScheduledCommand(context.Background(), platformpostgres.ScheduledCommand{
		CommandType: "agent_turn_deadline", TargetType: "AgentTurn", TargetID: "turn-1",
	})
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"Backend deadline claim handler dependencies are required",
		) {
		t.Fatalf("deadline handler error=%v", err)
	}
}

func TestAgentErrorTerminalClassificationIsBusinessTerminalNotBackendFailure(t *testing.T) {
	terminal, recovery, err := classifyAgentErrorTerminal(
		"acp_prompt_failed",
		false,
	)
	if err != nil || recovery ||
		terminal.reason != "agent_error" ||
		terminal.timedOut ||
		terminal.forcedStop {
		t.Fatalf(
			"Agent error classification terminal=%+v recovery=%t error=%v",
			terminal,
			recovery,
			err,
		)
	}

	deadline, recovery, err := classifyAgentErrorTerminal(
		"acp_prompt_failed",
		true,
	)
	if err != nil || recovery ||
		deadline.reason != "agent_turn_deadline" ||
		!deadline.timedOut {
		t.Fatalf(
			"deadline Agent error classification terminal=%+v recovery=%t error=%v",
			deadline,
			recovery,
			err,
		)
	}

	_, recovery, err = classifyAgentErrorTerminal("acp_eof", false)
	if err != nil || !recovery {
		t.Fatalf("ACP EOF recovery=%t error=%v", recovery, err)
	}
}

func TestResidentTargetsForTerminalIncludesAgentFailure(t *testing.T) {
	session, run, turn := residentTargetsForTerminal(turnTerminalResult{
		reason: "agent_error",
	})
	if session != "waiting_user" ||
		run != "failed" ||
		turn != "failed" {
		t.Fatalf(
			"Agent failure targets session=%s run=%s turn=%s",
			session,
			run,
			turn,
		)
	}
}

func TestBackendDeadlineSourceWaiterHandoffStartsOnlyAtDurableForce(
	t *testing.T,
) {
	for _, phase := range []workerapp.BackendDeadlineSourcePhase{
		workerapp.BackendDeadlineSourceClaimed,
		workerapp.BackendDeadlineSourceInterruptQueued,
		workerapp.BackendDeadlineSourceTerminalObserved,
	} {
		if backendDeadlineSourcePhaseHandsOffWaiter(phase) {
			t.Fatalf("phase %s handed off before durable force", phase)
		}
	}
	for _, phase := range []workerapp.BackendDeadlineSourcePhase{
		workerapp.BackendDeadlineSourceForceStopIntent,
		workerapp.BackendDeadlineSourceWriteFenced,
		workerapp.BackendDeadlineSourceCleanupConfirmed,
	} {
		if !backendDeadlineSourcePhaseHandsOffWaiter(phase) {
			t.Fatalf("phase %s did not hand off after durable force", phase)
		}
	}
}

func TestFinishInterruptCommandsBindsTerminalReasonAsText(t *testing.T) {
	pool := postgrestest.Open(t)
	now := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	runner := &runtimeRunner{pool: pool, now: func() time.Time { return now }}

	err := runner.finishInterruptCommands(context.Background(), pendingTurn{
		WorkerSessionID: "session-without-interrupt",
		WorkerRunID:     "run-without-interrupt",
		AgentTurnID:     "turn-without-interrupt",
	}, "end_turn")
	if err != nil {
		t.Fatalf("finish interrupt commands with no matching rows: %v", err)
	}
}

func TestRunOnceInitialCapacityRetainsStartupBarrierAndSettlementPriority(t *testing.T) {
	runner := &runtimeRunner{}
	settled, initialCalls, oldWork := true, 0, 0
	dependencies := runtimeRunOnceDependencies{
		reconcileSettlement: func(context.Context) (string, bool, error) { return "old-settlement", settled, nil },
		reconcileInitial:    func(context.Context) error { initialCalls++; return workerapp.CapacityContainerProofPending },
		startAdoption:       func(context.Context) (bool, error) { oldWork++; return false, nil },
		detectFailures:      func(context.Context) error { oldWork++; return nil },
		reconcileRecovery: func(context.Context) (workerapp.RuntimeRecoveryState, bool, error) {
			oldWork++
			return workerapp.RuntimeRecoveryState{}, false, nil
		},
		cleanupTerminal: func(context.Context) error { oldWork++; return nil },
		startQueuedTask: func(context.Context) error { t.Fatal("proof-pending owner started another Task"); return nil },
		executeTurn:     func(context.Context) error { t.Fatal("proof-pending owner dispatched Prompt"); return nil },
	}
	if err := runner.runOnceWith(context.Background(), dependencies); err != errRuntimeStartupRestoreInProgress || initialCalls != 0 {
		t.Fatal("initial capacity bypassed startup barrier")
	}
	runner.startupReady.Store(true)
	if err := runner.runOnceWith(context.Background(), dependencies); err != nil || initialCalls != 0 {
		t.Fatal("initial wait preempted old owned settlement")
	}
	settled = false
	if err := runner.runOnceWith(context.Background(), dependencies); err != workerapp.CapacityContainerProofPending || initialCalls != 1 || oldWork != 4 {
		t.Fatalf("initial wait stopped existing reconciliation: %v, %d", err, oldWork)
	}
}
