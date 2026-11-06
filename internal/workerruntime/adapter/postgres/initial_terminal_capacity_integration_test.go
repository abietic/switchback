//go:build integration

package postgres_test

import (
	"context"
	"errors"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestInitialTerminalCapacityDirect(t *testing.T) {
	for _, terminal := range []domain.AgentTurnStatus{domain.TurnCompleted, domain.TurnFailed, domain.TurnCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			started, active := startActiveRecoveryFixture(t, pool, time.Now().UTC().Add(-10*time.Minute))
			runtimecapacity.ObserveStale(t, pool)
			command := app.CompleteResidentStageCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, SessionTarget: domain.SessionInteractive, RunTarget: domain.WorkerRunStatus(terminal), TurnTarget: terminal, TerminalReason: "test_terminal"}
			_, err := workerpostgres.NewRuntimeResidencyStore(pool).CompleteResidentStage(ctx, app.CompleteResidentStageRecord{Command: command, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			var released, residents int
			if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime_capacity_executions WHERE released_at IS NOT NULL AND release_reason='turn_terminal'),(SELECT count(*) FROM runtime_capacity_residents WHERE removal_sequence IS NULL)`).Scan(&released, &residents); err != nil {
				t.Fatal(err)
			}
			if released != 1 || residents != 1 {
				t.Fatalf("terminal did not release exact execution while retaining resident: released=%d residents=%d", released, residents)
			}
		})
	}
}

func terminalCapacityRecord(started app.StartedWorkerRun, active app.ResidentRuntimeState) app.CompleteResidentStageRecord {
	return app.CompleteResidentStageRecord{Command: app.CompleteResidentStageCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, SessionTarget: domain.SessionInteractive, RunTarget: domain.RunCompleted, TurnTarget: domain.TurnCompleted}, Now: time.Now()}
}
func assertTerminalCapacity(t *testing.T, pool *pgxpool.Pool, wantReleased int) string {
	t.Helper()
	var released, residents int
	var identity string
	err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM runtime_capacity_executions WHERE released_at IS NOT NULL),(SELECT count(*) FROM runtime_capacity_residents WHERE removal_sequence IS NULL),(SELECT jsonb_agg(to_jsonb(e) ORDER BY token)::text FROM runtime_capacity_executions e)`).Scan(&released, &residents, &identity)
	if err != nil {
		t.Fatal(err)
	}
	if released != wantReleased || residents != 1 {
		t.Fatalf("execution released=%d want=%d resident=%d", released, wantReleased, residents)
	}
	return identity
}
func TestInitialTerminalCapacityDomainAndForcedDeadline(t *testing.T) {
	for _, kind := range []string{"ordinary", "forced_deadline"} {
		t.Run(kind, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Now().UTC().Add(-10 * time.Minute)
			var command app.CommitTurnSettlementDomainCommand
			var now time.Time
			if kind == "ordinary" {
				prepared, stage := prepareTurnSettlementDomainCommit(t, pool, base)
				command = app.CommitTurnSettlementDomainCommand{SettlementID: prepared.SettlementID, ExpectedRevision: prepared.Revision, LeaseOwnerID: prepared.LeaseOwnerID, LeaseFence: prepared.LeaseFence, Resident: stage}
				now = base.Add(7*time.Minute + time.Second)
			} else {
				started, cleaned, deadline := prepareBackendDeadlineSourceForApply(t, pool, base, true)
				_, command, now = prepareBackendDeadlineDomainCommit(t, pool, started, cleaned, deadline)
			}
			// A rejected current observation cannot block already-owned terminal facts.
			store, err := workerpostgres.NewCapacityStore(ctx, pool)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.ObservePool(ctx, []byte(`invalid signed envelope`)); !errors.Is(err, app.CapacityEvidenceInvalid) {
				t.Fatal(err)
			}
			service := app.TurnSettlementDomainService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: func() time.Time { return now }}
			if _, err = service.Commit(ctx, command); err != nil {
				t.Fatal(err)
			}
			before := assertTerminalCapacity(t, pool, 1)
			if _, err = service.Commit(ctx, command); err != nil {
				t.Fatal(err)
			}
			if after := assertTerminalCapacity(t, pool, 1); after != before {
				t.Fatal("terminal replay changed ledger")
			}
		})
	}
}
func TestInitialTerminalCapacityBindingAndRollback(t *testing.T) {
	for _, fault := range []string{"generation", "permit", "snapshot", "runtime_run", "target", "ledger_update", "outbox", "phase"} {
		t.Run(fault, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Now().UTC().Add(-10 * time.Minute)
			var operation func() error
			if fault == "phase" {
				prepared, stage := prepareTurnSettlementDomainCommit(t, pool, base)
				operation = func() error {
					_, err := (app.TurnSettlementDomainService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: func() time.Time { return base.Add(7*time.Minute + time.Second) }}).Commit(ctx, app.CommitTurnSettlementDomainCommand{SettlementID: prepared.SettlementID, ExpectedRevision: prepared.Revision, LeaseOwnerID: prepared.LeaseOwnerID, LeaseFence: prepared.LeaseFence, Resident: stage})
					return err
				}
			} else {
				started, active := startActiveRecoveryFixture(t, pool, base)
				record := terminalCapacityRecord(started, active)
				if fault == "outbox" {
					operation = func() error {
						_, err := workerpostgres.NewRuntimeLossStore(pool).RecordRuntimeLoss(ctx, app.RuntimeLossRecord{Command: app.ObserveRuntimeLossCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ExpectedContainerID: active.ContainerID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, IdempotencyKey: "terminal-fault"}, Now: time.Now(), EventID: "terminal-loss-event", TaskTransitionID: "terminal-loss-transition"})
						return err
					}
				} else {
					operation = func() error {
						_, err := workerpostgres.NewRuntimeResidencyStore(pool).CompleteResidentStage(ctx, record)
						return err
					}
				}
			}
			sql := map[string]string{"generation": `UPDATE runtime_capacity_executions SET target_bytes=convert_to(replace(convert_from(target_bytes,'UTF8'),'"Generation":1','"Generation":2'),'UTF8')`, "permit": `UPDATE session_dispatch_permits SET capacity_admission_ref='wrong-token'`, "snapshot": `UPDATE worker_runs SET start_snapshot=start_snapshot #- '{runtime_capacity,Reservation,Token}'`, "runtime_run": `UPDATE runtime_capacity_executions SET runtime_id='wrong-runtime'`, "target": `UPDATE runtime_capacity_executions SET target_digest='sha256:wrong'`, "ledger_update": `CREATE FUNCTION reject_terminal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'terminal fault'; END $$; CREATE TRIGGER reject_terminal BEFORE UPDATE ON runtime_capacity_executions FOR EACH ROW EXECUTE FUNCTION reject_terminal()`, "outbox": `CREATE FUNCTION reject_terminal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'terminal fault'; END $$; CREATE TRIGGER reject_terminal BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_terminal()`, "phase": `CREATE FUNCTION reject_terminal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'terminal fault'; END $$; CREATE TRIGGER reject_terminal BEFORE UPDATE ON worker_turn_settlements FOR EACH ROW EXECUTE FUNCTION reject_terminal()`}[fault]
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var result string
				if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('turn',(SELECT jsonb_agg(to_jsonb(t)) FROM agent_turns t),'run',(SELECT jsonb_agg(to_jsonb(t)) FROM worker_runs t),'session',(SELECT jsonb_agg(to_jsonb(t)) FROM worker_sessions t),'task',(SELECT jsonb_agg(to_jsonb(t)) FROM tasks t),'ledger',(SELECT jsonb_agg(to_jsonb(t)) FROM runtime_capacity_executions t),'phase',(SELECT jsonb_agg(to_jsonb(t)) FROM worker_turn_settlements t),'outbox',(SELECT jsonb_agg(to_jsonb(t)) FROM outbox_events t))::text`).Scan(&result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			before := snapshot()
			if err := operation(); err == nil {
				t.Fatal("fault accepted")
			}
			if after := snapshot(); after != before {
				t.Fatal("partial terminal transaction committed")
			}
		})
	}
}
func TestInitialTerminalCapacityLossDisabledAndDeadline(t *testing.T) {
	for _, kind := range []string{"loss", "disabled", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Now().UTC().Add(-10 * time.Minute)
			started, active := startActiveRecoveryFixture(t, pool, base)
			runtimecapacity.ObserveStale(t, pool)
			var operation func() error
			switch kind {
			case "loss":
				operation = func() error {
					_, err := workerpostgres.NewRuntimeLossStore(pool).RecordRuntimeLoss(ctx, app.RuntimeLossRecord{Command: app.ObserveRuntimeLossCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ExpectedContainerID: active.ContainerID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, IdempotencyKey: "terminal-loss"}, Now: time.Now(), EventID: "terminal-loss-event", TaskTransitionID: "terminal-loss-transition"})
					return err
				}
			case "disabled":
				observer := workerpostgres.NewGatedRuntimeFailureObserver(pool, app.RuntimeRecoveryService{Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{}, Now: time.Now}, false)
				operation = func() error {
					observed := time.Now()
					_, _, err := observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit, EvidenceRef: "terminal-loss-inspection", EvidenceAt: observed, Inspection: &app.RuntimeInspection{Exists: true, ContainerID: active.ContainerID, Generation: 1, ObservedAt: observed}})
					return err
				}
			case "deadline":
				var deadline time.Time
				if err := pool.QueryRow(ctx, `SELECT deadline_at FROM agent_turns WHERE agent_turn_id=$1`, started.AgentTurnID).Scan(&deadline); err != nil {
					t.Fatal(err)
				}
				operation = func() error {
					return (workerpostgres.LifecycleCommandHandler{Pool: pool, Now: func() time.Time { return deadline }}).HandleScheduledCommand(ctx, platformpostgres.ScheduledCommand{TenantID: "tenant-local", TargetType: "AgentTurn", TargetID: started.AgentTurnID, CommandType: workerpostgres.CommandAgentTurnDeadline, ExpectedRevision: active.TurnRevision, DueAt: deadline})
				}
			}
			if err := operation(); err != nil {
				t.Fatal(err)
			}
			before := assertTerminalCapacity(t, pool, 1)
			if err := operation(); err != nil {
				t.Fatal(err)
			}
			if after := assertTerminalCapacity(t, pool, 1); after != before {
				t.Fatal("replay renewed original execution")
			}
		})
	}
}
func TestInitialTerminalCapacityCancelAndExpiry(t *testing.T) {
	for _, kind := range []string{"unconsumed", "consumed", "expired", "consumed_expired"} {
		t.Run(kind, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			var taskID string
			var terminal app.CompleteResidentStageRecord
			consumed := kind == "consumed" || kind == "consumed_expired"
			if consumed {
				started, active := startActiveRecoveryFixture(t, pool, time.Now().UTC().Add(-10*time.Minute))
				taskID = started.TaskID
				terminal = terminalCapacityRecord(started, active)
			} else {
				runtimecapacity.Initialize(t, pool)
				dispatch, commands, _ := initialCapacityQueue(t, pool, 1)
				permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
				if err != nil {
					t.Fatal(err)
				}
				taskID = permit.TaskID
			}
			if kind == "expired" || kind == "consumed_expired" {
				// Explicit SQL time-boundary fault, never signed physical evidence.
				if _, err := pool.Exec(ctx, `UPDATE runtime_capacity_executions SET expires_at=clock_timestamp()-interval '1 second'; UPDATE session_dispatch_permits SET created_at=clock_timestamp()-interval '10 minutes',expires_at=(SELECT expires_at FROM runtime_capacity_executions LIMIT 1); UPDATE worker_runs SET start_snapshot=jsonb_set(start_snapshot,'{runtime_capacity,Reservation,ExpiresAt}',to_jsonb((SELECT expires_at FROM runtime_capacity_executions LIMIT 1)))`); err != nil {
					t.Fatal(err)
				}
				store, err := workerpostgres.NewCapacityStore(ctx, pool)
				if err != nil {
					t.Fatal(err)
				}
				dir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				reader, err := capacitysnapshot.NewReader(dir)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.ReconcileCapacity(ctx, reader); !errors.Is(err, app.CapacityEvidenceInvalid) {
					t.Fatal(err)
				}
				want := 1
				if consumed {
					want = 0
				}
				before := assertTerminalCapacity(t, pool, want)
				if err = store.ReconcileCapacity(ctx, reader); !errors.Is(err, app.CapacityEvidenceInvalid) {
					t.Fatal(err)
				}
				if after := assertTerminalCapacity(t, pool, want); after != before {
					t.Fatal("expiry replay changed original identity/TTL")
				}
				if consumed {
					if _, err = workerpostgres.NewRuntimeResidencyStore(pool).CompleteResidentStage(ctx, terminal); err != nil {
						t.Fatal(err)
					}
					assertTerminalCapacity(t, pool, 1)
				}
				return
			}
			var revision uint64
			if err := pool.QueryRow(ctx, `SELECT revision FROM tasks WHERE task_id=$1`, taskID).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			record := taskapp.CancelTaskRecord{Command: taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: taskID, ExpectedTaskRevision: revision, IdempotencyKey: "terminal-cancel"}, Now: time.Now(), TransitionID: "terminal-cancel-transition", EventID: "terminal-cancel-event", ControlCommandID: "terminal-cancel-control"}
			store := taskpostgres.NewStore(pool)
			if _, err := store.CancelTask(ctx, record); err != nil {
				t.Fatal(err)
			}
			want := 1
			if consumed {
				want = 0
			}
			before := assertTerminalCapacity(t, pool, want)
			if _, err := store.CancelTask(ctx, record); err != nil {
				t.Fatal(err)
			}
			if after := assertTerminalCapacity(t, pool, want); after != before {
				t.Fatal("cancel replay changed ledger")
			}
			if consumed {
				var interrupts int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_user_commands WHERE command_type='interrupt'`).Scan(&interrupts); err != nil || interrupts != 1 {
					t.Fatalf("interrupts=%d %v", interrupts, err)
				}
				if _, err := workerpostgres.NewRuntimeResidencyStore(pool).CompleteResidentStage(ctx, terminal); err != nil {
					t.Fatal(err)
				}
				assertTerminalCapacity(t, pool, 1)
			}
		})
	}
}

