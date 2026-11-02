//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fragmentIDs struct{ next int }

func (i *fragmentIDs) NewID(kind string) (string, error) {
	i.next++
	return fmt.Sprintf("fragment-%s-%d", kind, i.next), nil
}

type fragmentScanner struct{}

func (fragmentScanner) ScanJSON(context.Context, string, any) error { return nil }

func TestFragmentPublicationMigrationCodesRetainExistingEvidence(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 25)
	ctx := context.Background()
	seedEvidenceTarget(t, pool, time.Now().UTC())
	seedFragmentPublication(t, pool, "model", "retained-v26", "run-1")
	var before []byte
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('c',to_jsonb(c),'p',to_jsonb(p)) FROM observation_candidates c JOIN observation_publications p USING(kind,source_id)`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	readMigration := func(name string) (string, string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		up, down, ok := strings.Cut(string(raw), "-- migrate:down")
		if !ok {
			t.Fatal("migration lacks down")
		}
		return up, down
	}
	old, _ := readMigration("000025_observation_basis_scan_code.sql")
	up, down := readMigration("000026_observation_fragment_publication_codes.sql")
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	for _, codes := range []string{old, up} {
		parts := strings.Split(codes, "'")
		for i := 1; i < len(parts); i += 2 {
			var allowed bool
			if err := pool.QueryRow(ctx, `SELECT observation_fragment_safe_code($1)`, parts[i]).Scan(&allowed); err != nil || !allowed {
				t.Fatalf("safe code %q lost: allowed=%v err=%v", parts[i], allowed, err)
			}
		}
	}
	var allowed bool
	if err := pool.QueryRow(ctx, `SELECT observation_fragment_safe_code('external_secret_error_text')`).Scan(&allowed); err != nil || allowed {
		t.Fatalf("unsafe code accepted: %v %v", allowed, err)
	}
	if _, err := pool.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "observation fragment evidence is retained") {
		t.Fatalf("unsafe downgrade: %v", err)
	}
	var after []byte
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('c',to_jsonb(c),'p',to_jsonb(p)) FROM observation_candidates c JOIN observation_publications p USING(kind,source_id)`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("migration or refused down altered retained evidence")
	}
}

type fragmentFixture struct {
	pool            *pgxpool.Pool
	objects         *artifactlocal.ObjectStore
	objectRoot      string
	ids             *fragmentIDs
	now             time.Time
	started         workerapp.StartedWorkerRun
	journal         workerapp.RuntimeControlJournalService
	authority       workerapp.RuntimeControlAuthority
	start           workerapp.RuntimeControlMessage
	settlements     workerapp.TurnSettlementService
	current         workerapp.TurnSettlement
	stage           workerapp.CompleteResidentStageCommand
	finalized       artifactapp.FinalizedTurnEvidence
	command         artifactapp.FinalizeTurnEvidenceCommand
	firstDigest     string
	firstProof      []byte
	proof           workerapp.TurnSettlementDomainProof
	terminalRowSeed bool
	missingCapture  bool
	invalidModel    bool
	legacySource    func(artifactdomain.TurnIdentity, json.RawMessage, time.Time, time.Time) ([]byte, error)
}

func fragmentJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The only SQL seed below creates allocation inputs. Start/journal/first freeze,
// real Build/finalization/domain commit/metadata publish all use public services.
func newFragmentFixture(t *testing.T, withModel bool, gap ...bool) *fragmentFixture {
	return newFragmentFixtureMode(t, withModel, false, gap...)
}

func newFragmentFixtureMode(t *testing.T, withModel, historical bool, gap ...bool) *fragmentFixture {
	return newFragmentFixtureConfigured(t, withModel, historical, nil, gap...)
}

