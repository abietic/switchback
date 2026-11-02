//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeControlResumeArmsImmutableFirstHeartbeatWatchdog(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	completedAt := base.Add(6 * time.Minute)
	current := completedAt
	service := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return current },
	}
	if _, err := service.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := completeRuntimeControlClaim(t, service, plan)

	var (
		anchorKind                         string
		anchorRevision, watchdogRevision   uint64
		anchorAt, suspectDue, unhealthyDue time.Time
		status                             string
	)
	if err := pool.QueryRow(ctx, `
SELECT anchor_kind,anchor_revision,first_authenticated_resume_at,
       suspect_due_at,unhealthy_due_at,
       status,revision
FROM runtime_first_heartbeat_watchdogs
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&anchorKind,
		&anchorRevision,
		&anchorAt,
		&suspectDue,
		&unhealthyDue,
		&status,
		&watchdogRevision,
	); err != nil {
		t.Fatal(err)
	}
	if anchorKind != "resume_complete_v1" ||
		anchorRevision != completed.ResumeCompletedRevision ||
		!anchorAt.Equal(completedAt) ||
		!suspectDue.Equal(completedAt.Add(workerapplication.RuntimeSuspectAfter)) ||
		!unhealthyDue.Equal(completedAt.Add(workerapplication.RuntimeUnhealthyAfter)) ||
		status != "awaiting" ||
		watchdogRevision != 1 {
		t.Fatalf(
			"armed watchdog kind=%s anchor_revision=%d anchor=%s suspect=%s unhealthy=%s status=%s revision=%d completed=%+v",
			anchorKind,
			anchorRevision,
			anchorAt,
			suspectDue,
			unhealthyDue,
			status,
			watchdogRevision,
			completed,
		)
	}

	var (
		commandCount      int
		commandDue        time.Time
		commandExpected   uint64
		commandPayload    []byte
		commandStatus     string
		commandTarget     string
		commandTargetID   string
		commandIdempotent string
	)
	if err := pool.QueryRow(ctx, `
SELECT count(*),min(due_at),min(expected_revision),min(payload::text),
       min(status),min(target_type),min(target_id),min(idempotency_key)
FROM scheduled_commands
WHERE command_type='runtime_first_heartbeat_evaluate'
  AND target_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&commandCount,
		&commandDue,
		&commandExpected,
		&commandPayload,
		&commandStatus,
		&commandTarget,
		&commandTargetID,
		&commandIdempotent,
	); err != nil {
		t.Fatal(err)
	}
	if commandCount != 1 ||
		!commandDue.Equal(suspectDue) ||
		commandExpected != 1 ||
		commandStatus != "pending" ||
		commandTarget != "RuntimeFirstHeartbeatWatchdog" ||
		commandTargetID != started.RuntimeInstanceID ||
		commandIdempotent != "first-heartbeat/1/1/suspect" {
		t.Fatalf(
			"armed command count=%d due=%s expected=%d status=%s target=%s/%s key=%s payload=%s",
			commandCount,
			commandDue,
			commandExpected,
			commandStatus,
			commandTarget,
			commandTargetID,
			commandIdempotent,
			commandPayload,
		)
	}
	var payload struct {
		RuntimeGeneration uint64 `json:"runtime_generation"`
		ChannelEpoch      uint64 `json:"channel_epoch"`
		AnchorKind        string `json:"anchor_kind"`
		AnchorRevision    uint64 `json:"anchor_revision"`
		Stage             string `json:"stage"`
	}
	if err := json.Unmarshal(commandPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RuntimeGeneration != 1 ||
		payload.ChannelEpoch != 1 ||
		payload.AnchorKind != anchorKind ||
		payload.AnchorRevision != anchorRevision ||
		payload.Stage != "suspect" {
		t.Fatalf("armed command payload=%+v", payload)
	}

	if _, err := service.Disconnect(ctx, authorityFrom(completed)); err != nil {
		t.Fatal(err)
	}
	current = completedAt.Add(44 * time.Second)
	reconnectedPlan, err := service.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 2, "backend-2"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = completeRuntimeControlClaim(t, service, reconnectedPlan)

	var (
		reconnectedAnchor   time.Time
		reconnectedRevision uint64
		reconnectedCommands int
	)
	if err := pool.QueryRow(ctx, `
SELECT watchdog.first_authenticated_resume_at,watchdog.revision,
       count(command.scheduled_command_id)
FROM runtime_first_heartbeat_watchdogs watchdog
LEFT JOIN scheduled_commands command
  ON command.command_type='runtime_first_heartbeat_evaluate'
 AND command.target_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1
GROUP BY watchdog.first_authenticated_resume_at,watchdog.revision`,
		started.RuntimeInstanceID,
	).Scan(
		&reconnectedAnchor,
		&reconnectedRevision,
		&reconnectedCommands,
	); err != nil {
		t.Fatal(err)
	}
	if !reconnectedAnchor.Equal(completedAt) ||
		reconnectedRevision != 1 ||
		reconnectedCommands != 1 {
		t.Fatalf(
			"reconnect moved first-heartbeat anchor: anchor=%s revision=%d commands=%d",
			reconnectedAnchor,
			reconnectedRevision,
			reconnectedCommands,
		)
	}
}