func TestInitialTerminalCapacityRecoveryExhaustion(t *testing.T) {
	for _, reason := range []string{"exclusive mount authority could not be proven"} {
		t.Run(reason, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Now().UTC().Add(-10 * time.Minute)
			started, active := startActiveRecoveryFixture(t, pool, base)
			now := base.Add(6 * time.Minute)
			service := app.RuntimeRecoveryService{Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{}, Now: func() time.Time { return now }}
			incident, err := service.Open(ctx, app.OpenRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, FailedRuntimeInstanceID: started.RuntimeInstanceID, FailedGeneration: 1, FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerMissing, EvidenceRef: "recovery-terminal", EvidenceAt: now, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, IdempotencyKey: "recovery-terminal"})
			if err != nil {
				t.Fatal(err)
			}
			if reason == "worker_session_absolute_expired" {
				now = base.Add(8 * time.Minute)
				if _, err = pool.Exec(ctx, `UPDATE worker_sessions SET absolute_expires_at=$1`, now); err != nil {
					t.Fatal(err)
				}
			} else {
				now = now.Add(time.Second)
				incident, err = service.Advance(ctx, app.AdvanceRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryFencing, ExpectedIncidentRevision: incident.IncidentRevision, IdempotencyKey: "recovery-fencing"})
				if err != nil {
					t.Fatal(err)
				}
			}
			runtimecapacity.ObserveStale(t, pool)
			command := app.AdvanceRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryExhausted, ExpectedIncidentRevision: incident.IncidentRevision, OutcomeReason: reason, IdempotencyKey: "recovery-exhaust"}
			if _, err = service.Advance(ctx, command); err != nil {
				t.Fatal(err)
			}
			before := assertTerminalCapacity(t, pool, 1)
			if _, err = service.Advance(ctx, command); err != nil {
				t.Fatal(err)
			}
			if after := assertTerminalCapacity(t, pool, 1); after != before {
				t.Fatal("recovery replay changed original execution")
			}
		})
	}
}