func newFragmentFixtureConfigured(t *testing.T, withModel, historical bool, configure func(*fragmentFixture), gap ...bool) *fragmentFixture {
	t.Helper()
	ctx := context.Background()
	f := &fragmentFixture{pool: postgrestest.Open(t), ids: &fragmentIDs{}, now: time.Now().UTC().Truncate(time.Microsecond).Add(-10 * time.Minute)}
	if configure != nil {
		configure(f)
	}
	f.missingCapture = len(gap) > 1 && gap[1]
	f.invalidModel = len(gap) > 2 && gap[2]
	if !historical {
		if err := artifactpostgres.NewFragmentJobStore(f.pool).Activate(ctx, fragmentVersion); err != nil {
			t.Fatal(err)
		}
	}
	digest := artifactdomain.Checksum([]byte("fixture specification"))
	for _, sql := range []string{
		`INSERT INTO task_readiness_assessments(assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at) VALUES('assessment-01','tenant-local','user-local','workspace-demo','workspace-revision-1','api','direct-ready-v1','{}','[]','[]','ready_confirmed',$1,$2)`,
		`INSERT INTO task_creation_intents(intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,task_spec_digest,trigger_policy_revision,status,created_at,expires_at) VALUES('intent-01','tenant-local','user-local','workspace-demo','workspace-revision-1','assessment-01',$1,'direct-create-v1','active',$2,$2::timestamptz+interval '15 minutes')`,
		`INSERT INTO tasks(task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,task_spec_digest,creation_intent_id,status,acceptance_mode,result_refs,session_allocation_sequence,revision,created_at,updated_at) VALUES('task-01','tenant-local','user-local','workspace-demo','workspace-revision-1','{}',$1,'intent-01','queued','manual','[]',1,1,$2,$2)`,
	} {
		if _, err := f.pool.Exec(ctx, sql, digest, f.now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO session_allocation_requests(allocation_request_id,tenant_id,user_id,workspace_id,workspace_revision_id,request_kind,owner_id,owner_revision,context_policy,session_allocation_sequence,status,idempotency_key,created_at,updated_at) VALUES('allocation-01','tenant-local','user-local','workspace-demo','workspace-revision-1','task','task-01',1,'fresh_session',1,'pending','allocate-01',$1,$1)`, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO session_allocation_queue_positions(tenant_id,user_id,next_sequence,revision,updated_at) VALUES('tenant-local','user-local',2,1,$1)`, f.now); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	allocated, err := (workerapp.WorkerSessionAllocator{Store: workerpostgres.NewAllocatorStore(f.pool), IDs: f.ids, Now: func() time.Time { return f.now }}).Execute(ctx, workerapp.AllocateWorkerSessionCommand{TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01"})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := f.pool.Exec(ctx, `UPDATE worker_sessions SET status='interactive',runtime_generation=1,session_revision=2,updated_at=$2 WHERE worker_session_id=$1`, allocated.WorkerSessionID, f.now); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	dispatch := taskapp.SessionDispatchService{Store: taskpostgres.NewStore(f.pool), IDs: f.ids, Now: func() time.Time { return f.now }, PermitTTL: 5 * time.Minute}
	if _, err := dispatch.EnqueueTask(ctx, taskapp.EnqueueTaskCommand{TenantID: "tenant-local", UserID: "user-local", TaskID: "task-01", WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1, ExpectedSessionRevision: 2, IdempotencyKey: "enqueue-01"}); err != nil {
		t.Fatal(err)
	}
	permit, err := dispatch.IssueTaskPermit(ctx, taskapp.IssueTaskPermitCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: allocated.WorkerSessionID, RuntimeGeneration: 1, CapacityAdmissionRef: "capacity-local", IdempotencyKey: "permit-01"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	network := profile.Evidence()
	f.now = f.now.Add(time.Minute)
	f.started, err = (workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(f.pool), IDs: f.ids, Now: func() time.Time { return f.now }}).Execute(ctx, workerapp.StartWorkerRunCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: permit.WorkerSessionID, TaskID: permit.TaskID, PermitID: permit.PermitID, ExpectedSessionRevision: permit.ExpectedSessionRevision, RuntimeGeneration: permit.RuntimeGeneration, TriggerType: "task_initial",
		StartSnapshot: []byte(`{"working_copy":"fixture-checkpoint"}`), AgentProfileID: "codex-test", AgentProfileRevision: "1", AgentProfileDigest: digest, AgentTurnContent: []byte(`{"text":"execute task","n":9007199254740993}`), TurnIdempotencyKey: "turn/" + permit.PermitID, RuntimeImageRef: "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ResourceProfileID: "local_standard_v1", NetworkPolicyID: network.ID, NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest, NetworkEnforcementStatus: network.EnforcementStatus, ControlChannelEpoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := f.started
	if historical {
		if err := artifactpostgres.NewFragmentJobStore(f.pool).Activate(ctx, fragmentVersion); err != nil {
			t.Fatal(err)
		}
	}
	f.now = f.now.Add(time.Minute)
	if _, err := (workerapp.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(f.pool), Now: func() time.Time { return f.now }}).Activate(ctx, workerapp.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: s.WorkerSessionID, WorkerRunID: s.WorkerRunID, AgentTurnID: s.AgentTurnID, RuntimeInstanceID: s.RuntimeInstanceID, ContainerID: "fragment-test-container", RuntimeGeneration: 1, ControlChannelEpoch: 1, ExpectedSessionRevision: s.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	journalStore, err := workerpostgres.NewRuntimeControlJournalWithObservation(f.pool, &artifactpostgres.ObservationCaptureStore{})
	if err != nil {
		t.Fatal(err)
	}
	f.journal = workerapp.RuntimeControlJournalService{Store: journalStore, Now: func() time.Time { return f.now }}
	if _, err := f.journal.Bind(ctx, workerapp.BindRuntimeControlChannelCommand{WorkerSessionID: s.WorkerSessionID, RuntimeInstanceID: s.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: 1, Credential: "synthetic-fragment-credential"}); err != nil {
		t.Fatal(err)
	}
	plan, err := f.journal.Claim(ctx, workerapp.ClaimRuntimeControlStreamCommand{WorkerSessionID: s.WorkerSessionID, RuntimeInstanceID: s.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: 1, Credential: "synthetic-fragment-credential", ProcessNonce: "fragment-process", StreamAttempt: 1, OwnerID: "fragment-backend", WorkerEventCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest, WorkerCommandCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest})
	if err != nil {
		t.Fatal(err)
	}
	c := plan.Channel
	f.authority = workerapp.RuntimeControlAuthority{WorkerSessionID: c.WorkerSessionID, RuntimeInstanceID: c.RuntimeInstanceID, RuntimeGeneration: c.RuntimeGeneration, ChannelEpoch: c.ChannelEpoch, ProcessNonce: c.ProcessNonce, StreamAttempt: c.StreamAttempt, OwnerFence: c.OwnerFence}
	if _, err := f.journal.CompleteResume(ctx, workerapp.CompleteRuntimeControlResumeCommand{Authority: f.authority, Ready: contractsv1.RuntimeControlResumeReady{ServerConnectionID: c.OwnerID, AcknowledgedJournalRevision: c.ResumeClaimRevision, WorkerTXReplayedThroughSequence: c.EventCommittedSequence, WorkerTXCursorDigest: c.EventCommittedDigest, WorkerRXReceivedThroughSequence: c.CommandCommittedSequence, WorkerRXAppliedSequence: c.CommandAppliedSequence, WorkerRXCursorDigest: c.CommandCommittedDigest, ActiveTurn: c.ActiveEffectSnapshot}}); err != nil {
		t.Fatal(err)
	}
	envelope := f.envelope("command", 0, "start", "agent_turn.start", json.RawMessage(`{}`))
	envelope.SentAt = time.Time{}
	f.start, err = f.journal.ReserveCommand(ctx, workerapp.ReserveRuntimeControlCommandCommand{Authority: f.authority, MessageID: "start", IdempotencyKey: "start", Envelope: fragmentJSON(t, envelope)})
	if err != nil {
		t.Fatal(err)
	}
	active := contractsv1.RuntimeControlActiveTurn{WorkerRunID: contractsv1.WorkerRunID(s.WorkerRunID), AgentSessionID: contractsv1.AgentSessionID(s.AgentSessionID), AgentTurnID: contractsv1.AgentTurnID(s.AgentTurnID), StartCommandSequence: f.start.Sequence, StartMessageID: f.start.MessageID, StartIdempotencyKey: f.start.IdempotencyKey, StartEnvelopeDigest: f.start.Fingerprint, Phase: "active"}
	if _, err := f.journal.ReconcileEffect(ctx, workerapp.ReconcileRuntimeControlEffectCommand{Authority: f.authority, CommandSequence: f.start.Sequence, MessageID: f.start.MessageID, AgentTurnID: s.AgentTurnID, Phase: workerapp.RuntimeControlEffectActive, ActiveTurn: &active}); err != nil {
		t.Fatal(err)
	}
	seq := uint64(1)
	if withModel {
		f.captureModel(t, seq)
		seq++
	}
	f.now = f.now.Add(time.Minute)
	terminal := f.envelope("event", seq, "terminal", "agent_turn.terminal", json.RawMessage(`{}`))
	terminal.AckSequence = f.start.Sequence
	if _, err := f.journal.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{Authority: f.authority, Sequence: seq, MessageID: "terminal", IdempotencyKey: "terminal", Envelope: fragmentJSON(t, terminal)}); err != nil {
		t.Fatal(err)
	}
	settlementStore, err := workerpostgres.NewTurnSettlementRepositoryWithObservation(f.pool, &artifactpostgres.ObservationCaptureStore{})
	if err != nil {
		t.Fatal(err)
	}
	f.settlements = workerapp.TurnSettlementService{Store: settlementStore, Now: func() time.Time { return f.now }}
	id, _ := workerapp.TurnSettlementID(s.AgentTurnID)
	current, err := f.settlements.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.current, err = f.settlements.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: id, ExpectedRevision: current.Revision, OwnerID: "fragment-settlement"})
	if err != nil {
		t.Fatal(err)
	}
	f.objectRoot = t.TempDir()
	f.objects, err = artifactlocal.NewObjectStore(f.objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(gap) > 0 && gap[0] {
		if _, err := f.pool.Exec(ctx, `UPDATE agent_turns SET created_at=created_at+interval '1 second' WHERE agent_turn_id=$1`, s.AgentTurnID); err != nil {
			t.Fatal(err)
		}
	}
	f.freezeAndFinalize(t)
	return f
}

func (f *fragmentFixture) envelope(kind string, sequence uint64, id, messageType string, payload json.RawMessage) contractsv1.RuntimeControlEnvelope {
	s := f.started
	return contractsv1.RuntimeControlEnvelope{SchemaVersion: contractsv1.RuntimeControlSchemaVersion, MessageID: id, Kind: kind, WorkerSessionID: contractsv1.WorkerSessionID(s.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(s.WorkerRunID), AgentSessionID: contractsv1.AgentSessionID(s.AgentSessionID), AgentTurnID: contractsv1.AgentTurnID(s.AgentTurnID), RuntimeInstanceID: contractsv1.RuntimeInstanceID(s.RuntimeInstanceID), RuntimeGeneration: uint32(s.RuntimeGeneration), ChannelEpoch: 1, Sequence: sequence, IdempotencyKey: id, MessageType: messageType, Payload: payload, SentAt: f.now}
}

func (f *fragmentFixture) captureModel(t *testing.T, sequence uint64, name ...string) {
	t.Helper()
	ctx := context.Background()
	s := f.started
	m := workerapp.ModelObservation{ObservationID: "model-1", WorkerSessionID: s.WorkerSessionID, WorkerRunID: s.WorkerRunID, AgentTurnID: s.AgentTurnID, RuntimeID: s.RuntimeInstanceID, Generation: s.RuntimeGeneration, Model: "test-model", Route: "test-route", StartedAt: f.now, EndedAt: f.now.Add(time.Second), Status: "failed", Input: json.RawMessage(`{"n":9007199254740993}`), Output: json.RawMessage(`null`), InputTokens: 2, OutputTokens: 0}
	messageID := "model-event"
	if len(name) != 0 {
		m.ObservationID, messageID = name[0], name[0]+"-event"
	}
	if f.invalidModel {
		m.Model = ""
	}
	f.now = f.now.Add(time.Second)
	e := f.envelope("event", sequence, messageID, "model.observation", fragmentJSON(t, m))
	e.AckSequence = f.start.Sequence
	if _, err := f.journal.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{Authority: f.authority, Sequence: sequence, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: fragmentJSON(t, e)}); err != nil {
		t.Fatal(err)
	}
	if f.invalidModel {
		if _, err := f.journal.ApplyEvent(ctx, workerapp.ApplyRuntimeControlEventCommand{Authority: f.authority, Sequence: sequence, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Outcome: json.RawMessage(`{"status":"model_observation_recorded"}`), ObservationCapture: &workerapp.RuntimeObservationCapture{SafeCode: workerapp.ObservationCaptureInvalid}}); err != nil {
			t.Fatal(err)
		}
		return
	}
	scope := artifactdomain.ObservationScope{TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo", WorkerSessionID: s.WorkerSessionID, WorkerRunID: s.WorkerRunID}
	frame, err := artifactapp.ModelObservationFragment(scope, e)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := artifactdomain.EncodeObservationFragment(frame)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(e)
	if err != nil {
		t.Fatal(err)
	}
	p := (artifactapp.ObservationCapture{Compiler: lf.FragmentCompiler{}, Scanner: fragmentScanner{}, Scope: artifactapp.ObservationScanScope{WorkerSessionID: s.WorkerSessionID, RuntimeInstanceID: s.RuntimeInstanceID, RuntimeGeneration: s.RuntimeGeneration}}).Prepare(ctx, raw, e, artifactapp.ObservationProvenance{EnvelopeDigest: digest})
	if p.ErrorCode != "" {
		t.Fatal(p.ErrorCode)
	}
	sealed, err := artifactapp.EncodeSealedObservationBundle(artifactapp.SealedObservationBundle{SchemaVersion: "observation.capture.bundle.v1", Observations: []artifactapp.PreparedObservation{p}})
	if err != nil {
		t.Fatal(err)
	}
	capture := &workerapp.RuntimeObservationCapture{SafeCode: workerapp.ObservationCaptureUnavailable}
	if !f.missingCapture {
		capture = &workerapp.RuntimeObservationCapture{Sealed: sealed}
	}
	if _, err := f.journal.ApplyEvent(ctx, workerapp.ApplyRuntimeControlEventCommand{Authority: f.authority, Sequence: sequence, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Outcome: json.RawMessage(`{"status":"model_observation_recorded"}`), ObservationCapture: capture}); err != nil {
		t.Fatal(err)
	}
}

func (f *fragmentFixture) freezeAndFinalize(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s := f.started
	identity := artifactdomain.TurnIdentity{TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo", TaskID: "task-01", WorkerSessionID: contractsv1.WorkerSessionID(s.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(s.WorkerRunID), AgentSessionID: contractsv1.AgentSessionID(s.AgentSessionID), AgentTurnID: contractsv1.AgentTurnID(s.AgentTurnID), RuntimeInstanceID: contractsv1.RuntimeInstanceID(s.RuntimeInstanceID), RuntimeGeneration: 1, TraceStreamID: "trace-" + s.AgentTurnID, LangfuseTraceID: "langfuse-" + s.AgentTurnID}
	var input []byte
	var start, created time.Time
	if err := f.pool.QueryRow(ctx, `SELECT t.content,t.started_at,r.created_at,runtime.network_policy_id,runtime.network_policy_revision,runtime.network_policy_digest,runtime.network_enforcement_status,s.session_revision,r.revision,t.revision,runtime.revision
 FROM worker_sessions s JOIN worker_runs r USING(worker_session_id) JOIN agent_turns t USING(worker_run_id) JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4 WHERE s.worker_session_id=$1 AND r.worker_run_id=$2 AND t.agent_turn_id=$3`, s.WorkerSessionID, s.WorkerRunID, s.AgentTurnID, s.RuntimeInstanceID).Scan(&input, &start, &created, &identity.RuntimeNetwork.ID, &identity.RuntimeNetwork.Revision, &identity.RuntimeNetwork.Digest, &identity.RuntimeNetwork.EnforcementStatus, &f.stage.ExpectedSessionRevision, &f.stage.ExpectedRunRevision, &f.stage.ExpectedTurnRevision, &f.stage.ExpectedRuntimeRevision); err != nil {
		t.Fatal(err)
	}
	f.stage.TenantID = "tenant-local"
	f.stage.WorkerSessionID = s.WorkerSessionID
	f.stage.WorkerRunID = s.WorkerRunID
	f.stage.AgentTurnID = s.AgentTurnID
	f.stage.RuntimeInstanceID = s.RuntimeInstanceID
	f.stage.RuntimeGeneration = s.RuntimeGeneration
	f.stage.SessionTarget = workerdomain.SessionInteractive
	f.stage.RunTarget = workerdomain.RunCompleted
	f.stage.TurnTarget = workerdomain.TurnCompleted
	f.stage.TerminalReason = f.current.Source.Reason
	inner := fragmentJSON(t, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": s.AgentSessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "Done."}}}})
	raw := append(fragmentJSON(t, map[string]any{"type": "acp.raw_frame", "direction": "agent_to_client", "frame": string(inner) + "\n"}), '\n')
	frozen := &workerapp.FrozenArtifactMetadata{ReportArtifactID: "report-" + s.AgentTurnID, PatchArtifactID: "patch-" + s.AgentTurnID, CreatedAt: f.now}
	f.command = artifactapp.FinalizeTurnEvidenceCommand{Identity: identity, ServerRecordedAt: f.now, WorkerRecordedAt: f.now, RawACPJSONL: raw, NoFilesChanged: true, SourceCheckpointRef: contractsv1.CheckpointRef{CheckpointID: "fixture-working-copy-checkpoint", Digest: artifactdomain.Checksum([]byte("independent working copy"))}, FrozenArtifacts: frozen}
	if f.legacySource != nil {
		var err error
		f.command.ObservationSourceJSONL, err = f.legacySource(identity, input, start, f.current.Source.OccurredAt)
		if err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := artifactdomain.BuildTurnEvidence(artifactdomain.BuildTurnEvidenceInput{Identity: identity, ServerRecordedAt: f.now, WorkerRecordedAt: f.now, RawACPJSONL: raw, NoFilesChanged: true})
	if err != nil {
		t.Fatal(err)
	}
	var report []byte
	for _, object := range bundle.Objects {
		if object.Kind == artifactdomain.KindReport {
			report = object.Payload
		}
	}
	if len(report) == 0 {
		t.Fatal("real report missing")
	}
	checkpoint := workerapp.TurnSettlementCheckpoint{SchemaVersion: 1, FrozenArtifacts: frozen, AdditionalFields: map[string]json.RawMessage{
		"source":         fragmentJSON(t, map[string]any{"kind": f.current.Source.Kind, "reason": f.current.Source.Reason, "occurred_at": f.current.Source.OccurredAt}),
		"evidence_input": fragmentJSON(t, map[string]any{"tenant_id": identity.TenantID, "user_id": identity.UserID, "workspace_id": identity.WorkspaceID, "task_id": identity.TaskID, "worker_session_id": s.WorkerSessionID, "worker_run_id": s.WorkerRunID, "agent_session_id": s.AgentSessionID, "agent_turn_id": s.AgentTurnID, "runtime_instance_id": s.RuntimeInstanceID, "runtime_generation": s.RuntimeGeneration, "network_policy_id": identity.RuntimeNetwork.ID, "network_policy_revision": identity.RuntimeNetwork.Revision, "network_policy_digest": identity.RuntimeNetwork.Digest, "network_enforcement_status": identity.RuntimeNetwork.EnforcementStatus, "trace_start_offset": 0, "trace_end_offset": len(raw), "trace_digest": artifactdomain.Checksum(raw), "patch_snapshot_file": "fragment-patch.patch", "patch_size": 0, "no_files_changed": true, "patch_digest": artifactdomain.Checksum(nil), "server_recorded_at": f.now, "worker_recorded_at": f.now, "report_artifact_id": frozen.ReportArtifactID, "patch_artifact_id": frozen.PatchArtifactID, "artifact_created_at": frozen.CreatedAt, "source_checkpoint_id": f.command.SourceCheckpointRef.CheckpointID, "source_checkpoint_digest": f.command.SourceCheckpointRef.Digest}),
		"domain_plan":    fragmentJSON(t, map[string]any{"session_target": f.stage.SessionTarget, "run_target": f.stage.RunTarget, "turn_target": f.stage.TurnTarget, "terminal_reason": f.stage.TerminalReason, "expected_session_revision": f.stage.ExpectedSessionRevision, "expected_run_revision": f.stage.ExpectedRunRevision, "expected_turn_revision": f.stage.ExpectedTurnRevision, "expected_runtime_revision": f.stage.ExpectedRuntimeRevision}),
	}}
	if len(f.command.ObservationSourceJSONL) != 0 {
		name := "observation-source-" + s.AgentTurnID + ".jsonl"
		if err := os.WriteFile(filepath.Join(f.objectRoot, name), f.command.ObservationSourceJSONL, 0o600); err != nil {
			t.Fatal(err)
		}
		var input map[string]json.RawMessage
		if err := json.Unmarshal(checkpoint.AdditionalFields["evidence_input"], &input); err != nil {
			t.Fatal(err)
		}
		input["observation_source_snapshot_file"] = fragmentJSON(t, name)
		input["observation_source_size"] = fragmentJSON(t, len(f.command.ObservationSourceJSONL))
		input["observation_source_digest"] = fragmentJSON(t, artifactdomain.Checksum(f.command.ObservationSourceJSONL))
		checkpoint.AdditionalFields["evidence_input"] = fragmentJSON(t, input)
	}
	canonical, digest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(fragmentJSON(t, map[string]any{"schema_version": 1, "frozen_artifacts": frozen, "source": checkpoint.AdditionalFields["source"], "evidence_input": checkpoint.AdditionalFields["evidence_input"], "domain_plan": checkpoint.AdditionalFields["domain_plan"]}))
	_ = canonical
	if err != nil {
		t.Fatal(err)
	}
	f.firstDigest = digest
	scope := artifactdomain.ObservationScope{TenantID: identity.TenantID, UserID: identity.UserID, WorkspaceID: identity.WorkspaceID, WorkerSessionID: s.WorkerSessionID, WorkerRunID: s.WorkerRunID}
	end := f.current.Source.OccurredAt
	frames := []artifactdomain.ObservationFragment{{SchemaVersion: "observation.fragment.v1", Kind: artifactdomain.FragmentTurnBasis, Scope: scope, Turn: &identity, StartedAt: start, EndedAt: &end, InputJSON: string(input), OutputJSON: string(fragmentJSON(t, string(report)))}}
	var initial []string
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(array_agg(agent_turn_id),ARRAY[]::text[]) FROM agent_turns WHERE worker_run_id=$1 AND created_at=$2 AND mode IN ('prompt','queue')`, s.WorkerRunID, created).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	if len(initial) != 1 || initial[0] == s.AgentTurnID {
		frames = append(frames, artifactdomain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: artifactdomain.FragmentRunBasis, Scope: scope, InitialTurnID: s.AgentTurnID, StartedAt: created, InputJSON: string(input)})
	}
	sealed := artifactapp.SealedObservationBundle{SchemaVersion: "observation.capture.bundle.v1"}
	for _, frame := range frames {
		raw, err := artifactdomain.EncodeObservationFragment(frame)
		if err != nil {
			t.Fatal(err)
		}
		p := (artifactapp.ObservationCapture{Compiler: lf.FragmentCompiler{}, Scanner: fragmentScanner{}, Scope: artifactapp.ObservationScanScope{WorkerSessionID: s.WorkerSessionID, RuntimeInstanceID: s.RuntimeInstanceID, RuntimeGeneration: s.RuntimeGeneration}}).Prepare(ctx, raw, map[string]any{"input": json.RawMessage(input), "report": string(report)}, artifactapp.ObservationProvenance{CheckpointDigest: digest, InputDigest: artifactdomain.Checksum(input), ReportDigest: artifactdomain.Checksum(report)})
		if p.ErrorCode != "" {
			t.Fatal(p.ErrorCode)
		}
		sealed.Observations = append(sealed.Observations, p)
	}
	sealedRaw, err := artifactapp.EncodeSealedObservationBundle(sealed)
	if err != nil {
		t.Fatal(err)
	}
	var capture *workerapp.RuntimeObservationCapture
	if !f.missingCapture {
		capture = &workerapp.RuntimeObservationCapture{Sealed: sealedRaw}
	}
	f.current, err = f.settlements.FreezeCheckpoint(ctx, workerapp.FreezeTurnSettlementCheckpointCommand{SettlementID: f.current.SettlementID, ExpectedRevision: f.current.Revision, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence, Checkpoint: checkpoint, ObservationCapture: capture})
	if err != nil {
		t.Fatal(err)
	}
	f.firstProof = fragmentProofSnapshot(t, f.pool)
	f.finalized, err = (artifactapp.TurnEvidenceFinalizer{Store: f.objects, IDs: f.ids, Now: func() time.Time { return f.now }, Policy: artifactapp.FinalizationPolicy{ArtifactPermission: contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "single-user-local-v1"}, ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1"}}).Execute(ctx, f.command)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.AdditionalFields["evidence"] = fragmentJSON(t, map[string]any{"manifest_digest": f.finalized.ManifestObject.Checksum, "finalized": f.finalized})
	f.current, err = f.settlements.FreezeCheckpoint(ctx, workerapp.FreezeTurnSettlementCheckpointCommand{SettlementID: f.current.SettlementID, ExpectedRevision: f.current.Revision, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence, Checkpoint: checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	f.current, err = f.settlements.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{SettlementID: f.current.SettlementID, ExpectedRevision: f.current.Revision, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence, FromPhase: f.current.Phase, ToPhase: workerapp.TurnSettlementEvidencePrepared, Checkpoint: f.current.PhaseState})
	if err != nil {
		t.Fatal(err)
	}
	if f.terminalRowSeed {
		f.proof = workerapp.TurnSettlementDomainProof{SessionRevision: f.stage.ExpectedSessionRevision, RunRevision: f.stage.ExpectedRunRevision, TurnRevision: f.stage.ExpectedTurnRevision, RuntimeRevision: f.stage.ExpectedRuntimeRevision}
		return
	}
	committed, err := (workerapp.TurnSettlementDomainService{Store: workerpostgres.NewTurnSettlementRepository(f.pool), Now: func() time.Time { return f.now }}).Commit(ctx, workerapp.CommitTurnSettlementDomainCommand{SettlementID: f.current.SettlementID, ExpectedRevision: f.current.Revision, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence, Resident: f.stage})
	if err != nil {
		t.Fatal(err)
	}
	f.current = committed.Settlement
	f.proof = committed.Proof
}

func (f *fragmentFixture) publishCore(t *testing.T) {
	t.Helper()
	p := artifactapp.ArtifactMetadataPublisher{Objects: f.objects, Metadata: artifactpostgres.NewMetadataStore(f.pool), IDs: f.ids, Now: func() time.Time { return f.now }}
	if _, err := p.Execute(context.Background(), artifactapp.PublishTurnEvidenceCommand{Evidence: f.finalized, ExpectedSessionRevision: f.proof.SessionRevision, ExpectedRunRevision: f.proof.RunRevision, ExpectedTurnRevision: f.proof.TurnRevision, ExpectedRuntimeRevision: f.proof.RuntimeRevision, IdempotencyKey: "publish-turn/" + f.started.AgentTurnID}); err != nil {
		t.Fatal(err)
	}
}

func TestFragmentPublicationActualCoreBeforeCheckpointPublication(t *testing.T) {
	f := newFragmentFixture(t, false)
	ctx := context.Background()
	ledger := artifactpostgres.NewObservationLedger(f.pool)
	var ref artifactapp.FragmentRef
	if err := f.pool.QueryRow(ctx, `SELECT kind,source_id,frame_digest FROM observation_candidates WHERE kind='run_basis'`).Scan(&ref.Kind, &ref.SourceID, &ref.FrameDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReadFragmentEvidence(ctx, ref); err == nil {
		t.Fatal("finalized objects alone accepted as core publication")
	}
	f.publishCore(t)
	if bytes.Contains(f.current.PhaseState, []byte(`"publication"`)) || f.current.PhaseStateDigest == f.firstDigest {
		t.Fatal("fixture did not preserve historical first proof window")
	}
	p := artifactapp.FragmentPublisher{Store: ledger, Objects: f.objects}
	for range 2 {
		if err := p.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, f.pool, "observation_publications", 2)
	assertCount(t, f.pool, "evidence_object_refs", 7)
	// Old core replay neither counts nor rewrites the new sidecar references.
	f.publishCore(t)
	assertCount(t, f.pool, "evidence_object_refs", 7)
	for _, sql := range []string{`UPDATE observation_publications SET object_key='changed'`, `UPDATE observation_publications SET object_size=999`, `DELETE FROM observation_publications`, `UPDATE observation_candidates SET status='pending' WHERE status='published'`} {
		if _, err := f.pool.Exec(ctx, sql); err == nil {
			t.Fatalf("published immutable evidence mutation accepted: %s", sql)
		}
	}
	e, err := ledger.ReadFragmentEvidence(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(e.Frame, func() []byte {
		r, err := f.objects.Open(ctx, e.Object.Key)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		return b.Bytes()
	}()) {
		t.Fatal("published bytes differ")
	}
	if _, err := (artifactapp.FragmentLoader{Metadata: ledger, Objects: f.objects}).Load(ctx, artifactapp.FragmentJob{Source: ref, Version: fragmentVersion}); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(f.current.PhaseState, &fields); err != nil {
		t.Fatal(err)
	}
	fields["publication"] = fragmentJSON(t, map[string]any{"trace_stream_id": f.finalized.Manifest.TraceStreamID, "artifact_ids": []string{f.command.FrozenArtifacts.ReportArtifactID, f.command.FrozenArtifacts.PatchArtifactID}})
	delete(fields, "schema_version")
	delete(fields, "frozen_artifacts")
	f.current, err = f.settlements.FreezeCheckpoint(ctx, workerapp.FreezeTurnSettlementCheckpointCommand{SettlementID: f.current.SettlementID, ExpectedRevision: f.current.Revision, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence, Checkpoint: workerapp.TurnSettlementCheckpoint{SchemaVersion: 1, FrozenArtifacts: f.command.FrozenArtifacts, AdditionalFields: fields}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.firstProof, fragmentProofSnapshot(t, f.pool)) {
		t.Fatal("core extension or publication changed sealed proof bytes")
	}
	if _, err := ledger.ReadFragmentEvidence(ctx, ref); err != nil {
		t.Fatal("later checkpoint extension invalidated first attestation", err)
	}
}

func fragmentProofSnapshot(t *testing.T, pool *pgxpool.Pool) []byte {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(), `SELECT jsonb_agg(jsonb_build_array(kind,source_id,frame_digest,frame,receipt,provenance) ORDER BY kind,source_id) FROM observation_candidates`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFragmentPublicationModelWaitsForExactParents(t *testing.T) {
	f := newFragmentFixture(t, true)
	ctx := context.Background()
	ledger := artifactpostgres.NewObservationLedger(f.pool)
	model := fragmentRef(t, f.pool, "model")
	_, err := ledger.ReadFragmentEvidence(ctx, model)
	assertFragmentCode(t, err, "parent_basis_pending", false)
	f.publishCore(t)
	_, err = ledger.ReadFragmentEvidence(ctx, model)
	assertFragmentCode(t, err, "parent_basis_pending", false)
	p := artifactapp.FragmentPublisher{Store: ledger, Objects: f.objects}
	for range 6 {
		if err := p.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE observation_candidates SET next_attempt_at=clock_timestamp() WHERE status='retryable'`); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, f.pool, "observation_publications", 3)
	loaded, err := (artifactapp.FragmentLoader{Metadata: ledger, Objects: f.objects}).Load(ctx, artifactapp.FragmentJob{Source: model, Version: fragmentVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Frames) != 3 {
		t.Fatalf("loaded %d frames", len(loaded.Frames))
	}
	for _, raw := range loaded.Frames {
		frame, err := artifactdomain.DecodeObservationFragment(raw)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Kind == artifactdomain.FragmentModel && (frame.Status != "failed" || frame.OutputJSON != "null" || frame.Turn.TaskID != "") {
			t.Fatal("model changed or artificial TaskID added")
		}
	}
	for _, kind := range []string{"model", "run_basis", "turn_basis"} {
		e, err := ledger.ReadFragmentEvidence(ctx, fragmentRef(t, f.pool, kind))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(e.Object.Key, "/turns/"+f.started.AgentTurnID+"/observation-fragment/") {
			t.Fatal("wrong object scope", e.Object.Key)
		}
	}
}

func TestFragmentPublicationLaterTurnKeepsInitialRunObjectAndOldBytes(t *testing.T) {
	first := newFragmentFixture(t, false)
	first.publishCore(t)
	ctx := context.Background()
	ledger := artifactpostgres.NewObservationLedger(first.pool)
	p := artifactapp.FragmentPublisher{Store: ledger, Objects: first.objects}
	for range 2 {
		if err := p.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runRef := fragmentRef(t, first.pool, "run_basis")
	original, err := ledger.ReadFragmentEvidence(ctx, runRef)
	if err != nil {
		t.Fatal(err)
	}
	f := seedFragmentLaterTurn(t, first)
	f.journal.Now = func() time.Time { return f.now }
	e := f.envelope("event", 2, "later-terminal", "agent_turn.terminal", json.RawMessage(`{}`))
	e.AckSequence = f.start.Sequence
	if _, err := f.journal.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{Authority: f.authority, Sequence: 2, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: fragmentJSON(t, e)}); err != nil {
		t.Fatal(err)
	}
	f.settlements.Now = func() time.Time { return f.now }
	id, _ := workerapp.TurnSettlementID(f.started.AgentTurnID)
	f.current, err = f.settlements.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: id, ExpectedRevision: 1, OwnerID: "later-owner"})
	if err != nil {
		t.Fatal(err)
	}
	f.freezeAndFinalize(t)
	f.publishCore(t)
	if err := p.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var ref artifactapp.FragmentRef
	if err := f.pool.QueryRow(ctx, `SELECT kind,source_id,frame_digest FROM observation_candidates WHERE kind='turn_basis' AND convert_from(frame,'UTF8')::jsonb->'turn'->>'agent_turn_id'=$1`, f.started.AgentTurnID).Scan(&ref.Kind, &ref.SourceID, &ref.FrameDigest); err != nil {
		t.Fatal(err)
	}
	loaded, err := (artifactapp.FragmentLoader{Metadata: ledger, Objects: f.objects}).Load(ctx, artifactapp.FragmentJob{Source: ref, Version: fragmentVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Frames) != 2 {
		t.Fatal("later turn parents", len(loaded.Frames))
	}
	nowRun, err := ledger.ReadFragmentEvidence(ctx, runRef)
	if err != nil {
		t.Fatal(err)
	}
	if nowRun.Object != original.Object || !bytes.Equal(nowRun.Frame, original.Frame) || !bytes.Equal(nowRun.Receipt, original.Receipt) {
		t.Fatal("later Turn rewrote initial Run")
	}
	later, err := ledger.ReadFragmentEvidence(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(later.Object.Key, "/turns/fragment-later-turn/") || !strings.Contains(nowRun.Object.Key, "/turns/"+first.started.AgentTurnID+"/") {
		t.Fatal("wrong initial/current object paths")
	}
	first.publishCore(t)
	assertCount(t, f.pool, "trace_manifests", 2)
	assertCount(t, f.pool, "observation_publications", 3)
}

// This historical terminal-row seed tests distinct-Turn evidence identity.
// It does not represent a full user queue/continuation path or reset a Run.
func seedFragmentLaterTurn(t *testing.T, first *fragmentFixture) *fragmentFixture {
	t.Helper()
	ctx := context.Background()
	f := *first
	f.started.AgentTurnID = "fragment-later-turn"
	f.now = first.now.Add(2 * time.Minute)
	f.terminalRowSeed = true
	if _, err := f.pool.Exec(ctx, `INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,started_at,deadline_at,terminal_at,terminal_reason)
 SELECT $2,worker_session_id,worker_run_id,agent_session_id,'queue','{"text":"second input"}','completed',$2,$3::timestamptz-interval '1 second',$3::timestamptz-interval '1 second',$3::timestamptz+interval '24 hours'-interval '1 second',$3,'end_turn' FROM agent_turns WHERE agent_turn_id=$1`, first.started.AgentTurnID, f.started.AgentTurnID, f.now); err != nil {
		t.Fatal(err)
	}
	if tag, err := f.pool.Exec(ctx, `INSERT INTO scheduled_commands(scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,due_at,status,idempotency_key,created_at,updated_at)
 SELECT $2,c.tenant_id,c.command_type,c.target_type,$2,c.expected_revision,'{}',t.deadline_at,'pending',$2,c.created_at,c.updated_at FROM scheduled_commands c JOIN agent_turns t ON t.agent_turn_id=$2 WHERE c.command_type='agent_turn_deadline' AND c.target_id=$1`, first.started.AgentTurnID, f.started.AgentTurnID); err != nil || tag.RowsAffected() != 1 {
		t.Fatal(err)
	}
	var exactDeadline bool
	if err := f.pool.QueryRow(ctx, `SELECT c.due_at=t.deadline_at FROM scheduled_commands c JOIN agent_turns t ON t.agent_turn_id=c.target_id WHERE t.agent_turn_id=$1 AND c.command_type='agent_turn_deadline'`, f.started.AgentTurnID).Scan(&exactDeadline); err != nil || !exactDeadline {
		t.Fatalf("deadline seed mismatch %v %v", exactDeadline, err)
	}
	return &f
}

func fragmentRef(t *testing.T, pool *pgxpool.Pool, kind string) artifactapp.FragmentRef {
	t.Helper()
	var ref artifactapp.FragmentRef
	if err := pool.QueryRow(context.Background(), `SELECT kind,source_id,frame_digest FROM observation_candidates WHERE kind=$1`, kind).Scan(&ref.Kind, &ref.SourceID, &ref.FrameDigest); err != nil {
		t.Fatal(err)
	}
	return ref
}
func assertFragmentCode(t *testing.T, err error, code string, permanent bool) {
	t.Helper()
	var typed *artifactapp.ProjectionError
	if !errors.As(err, &typed) || typed.Code != code || typed.Permanent != permanent {
		t.Fatalf("got %v, want %s permanent=%v", err, code, permanent)
	}
}

type fragmentFinalizeBarrier struct {
	workerapp.ObjectStore
	ready   chan workerapp.StoredObject
	release chan struct{}
}

func (s fragmentFinalizeBarrier) Finalize(ctx context.Context, i workerapp.ObjectIdentity, temp workerapp.TemporaryObject) (workerapp.StoredObject, error) {
	o, err := s.ObjectStore.Finalize(ctx, i, temp)
	if err != nil {
		return o, err
	}
	select {
	case s.ready <- o:
	case <-ctx.Done():
		return o, ctx.Err()
	}
	select {
	case <-s.release:
		return o, nil
	case <-ctx.Done():
		return o, ctx.Err()
	}
}

func TestFragmentOrphanFinalizeBarrierAndCrashRestart(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(fmt.Sprintf("crash=%v", crash), func(t *testing.T) {
			f := newFragmentFixture(t, false)
			f.publishCore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			barrier := fragmentFinalizeBarrier{ObjectStore: f.objects, ready: make(chan workerapp.StoredObject, 1), release: make(chan struct{})}
			p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: barrier}
			done := make(chan error, 1)
			go func() { done <- p.Tick(ctx) }()
			var object workerapp.StoredObject
			select {
			case object = <-barrier.ready:
			case <-time.After(10 * time.Second):
				t.Fatal("finalize barrier not reached")
			}
			inventory, err := f.objects.ListCandidates(ctx, time.Now().Add(time.Hour), 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range inventory {
				if candidate.Key == object.Key {
					t.Fatal("uncommitted fragment in inventory")
				}
			}
			result, err := (artifactapp.OrphanReconciler{Objects: f.objects, Metadata: artifactpostgres.NewOrphanStore(f.pool), Now: func() time.Time { return time.Now().Add(2 * time.Hour) }}).Execute(ctx, artifactapp.ReconcileOrphansCommand{OlderThan: time.Now().Add(time.Hour), Limit: 100})
			if err != nil || result.Deleted != 0 {
				t.Fatalf("reconcile %+v %v", result, err)
			}
			deleted, err := f.objects.DeleteCandidate(ctx, artifactapp.ObjectCandidate{Key: object.Key, Checksum: object.Checksum, Size: object.Size, State: artifactapp.ObjectStateFinalized})
			if err == nil || err.Error() != "observation_fragment_retained" || deleted.Deleted || deleted.AlreadyAbsent {
				t.Fatalf("forged inventory deletion %+v %v", deleted, err)
			}
			if err := f.objects.Verify(ctx, object.Key, object.Checksum); err != nil {
				t.Fatal(err)
			}
			if crash {
				cancel()
			} else {
				close(barrier.release)
			}
			if err := <-done; !crash && err != nil {
				t.Fatal(err)
			}
			if crash {
				assertCount(t, f.pool, "observation_publications", 0)
				if _, err := f.pool.Exec(context.Background(), `UPDATE observation_candidates SET lease_until=clock_timestamp()-interval '1 second' WHERE status='running'`); err != nil {
					t.Fatal(err)
				}
			} else {
				assertCount(t, f.pool, "observation_publications", 1)
			}
			// Restart after Finalize or after publication commit, before discovery.
			f.pool = postgrestest.Restart(t, f.pool)
			p = artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
			for range 3 {
				if err := p.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			assertCount(t, f.pool, "observation_publications", 2)
			assertCount(t, f.pool, "observation_fragment_jobs", 0)
			jobs := artifactpostgres.NewFragmentJobStore(f.pool)
			for range 2 {
				if _, err := jobs.Discover(context.Background(), fragmentVersion, 50); err != nil {
					t.Fatal(err)
				}
			}
			assertCount(t, f.pool, "observation_fragment_jobs", 2)
			if err := f.objects.Verify(context.Background(), object.Key, object.Checksum); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFragmentPublicationRechecksExactMetadataAfterObjectIO(t *testing.T) {
	for _, mutation := range []string{"input", "manifest_ref", "report_ref", "lease"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFragmentFixture(t, false)
			f.publishCore(t)
			ctx := context.Background()
			ledger := artifactpostgres.NewObservationLedger(f.pool)
			c, ok, err := ledger.ClaimCandidate(ctx, 30*time.Second)
			if err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			e, err := ledger.ReadFragmentEvidence(ctx, c.Source)
			if err != nil {
				t.Fatal(err)
			}
			c.Evidence = &e
			frame, _ := artifactdomain.DecodeObservationFragment(c.Frame)
			turn := frame.InitialTurnID
			if frame.Turn != nil {
				turn = string(frame.Turn.AgentTurnID)
			}
			i := workerapp.ObjectIdentity{TenantID: frame.Scope.TenantID, UserID: frame.Scope.UserID, WorkspaceID: frame.Scope.WorkspaceID, WorkerSessionID: frame.Scope.WorkerSessionID, WorkerRunID: frame.Scope.WorkerRunID, AgentTurnID: turn, Kind: artifactapp.ObservationFragmentObjectKind}
			temp, err := f.objects.PutTemporary(ctx, i, bytes.NewReader(c.Frame))
			if err != nil {
				t.Fatal(err)
			}
			object, err := f.objects.Finalize(ctx, i, temp)
			if err != nil {
				t.Fatal(err)
			}
			queries := map[string]string{"input": `UPDATE agent_turns SET content='{"text":"changed"}'`, "manifest_ref": `UPDATE trace_manifests SET manifest_size_bytes=manifest_size_bytes+1`, "report_ref": `UPDATE artifact_refs SET size_bytes=size_bytes+1 WHERE artifact_type='report'`, "lease": `UPDATE observation_candidates SET lease_until=clock_timestamp()-interval '1 second' WHERE status='running'`}
			if _, err := f.pool.Exec(ctx, queries[mutation]); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.Publish(ctx, c, object); err == nil {
				t.Fatal("stale I/O proof accepted")
			}
			assertCount(t, f.pool, "observation_publications", 0)
			assertCount(t, f.pool, "evidence_object_refs", 5)
			if err := f.objects.Verify(ctx, object.Key, object.Checksum); err != nil {
				t.Fatal("failed publication residue must remain retained", err)
			}
		})
	}
}

func TestFragmentPublicationExistingObjectRefMustMatchExactly(t *testing.T) {
	for _, mutation := range []string{"exact", "kind", "scope", "checksum", "size"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFragmentFixture(t, false)
			f.publishCore(t)
			ctx := context.Background()
			barrier := fragmentFinalizeBarrier{ObjectStore: f.objects, ready: make(chan workerapp.StoredObject, 1), release: make(chan struct{})}
			p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: barrier}
			done := make(chan error, 1)
			go func() { done <- p.Tick(ctx) }()
			object := <-barrier.ready
			kind, user, sum, size := artifactapp.ObservationFragmentObjectKind, "user-local", object.Checksum, object.Size
			switch mutation {
			case "kind":
				kind = "report"
			case "scope":
				user = "another-user"
			case "checksum":
				sum = artifactdomain.Checksum([]byte("different"))
			case "size":
				size++
			}
			if _, err := f.pool.Exec(ctx, `INSERT INTO evidence_object_refs(object_key,trace_stream_id,tenant_id,user_id,workspace_id,object_kind,object_checksum,size_bytes,visible_at) VALUES($1,$2,'tenant-local',$3,'workspace-demo',$4,$5,$6,clock_timestamp())`, object.Key, f.finalized.Manifest.TraceStreamID, user, kind, sum, size); err != nil {
				close(barrier.release)
				<-done
				t.Fatal(err)
			}
			close(barrier.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			want := 0
			if mutation == "exact" {
				want = 1
			}
			assertCount(t, f.pool, "observation_publications", want)
			assertCount(t, f.pool, "evidence_object_refs", 6)
			if err := f.objects.Verify(ctx, object.Key, object.Checksum); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFragmentPublicationWorkerDiscoversAndVerifiesWithRealHTTP(t *testing.T) {
	f := newFragmentFixture(t, true)
	f.publishCore(t)
	ctx := context.Background()
	p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
	for range 6 {
		if err := p.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE observation_candidates SET next_attempt_at=clock_timestamp() WHERE status='retryable'`); err != nil {
			t.Fatal(err)
		}
	}
	var root map[string]any
	observations := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var batch lf.IngestionBatch
			d := json.NewDecoder(r.Body)
			d.UseNumber()
			if err := d.Decode(&batch); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			for _, event := range batch.Batch {
				body := event.Body
				metadata := body["metadata"].(map[string]any)
				for key, value := range metadata {
					d := json.NewDecoder(strings.NewReader(value.(string)))
					d.UseNumber()
					var decoded any
					if err := d.Decode(&decoded); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					metadata[key] = decoded
				}
				if event.Type == "trace-create" {
					root = body
				} else {
					body["type"] = "SPAN"
					if event.Type == "generation-create" {
						body["type"] = "GENERATION"
					}
					observations[body["id"].(string)] = body
				}
			}
			w.WriteHeader(202)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/public/traces/") {
			_ = json.NewEncoder(w).Encode(root)
			return
		}
		_ = json.NewEncoder(w).Encode(observations[strings.TrimPrefix(r.URL.Path, "/api/public/observations/")])
	}))
	defer server.Close()
	client, err := lf.NewClient(lf.ClientOptions{BaseURL: server.URL, PublicKey: "synthetic-pk", SecretKey: "synthetic-sk"})
	if err != nil {
		t.Fatal(err)
	}
	projector, err := lf.NewFragmentProjector(client, fragmentScanner{})
	if err != nil {
		t.Fatal(err)
	}
	w := artifactapp.FragmentWorker{Jobs: artifactpostgres.NewFragmentJobStore(f.pool), Loader: artifactapp.FragmentLoader{Metadata: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}, Projector: projector}
	for range 3 {
		if err := w.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var passed int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_fragment_jobs WHERE status='passed' AND error_code IS NULL`).Scan(&passed); err != nil || passed != 3 {
		var states []byte
		_ = f.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(kind,status,error_code)) FROM observation_fragment_jobs`).Scan(&states)
		t.Fatalf("passed=%d err=%v states=%s", passed, err, states)
	}
	if len(observations) != 2 {
		t.Fatal("expected exact Turn and failed model", len(observations))
	}
	if !bytes.Equal(f.firstProof, fragmentProofSnapshot(t, f.pool)) {
		t.Fatal("worker modified original receipt/provenance")
	}
}

func TestFragmentPublicationBlockedRunGap(t *testing.T) {
	f := newFragmentFixture(t, true, true)
	f.publishCore(t)
	ctx := context.Background()
	ledger := artifactpostgres.NewObservationLedger(f.pool)
	_, err := ledger.ReadFragmentEvidence(ctx, fragmentRef(t, f.pool, "model"))
	assertFragmentCode(t, err, "parent_basis_unavailable", true)
	p := artifactapp.FragmentPublisher{Store: ledger, Objects: f.objects}
	for range 3 {
		if err := p.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, f.pool, "observation_publications", 1)
	turn := fragmentRef(t, f.pool, "turn_basis")
	_, err = (artifactapp.FragmentLoader{Metadata: ledger, Objects: f.objects}).Load(ctx, artifactapp.FragmentJob{Source: turn, Version: fragmentVersion})
	assertFragmentCode(t, err, "parent_basis_unavailable", true)
	if !bytes.Equal(f.firstProof, fragmentProofSnapshot(t, f.pool)) {
		t.Fatal("blocked Run gap rewritten")
	}
}

func TestFragmentPublicationMissingExpectedParentProofIsNotPending(t *testing.T) {
	f := newFragmentFixture(t, true)
	f.publishCore(t)
	ctx := context.Background()
	// Simulate storage corruption before publication, never a production repair.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE observation_candidates DISABLE TRIGGER observation_candidates_immutable; DELETE FROM observation_candidates WHERE kind='run_basis'; ALTER TABLE observation_candidates ENABLE TRIGGER observation_candidates_immutable`); err != nil {
		t.Fatal(err)
	}
	_, err := artifactpostgres.NewObservationLedger(f.pool).ReadFragmentEvidence(ctx, fragmentRef(t, f.pool, "model"))
	assertFragmentCode(t, err, "fragment_proof_invalid", true)
}

func TestFragmentPublicationRejectsChangedModelAuthority(t *testing.T) {
	for _, mutation := range []string{"event_received", "outcome", "start_digest"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFragmentFixture(t, true)
			f.publishCore(t)
			ctx := context.Background()
			queries := map[string]string{
				"event_received": `UPDATE runtime_control_messages SET effect_status='received',outcome=NULL,apply_idempotency_key=NULL,apply_outcome_digest=NULL,applied_at=NULL WHERE direction='event' AND envelope->>'message_type'='model.observation'`,
				"outcome":        `UPDATE runtime_control_messages SET outcome='{"status":"ignored"}' WHERE direction='event' AND envelope->>'message_type'='model.observation'`,
				"start_digest":   `UPDATE runtime_control_messages SET fingerprint='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`,
			}
			if _, err := f.pool.Exec(ctx, queries[mutation]); err != nil {
				t.Fatal(err)
			}
			_, err := artifactpostgres.NewObservationLedger(f.pool).ReadFragmentEvidence(ctx, fragmentRef(t, f.pool, "model"))
			assertFragmentCode(t, err, "fragment_proof_invalid", true)
		})
	}
}

func TestFragmentPublicationRejectsChangedActualObjectBytes(t *testing.T) {
	for _, kind := range []string{"report", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			f := newFragmentFixture(t, false)
			f.publishCore(t)
			key := f.finalized.ManifestObject.Key
			if kind == "report" {
				for _, ref := range f.finalized.ArtifactRefs {
					if ref.Type == "report" {
						key = strings.TrimPrefix(ref.URI, "object://")
					}
				}
			}
			if err := os.WriteFile(filepath.Join(f.objectRoot, filepath.FromSlash(key)), []byte("changed actual bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
			for range 2 {
				if err := p.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			assertCount(t, f.pool, "observation_publications", 0)
			assertCount(t, f.pool, "evidence_object_refs", 5)
			var blocked int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM observation_candidates WHERE status='blocked'`).Scan(&blocked); err != nil || blocked != 2 {
				t.Fatalf("object corruption blocked=%d %v", blocked, err)
			}
		})
	}
}

func TestFragmentPublicationBlocksCorruptedRetainedFragment(t *testing.T) {
	for _, crash := range []bool{true, false} {
		t.Run(fmt.Sprintf("restart_before_finalize=%v", crash), func(t *testing.T) {
			f := newFragmentFixture(t, false)
			f.publishCore(t)
			// Isolate the damaged Run candidate from the independently pending Turn.
			if _, err := f.pool.Exec(context.Background(), `UPDATE observation_candidates SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE kind<>'run_basis'`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			barrier := fragmentFinalizeBarrier{ObjectStore: f.objects, ready: make(chan workerapp.StoredObject, 1), release: make(chan struct{})}
			p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: barrier}
			done := make(chan error, 1)
			go func() { done <- p.Tick(ctx) }()
			var object workerapp.StoredObject
			select {
			case object = <-barrier.ready:
			case <-time.After(10 * time.Second):
				t.Fatal("finalize barrier not reached")
			}
			if err := os.WriteFile(filepath.Join(f.objectRoot, filepath.FromSlash(object.Key)), []byte("damaged retained fragment"), 0600); err != nil {
				t.Fatal(err)
			}
			if crash {
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled publisher: %v", err)
				}
				if _, err := f.pool.Exec(context.Background(), `UPDATE observation_candidates SET lease_until=clock_timestamp()-interval '1 second' WHERE status='running'`); err != nil {
					t.Fatal(err)
				}
				f.pool = postgrestest.Restart(t, f.pool)
				p = artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
				if err := p.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				close(barrier.release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				p.Objects = f.objects
			}
			var status, code string
			if err := f.pool.QueryRow(context.Background(), `SELECT status,error_code FROM observation_candidates WHERE kind='run_basis'`).Scan(&status, &code); err != nil {
				t.Fatal(err)
			}
			if status != "blocked" || code != "source_checksum_mismatch" {
				t.Fatalf("corrupted retained fragment status=%s code=%s; want blocked/source_checksum_mismatch", status, code)
			}
			uploads := func() []string {
				t.Helper()
				var paths []string
				err := filepath.WalkDir(filepath.Join(f.objectRoot, "temporary"), func(path string, entry os.DirEntry, err error) error {
					if err == nil && !entry.IsDir() && strings.Contains(path, "/observation-fragment/") {
						paths = append(paths, path)
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				return paths
			}
			retained := uploads()
			wantUploads := 0
			if crash {
				wantUploads = 1 // Failed re-finalization retains its upload by policy.
			}
			if len(retained) != wantUploads {
				t.Fatalf("retained temporary uploads=%d; want %d", len(retained), wantUploads)
			}
			before := strings.Join(retained, "\n")
			for range 3 {
				if _, err := f.pool.Exec(context.Background(), `UPDATE observation_candidates SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE status='retryable'`); err != nil {
					t.Fatal(err)
				}
				if err := p.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if after := strings.Join(uploads(), "\n"); after != before {
				t.Fatalf("blocked candidate created more retained uploads: before=%q after=%q", before, after)
			}
			assertCount(t, f.pool, "observation_publications", 0)
			assertCount(t, f.pool, "evidence_object_refs", 5)
			if retained, err := os.ReadFile(filepath.Join(f.objectRoot, filepath.FromSlash(object.Key))); err != nil || string(retained) != "damaged retained fragment" {
				t.Fatalf("retained evidence removed or rewritten: %q %v", retained, err)
			}
		})
	}
}

func TestFragmentPublicationDoesNotWaitForLiveOwnerLocks(t *testing.T) {
	f := newFragmentFixture(t, true)
	f.publishCore(t)
	ctx := context.Background()
	// Publish both parents before holding the execution owner's locks.
	if _, err := f.pool.Exec(ctx, `UPDATE observation_candidates SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE kind='model'`); err != nil {
		t.Fatal(err)
	}
	p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
	for range 2 {
		if err := p.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, `UPDATE observation_candidates SET next_attempt_at=clock_timestamp() WHERE kind='model'`); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var ignored int
	// Existing journal order: Session/Runtime, original event, then settlement.
	if err := tx.QueryRow(ctx, `SELECT 1 FROM worker_sessions s JOIN runtime_instances r USING(worker_session_id) WHERE s.worker_session_id=$1 FOR UPDATE OF s,r`, f.started.WorkerSessionID).Scan(&ignored); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT 1 FROM runtime_control_messages WHERE message_id='model-event' FOR UPDATE`).Scan(&ignored); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT 1 FROM worker_turn_settlements WHERE settlement_id=$1 FOR UPDATE`, f.current.SettlementID).Scan(&ignored); err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	jobCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Tick(jobCtx) }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("publication waits on live execution owner: %v", err)
			}
			assertCount(t, f.pool, "observation_publications", 3)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			return
		case <-ticker.C:
			var blocked bool
			if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				cancel()
				<-done
				t.Fatal("pg_blocking_pids proves publication waits on live execution owner")
			}
		case <-jobCtx.Done():
			t.Fatal("publication did not finish while owner locks remained held")
		}
	}
}