func TestFirstRealHeartbeatAtomicallySatisfiesResumeWatchdog(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	service := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return resumeAt },
	}
	if _, err := service.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = completeRuntimeControlClaim(t, service, plan)

	heartbeatAt := resumeAt.Add(15 * time.Second)
	heartbeat, err := (workerapplication.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   &concurrentAllocationIDs{},
	}).Observe(ctx, workerapplication.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID:   started.WorkerSessionID,
		RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1,
		ChannelEpoch:      1,
		HeartbeatSequence: 1,
		ControlSequence:   7,
		ReceivedAt:        heartbeatAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.HeartbeatSequence != 1 ||
		!heartbeat.LastReceivedAt.Equal(heartbeatAt) {
		t.Fatalf("first heartbeat=%+v", heartbeat)
	}

	var (
		status               string
		firstSequence        uint64
		firstControlSequence uint64
		firstReceivedAt      time.Time
		watchdogRevision     uint64
		pending, cancelled   int
	)
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.first_heartbeat_sequence,
       watchdog.first_heartbeat_control_sequence,
       watchdog.first_heartbeat_received_at,watchdog.revision,
       count(command.scheduled_command_id) FILTER (WHERE command.status='pending'),
       count(command.scheduled_command_id) FILTER (WHERE command.status='cancelled')
FROM runtime_first_heartbeat_watchdogs watchdog
LEFT JOIN scheduled_commands command
  ON command.command_type='runtime_first_heartbeat_evaluate'
 AND command.target_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1
GROUP BY watchdog.status,watchdog.first_heartbeat_sequence,
         watchdog.first_heartbeat_control_sequence,
         watchdog.first_heartbeat_received_at,watchdog.revision`,
		started.RuntimeInstanceID,
	).Scan(
		&status,
		&firstSequence,
		&firstControlSequence,
		&firstReceivedAt,
		&watchdogRevision,
		&pending,
		&cancelled,
	); err != nil {
		t.Fatal(err)
	}
	if status != "satisfied" ||
		firstSequence != 1 ||
		firstControlSequence != 7 ||
		!firstReceivedAt.Equal(heartbeatAt) ||
		watchdogRevision != 2 ||
		pending != 0 ||
		cancelled != 1 {
		t.Fatalf(
			"satisfied watchdog status=%s sequence=%d control=%d received=%s revision=%d pending=%d cancelled=%d",
			status,
			firstSequence,
			firstControlSequence,
			firstReceivedAt,
			watchdogRevision,
			pending,
			cancelled,
		)
	}
}

func TestHeartbeatPersistedBeforeResumeCompletePreventsWatchdogArm(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	heartbeatAt := base.Add(6 * time.Minute)
	if _, err := (workerapplication.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   &concurrentAllocationIDs{},
	}).Observe(ctx, workerapplication.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID:   started.WorkerSessionID,
		RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1,
		ChannelEpoch:      1,
		HeartbeatSequence: 1,
		ControlSequence:   1,
		ReceivedAt:        heartbeatAt,
	}); err != nil {
		t.Fatal(err)
	}

	service := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return heartbeatAt.Add(time.Second) },
	}
	if _, err := service.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = completeRuntimeControlClaim(t, service, plan)

	var watchdogCount, commandCount int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM runtime_first_heartbeat_watchdogs WHERE runtime_instance_id=$1),
  (SELECT count(*) FROM scheduled_commands
   WHERE command_type='runtime_first_heartbeat_evaluate' AND target_id=$1)`,
		started.RuntimeInstanceID,
	).Scan(&watchdogCount, &commandCount); err != nil {
		t.Fatal(err)
	}
	if watchdogCount != 0 || commandCount != 0 {
		t.Fatalf(
			"persisted heartbeat still armed watchdog: watchdogs=%d commands=%d",
			watchdogCount,
			commandCount,
		)
	}
}

