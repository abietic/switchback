//go:build integration

package localproduct

import (
	"context"
	"strconv"
	"testing"
	"time"

	platformconfig "github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestLoadStartupRuntimeCandidatesRestoresCurrentTurnFromOriginalBootstrapRun(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	checkpoint := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profile, ok := platformconfig.ProductionProfileRegistry().Agent(
		platformconfig.DeepSeekCodexOpenAIV1,
	)
	if !ok {
		t.Fatal("production AgentProfile is missing")
	}
	network, err := platformconfig.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		platformconfig.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	networkEvidence := network.Evidence()
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,
   status,active_runtime_instance_id,active_worker_run_id,session_revision,runtime_generation,
   quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at)
VALUES
  ('session-1','tenant-local','user-local','workspace-1','workspace-revision-1','working-copy-1',
   'running','runtime-1','run-2',7,1,1,$1,$2,$3,$4)`,
		now.Add(48*time.Hour),
		now.Add(14*24*time.Hour),
		now.Add(-time.Hour),
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_runs
  (worker_run_id,worker_session_id,trigger_type,previous_worker_run_id,start_snapshot,
   agent_profile_id,agent_profile_revision,agent_profile_digest,status,revision,
   created_at,started_at,terminal_at)
VALUES
  ('run-1','session-1','direct_agent_turn',NULL,
   jsonb_build_object('working_copy_checkpoint_digest',$1::text),
   $2,$3,$4,'completed',3,$5,$5,$6),
  ('run-2','session-1','direct_agent_turn','run-1',
   '{"resident_runtime_instance_id":"runtime-1"}'::jsonb,
   $2,$3,$4,'running',2,$6,$6,NULL)`,
		checkpoint,
		profile.ID,
		strconv.FormatUint(profile.Revision, 10),
		profile.Digest,
		now.Add(-50*time.Minute),
		now.Add(-10*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,
   revision,idempotency_key,created_at,started_at,deadline_at,terminal_at)