func TestInitialTerminalCapacityPoolPrefixAndSingleConnection(t *testing.T) {
	for _, kind := range []string{"direct", "domain", "cancel", "deadline", "loss", "disabled", "absolute", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool := postgrestest.Open(t)
			base := time.Now().UTC().Add(-10 * time.Minute)
			var operation func(*pgxpool.Pool) error
			switch kind {
			case "domain":
				prepared, stage := prepareTurnSettlementDomainCommit(t, pool, base)
				operation = func(p *pgxpool.Pool) error {
					_, err := (app.TurnSettlementDomainService{Store: workerpostgres.NewTurnSettlementRepository(p), Now: func() time.Time { return base.Add(7*time.Minute + time.Second) }}).Commit(ctx, app.CommitTurnSettlementDomainCommand{SettlementID: prepared.SettlementID, ExpectedRevision: prepared.Revision, LeaseOwnerID: prepared.LeaseOwnerID, LeaseFence: prepared.LeaseFence, Resident: stage})
					return err
				}
			case "cancel":
				runtimecapacity.Initialize(t, pool)
				dispatch, commands, _ := initialCapacityQueue(t, pool, 1)
				permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
				if err != nil {
					t.Fatal(err)
				}
				var revision uint64
				if err = pool.QueryRow(ctx, `SELECT revision FROM tasks WHERE task_id=$1`, permit.TaskID).Scan(&revision); err != nil {
					t.Fatal(err)
				}
				operation = func(p *pgxpool.Pool) error {
					_, err := taskpostgres.NewStore(p).CancelTask(ctx, taskapp.CancelTaskRecord{Command: taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: permit.TaskID, ExpectedTaskRevision: revision, IdempotencyKey: "prefix-cancel"}, Now: time.Now(), EventID: "prefix-event", TransitionID: "prefix-transition", ControlCommandID: "prefix-control"})
					return err
				}
			default:
				started, active := startActiveRecoveryFixture(t, pool, base)
				switch kind {
				case "direct":
					record := terminalCapacityRecord(started, active)
					operation = func(p *pgxpool.Pool) error {
						_, err := workerpostgres.NewRuntimeResidencyStore(p).CompleteResidentStage(ctx, record)
						return err
					}
				case "deadline":
					var deadline time.Time
					if err := pool.QueryRow(ctx, `SELECT deadline_at FROM agent_turns WHERE agent_turn_id=$1`, started.AgentTurnID).Scan(&deadline); err != nil {
						t.Fatal(err)
					}
					operation = func(p *pgxpool.Pool) error {
						return (workerpostgres.LifecycleCommandHandler{Pool: p, Now: func() time.Time { return deadline }}).HandleScheduledCommand(ctx, platformpostgres.ScheduledCommand{TenantID: "tenant-local", TargetType: "AgentTurn", TargetID: started.AgentTurnID, CommandType: workerpostgres.CommandAgentTurnDeadline, ExpectedRevision: active.TurnRevision, DueAt: deadline})
					}
				case "loss":
					operation = func(p *pgxpool.Pool) error {
						_, err := workerpostgres.NewRuntimeLossStore(p).RecordRuntimeLoss(ctx, app.RuntimeLossRecord{Command: app.ObserveRuntimeLossCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ExpectedContainerID: active.ContainerID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, IdempotencyKey: "prefix-loss"}, Now: time.Now(), EventID: "prefix-loss-event", TaskTransitionID: "prefix-loss-transition"})
						return err
					}
				case "absolute":
					due := time.Now()
					if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET absolute_expires_at=$1`, due); err != nil {
						t.Fatal(err)
					}
					operation = func(p *pgxpool.Pool) error {
						return (workerpostgres.LifecycleCommandHandler{Pool: p, Now: func() time.Time { return due }}).HandleScheduledCommand(ctx, platformpostgres.ScheduledCommand{TenantID: "tenant-local", TargetType: "WorkerSession", TargetID: started.WorkerSessionID, CommandType: workerpostgres.CommandWorkerSessionAbsolute, ExpectedRevision: active.SessionRevision, DueAt: due})
					}
				case "disabled":
					observed := time.Now()
					operation = func(p *pgxpool.Pool) error {
						observer := workerpostgres.NewGatedRuntimeFailureObserver(p, app.RuntimeRecoveryService{Store: workerpostgres.NewRuntimeRecoveryStore(p), IDs: &concurrentAllocationIDs{}, Now: time.Now}, false)
						_, _, err := observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit, EvidenceRef: "prefix-disabled", EvidenceAt: observed, Inspection: &app.RuntimeInspection{Exists: true, ContainerID: active.ContainerID, Generation: 1, ObservedAt: observed}})
						return err
					}

				case "recovery":
					service := app.RuntimeRecoveryService{Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{}, Now: time.Now}
					incident, err := service.Open(ctx, app.OpenRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, FailedRuntimeInstanceID: started.RuntimeInstanceID, FailedGeneration: 1, FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerMissing, EvidenceRef: "prefix-recovery", EvidenceAt: time.Now(), ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, IdempotencyKey: "prefix-recovery"})
					if err != nil {
						t.Fatal(err)
					}
					operation = func(p *pgxpool.Pool) error {
						svc := service
						svc.Store = workerpostgres.NewRuntimeRecoveryStore(p)
						_, err := svc.Advance(ctx, app.AdvanceRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryExhausted, ExpectedIncidentRevision: incident.IncidentRevision, OutcomeReason: "exclusive mount authority could not be proven", IdempotencyKey: "prefix-exhaust"})
						return err
					}
				}
			}
			gatePool := lockOrderPool(t, pool, "terminal-capacity-gate")
			gate, err := gatePool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(ctx, `SELECT 1 FROM runtime_capacity_pools FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			cfg := pool.Config()
			cfg.MaxConns = 1
			cfg.ConnConfig.RuntimeParams["application_name"] = "terminal-capacity-waiter"
			single, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer single.Close()
			done := make(chan error, 1)
			go func() { done <- operation(single) }()
			waitDispatchLockEdge(t, ctx, pool, "terminal-capacity-waiter", "terminal-capacity-gate")
			for _, table := range []string{"tasks", "worker_sessions", "worker_runs", "agent_turns", "runtime_instances", "session_dispatch_permits", "worker_turn_settlements", "runtime_recovery_incidents"} {
				if _, err = gate.Exec(ctx, "SELECT 1 FROM "+table+" FOR UPDATE NOWAIT"); err != nil {
					t.Fatalf("%s took %s before pool: %v", kind, table, err)
				}
			}
			if err = gate.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if kind == "absolute" {
				if !errors.Is(err, app.CapacityPathNotIntegrated) {
					t.Fatal(err)
				}
				assertTerminalCapacity(t, pool, 0)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertTerminalCapacity(t, pool, 1)
			}
		})
	}
}