func TestAcceptedHeartbeatIsProjectedBeforeWatchdogDecision(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 30, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	current := resumeAt
	journal := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return current },
	}
	if _, err := journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := completeRuntimeControlClaim(t, journal, plan)
	suspectCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"suspect",
	)

	current = resumeAt.Add(10 * time.Second)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authorityFrom(completed),
			Sequence:       1,
			MessageID:      "business-event-1",
			IdempotencyKey: "business-event-1",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"business-event-1",
				"business-event-1",
				"runtime.update",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	current = resumeAt.Add(20 * time.Second)
	earlyEncoded := runtimeControlEnvelope(
		started,
		"heartbeat",
		2,
		"heartbeat-early",
		"heartbeat-early",
		"runtime.heartbeat",
	)
	earlyEnvelope, err :=
		contractsv1.DecodeRuntimeControlEnvelope(earlyEncoded)
	if err != nil {
		t.Fatal(err)
	}
	earlyEnvelope.Payload = json.RawMessage(`{"heartbeat_sequence":1}`)
	earlyEncoded, err = json.Marshal(earlyEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authorityFrom(completed),
			Sequence:       2,
			MessageID:      "heartbeat-early",
			IdempotencyKey: "heartbeat-early",
			Envelope:       earlyEncoded,
		},
	); err != nil {
		t.Fatal(err)
	}

	current = resumeAt.Add(89*time.Second + 999*time.Millisecond)
	lateEncoded := runtimeControlEnvelope(
		started,
		"heartbeat",
		3,
		"heartbeat-late",
		"heartbeat-late",
		"runtime.heartbeat",
	)
	lateEnvelope, err :=
		contractsv1.DecodeRuntimeControlEnvelope(lateEncoded)
	if err != nil {
		t.Fatal(err)
	}
	lateEnvelope.Payload = json.RawMessage(`{"heartbeat_sequence":2}`)
	lateEncoded, err = json.Marshal(lateEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authorityFrom(completed),
			Sequence:       3,
			MessageID:      "heartbeat-late",
			IdempotencyKey: "heartbeat-late",
			Envelope:       lateEncoded,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Message.EffectStatus != "received" ||
		accepted.Channel.EventCommittedSequence != 3 ||
		accepted.Channel.EventAcknowledgedSequence != 3 ||
		accepted.Channel.EventAppliedSequence != 0 {
		t.Fatalf("accepted heartbeat=%+v", accepted)
	}
	var observationCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM runtime_heartbeat_observations
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(&observationCount); err != nil {
		t.Fatal(err)
	}
	if observationCount != 0 {
		t.Fatalf(
			"heartbeat observer unexpectedly ran before watchdog repair: %d",
			observationCount,
		)
	}

	current = resumeAt.Add(90 * time.Second)
	ids := &concurrentAllocationIDs{}
	handler := workerpostgres.RuntimeFirstHeartbeatCommandHandler{
		Watchdog: workerapplication.RuntimeFirstHeartbeatService{
			Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
	}
	if err := handler.HandleScheduledCommand(
		ctx,
		suspectCommand,
	); err != nil {
		t.Fatal(err)
	}

	var (
		watchdogStatus, runtimeStatus, effectStatus string
		firstSequence, firstControlSequence         uint64
		firstReceivedAt                             time.Time
		heartbeatSequence, observationRevision      uint64
		eventAppliedSequence                        uint64
		livenessCommands, incidents                 int
	)
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.first_heartbeat_sequence,
       watchdog.first_heartbeat_control_sequence,
       watchdog.first_heartbeat_received_at,
       runtime.status,
       observation.heartbeat_sequence,observation.revision,
       message.effect_status,channel.event_applied_sequence,
       (SELECT count(*) FROM scheduled_commands
        WHERE command_type='runtime_liveness_evaluate'
          AND target_id=watchdog.runtime_instance_id
          AND status='pending'),
       (SELECT count(*) FROM runtime_recovery_incidents
        WHERE failed_runtime_instance_id=watchdog.runtime_instance_id)
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
JOIN runtime_heartbeat_observations observation
  ON observation.runtime_instance_id=watchdog.runtime_instance_id
	JOIN runtime_control_messages message
	  ON message.runtime_instance_id=watchdog.runtime_instance_id
	 AND message.direction='event' AND message.sequence=3
JOIN runtime_control_channels channel
  ON channel.runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&watchdogStatus,
		&firstSequence,
		&firstControlSequence,
		&firstReceivedAt,
		&runtimeStatus,
		&heartbeatSequence,
		&observationRevision,
		&effectStatus,
		&eventAppliedSequence,
		&livenessCommands,
		&incidents,
	); err != nil {
		t.Fatal(err)
	}
	if watchdogStatus != "satisfied" ||
		firstSequence != 1 ||
		firstControlSequence != 2 ||
		!firstReceivedAt.Equal(resumeAt.Add(20*time.Second)) ||
		runtimeStatus != "active" ||
		heartbeatSequence != 2 ||
		observationRevision != 2 ||
		effectStatus != "received" ||
		eventAppliedSequence != 0 ||
		livenessCommands != 1 ||
		incidents != 0 {
		t.Fatalf(
			"journal repair watchdog=%s first=%d/%d/%s runtime=%s observation=%d/r%d journal=%s/applied-%d liveness=%d incidents=%d",
			watchdogStatus,
			firstSequence,
			firstControlSequence,
			firstReceivedAt,
			runtimeStatus,
			heartbeatSequence,
			observationRevision,
			effectStatus,
			eventAppliedSequence,
			livenessCommands,
			incidents,
		)
	}

	applyHeartbeat := workerapplication.ApplyRuntimeControlEventCommand{
		Authority:      authorityFrom(completed),
		Sequence:       3,
		MessageID:      "heartbeat-late",
		IdempotencyKey: "heartbeat-late",
		Outcome: json.RawMessage(
			`{"status":"adapter_replay_applied"}`,
		),
	}
	if _, err := journal.ApplyEvent(
		ctx,
		applyHeartbeat,
	); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlSequenceGap,
	) {
		t.Fatalf("heartbeat bypassed pending business event err=%v", err)
	}
	if _, err := journal.ApplyEvent(
		ctx,
		workerapplication.ApplyRuntimeControlEventCommand{
			Authority:      authorityFrom(completed),
			Sequence:       1,
			MessageID:      "business-event-1",
			IdempotencyKey: "business-event-1",
			Outcome:        json.RawMessage(`{"status":"applied"}`),
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := (workerapplication.RuntimeHeartbeatObserver{
		Service: workerapplication.RuntimeHeartbeatService{
			Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
			IDs:   ids,
		},
	}).ObserveWorkerControlEvent(
		ctx,
		workerapplication.WorkerControlEvent{
			Envelope: earlyEnvelope,
			Payload:  earlyEnvelope.Payload,
		},
		resumeAt.Add(20*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ApplyEvent(
		ctx,
		workerapplication.ApplyRuntimeControlEventCommand{
			Authority:      authorityFrom(completed),
			Sequence:       2,
			MessageID:      "heartbeat-early",
			IdempotencyKey: "heartbeat-early",
			Outcome: json.RawMessage(
				`{"status":"adapter_replay_applied"}`,
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := (workerapplication.RuntimeHeartbeatObserver{
		Service: workerapplication.RuntimeHeartbeatService{
			Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
			IDs:   ids,
		},
	}).ObserveWorkerControlEvent(
		ctx,
		workerapplication.WorkerControlEvent{
			Envelope: lateEnvelope,
			Payload:  lateEnvelope.Payload,
		},
		resumeAt.Add(89*time.Second+999*time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}
	appliedHeartbeat, err := journal.ApplyEvent(
		ctx,
		applyHeartbeat,
	)
	if err != nil {
		t.Fatal(err)
	}
	if appliedHeartbeat.Channel.EventAppliedSequence != 3 {
		t.Fatalf("applied heartbeat=%+v", appliedHeartbeat)
	}
	if err := pool.QueryRow(ctx, `
SELECT observation.revision,
       watchdog.first_heartbeat_sequence,
       watchdog.first_heartbeat_control_sequence,
       count(command.scheduled_command_id)
         FILTER (WHERE command.status='pending')
FROM runtime_heartbeat_observations observation
JOIN runtime_first_heartbeat_watchdogs watchdog
  ON watchdog.runtime_instance_id=observation.runtime_instance_id
LEFT JOIN scheduled_commands command
  ON command.command_type='runtime_liveness_evaluate'
 AND command.target_id=observation.runtime_instance_id
WHERE observation.runtime_instance_id=$1
GROUP BY observation.revision,
         watchdog.first_heartbeat_sequence,
         watchdog.first_heartbeat_control_sequence`,
		started.RuntimeInstanceID,
	).Scan(
		&observationRevision,
		&firstSequence,
		&firstControlSequence,
		&livenessCommands,
	); err != nil {
		t.Fatal(err)
	}
	if observationRevision != 2 ||
		firstSequence != 1 ||
		firstControlSequence != 2 ||
		livenessCommands != 1 {
		t.Fatalf(
			"ordered replay changed repaired proof observation=r%d first=%d/%d liveness=%d",
			observationRevision,
			firstSequence,
			firstControlSequence,
			livenessCommands,
		)
	}
}

func TestInvalidOrBoundaryJournalHeartbeatDoesNotSatisfyWatchdog(
	t *testing.T,
) {
	tests := []struct {
		name      string
		accepted  time.Duration
		payload   json.RawMessage
		messageID string
	}{
		{
			name:      "malformed payload",
			accepted:  30 * time.Second,
			payload:   json.RawMessage(`{}`),
			messageID: "heartbeat-malformed",
		},
		{
			name:      "exact unhealthy boundary",
			accepted:  90 * time.Second,
			payload:   json.RawMessage(`{"heartbeat_sequence":1}`),
			messageID: "heartbeat-boundary",
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Date(
				2026,
				7,
				27,
				3,
				40+index,
				0,
				0,
				time.UTC,
			)
			started, _ := startActiveRecoveryFixture(t, pool, base)
			resumeAt := base.Add(6 * time.Minute)
			current := resumeAt
			journal := workerapplication.RuntimeControlJournalService{
				Store: workerpostgres.NewRuntimeControlJournal(pool),
				Now:   func() time.Time { return current },
			}
			if _, err := journal.Bind(
				ctx,
				runtimeControlBind(started),
			); err != nil {
				t.Fatal(err)
			}
			plan, err := journal.Claim(
				ctx,
				runtimeControlClaim(
					started,
					"process-1",
					1,
					"backend-1",
				),
			)
			if err != nil {
				t.Fatal(err)
			}
			completed := completeRuntimeControlClaim(
				t,
				journal,
				plan,
			)
			suspectCommand := loadFirstHeartbeatScheduledCommand(
				t,
				pool,
				started.RuntimeInstanceID,
				"suspect",
			)

			current = resumeAt.Add(test.accepted)
			encoded := runtimeControlEnvelope(
				started,
				"heartbeat",
				1,
				test.messageID,
				test.messageID,
				"runtime.heartbeat",
			)
			envelope, err :=
				contractsv1.DecodeRuntimeControlEnvelope(encoded)
			if err != nil {
				t.Fatal(err)
			}
			envelope.Payload = test.payload
			encoded, err = json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := journal.AcceptEvent(
				ctx,
				workerapplication.AcceptRuntimeControlEventCommand{
					Authority:      authorityFrom(completed),
					Sequence:       1,
					MessageID:      test.messageID,
					IdempotencyKey: test.messageID,
					Envelope:       encoded,
				},
			); err != nil {
				t.Fatal(err)
			}

			current = resumeAt.Add(90 * time.Second)
			ids := &concurrentAllocationIDs{}
			handler :=
				workerpostgres.RuntimeFirstHeartbeatCommandHandler{
					Watchdog: workerapplication.
						RuntimeFirstHeartbeatService{
						Store: workerpostgres.
							NewRuntimeFirstHeartbeatStore(pool),
						IDs: ids,
						Now: func() time.Time {
							return current
						},
					},
					Recovery: workerapplication.
						RuntimeRecoveryService{
						Store: workerpostgres.
							NewRuntimeRecoveryStore(pool),
						IDs: ids,
						Now: func() time.Time {
							return current
						},
					},
				}
			if err := handler.HandleScheduledCommand(
				ctx,
				suspectCommand,
			); err != nil {
				t.Fatal(err)
			}

			var (
				watchdogStatus, runtimeStatus string
				observationCount, incidents   int
			)
			if err := pool.QueryRow(ctx, `
SELECT watchdog.status,runtime.status,
       (SELECT count(*) FROM runtime_heartbeat_observations
        WHERE runtime_instance_id=watchdog.runtime_instance_id),
       (SELECT count(*) FROM runtime_recovery_incidents
        WHERE failed_runtime_instance_id=watchdog.runtime_instance_id)
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
				started.RuntimeInstanceID,
			).Scan(
				&watchdogStatus,
				&runtimeStatus,
				&observationCount,
				&incidents,
			); err != nil {
				t.Fatal(err)
			}
			if watchdogStatus != "unhealthy" ||
				runtimeStatus != "unhealthy" ||
				observationCount != 0 ||
				incidents != 1 {
				t.Fatalf(
					"invalid evidence watchdog=%s runtime=%s observations=%d incidents=%d",
					watchdogStatus,
					runtimeStatus,
					observationCount,
					incidents,
				)
			}
		})
	}
}

func TestHeartbeatAcceptedAfterUnhealthyDecisionFailsClosed(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 50, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	current := resumeAt
	journal := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return current },
	}
	if _, err := journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := completeRuntimeControlClaim(t, journal, plan)
	suspectCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"suspect",
	)
	current = resumeAt.Add(90 * time.Second)
	ids := &concurrentAllocationIDs{}
	handler := workerpostgres.RuntimeFirstHeartbeatCommandHandler{
		Watchdog: workerapplication.RuntimeFirstHeartbeatService{
			Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
	}
	if err := handler.HandleScheduledCommand(
		ctx,
		suspectCommand,
	); err != nil {
		t.Fatal(err)
	}

	current = current.Add(time.Microsecond)
	encoded := runtimeControlEnvelope(
		started,
		"heartbeat",
		1,
		"heartbeat-too-late",
		"heartbeat-too-late",
		"runtime.heartbeat",
	)
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(encoded)
	if err != nil {
		t.Fatal(err)
	}
	envelope.Payload = json.RawMessage(`{"heartbeat_sequence":1}`)
	encoded, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	_, err = journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authorityFrom(completed),
			Sequence:       1,
			MessageID:      "heartbeat-too-late",
			IdempotencyKey: "heartbeat-too-late",
			Envelope:       encoded,
		},
	)
	if !errors.Is(
		err,
		workerapplication.ErrRuntimeControlIdentityConflict,
	) {
		t.Fatalf("late heartbeat acceptance err=%v", err)
	}
	var messages, committed uint64
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM runtime_control_messages
   WHERE runtime_instance_id=$1 AND direction='event'),
  (SELECT event_committed_sequence FROM runtime_control_channels
   WHERE runtime_instance_id=$1)`,
		started.RuntimeInstanceID,
	).Scan(&messages, &committed); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || committed != 0 {
		t.Fatalf(
			"late heartbeat was journaled messages=%d committed=%d",
			messages,
			committed,
		)
	}
}

func TestFirstHeartbeatEvaluatorYieldsToPendingRuntimeEventSettlement(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 55, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	current := resumeAt
	journal := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return current },
	}
	if _, err := journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := completeRuntimeControlClaim(t, journal, plan)
	suspectCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"suspect",
	)

	current = resumeAt.Add(30 * time.Second)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authorityFrom(completed),
			Sequence:       1,
			MessageID:      "terminal-before-first-heartbeat",
			IdempotencyKey: "terminal-before-first-heartbeat",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"terminal-before-first-heartbeat",
				"terminal-before-first-heartbeat",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	var runtimeRevisionBefore uint64
	if err := pool.QueryRow(ctx, `
SELECT revision
FROM runtime_instances
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(&runtimeRevisionBefore); err != nil {
		t.Fatal(err)
	}

	current = resumeAt.Add(90 * time.Second)
	ids := &concurrentAllocationIDs{}
	handler := workerpostgres.RuntimeFirstHeartbeatCommandHandler{
		Watchdog: workerapplication.RuntimeFirstHeartbeatService{
			Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
	}
	if err := handler.HandleScheduledCommand(
		ctx,
		suspectCommand,
	); err != nil {
		t.Fatal(err)
	}

	var (
		watchdogStatus, terminalReason string
		runtimeStatus, sourceKind      string
		runtimeRevision                uint64
		incidentCount                  int
	)
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.terminal_reason,
       runtime.status,runtime.revision,settlement.source_kind,
       (SELECT count(*) FROM runtime_recovery_incidents
        WHERE failed_runtime_instance_id=watchdog.runtime_instance_id)
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
JOIN worker_turn_settlements settlement
  ON settlement.runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&watchdogStatus,
		&terminalReason,
		&runtimeStatus,
		&runtimeRevision,
		&sourceKind,
		&incidentCount,
	); err != nil {
		t.Fatal(err)
	}
	if watchdogStatus != "cancelled" ||
		terminalReason != "turn_outcome_owned" ||
		runtimeStatus != "active" ||
		runtimeRevision != runtimeRevisionBefore ||
		sourceKind != "runtime_event" ||
		incidentCount != 0 {
		t.Fatalf(
			"outcome-owned watchdog=%s/%s runtime=%s/r%d source=%s incidents=%d",
			watchdogStatus,
			terminalReason,
			runtimeStatus,
			runtimeRevision,
			sourceKind,
			incidentCount,
		)
	}
}

func TestFirstHeartbeatEvaluatorSerializesBehindEarlierTerminalOutcomeAuthority(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 57, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	journal := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return resumeAt },
	}
	if _, err := journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := completeRuntimeControlClaim(t, journal, plan)
	suspectCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"suspect",
	)
	var runtimeRevisionBefore uint64
	if err := pool.QueryRow(ctx, `
SELECT revision
FROM runtime_instances
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(&runtimeRevisionBefore); err != nil {
		t.Fatal(err)
	}

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	lockKey := "worker-runtime:agent-turn-outcome:" + started.AgentTurnID
	if _, err := blocker.Exec(
		ctx,
		`SELECT pg_advisory_lock(hashtextextended($1, 0))`,
		lockKey,
	); err != nil {
		t.Fatal(err)
	}
	lockHeld := true
	unlock := func() {
		if !lockHeld {
			return
		}
		var unlocked bool
		if err := blocker.QueryRow(
			ctx,
			`SELECT pg_advisory_unlock(hashtextextended($1, 0))`,
			lockKey,
		).Scan(&unlocked); err != nil {
			t.Errorf("unlock first-heartbeat outcome barrier: %v", err)
		} else if !unlocked {
			t.Error("first-heartbeat outcome barrier was not held")
		}
		lockHeld = false
	}
	defer unlock()

	terminalAt := resumeAt.Add(30 * time.Second)
	terminalResult := make(chan error, 1)
	go func() {
		_, err := (workerapplication.RuntimeControlJournalService{
			Store: workerpostgres.NewRuntimeControlJournal(pool),
			Now:   func() time.Time { return terminalAt },
		}).AcceptEvent(
			ctx,
			workerapplication.AcceptRuntimeControlEventCommand{
				Authority:      authorityFrom(completed),
				Sequence:       1,
				MessageID:      "terminal-before-concurrent-first-heartbeat",
				IdempotencyKey: "terminal-before-concurrent-first-heartbeat",
				Envelope: runtimeControlEnvelope(
					started,
					"event",
					1,
					"terminal-before-concurrent-first-heartbeat",
					"terminal-before-concurrent-first-heartbeat",
					"agent_turn.terminal",
				),
			},
		)
		terminalResult <- err
	}()
	waiters, err := waitForAgentTurnOutcomeWaiters(
		ctx,
		pool,
		lockKey,
		1,
		3*time.Second,
	)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if waiters != 1 {
		unlock()
		t.Fatalf("terminal outcome lock waiters=%d", waiters)
	}

	evaluationResult := make(
		chan workerapplication.RuntimeFirstHeartbeatEvaluation,
		1,
	)
	evaluationErr := make(chan error, 1)
	evaluatedAt := resumeAt.Add(workerapplication.RuntimeUnhealthyAfter)
	go func() {
		evaluation, err :=
			(workerapplication.RuntimeFirstHeartbeatService{
				Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
				IDs:   &concurrentAllocationIDs{},
				Now:   func() time.Time { return evaluatedAt },
			}).Evaluate(
				ctx,
				firstHeartbeatEvaluateCommand(suspectCommand),
			)
		if err == nil {
			evaluationResult <- evaluation
		}
		evaluationErr <- err
	}()
	waiters, err = waitForAgentTurnOutcomeWaiters(
		ctx,
		pool,
		lockKey,
		2,
		3*time.Second,
	)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	if waiters != 2 {
		t.Fatalf(
			"terminal and first-heartbeat evaluator outcome waiters=%d",
			waiters,
		)
	}

	select {
	case err := <-terminalResult:
		if err != nil {
			t.Fatalf("terminal Accept after outcome barrier: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal Accept did not finish after outcome barrier")
	}
	var evaluation workerapplication.RuntimeFirstHeartbeatEvaluation
	select {
	case err := <-evaluationErr:
		if err != nil {
			t.Fatalf("first-heartbeat evaluation after terminal: %v", err)
		}
		evaluation = <-evaluationResult
	case <-time.After(5 * time.Second):
		t.Fatal("first-heartbeat evaluator did not finish after outcome barrier")
	}
	if !evaluation.Applied ||
		evaluation.Status !=
			workerapplication.RuntimeFirstHeartbeatCancelled ||
		evaluation.TerminalReason !=
			workerapplication.RuntimeFirstHeartbeatTurnOutcome ||
		evaluation.OpenRecovery != nil {
		t.Fatalf("serialized first-heartbeat evaluation=%+v", evaluation)
	}

	var (
		watchdogStatus, terminalReason string
		runtimeStatus, sourceKind      string
		runtimeRevision                uint64
		incidentCount                  int
	)
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.terminal_reason,
       runtime.status,runtime.revision,settlement.source_kind,
       (SELECT count(*) FROM runtime_recovery_incidents
        WHERE failed_runtime_instance_id=watchdog.runtime_instance_id)
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
JOIN worker_turn_settlements settlement
  ON settlement.runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&watchdogStatus,
		&terminalReason,
		&runtimeStatus,
		&runtimeRevision,
		&sourceKind,
		&incidentCount,
	); err != nil {
		t.Fatal(err)
	}
	if watchdogStatus != "cancelled" ||
		terminalReason != "turn_outcome_owned" ||
		runtimeStatus != "active" ||
		runtimeRevision != runtimeRevisionBefore+1 ||
		sourceKind != "runtime_event" ||
		incidentCount != 0 {
		t.Fatalf(
			"serialized outcome watchdog=%s/%s runtime=%s/r%d source=%s incidents=%d",
			watchdogStatus,
			terminalReason,
			runtimeStatus,
			runtimeRevision,
			sourceKind,
			incidentCount,
		)
	}
}

func TestPendingOutcomeAtFirstHeartbeatUnhealthyBoundaryStillRecordsAuditObservation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 58, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	current := resumeAt
	journal := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return current },
	}
	if _, err := journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := completeRuntimeControlClaim(t, journal, plan)
	suspectCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"suspect",
	)
	ids := &concurrentAllocationIDs{}
	handler := workerpostgres.RuntimeFirstHeartbeatCommandHandler{
		Watchdog: workerapplication.RuntimeFirstHeartbeatService{
			Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
	}
	current = resumeAt.Add(workerapplication.RuntimeSuspectAfter)
	if err := handler.HandleScheduledCommand(
		ctx,
		suspectCommand,
	); err != nil {
		t.Fatal(err)
	}
	unhealthyCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"unhealthy",
	)

	current = resumeAt.Add(60 * time.Second)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authorityFrom(completed),
			Sequence:       1,
			MessageID:      "terminal-before-boundary-heartbeat",
			IdempotencyKey: "terminal-before-boundary-heartbeat",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"terminal-before-boundary-heartbeat",
				"terminal-before-boundary-heartbeat",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	var (
		runtimeStatusBefore   string
		runtimeRevisionBefore uint64
		livenessPendingBefore int
	)
	if err := pool.QueryRow(ctx, `
SELECT runtime.status,runtime.revision,
       (SELECT count(*) FROM scheduled_commands
        WHERE command_type='runtime_liveness_evaluate'
          AND target_id=runtime.runtime_instance_id
          AND status='pending')
FROM runtime_instances runtime
WHERE runtime.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&runtimeStatusBefore,
		&runtimeRevisionBefore,
		&livenessPendingBefore,
	); err != nil {
		t.Fatal(err)
	}
	if runtimeStatusBefore != "suspect" {
		t.Fatalf(
			"boundary heartbeat Runtime prerequisite=%s/r%d",
			runtimeStatusBefore,
			runtimeRevisionBefore,
		)
	}

	current = resumeAt.Add(workerapplication.RuntimeUnhealthyAfter)
	heartbeat, err := (workerapplication.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
	}).Observe(
		ctx,
		workerapplication.ObserveRuntimeHeartbeatCommand{
			WorkerSessionID:   started.WorkerSessionID,
			RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration,
			ChannelEpoch:      started.ControlChannelEpoch,
			HeartbeatSequence: 1,
			ControlSequence:   2,
			ReceivedAt:        current,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.HeartbeatSequence != 1 ||
		heartbeat.ObservationRevision != 1 ||
		!heartbeat.LastReceivedAt.Equal(current) ||
		heartbeat.RuntimeRevision != runtimeRevisionBefore {
		t.Fatalf("boundary audit heartbeat=%+v", heartbeat)
	}
	var (
		observedSequence, observationRevision uint64
		observedAt                            time.Time
		runtimeStatus                         string
		runtimeRevision                       uint64
		watchdogStatus                        string
		livenessPendingAfter                  int
	)
	if err := pool.QueryRow(ctx, `
SELECT heartbeat.heartbeat_sequence,heartbeat.revision,
       heartbeat.last_received_at,runtime.status,runtime.revision,
       watchdog.status,
       (SELECT count(*) FROM scheduled_commands
        WHERE command_type='runtime_liveness_evaluate'
          AND target_id=runtime.runtime_instance_id
          AND status='pending')
FROM runtime_heartbeat_observations heartbeat
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=heartbeat.runtime_instance_id
JOIN runtime_first_heartbeat_watchdogs watchdog
  ON watchdog.runtime_instance_id=runtime.runtime_instance_id
WHERE runtime.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&observedSequence,
		&observationRevision,
		&observedAt,
		&runtimeStatus,
		&runtimeRevision,
		&watchdogStatus,
		&livenessPendingAfter,
	); err != nil {
		t.Fatal(err)
	}
	if observedSequence != 1 ||
		observationRevision != 1 ||
		!observedAt.Equal(current) ||
		runtimeStatus != runtimeStatusBefore ||
		runtimeRevision != runtimeRevisionBefore ||
		watchdogStatus != "suspect" ||
		livenessPendingAfter != livenessPendingBefore {
		t.Fatalf(
			"boundary audit observation=%d/r%d@%s runtime=%s/r%d watchdog=%s liveness=%d want=%d",
			observedSequence,
			observationRevision,
			observedAt,
			runtimeStatus,
			runtimeRevision,
			watchdogStatus,
			livenessPendingAfter,
			livenessPendingBefore,
		)
	}

	if err := handler.HandleScheduledCommand(
		ctx,
		unhealthyCommand,
	); err != nil {
		t.Fatal(err)
	}
	var terminalReason string
	var incidentCount int
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.terminal_reason,
       runtime.status,runtime.revision,
       (SELECT count(*) FROM runtime_recovery_incidents
        WHERE failed_runtime_instance_id=runtime.runtime_instance_id)
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&watchdogStatus,
		&terminalReason,
		&runtimeStatus,
		&runtimeRevision,
		&incidentCount,
	); err != nil {
		t.Fatal(err)
	}
	if watchdogStatus != "cancelled" ||
		terminalReason != "turn_outcome_owned" ||
		runtimeStatus != runtimeStatusBefore ||
		runtimeRevision != runtimeRevisionBefore ||
		incidentCount != 0 {
		t.Fatalf(
			"boundary outcome watchdog=%s/%s runtime=%s/r%d incidents=%d",
			watchdogStatus,
			terminalReason,
			runtimeStatus,
			runtimeRevision,
			incidentCount,
		)
	}
}

func TestRuntimeFirstHeartbeatBoundariesOpenOneStableRecovery(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	journal := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return resumeAt },
	}
	if _, err := journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = completeRuntimeControlClaim(t, journal, plan)
	suspectCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"suspect",
	)

	current := resumeAt.Add(45*time.Second - time.Microsecond)
	ids := &concurrentAllocationIDs{}
	handler := workerpostgres.RuntimeFirstHeartbeatCommandHandler{
		Watchdog: workerapplication.RuntimeFirstHeartbeatService{
			Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
	}
	if err := handler.HandleScheduledCommand(
		ctx,
		suspectCommand,
	); err != nil {
		t.Fatal(err)
	}
	var watchdogStatus, runtimeStatus string
	var watchdogRevision, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.revision,runtime.status,runtime.revision
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&watchdogStatus,
		&watchdogRevision,
		&runtimeStatus,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	if watchdogStatus != "awaiting" ||
		watchdogRevision != 1 ||
		runtimeStatus != "active" ||
		runtimeRevision != active.RuntimeRevision {
		t.Fatalf(
			"T+44.999 watchdog=%s/r%d runtime=%s/r%d",
			watchdogStatus,
			watchdogRevision,
			runtimeStatus,
			runtimeRevision,
		)
	}

	current = resumeAt.Add(45 * time.Second)
	if err := handler.HandleScheduledCommand(
		ctx,
		suspectCommand,
	); err != nil {
		t.Fatal(err)
	}
	var suspectAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.revision,watchdog.suspect_at,
       runtime.status,runtime.revision
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&watchdogStatus,
		&watchdogRevision,
		&suspectAt,
		&runtimeStatus,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	if watchdogStatus != "suspect" ||
		watchdogRevision != 2 ||
		!suspectAt.Equal(current) ||
		runtimeStatus != "suspect" ||
		runtimeRevision != active.RuntimeRevision+1 {
		t.Fatalf(
			"T+45 watchdog=%s/r%d suspect_at=%s runtime=%s/r%d",
			watchdogStatus,
			watchdogRevision,
			suspectAt,
			runtimeStatus,
			runtimeRevision,
		)
	}
	unhealthyCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"unhealthy",
	)
	if !unhealthyCommand.DueAt.Equal(
		resumeAt.Add(90*time.Second),
	) ||
		unhealthyCommand.ExpectedRevision != 2 {
		t.Fatalf("unhealthy command=%+v", unhealthyCommand)
	}

	current = resumeAt.Add(90 * time.Second)
	firstClassification, err := handler.Watchdog.Evaluate(
		ctx,
		firstHeartbeatEvaluateCommand(
			unhealthyCommand,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !firstClassification.Applied ||
		firstClassification.OpenRecovery == nil ||
		firstClassification.Status !=
			workerapplication.RuntimeFirstHeartbeatUnhealthy {
		t.Fatalf(
			"first unhealthy classification=%+v",
			firstClassification,
		)
	}
	var incidentCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM runtime_recovery_incidents
WHERE failed_runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(&incidentCount); err != nil {
		t.Fatal(err)
	}
	if incidentCount != 0 {
		t.Fatalf(
			"classification opened recovery before handler side effect: %d",
			incidentCount,
		)
	}
	replayed, err := handler.Watchdog.Evaluate(
		ctx,
		firstHeartbeatEvaluateCommand(
			unhealthyCommand,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.OpenRecovery == nil {
		t.Fatal("replayed recovery intent is nil")
	}
	firstRecovery := *firstClassification.OpenRecovery
	replayedRecovery := *replayed.OpenRecovery
	firstRecovery.EvidenceAt = firstRecovery.EvidenceAt.UTC()
	replayedRecovery.EvidenceAt = replayedRecovery.EvidenceAt.UTC()
	if replayed.Applied || !reflect.DeepEqual(firstRecovery, replayedRecovery) {
		t.Fatalf(
			"unhealthy recovery intent was not stable: first=%+v replay=%+v",
			firstRecovery,
			replayedRecovery,
		)
	}
	if err := handler.HandleScheduledCommand(
		ctx,
		unhealthyCommand,
	); err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleScheduledCommand(
		ctx,
		unhealthyCommand,
	); err != nil {
		t.Fatal(err)
	}

	var (
		sessionStatus            string
		recoveryTrigger          string
		recoveryIntent           []byte
		unhealthyAt              time.Time
		unhealthyRuntimeRevision uint64
	)
	if err := pool.QueryRow(ctx, `
SELECT session.status,runtime.status,watchdog.status,
       watchdog.unhealthy_at,watchdog.unhealthy_runtime_revision,
       watchdog.recovery_intent,incident.trigger,
       count(*) OVER ()
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
JOIN worker_sessions session
  ON session.worker_session_id=watchdog.worker_session_id
JOIN runtime_recovery_incidents incident
  ON incident.failed_runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&sessionStatus,
		&runtimeStatus,
		&watchdogStatus,
		&unhealthyAt,
		&unhealthyRuntimeRevision,
		&recoveryIntent,
		&recoveryTrigger,
		&incidentCount,
	); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "recovering" ||
		runtimeStatus != "unhealthy" ||
		watchdogStatus != "unhealthy" ||
		!unhealthyAt.Equal(resumeAt.Add(90*time.Second)) ||
		unhealthyRuntimeRevision != active.RuntimeRevision+2 ||
		len(recoveryIntent) == 0 ||
		recoveryTrigger != "heartbeat_unhealthy" ||
		incidentCount != 1 {
		t.Fatalf(
			"recovery convergence session=%s runtime=%s watchdog=%s unhealthy_at=%s unhealthy_runtime_revision=%d intent=%s trigger=%s incidents=%d",
			sessionStatus,
			runtimeStatus,
			watchdogStatus,
			unhealthyAt,
			unhealthyRuntimeRevision,
			recoveryIntent,
			recoveryTrigger,
			incidentCount,
		)
	}
}

func TestQueuedPreBoundaryHeartbeatCanSatisfyAfterSuspectCommits(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	journal := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return resumeAt },
	}
	if _, err := journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = completeRuntimeControlClaim(t, journal, plan)
	suspectCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"suspect",
	)
	current := resumeAt.Add(45 * time.Second)
	ids := &concurrentAllocationIDs{}
	handler := workerpostgres.RuntimeFirstHeartbeatCommandHandler{
		Watchdog: workerapplication.RuntimeFirstHeartbeatService{
			Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
	}
	if err := handler.HandleScheduledCommand(
		ctx,
		suspectCommand,
	); err != nil {
		t.Fatal(err)
	}

	receivedBeforeLock := resumeAt.Add(
		45*time.Second - time.Microsecond,
	)
	heartbeat, err := (workerapplication.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
	}).Observe(ctx, workerapplication.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID:   started.WorkerSessionID,
		RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1,
		ChannelEpoch:      1,
		HeartbeatSequence: 1,
		ControlSequence:   9,
		ReceivedAt:        receivedBeforeLock,
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		watchdogStatus     string
		suspectAt          time.Time
		firstReceivedAt    time.Time
		runtimeStatus      string
		pending, cancelled int
	)
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.suspect_at,
       watchdog.first_heartbeat_received_at,runtime.status,
       count(command.scheduled_command_id) FILTER (WHERE command.status='pending'),
       count(command.scheduled_command_id) FILTER (WHERE command.status='cancelled')
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
LEFT JOIN scheduled_commands command
  ON command.command_type='runtime_first_heartbeat_evaluate'
 AND command.target_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1
GROUP BY watchdog.status,watchdog.suspect_at,
         watchdog.first_heartbeat_received_at,runtime.status`,
		started.RuntimeInstanceID,
	).Scan(
		&watchdogStatus,
		&suspectAt,
		&firstReceivedAt,
		&runtimeStatus,
		&pending,
		&cancelled,
	); err != nil {
		t.Fatal(err)
	}
	if watchdogStatus != "satisfied" ||
		!suspectAt.Equal(current) ||
		!firstReceivedAt.Equal(receivedBeforeLock) ||
		runtimeStatus != "active" ||
		heartbeat.LivenessStatus !=
			workerapplication.RuntimeLivenessHealthy ||
		pending != 0 ||
		cancelled != 2 {
		t.Fatalf(
			"queued heartbeat convergence watchdog=%s suspect=%s received=%s runtime=%s heartbeat=%+v pending=%d cancelled=%d",
			watchdogStatus,
			suspectAt,
			firstReceivedAt,
			runtimeStatus,
			heartbeat,
			pending,
			cancelled,
		)
	}
}

func TestReplacementFirstHeartbeatTimeoutDoesNotOpenNestedRecovery(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	resumeAt := base.Add(6 * time.Minute)
	journal := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return resumeAt },
	}
	if _, err := journal.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = completeRuntimeControlClaim(t, journal, plan)
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET status='recovering',session_revision=session_revision+1
WHERE worker_session_id=$1`,
		started.WorkerSessionID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE runtime_instances
SET status='starting',revision=revision+1
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	); err != nil {
		t.Fatal(err)
	}

	suspectCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"suspect",
	)
	current := resumeAt.Add(45 * time.Second)
	ids := &concurrentAllocationIDs{}
	handler := workerpostgres.RuntimeFirstHeartbeatCommandHandler{
		Watchdog: workerapplication.RuntimeFirstHeartbeatService{
			Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return current },
		},
	}
	if err := handler.HandleScheduledCommand(
		ctx,
		suspectCommand,
	); err != nil {
		t.Fatal(err)
	}
	unhealthyCommand := loadFirstHeartbeatScheduledCommand(
		t,
		pool,
		started.RuntimeInstanceID,
		"unhealthy",
	)
	current = resumeAt.Add(90 * time.Second)
	if err := handler.HandleScheduledCommand(
		ctx,
		unhealthyCommand,
	); err != nil {
		t.Fatal(err)
	}

	var (
		watchdogStatus, terminalReason string
		runtimeStatus                  string
		recoveryIntent                 []byte
		incidentCount                  int
	)
	if err := pool.QueryRow(ctx, `
SELECT watchdog.status,watchdog.terminal_reason,
       watchdog.recovery_intent,runtime.status,
       (SELECT count(*) FROM runtime_recovery_incidents
        WHERE failed_runtime_instance_id=watchdog.runtime_instance_id)
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
WHERE watchdog.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&watchdogStatus,
		&terminalReason,
		&recoveryIntent,
		&runtimeStatus,
		&incidentCount,
	); err != nil {
		t.Fatal(err)
	}
	if watchdogStatus != "unhealthy" ||
		terminalReason != "replacement_first_heartbeat_timeout" ||
		len(recoveryIntent) != 0 ||
		runtimeStatus != "starting" ||
		incidentCount != 0 {
		t.Fatalf(
			"replacement timeout watchdog=%s reason=%s intent=%s runtime=%s incidents=%d",
			watchdogStatus,
			terminalReason,
			recoveryIntent,
			runtimeStatus,
			incidentCount,
		)
	}
}