VALUES
  ('turn-1','session-1','run-1','agent-session-1','prompt','{"text":"first"}'::jsonb,
   'completed',3,'turn-1',$1,$1,$1::timestamptz + interval '24 hours',$2),
  ('turn-2','session-1','run-2','agent-session-1','queue','{"text":"continue"}'::jsonb,
   'consumed',2,'turn-2',$2,$2,$2::timestamptz + interval '24 hours',NULL)`,
		now.Add(-50*time.Minute),
		now.Add(-10*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,
   container_id,status,image_ref,resource_profile_id,network_policy_id,
   control_channel_epoch,revision,created_at,started_at,network_policy_revision,
   network_policy_digest,network_enforcement_status)
VALUES
  ('runtime-1','session-1','run-2',1,'docker_container','container-1','active',
   'switchback-worker:test','local_standard_v1',$1,9,5,$2,$3,$4,$5,$6)`,
		networkEvidence.ID,
		now.Add(-55*time.Minute),
		now.Add(-50*time.Minute),
		networkEvidence.Revision,
		networkEvidence.Digest,
		networkEvidence.EnforcementStatus,
	); err != nil {
		t.Fatal(err)
	}

	runner := &runtimeRunner{
		pool: pool,
		configuration: Config{Profile: platformconfig.SingleUserLocalProfile{
			TenantID: "tenant-local", ActorUserID: "user-local",
		}},
	}
	candidates, err := runner.loadStartupRuntimeCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates=%+v", candidates)
	}
	candidate := candidates[0]
	if candidate.Restore.WorkerRunID != "run-2" ||
		candidate.Restore.BootstrapWorkerRunID != "run-1" ||
		candidate.Restore.WorkingCopyCheckpointDigest != checkpoint ||
		candidate.Restore.RuntimeInstanceID != "runtime-1" ||
		candidate.Restore.RuntimeGeneration != 1 ||
		candidate.Restore.ControlChannelEpoch != 9 ||
		candidate.ContainerID != "container-1" ||
		candidate.ActiveTurn == nil ||
		candidate.ActiveTurn.AgentTurnID != "turn-2" ||
		candidate.ActiveTurn.AgentSessionID != "agent-session-1" {
		t.Fatalf("candidate=%+v", candidate)
	}
	if !candidate.LivenessAnchor.Before(
		now.Add(-workerapp.RuntimeUnhealthyAfter),
	) {
		t.Fatalf(
			"fixture does not contain an expired persisted heartbeat anchor: %s",
			candidate.LivenessAnchor,
		)
	}
	durableAnchor := time.Now().UTC().Truncate(time.Microsecond).Add(-30 * time.Second)
	if _, err := newRuntimeControlJournalService(
		pool,
		func() time.Time { return durableAnchor },
	).Bind(ctx, workerapp.BindRuntimeControlChannelCommand{
		WorkerSessionID:   "session-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		ChannelEpoch:      9,
		Credential:        "worker-control-token",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_first_heartbeat_watchdogs
  (runtime_instance_id,tenant_id,worker_session_id,runtime_generation,
   channel_epoch,anchor_kind,anchor_revision,
   first_authenticated_resume_at,suspect_due_at,unhealthy_due_at,
   status,revision,created_at,updated_at)
VALUES
  ('runtime-1','tenant-local','session-1',1,9,
   'legacy_runtime_started_v1',$1,$2,$2::timestamptz + interval '45 seconds',
   $2::timestamptz + interval '90 seconds','awaiting',1,$2,$2)`,
		candidate.RuntimeRevision,
		durableAnchor,
	); err != nil {
		t.Fatal(err)
	}
	candidates, err = runner.loadStartupRuntimeCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 ||
		!candidates[0].LivenessAnchor.Equal(durableAnchor) {
		t.Fatalf(
			"startup candidate did not prefer durable first-heartbeat anchor: %+v",
			candidates,
		)
	}
	candidate = candidates[0]

	// Model ResumeComplete at T+89. It does not reset the heartbeat clock:
	// the first exact heartbeat must win the remaining one-second CAS window.
	candidate.LivenessAnchor = time.Now().UTC().Add(
		-workerapp.RuntimeUnhealthyAfter + time.Second,
	)
	if _, err := pool.Exec(ctx, `
UPDATE runtime_instances
SET status='active',revision=revision+1
WHERE runtime_instance_id='runtime-1'`); err != nil {
		t.Fatal(err)
	}
	type activeResult struct {
		active bool
		err    error
	}
	active := make(chan activeResult, 1)
	go func() {
		ok, awaitErr := runner.awaitRestoredRuntimeActive(ctx, candidate)
		active <- activeResult{active: ok, err: awaitErr}
	}()
	heartbeat, err := (workerapp.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   randomIDs{},
	}).Observe(ctx, workerapp.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1, ChannelEpoch: 9,
		HeartbeatSequence: 1, ControlSequence: 1,
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-active:
		if result.err != nil || !result.active {
			t.Fatalf("await restored active Runtime=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("restored Runtime did not reactivate after the first heartbeat")
	}
	freshTurn, err := runner.readRestoredActiveTurn(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if freshTurn.RuntimeRevision != heartbeat.RuntimeRevision ||
		freshTurn.RuntimeRevision == candidate.ActiveTurn.RuntimeRevision ||
		freshTurn.TurnRevision != candidate.ActiveTurn.TurnRevision {
		t.Fatalf(
			"fresh turn=%+v heartbeat=%+v stale=%+v",
			freshTurn,
			heartbeat,
			candidate.ActiveTurn,
		)
	}

	journal := newRuntimeControlJournalService(pool, time.Now)
	hub := controlgrpc.NewHubWithJournal(journal)
	if err := hub.RegisterCredential(controlgrpc.Credential{
		Identity: controlgrpc.RuntimeIdentity{
			WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1, ChannelEpoch: 9,
		},
		Token: "worker-control-token",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE runtime_instances
SET status='active',revision=revision+1
WHERE runtime_instance_id='runtime-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE runtime_heartbeat_observations
SET liveness_status='healthy',last_received_at=$1::timestamptz,
    suspect_due_at=$1::timestamptz + interval '45 seconds',
    unhealthy_due_at=$1::timestamptz + interval '90 seconds',revision=revision+1
WHERE runtime_instance_id='runtime-1'`,
		time.Now().UTC().Add(-workerapp.RuntimeUnhealthyAfter-time.Second),
	); err != nil {
		t.Fatal(err)
	}
	recoveryService := workerapp.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   randomIDs{},
		Now:   time.Now,
	}
	runner.hub = hub
	runner.now = time.Now
	runner.configuration.RuntimeRecoveryEnabled = true
	runner.failures = workerpostgres.NewGatedRuntimeFailureObserver(
		pool,
		recoveryService,
		true,
	)
	candidate.LivenessAnchor = time.Now().UTC().Add(
		-workerapp.RuntimeUnhealthyAfter - time.Second,
	)
	stillActive, err := runner.awaitRestoredRuntimeActive(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if stillActive {
		t.Fatal("T+90 suspect Runtime without a heartbeat remained active")
	}
	var runtimeStatus, sessionStatus, channelStatus string
	var incidentCount int
	if err := pool.QueryRow(ctx, `
SELECT runtime.status,session.status,channel.status,
       (SELECT count(*) FROM runtime_recovery_incidents
        WHERE failed_runtime_instance_id=runtime.runtime_instance_id)
FROM runtime_instances runtime
JOIN worker_sessions session
  ON session.worker_session_id=runtime.worker_session_id
JOIN runtime_control_channels channel
  ON channel.runtime_instance_id=runtime.runtime_instance_id
WHERE runtime.runtime_instance_id='runtime-1'`).Scan(
		&runtimeStatus,
		&sessionStatus,
		&channelStatus,
		&incidentCount,
	); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != "unhealthy" || sessionStatus != "recovering" ||
		channelStatus != "fenced" || incidentCount != 1 {
		t.Fatalf(
			"deadline settlement runtime=%s session=%s channel=%s incidents=%d",
			runtimeStatus,
			sessionStatus,
			channelStatus,
			incidentCount,
		)
	}
	if _, err := (workerapp.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   randomIDs{},
	}).Observe(ctx, workerapp.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1, ChannelEpoch: 9,
		HeartbeatSequence: 2, ControlSequence: 2,
		ReceivedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("late heartbeat revived a fenced unhealthy Runtime")
	}
}