func TestInitialTerminalCapacityAbsoluteExpiryUnprovenIsBlocked(t *testing.T) {
	for _, kind := range []string{"live", "recovering"} {
		t.Run(kind, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Now().UTC().Add(-10 * time.Minute)
			started, active := startActiveRecoveryFixture(t, pool, base)
			now := base.Add(6 * time.Minute)
			var operation func() error
			if kind == "recovering" {
				service := app.RuntimeRecoveryService{Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{}, Now: func() time.Time { return now }}
				incident, err := service.Open(ctx, app.OpenRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, FailedRuntimeInstanceID: started.RuntimeInstanceID, FailedGeneration: 1, FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerMissing, EvidenceRef: "unproven-absolute", EvidenceAt: now, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, IdempotencyKey: "unproven-absolute"})
				if err != nil {
					t.Fatal(err)
				}
				operation = func() error {
					_, err := service.Advance(ctx, app.AdvanceRuntimeRecoveryCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryExhausted, ExpectedIncidentRevision: incident.IncidentRevision, OutcomeReason: "worker_session_absolute_expired", IdempotencyKey: "unproven-expiry"})
					return err
				}
			} else {
				operation = func() error {
					return (workerpostgres.LifecycleCommandHandler{Pool: pool, Now: func() time.Time { return now }}).HandleScheduledCommand(ctx, platformpostgres.ScheduledCommand{TenantID: "tenant-local", TargetType: "WorkerSession", TargetID: started.WorkerSessionID, CommandType: workerpostgres.CommandWorkerSessionAbsolute, ExpectedRevision: active.SessionRevision, DueAt: now})
				}
			}
			now = base.Add(8 * time.Minute)
			if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET absolute_expires_at=$1`, now); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var value string
				if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('session',(SELECT jsonb_agg(to_jsonb(t)) FROM worker_sessions t),'turn',(SELECT jsonb_agg(to_jsonb(t)) FROM agent_turns t),'run',(SELECT jsonb_agg(to_jsonb(t)) FROM worker_runs t),'task',(SELECT jsonb_agg(to_jsonb(t)) FROM tasks t),'runtime',(SELECT jsonb_agg(to_jsonb(t)) FROM runtime_instances t),'quota',(SELECT jsonb_agg(to_jsonb(t)) FROM worker_session_continuity_quotas t),'execution',(SELECT jsonb_agg(to_jsonb(t)) FROM runtime_capacity_executions t),'resident',(SELECT jsonb_agg(to_jsonb(t)) FROM runtime_capacity_residents t),'incident',(SELECT jsonb_agg(to_jsonb(t)) FROM runtime_recovery_incidents t))::text`).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := snapshot()
			if err := operation(); !errors.Is(err, app.CapacityPathNotIntegrated) {
				t.Fatalf("unfenced initial expiry must block: %v", err)
			}
			if after := snapshot(); after != before {
				t.Fatal("unproven expiry changed owned state")
			}
		})
	}
}