func loadFirstHeartbeatScheduledCommand(
	t *testing.T,
	pool *pgxpool.Pool,
	runtimeInstanceID string,
	stage string,
) platformpostgres.ScheduledCommand {
	t.Helper()
	var command platformpostgres.ScheduledCommand
	if err := pool.QueryRow(context.Background(), `
SELECT scheduled_command_id,tenant_id,command_type,target_type,target_id,
       expected_revision,payload,due_at,fencing_token
FROM scheduled_commands
WHERE command_type='runtime_first_heartbeat_evaluate'
  AND target_id=$1
  AND payload->>'stage'=$2
ORDER BY due_at,scheduled_command_id
LIMIT 1`,
		runtimeInstanceID,
		stage,
	).Scan(
		&command.ID,
		&command.TenantID,
		&command.CommandType,
		&command.TargetType,
		&command.TargetID,
		&command.ExpectedRevision,
		&command.Payload,
		&command.DueAt,
		&command.FencingToken,
	); err != nil {
		t.Fatal(err)
	}
	return command
}

func firstHeartbeatEvaluateCommand(
	command platformpostgres.ScheduledCommand,
) workerapplication.EvaluateRuntimeFirstHeartbeatCommand {
	var payload struct {
		RuntimeGeneration uint64 `json:"runtime_generation"`
		ChannelEpoch      uint64 `json:"channel_epoch"`
		AnchorKind        string `json:"anchor_kind"`
		AnchorRevision    uint64 `json:"anchor_revision"`
		Stage             string `json:"stage"`
	}
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		panic(err)
	}
	return workerapplication.EvaluateRuntimeFirstHeartbeatCommand{
		TenantID:          command.TenantID,
		RuntimeInstanceID: command.TargetID,
		RuntimeGeneration: payload.RuntimeGeneration,
		ChannelEpoch:      payload.ChannelEpoch,
		AnchorKind: workerapplication.RuntimeFirstHeartbeatAnchorKind(
			payload.AnchorKind,
		),
		AnchorRevision: payload.AnchorRevision,
		Stage: workerapplication.RuntimeFirstHeartbeatStage(
			payload.Stage,
		),
		ExpectedWatchdogRevision: command.ExpectedRevision,
	}
}