func TestInitialTerminalCapacityCancelConsumeRace(t *testing.T) {
	for _, first := range []string{"cancel", "consume"} {
		t.Run(first, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			base := time.Now().UTC().Add(-10 * time.Minute)
			permit, ids := prepareDispatchLockPermit(t, pool, base)
			cancelPool := lockOrderPool(t, pool, "terminal-cancel-race")
			startPool := lockOrderPool(t, pool, "terminal-consume-race")
			firstName, secondName := "terminal-cancel-race", "terminal-consume-race"
			revision := uint64(2)
			if first == "consume" {
				firstName, secondName = secondName, firstName
				revision = 3
			}
			release := holdDispatchTaskUpdate(t, ctx, pool, firstName)
			cancelDone, startDone := make(chan error, 1), make(chan error, 1)
			cancelTask := func() {
				_, err := taskpostgres.NewStore(cancelPool).CancelTask(ctx, taskapp.CancelTaskRecord{Command: taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: permit.TaskID, ExpectedTaskRevision: revision, IdempotencyKey: "race-cancel"}, Now: time.Now(), EventID: "race-cancel-event", TransitionID: "race-cancel-transition", ControlCommandID: "race-cancel-interrupt"})
				cancelDone <- err
			}
			consume := func() {
				_, err := (app.StartWorkerRunService{Store: workerpostgres.NewRunStore(startPool), IDs: ids, Now: time.Now}).Execute(ctx, residentStartCommand(permit))
				startDone <- err
			}
			if first == "cancel" {
				go cancelTask()
			} else {
				go consume()
			}
			waitDispatchLockEdge(t, ctx, pool, firstName, "dispatch-lock-gate")
			if first == "cancel" {
				go consume()
			} else {
				go cancelTask()
			}
			waitDispatchLockEdge(t, ctx, pool, secondName, firstName)
			release()
			cancelErr, startErr := <-cancelDone, <-startDone
			if cancelErr != nil {
				t.Fatal(cancelErr)
			}
			want := 1
			if first == "consume" {
				want = 0
				if startErr != nil {
					t.Fatal(startErr)
				}
			} else if !errors.Is(startErr, workerpostgres.ErrDispatchPermitInvalid) {
				t.Fatalf("cancel winner permitted consume: %v", startErr)
			}
			assertTerminalCapacity(t, pool, want)
			var consumed, interrupts int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime_capacity_executions WHERE consumed_at IS NOT NULL),(SELECT count(*) FROM worker_user_commands WHERE command_type='interrupt')`).Scan(&consumed, &interrupts); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if first == "consume" {
				expected = 1
			}
			if consumed != expected || interrupts != expected {
				t.Fatalf("consumed=%d interrupt=%d expected=%d", consumed, interrupts, expected)
			}
		})
	}
}

func TestInitialTerminalCapacityLegacyAndMissingSnapshot(t *testing.T) {
	for _, kind := range []string{"legacy", "uninitialized", "no_snapshot", "binding_missing_snapshot"} {
		t.Run(kind, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			started, active := startActiveRecoveryFixture(t, pool, time.Now().UTC().Add(-10*time.Minute))
			record := terminalCapacityRecord(started, active)
			var sql string
			switch kind {
			case "legacy":
				sql = `DELETE FROM runtime_capacity_executions; UPDATE worker_runs SET start_snapshot=start_snapshot-'runtime_capacity'`
			case "uninitialized":
				sql = `UPDATE runtime_capacity_pools SET latest_sequence=0,status='capacity_uninitialized',trust_bytes=NULL,trust_digest=NULL`
			case "no_snapshot":
				sql = `UPDATE runtime_capacity_pools SET latest_sequence=999999`
			case "binding_missing_snapshot":
				sql = `UPDATE worker_runs SET start_snapshot=start_snapshot-'runtime_capacity'`
			}
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			_, err := workerpostgres.NewRuntimeResidencyStore(pool).CompleteResidentStage(ctx, record)
			if kind == "binding_missing_snapshot" {
				if !errors.Is(err, app.CapacityBindingMismatch) {
					t.Fatal(err)
				}
				assertTerminalCapacity(t, pool, 0)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind != "legacy" {
				assertTerminalCapacity(t, pool, 1)
			} else {
				var n int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_capacity_executions`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("legacy invented execution: %d %v", n, err)
				}
			}
		})
	}
}

func TestInitialTerminalCapacityCancelRollback(t *testing.T) {
	for _, kind := range []string{"permit", "target", "outbox"} {
		t.Run(kind, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			runtimecapacity.Initialize(t, pool)
			dispatch, commands, _ := initialCapacityQueue(t, pool, 1)
			permit, err := dispatch.IssueTaskPermit(ctx, commands[0])
			if err != nil {
				t.Fatal(err)
			}
			sql := map[string]string{"permit": `UPDATE session_dispatch_permits SET capacity_admission_ref='wrong-token'`, "target": `UPDATE runtime_capacity_executions SET target_digest='sha256:wrong'`, "outbox": `CREATE FUNCTION reject_cancel() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'cancel fault'; END $$; CREATE TRIGGER reject_cancel BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_cancel()`}[kind]
			if _, err = pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var v string
				if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('task',(SELECT jsonb_agg(to_jsonb(t)) FROM tasks t),'permit',(SELECT jsonb_agg(to_jsonb(t)) FROM session_dispatch_permits t),'queue',(SELECT jsonb_agg(to_jsonb(t)) FROM session_task_queue_entries t),'execution',(SELECT jsonb_agg(to_jsonb(t)) FROM runtime_capacity_executions t),'outbox',(SELECT jsonb_agg(to_jsonb(t)) FROM outbox_events t))::text`).Scan(&v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			before := snapshot()
			_, err = taskpostgres.NewStore(pool).CancelTask(ctx, taskapp.CancelTaskRecord{Command: taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: permit.TaskID, ExpectedTaskRevision: 2, IdempotencyKey: "fault-cancel"}, Now: time.Now(), EventID: "fault-cancel-event", TransitionID: "fault-cancel-transition", ControlCommandID: "fault-cancel-interrupt"})
			if err == nil {
				t.Fatal("fault cancellation accepted")
			}
			if after := snapshot(); after != before {
				t.Fatal("cancel fault partially committed")
			}
		})
	}
}

func TestInitialTerminalCapacityHistoricalAbsoluteExpiryCannotReleaseOnReplay(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, active := startActiveRecoveryFixture(t, pool, time.Now().UTC().Add(-10*time.Minute))
	// Adversarial historical DB terminal is not independent physical write fencing.
	if _, err := pool.Exec(ctx, `UPDATE agent_turns SET status='timed_out',revision=revision+1,terminal_at=clock_timestamp(),terminal_reason='worker_session_absolute_expired' WHERE agent_turn_id=$1`, started.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	if err := pool.QueryRow(ctx, `SELECT deadline_at FROM agent_turns WHERE agent_turn_id=$1`, started.AgentTurnID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	err := (workerpostgres.LifecycleCommandHandler{Pool: pool, Now: func() time.Time { return deadline }}).HandleScheduledCommand(ctx, platformpostgres.ScheduledCommand{TenantID: "tenant-local", TargetType: "AgentTurn", TargetID: started.AgentTurnID, CommandType: workerpostgres.CommandAgentTurnDeadline, ExpectedRevision: active.TurnRevision, DueAt: deadline})
	if !errors.Is(err, app.CapacityPathNotIntegrated) {
		t.Fatalf("historical absolute terminal granted release: %v", err)
	}
	assertTerminalCapacity(t, pool, 0)
}

// terminalCapacityR2State includes every owned row the late deadline could
// affect, plus retained commitments and the historical owner's detached graph.
func terminalCapacityR2State(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'turns',(SELECT jsonb_agg(to_jsonb(t) ORDER BY agent_turn_id) FROM agent_turns t),
 'runs',(SELECT jsonb_agg(to_jsonb(t) ORDER BY worker_run_id) FROM worker_runs t),
 'runtimes',(SELECT jsonb_agg(to_jsonb(t) ORDER BY runtime_instance_id) FROM runtime_instances t),
 'sessions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY worker_session_id) FROM worker_sessions t),
 'tasks',(SELECT jsonb_agg(to_jsonb(t) ORDER BY task_id) FROM tasks t),
 'quota',(SELECT jsonb_agg(to_jsonb(t) ORDER BY tenant_id,user_id) FROM worker_session_continuity_quotas t),
 'execution',(SELECT jsonb_agg(to_jsonb(t) ORDER BY token) FROM runtime_capacity_executions t),
 'residents',(SELECT jsonb_agg(to_jsonb(t) ORDER BY commitment_id) FROM runtime_capacity_residents t),
 'permits',(SELECT jsonb_agg(to_jsonb(t) ORDER BY permit_id) FROM session_dispatch_permits t),
 'queue',(SELECT jsonb_agg(to_jsonb(t) ORDER BY worker_session_id,session_queue_sequence) FROM session_task_queue_entries t),
 'scheduled',(SELECT jsonb_agg(to_jsonb(t) ORDER BY scheduled_command_id) FROM scheduled_commands t),
 'transitions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY transition_id) FROM task_transitions t),
 'outbox',(SELECT jsonb_agg(to_jsonb(t) ORDER BY event_id) FROM outbox_events t)
 )::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestInitialTerminalCapacityHistoricalLiveAbsoluteExpiryBlocksLateDeadline(t *testing.T) {
	for _, turnStatus := range []string{"delivered", "consumed"} {
		t.Run(turnStatus, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			started, active := startActiveRecoveryFixture(t, pool, time.Now().UTC().Add(-10*time.Minute))
			var deadline time.Time
			if err := pool.QueryRow(ctx, `SELECT deadline_at FROM agent_turns WHERE agent_turn_id=$1`, started.AgentTurnID).Scan(&deadline); err != nil {
				t.Fatal(err)
			}
			historicalAt := deadline.Add(-time.Hour)
			// Explicit historical fault reproduces the old live absolute-expiry owner's
			// Run/Session/Task/quota writes. It is not physical or signed fencing evidence.
			// Original Turn identity, immutable deadline, ledger, permit and snapshot stay.
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if turnStatus == "consumed" {
				if _, err = tx.Exec(ctx, `UPDATE agent_turns SET status='consumed',revision=revision+1 WHERE agent_turn_id=$1`, started.AgentTurnID); err != nil {
					t.Fatal(err)
				}
				active.TurnRevision++
			}
			if _, err = tx.Exec(ctx, `UPDATE worker_runs SET status='timed_out',revision=revision+1,terminal_at=$2,terminal_reason='worker_session_absolute_expired' WHERE worker_run_id=$1`, started.WorkerRunID, historicalAt); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE tasks SET status='waiting_user',waiting_reason='{"code":"worker_session_expired"}'::jsonb,current_worker_run_id=NULL,worker_session_id=NULL,revision=revision+1,updated_at=$2 WHERE task_id=$1`, started.TaskID, historicalAt); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE worker_sessions SET status='expired',bound_task_id=NULL,active_worker_run_id=NULL,session_revision=session_revision+1,absolute_expires_at=$2,terminal_at=$2,terminal_reason='worker_session_absolute_expired',updated_at=$2 WHERE worker_session_id=$1`, started.WorkerSessionID, historicalAt); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE worker_session_continuity_quotas SET reserved_count=reserved_count-1,revision=revision+1,updated_at=$1 WHERE tenant_id='tenant-local' AND user_id='user-local'`, historicalAt); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			before := terminalCapacityR2State(t, pool)
			command := platformpostgres.ScheduledCommand{TenantID: "tenant-local", TargetType: "AgentTurn", TargetID: started.AgentTurnID, CommandType: workerpostgres.CommandAgentTurnDeadline, ExpectedRevision: active.TurnRevision, DueAt: deadline}
			handler := workerpostgres.LifecycleCommandHandler{Pool: pool, Now: func() time.Time { return deadline }}
			for attempt := 0; attempt < 2; attempt++ {
				err = handler.HandleScheduledCommand(ctx, command)
				if !errors.Is(err, app.CapacityPathNotIntegrated) {
					var status, reason string
					var released bool
					if e := pool.QueryRow(ctx, `SELECT t.status,t.terminal_reason,e.released_at IS NOT NULL FROM agent_turns t JOIN runtime_capacity_executions e ON e.turn_id=t.agent_turn_id WHERE t.agent_turn_id=$1`, started.AgentTurnID).Scan(&status, &reason, &released); e != nil {
						t.Fatal(e)
					}
					t.Fatalf("late deadline must block original Run absolute expiry: error=%v Turn=%s/%s execution_released=%t", err, status, reason, released)
				}
				if after := terminalCapacityR2State(t, pool); after != before {
					t.Fatal("late deadline partially mutated historical graph or commitments")
				}
			}
			assertTerminalCapacity(t, pool, 0)
		})
	}
}

func TestInitialTerminalCapacityOrdinaryTerminalReplayAfterSessionExpiry(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, active := startActiveRecoveryFixture(t, pool, time.Now().UTC().Add(-10*time.Minute))
	if _, err := workerpostgres.NewRuntimeResidencyStore(pool).CompleteResidentStage(ctx, terminalCapacityRecord(started, active)); err != nil {
		t.Fatal(err)
	}
	// Later Session state is independent of the original completed Run's reason.
	// This counterexample must not turn current Session status into a release gate.
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status='expired',bound_task_id=NULL,session_revision=session_revision+1,terminal_at=clock_timestamp(),terminal_reason='worker_session_absolute_expired',updated_at=clock_timestamp() WHERE worker_session_id=$1`, started.WorkerSessionID); err != nil {
		t.Fatal(err)
	}
	before := terminalCapacityR2State(t, pool)
	var deadline time.Time
	if err := pool.QueryRow(ctx, `SELECT deadline_at FROM agent_turns WHERE agent_turn_id=$1`, started.AgentTurnID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	handler := workerpostgres.LifecycleCommandHandler{Pool: pool, Now: func() time.Time { return deadline }}
	command := platformpostgres.ScheduledCommand{TenantID: "tenant-local", TargetType: "AgentTurn", TargetID: started.AgentTurnID, CommandType: workerpostgres.CommandAgentTurnDeadline, ExpectedRevision: active.TurnRevision, DueAt: deadline}
	for i := 0; i < 2; i++ {
		if err := handler.HandleScheduledCommand(ctx, command); err != nil {
			t.Fatal(err)
		}
		if terminalCapacityR2State(t, pool) != before {
			t.Fatal("ordinary terminal replay changed original or current ownership")
		}
	}
	assertTerminalCapacity(t, pool, 1)
}
