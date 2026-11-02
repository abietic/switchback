//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/platform/secretcheck"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

type basisPGFixture struct {
	pool    *pgxpool.Pool
	service workerapp.TurnSettlementService
	current workerapp.TurnSettlement
	command workerapp.FreezeTurnSettlementCheckpointCommand
	started workerapp.StartedWorkerRun
}

func newBasisPGFixture(t *testing.T, active bool) basisPGFixture {
	t.Helper()
	f := basisPGFixture{pool: postgrestest.Open(t)}
	if active {
		if err := artifactpostgres.NewFragmentJobStore(f.pool).Activate(context.Background(), "langfuse.projection.v3"); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	fixture := prepareTerminalSettlementApplyFixture(t, f.pool, now, "basis", func(current workerapp.TurnSettlement, command workerapp.FreezeTurnSettlementCheckpointCommand) {
		f.current = current
		f.command = command
	})
	f.started = fixture.Started
	store, err := workerpostgres.NewTurnSettlementRepositoryWithObservation(f.pool, &artifactpostgres.ObservationCaptureStore{})
	if err != nil {
		t.Fatal(err)
	}
	f.service = workerapp.TurnSettlementService{Store: store, Now: func() time.Time { return now.Add(7 * time.Minute) }}
	return f
}

func basisCheckpointBytes(t *testing.T, command workerapp.FreezeTurnSettlementCheckpointCommand) ([]byte, string) {
	t.Helper()
	fields := map[string]any{"schema_version": command.Checkpoint.SchemaVersion, "frozen_artifacts": command.Checkpoint.FrozenArtifacts}
	for k, v := range command.Checkpoint.AdditionalFields {
		fields[k] = v
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(raw)
	if err != nil {
		t.Fatal(err)
	}
	return canonical, digest
}

func prepareBasisCapture(t *testing.T, f basisPGFixture, mutate func(*artifactdomain.ObservationFragment)) *workerapp.RuntimeObservationCapture {
	t.Helper()
	ctx := context.Background()
	c := f.current
	var scope artifactdomain.ObservationScope
	var input []byte
	var start, created time.Time
	var taskID string
	var network artifactdomain.RuntimeNetworkEvidence
	if err := f.pool.QueryRow(ctx, `SELECT s.tenant_id,s.user_id,s.workspace_id,s.worker_session_id,r.worker_run_id,t.content,t.started_at,r.created_at,COALESCE(r.task_id,''),i.network_policy_id,i.network_policy_revision,i.network_policy_digest,i.network_enforcement_status FROM worker_runs r JOIN worker_sessions s USING(worker_session_id) JOIN agent_turns t ON t.agent_turn_id=$2 JOIN runtime_instances i ON i.runtime_instance_id=$3 WHERE r.worker_run_id=$1`, c.WorkerRunID, c.AgentTurnID, c.RuntimeInstanceID).Scan(&scope.TenantID, &scope.UserID, &scope.WorkspaceID, &scope.WorkerSessionID, &scope.WorkerRunID, &input, &start, &created, &taskID, &network.ID, &network.Revision, &network.Digest, &network.EnforcementStatus); err != nil {
		t.Fatal(err)
	}
	identity := artifactdomain.TurnIdentity{TenantID: scope.TenantID, UserID: scope.UserID, WorkspaceID: scope.WorkspaceID, TaskID: contractsv1.TaskID(taskID), WorkerSessionID: contractsv1.WorkerSessionID(c.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(c.WorkerRunID), AgentSessionID: contractsv1.AgentSessionID(c.AgentSessionID), AgentTurnID: contractsv1.AgentTurnID(c.AgentTurnID), RuntimeInstanceID: contractsv1.RuntimeInstanceID(c.RuntimeInstanceID), RuntimeGeneration: uint32(c.RuntimeGeneration), TraceStreamID: "trace-" + c.AgentTurnID, LangfuseTraceID: "langfuse-" + c.AgentTurnID, RuntimeNetwork: network}
	report := []byte("# Frozen report\nDone\n")
	reportJSON, _ := json.Marshal(string(report))
	frames := []artifactdomain.ObservationFragment{{SchemaVersion: "observation.fragment.v1", Kind: artifactdomain.FragmentTurnBasis, Scope: scope, Turn: &identity, StartedAt: start, EndedAt: &c.Source.OccurredAt, InputJSON: string(input), OutputJSON: string(reportJSON)}, {SchemaVersion: "observation.fragment.v1", Kind: artifactdomain.FragmentRunBasis, Scope: scope, InitialTurnID: c.AgentTurnID, StartedAt: created, InputJSON: string(input)}}
	scanner, err := secretcheck.New([]secretcheck.KnownToken{{Label: "fixture", Value: "fixture-secret-not-in-observation"}})
	if err != nil {
		t.Fatal(err)
	}
	_, digest := basisCheckpointBytes(t, f.command)
	provenance := artifactapp.ObservationProvenance{CheckpointDigest: digest, InputDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(input)), ReportDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(report))}
	var observations []artifactapp.PreparedObservation
	for _, frame := range frames {
		if mutate != nil {
			mutate(&frame)
		}
		raw, err := artifactdomain.EncodeObservationFragment(frame)
		if err != nil {
			t.Fatal(err)
		}
		p := (artifactapp.ObservationCapture{Compiler: artifactlangfuse.FragmentCompiler{}, Scanner: scanner, Scope: artifactapp.ObservationScanScope{WorkerSessionID: c.WorkerSessionID, RuntimeInstanceID: c.RuntimeInstanceID, RuntimeGeneration: c.RuntimeGeneration}}).Prepare(ctx, raw, struct {
			Input  json.RawMessage
			Report string
		}{input, string(report)}, provenance)
		if p.ErrorCode != "" {
			t.Fatal(p.ErrorCode)
		}
		observations = append(observations, p)
	}
	sealed, err := artifactapp.EncodeSealedObservationBundle(artifactapp.SealedObservationBundle{SchemaVersion: "observation.capture.bundle.v1", Observations: observations})
	if err != nil {
		t.Fatal(err)
	}
	return &workerapp.RuntimeObservationCapture{Sealed: sealed}
}

func TestObservationBasisContentFreezeReplayAndMissingProof(t *testing.T) {
	f := newBasisPGFixture(t, true)
	f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
	wantBytes, wantDigest := basisCheckpointBytes(t, f.command)
	frozen, err := f.service.FreezeCheckpoint(context.Background(), f.command)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.PhaseStateDigest != wantDigest || !bytes.Equal(frozen.PhaseState, wantBytes) {
		t.Fatal("sidecar changed canonical checkpoint")
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM observation_candidates WHERE frame IS NOT NULL AND receipt IS NOT NULL AND provenance->>'checkpoint_digest'=$1`, wantDigest).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("first freeze content receipts=%d want 2", count)
	}
	// Response loss: replay has no scanner or transient sidecar, but uses sealed evidence.
	f.command.ObservationCapture = nil
	replayed, err := f.service.FreezeCheckpoint(context.Background(), f.command)
	if err != nil || replayed.Revision != frozen.Revision {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE observation_candidates DISABLE TRIGGER observation_candidates_immutable; DELETE FROM observation_candidates WHERE kind='turn_basis'; ALTER TABLE observation_candidates ENABLE TRIGGER observation_candidates_immutable`); err != nil {
		t.Fatal(err)
	}
	f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
	if _, err := f.service.FreezeCheckpoint(context.Background(), f.command); err == nil {
		t.Fatal("missing proof was re-signed")
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM observation_candidates WHERE kind='turn_basis'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("replay recreated deleted proof")
	}
}

func TestObservationBasisReceiptFailureRollsBackFirstFreeze(t *testing.T) {
	f := newBasisPGFixture(t, true)
	f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_basis_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='run_basis' THEN RAISE EXCEPTION 'injected receipt failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_basis_receipt BEFORE INSERT ON observation_candidates FOR EACH ROW EXECUTE FUNCTION reject_basis_receipt()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.FreezeCheckpoint(ctx, f.command); err == nil {
		t.Fatal("receipt failure was ignored")
	}
	current, err := f.service.Load(ctx, f.current.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if current.Revision != f.current.Revision || current.PhaseStateDigest != f.current.PhaseStateDigest || count != 0 {
		t.Fatal("receipt failure partially committed checkpoint or first Turn receipt")
	}
	if _, err := f.pool.Exec(ctx, `DROP TRIGGER reject_basis_receipt ON observation_candidates; DROP FUNCTION reject_basis_receipt()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.FreezeCheckpoint(ctx, f.command); err != nil {
		t.Fatal(err)
	}
}

func TestObservationBasisLegacyReplayIsExcludedAndNeverSigned(t *testing.T) {
	f := newBasisPGFixture(t, false)
	first, err := f.service.FreezeCheckpoint(context.Background(), f.command)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifactpostgres.NewFragmentJobStore(f.pool).Activate(context.Background(), "langfuse.projection.v3"); err != nil {
		t.Fatal(err)
	}
	f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
	replay, err := f.service.FreezeCheckpoint(context.Background(), f.command)
	if err != nil || replay.PhaseStateDigest != first.PhaseStateDigest {
		t.Fatal("excluded legacy replay failed")
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM observation_candidates`).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy replay signed new proof")
	}
}

func TestObservationBasisScopeMismatchDegradesWithoutContent(t *testing.T) {
	f := newBasisPGFixture(t, true)
	f.command.ObservationCapture = prepareBasisCapture(t, f, func(frame *artifactdomain.ObservationFragment) {
		frame.Scope.WorkspaceID = "wrong-workspace"
		if frame.Turn != nil {
			frame.Turn.WorkspaceID = "wrong-workspace"
		}
	})
	if _, err := f.service.FreezeCheckpoint(context.Background(), f.command); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM observation_candidates WHERE frame IS NULL AND receipt IS NULL AND error_code='capture_invalid'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("mismatched scope leaked receipt: count=%d err=%v", count, err)
	}
}

func TestObservationBasisRunReceiptMustBindSameFrozenReport(t *testing.T) {
	f := newBasisPGFixture(t, true)
	f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
	bundle, err := artifactapp.DecodeSealedObservationBundle(f.command.ObservationCapture.Sealed)
	if err != nil {
		t.Fatal(err)
	}
	r, err := artifactapp.DecodeObservationScanReceipt(bundle.Observations[1].Receipt, bundle.Observations[1].Frame)
	if err != nil {
		t.Fatal(err)
	}
	r.Provenance.ReportDigest = turnSettlementTestDigest
	bundle.Observations[1].Receipt, _ = json.Marshal(r)
	f.command.ObservationCapture.Sealed, err = artifactapp.EncodeSealedObservationBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.FreezeCheckpoint(context.Background(), f.command); err != nil {
		t.Fatal(err)
	}
	var code string
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(error_code,'') FROM observation_candidates WHERE kind='run_basis'`).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != "capture_invalid" {
		t.Fatalf("Run receipt with different frozen report was sealed: code=%q", code)
	}
}

func TestObservationBasisLaterCheckpointExtensionKeepsFirstProof(t *testing.T) {
	f := newBasisPGFixture(t, true)
	f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
	ctx := context.Background()
	first, err := f.service.FreezeCheckpoint(ctx, f.command)
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := f.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('frame',encode(frame,'hex'),'receipt',encode(receipt,'hex'),'provenance',provenance) ORDER BY kind) FROM observation_candidates`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]json.RawMessage
	if err := json.Unmarshal(first.PhaseState, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint["evidence"], _ = json.Marshal(map[string]any{"manifest_digest": turnSettlementTestDigest, "finalized": validFinalizedTurnEvidenceCheckpoint(f.started, f.command.Checkpoint.FrozenArtifacts.ReportArtifactID, f.command.Checkpoint.FrozenArtifacts.PatchArtifactID, f.command.Checkpoint.FrozenArtifacts.CreatedAt)})
	raw, _ := json.Marshal(checkpoint)
	acceptedExtension := workerapp.FreezeTurnSettlementCheckpointCommand{SettlementID: first.SettlementID, ExpectedRevision: first.Revision, LeaseOwnerID: first.LeaseOwnerID, LeaseFence: first.LeaseFence, Checkpoint: typedTurnSettlementCheckpoint(t, raw)}
	accepted, err := f.service.FreezeCheckpoint(ctx, acceptedExtension)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.FreezeCheckpoint(ctx, acceptedExtension); err != nil {
		t.Fatalf("accepted extension replay before phase advance: %v", err)
	}
	advanced, err := f.service.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{SettlementID: first.SettlementID, ExpectedRevision: accepted.Revision, LeaseOwnerID: first.LeaseOwnerID, LeaseFence: first.LeaseFence, FromPhase: first.Phase, ToPhase: workerapp.TurnSettlementEvidencePrepared, Checkpoint: raw})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint["task_plan"] = json.RawMessage(`{"action":"none","reason":"no_task"}`)
	raw, _ = json.Marshal(checkpoint)
	command := workerapp.FreezeTurnSettlementCheckpointCommand{SettlementID: first.SettlementID, ExpectedRevision: advanced.Revision, LeaseOwnerID: first.LeaseOwnerID, LeaseFence: first.LeaseFence, Checkpoint: typedTurnSettlementCheckpoint(t, raw)}
	extended, err := f.service.FreezeCheckpoint(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if extended.PhaseStateDigest == first.PhaseStateDigest {
		t.Fatal("fixture did not extend checkpoint")
	}
	if _, err := f.service.FreezeCheckpoint(ctx, command); err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err := f.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('frame',encode(frame,'hex'),'receipt',encode(receipt,'hex'),'provenance',provenance) ORDER BY kind) FROM observation_candidates`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("later checkpoint extension rebound first-freeze receipt")
	}
}

func TestObservationBasisInitialAmbiguityOnlyBlocksRun(t *testing.T) {
	for _, scenario := range []string{"missing", "ambiguous", "queue", "later"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBasisPGFixture(t, true)
			ctx := context.Background()
			f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
			switch scenario {
			case "missing":
				if _, err := f.pool.Exec(ctx, `UPDATE agent_turns SET created_at=created_at+interval '1 second' WHERE agent_turn_id=$1`, f.current.AgentTurnID); err != nil {
					t.Fatal(err)
				}
			case "ambiguous", "later":
				if _, err := f.pool.Exec(ctx, `INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,started_at,deadline_at,terminal_at,terminal_reason) SELECT 'other-initial',worker_session_id,worker_run_id,agent_session_id,'queue',content,'completed','other-initial',created_at,started_at,deadline_at,started_at+interval '1 second','done' FROM agent_turns WHERE agent_turn_id=$1`, f.current.AgentTurnID); err != nil {
					t.Fatal(err)
				}
				if scenario == "later" {
					if _, err := f.pool.Exec(ctx, `UPDATE agent_turns SET created_at=created_at+interval '1 second' WHERE agent_turn_id=$1`, f.current.AgentTurnID); err != nil {
						t.Fatal(err)
					}
					bundle, _ := artifactapp.DecodeSealedObservationBundle(f.command.ObservationCapture.Sealed)
					bundle.Observations = bundle.Observations[:1]
					f.command.ObservationCapture.Sealed, _ = artifactapp.EncodeSealedObservationBundle(bundle)
				}
			case "queue":
				if _, err := f.pool.Exec(ctx, `UPDATE agent_turns SET mode='queue' WHERE agent_turn_id=$1`, f.current.AgentTurnID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.service.FreezeCheckpoint(ctx, f.command); err != nil {
				t.Fatal(err)
			}
			var turns, runs, gaps int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='turn_basis' AND frame IS NOT NULL),count(*) FILTER(WHERE kind='run_basis' AND frame IS NOT NULL),count(*) FILTER(WHERE kind='run_basis' AND frame IS NULL AND error_code='source_invalid') FROM observation_candidates`).Scan(&turns, &runs, &gaps); err != nil {
				t.Fatal(err)
			}
			wantRuns, wantGaps := 0, 1
			if scenario == "queue" {
				wantRuns, wantGaps = 1, 0
			}
			if scenario == "later" {
				wantGaps = 0
			}
			if turns != 1 || runs != wantRuns || gaps != wantGaps {
				t.Fatalf("Turn/Run/gap=%d/%d/%d", turns, runs, gaps)
			}
			if _, err := f.service.FreezeCheckpoint(ctx, f.command); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func nextBasisPGTurn(t *testing.T, first basisPGFixture) basisPGFixture {
	t.Helper()
	ctx := context.Background()
	f := first
	f.started.AgentTurnID = "second-basis-turn"
	if _, err := f.pool.Exec(ctx, `INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,started_at,deadline_at,terminal_at,terminal_reason)
 SELECT $2,worker_session_id,worker_run_id,agent_session_id,'queue','{"text":"second input"}', 'completed',$2,created_at+interval '1 minute',started_at+interval '1 minute',deadline_at+interval '1 minute',started_at+interval '2 minutes','done' FROM agent_turns WHERE agent_turn_id=$1`, first.current.AgentTurnID, f.started.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO scheduled_commands(scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,due_at,status,idempotency_key,created_at,updated_at)
 SELECT $2,tenant_id,command_type,target_type,$2,expected_revision,'{}',due_at+interval '1 minute','pending',$2,created_at,updated_at FROM scheduled_commands WHERE command_type='agent_turn_deadline' AND target_id=$1`, first.current.AgentTurnID, f.started.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	authority := workerapp.RuntimeControlAuthority{WorkerSessionID: first.current.WorkerSessionID, RuntimeInstanceID: first.current.RuntimeInstanceID, RuntimeGeneration: first.current.RuntimeGeneration, ChannelEpoch: first.current.ChannelEpoch}
	if err := f.pool.QueryRow(ctx, `SELECT process_nonce,stream_attempt,owner_fence FROM runtime_control_channels WHERE runtime_instance_id=$1`, authority.RuntimeInstanceID).Scan(&authority.ProcessNonce, &authority.StreamAttempt, &authority.OwnerFence); err != nil {
		t.Fatal(err)
	}
	journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(f.pool), Now: f.service.Now}
	if _, err := journal.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{Authority: authority, Sequence: 2, MessageID: "second-basis-terminal", IdempotencyKey: "second-basis-terminal", Envelope: runtimeControlEnvelopeWithAck(t, runtimeControlEnvelope(f.started, "event", 2, "second-basis-terminal", "second-basis-terminal", "agent_turn.terminal"), 1)}); err != nil {
		t.Fatal(err)
	}
	settlementID, err := workerapp.TurnSettlementID(f.started.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	f.current, err = f.service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: settlementID, ExpectedRevision: 1, OwnerID: "second-basis-owner"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := basisCheckpointBytes(t, first.command)
	raw = bytes.ReplaceAll(raw, []byte(first.current.AgentTurnID), []byte(f.current.AgentTurnID))
	f.command = workerapp.FreezeTurnSettlementCheckpointCommand{SettlementID: settlementID, ExpectedRevision: f.current.Revision, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence, Checkpoint: typedTurnSettlementCheckpoint(t, raw)}
	f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
	return f
}

func TestObservationBasisAmbiguousRunGapAllowsTwoTurns(t *testing.T) {
	for _, scenario := range []string{"missing", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBasisPGFixture(t, true)
			ctx := context.Background()
			if scenario == "missing" {
				if _, err := f.pool.Exec(ctx, `UPDATE agent_turns SET created_at=created_at+interval '1 second' WHERE agent_turn_id=$1`, f.current.AgentTurnID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.pool.Exec(ctx, `INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,started_at,deadline_at,terminal_at,terminal_reason) SELECT 'other-initial',worker_session_id,worker_run_id,agent_session_id,'queue',content,'completed','other-initial',created_at,started_at,deadline_at,started_at+interval '1 second','done' FROM agent_turns WHERE agent_turn_id=$1`, f.current.AgentTurnID); err != nil {
				t.Fatal(err)
			}
			f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
			if _, err := f.service.FreezeCheckpoint(ctx, f.command); err != nil {
				t.Fatal(err)
			}
			var before, after []byte
			if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM observation_candidates c WHERE kind='run_basis' AND error_code='source_invalid' AND frame IS NULL AND receipt IS NULL`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			second := nextBasisPGTurn(t, f)
			// Extending the original checkpoint must not rebind its gap digest.
			original, err := f.service.Load(ctx, f.current.SettlementID)
			if err != nil {
				t.Fatal(err)
			}
			var extended map[string]json.RawMessage
			if err := json.Unmarshal(original.PhaseState, &extended); err != nil {
				t.Fatal(err)
			}
			extended["evidence"], _ = json.Marshal(map[string]any{"manifest_digest": turnSettlementTestDigest, "finalized": validFinalizedTurnEvidenceCheckpoint(f.started, f.command.Checkpoint.FrozenArtifacts.ReportArtifactID, f.command.Checkpoint.FrozenArtifacts.PatchArtifactID, f.command.Checkpoint.FrozenArtifacts.CreatedAt)})
			raw, _ := json.Marshal(extended)
			f.command.Checkpoint = typedTurnSettlementCheckpoint(t, raw)
			f.command.ExpectedRevision = original.Revision
			f.command.ObservationCapture = nil
			if _, err := f.service.FreezeCheckpoint(ctx, f.command); err != nil {
				t.Fatal(err)
			}
			for index, turn := range []basisPGFixture{second, f, second} {
				if index > 0 {
					turn.command.ObservationCapture = nil
				}
				wantBytes, wantDigest := basisCheckpointBytes(t, turn.command)
				frozen, err := turn.service.FreezeCheckpoint(ctx, turn.command)
				if err != nil {
					t.Fatalf("freeze/replay %s: %v", turn.current.AgentTurnID, err)
				}
				if frozen.PhaseStateDigest != wantDigest || !bytes.Equal(frozen.PhaseState, wantBytes) {
					t.Fatal("Turn checkpoint changed")
				}
			}
			if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM observation_candidates c WHERE kind='run_basis'`).Scan(&after); err != nil || !bytes.Equal(before, after) {
				t.Fatalf("Run gap changed: %v", err)
			}
			for _, turn := range []basisPGFixture{f, second} {
				var count int
				if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates WHERE kind='turn_basis' AND frame IS NOT NULL AND receipt IS NOT NULL AND provenance->>'settlement_id'=$1`, turn.current.SettlementID).Scan(&count); err != nil || count != 1 {
					t.Fatalf("Turn %s basis count=%d: %v", turn.current.AgentTurnID, count, err)
				}
			}
			assertNoBasisConflicts(t, f)
		})
	}
}

func assertNoBasisConflicts(t *testing.T, f basisPGFixture) {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM observation_fragment_conflicts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("gap reuse created conflicts: count=%d err=%v", count, err)
	}
}

func TestObservationBasisRunGapRevalidatesOriginalProof(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, corruption := range []string{"input", "tenant", "unfrozen", "origin_turn_proof", "wrong_turn_source", "gap_scope"} {
			t.Run(fmt.Sprintf("replay=%t/%s", replay, corruption), func(t *testing.T) {
				f := newBasisPGFixture(t, true)
				ctx := context.Background()
				if _, err := f.pool.Exec(ctx, `UPDATE agent_turns SET created_at=created_at+interval '1 second' WHERE agent_turn_id=$1`, f.current.AgentTurnID); err != nil {
					t.Fatal(err)
				}
				f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
				if _, err := f.service.FreezeCheckpoint(ctx, f.command); err != nil {
					t.Fatal(err)
				}
				second := nextBasisPGTurn(t, f)
				before := second.current
				if replay {
					var err error
					before, err = second.service.FreezeCheckpoint(ctx, second.command)
					if err != nil {
						t.Fatal(err)
					}
					second.command.ObservationCapture = nil
				}
				var err error
				switch corruption {
				case "input":
					_, err = f.pool.Exec(ctx, `UPDATE agent_turns SET content='{"text":"changed original input"}' WHERE agent_turn_id=$1`, f.current.AgentTurnID)
				case "tenant":
					_, err = f.pool.Exec(ctx, `UPDATE worker_turn_settlements SET tenant_id='wrong-tenant' WHERE settlement_id=$1`, f.current.SettlementID)
				case "unfrozen":
					_, err = f.pool.Exec(ctx, `UPDATE worker_turn_settlements SET phase_state='{}' WHERE settlement_id=$1`, f.current.SettlementID)
				case "origin_turn_proof":
					_, err = f.pool.Exec(ctx, `ALTER TABLE observation_candidates DISABLE TRIGGER observation_candidates_immutable`)
					if err == nil {
						_, err = f.pool.Exec(ctx, `DELETE FROM observation_candidates WHERE kind='turn_basis' AND provenance->>'settlement_id'=$1`, f.current.SettlementID)
					}
				case "wrong_turn_source":
					_, err = f.pool.Exec(ctx, `ALTER TABLE observation_candidates DISABLE TRIGGER observation_candidates_immutable`)
					if err == nil {
						_, err = f.pool.Exec(ctx, `UPDATE observation_candidates SET source_id=$2 WHERE kind='turn_basis' AND provenance->>'settlement_id'=$1`, f.current.SettlementID, turnSettlementTestDigest)
					}
				case "gap_scope":
					_, err = f.pool.Exec(ctx, `ALTER TABLE observation_candidates DISABLE TRIGGER observation_candidates_immutable`)
					if err == nil {
						_, err = f.pool.Exec(ctx, `UPDATE observation_candidates SET tenant_id='wrong-tenant' WHERE kind='run_basis'`)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				var gapBefore, gapAfter []byte
				if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM observation_candidates c WHERE kind='run_basis'`).Scan(&gapBefore); err != nil {
					t.Fatal(err)
				}
				if _, err := second.service.FreezeCheckpoint(ctx, second.command); err == nil || err.Error() != "observation_receipt_binding_invalid" {
					t.Fatalf("invalid original proof accepted: %v", err)
				}
				after, err := second.service.Load(ctx, second.current.SettlementID)
				if err != nil || after.Revision != before.Revision || after.PhaseStateDigest != before.PhaseStateDigest || !bytes.Equal(after.PhaseState, before.PhaseState) {
					t.Fatalf("failed validation changed second checkpoint: %v", err)
				}
				if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM observation_candidates c WHERE kind='run_basis'`).Scan(&gapAfter); err != nil || !bytes.Equal(gapBefore, gapAfter) {
					t.Fatalf("failed validation repaired the Run gap: %v", err)
				}
				var turns int
				wantTurns := 0
				if replay {
					wantTurns = 1
				}
				if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates WHERE kind='turn_basis' AND provenance->>'settlement_id'=$1`, second.current.SettlementID).Scan(&turns); err != nil || turns != wantTurns {
					t.Fatalf("failed validation partially committed second Turn basis: %d %v", turns, err)
				}
				assertNoBasisConflicts(t, f)
			})
		}
	}
}

type heldBasisParticipant struct {
	artifactpostgres.ObservationCaptureStore
	settlementID string
	inserted     chan int
	release      chan struct{}
}

func (p *heldBasisParticipant) CommitTurnBasis(ctx context.Context, tx platformpostgres.Tx, current workerapp.TurnSettlement, record workerapp.FreezeTurnSettlementCheckpointRecord) error {
	if err := p.ObservationCaptureStore.CommitTurnBasis(ctx, tx, current, record); err != nil {
		return err
	}
	if current.SettlementID == p.settlementID {
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		p.inserted <- pid
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestObservationBasisRunGapConcurrentFirstFreeze(t *testing.T) {
	f := newBasisPGFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, `UPDATE agent_turns SET created_at=created_at+interval '1 second' WHERE agent_turn_id=$1`, f.current.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	f.command.ObservationCapture = prepareBasisCapture(t, f, nil)
	second := nextBasisPGTurn(t, f)
	p := &heldBasisParticipant{settlementID: f.current.SettlementID, inserted: make(chan int, 1), release: make(chan struct{})}
	store, err := workerpostgres.NewTurnSettlementRepositoryWithObservation(f.pool, p)
	if err != nil {
		t.Fatal(err)
	}
	service := workerapp.TurnSettlementService{Store: store, Now: f.service.Now}
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := service.FreezeCheckpoint(ctx, f.command); firstDone <- err }()
	var pid int
	select {
	case pid = <-p.inserted:
	case err := <-firstDone:
		t.Fatalf("first freeze did not reach candidate insertion: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { _, err := second.service.FreezeCheckpoint(ctx, second.command); secondDone <- err }()
	// Prove the second transaction really contends on the uncommitted first
	// gap, instead of relying on goroutine scheduling to create overlap.
	blocked := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
			close(p.release)
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(p.release)
	if !blocked {
		t.Fatal("second freeze did not wait for the first gap transaction")
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	var turns, gaps int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='turn_basis' AND frame IS NOT NULL),count(*) FILTER(WHERE kind='run_basis' AND error_code='source_invalid') FROM observation_candidates`).Scan(&turns, &gaps); err != nil || turns != 2 || gaps != 1 {
		t.Fatalf("concurrent Turn basis/Run gap=%d/%d: %v", turns, gaps, err)
	}
	for _, turn := range []basisPGFixture{f, second} {
		turn.command.ObservationCapture = nil
		if _, err := turn.service.FreezeCheckpoint(ctx, turn.command); err != nil {
			t.Fatal(err)
		}
	}
	assertNoBasisConflicts(t, f)
}

func TestObservationBasisDetailedCodeCannotBeUsedForModel(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	sealed, err := artifactapp.EncodeSealedObservationBundle(artifactapp.SealedObservationBundle{SchemaVersion: "observation.capture.bundle.v1", Observations: []artifactapp.PreparedObservation{{ErrorCode: "basis_scan_unavailable"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.command.ObservationCapture = &workerapp.RuntimeObservationCapture{Sealed: sealed}
	if _, err := f.service.ApplyEvent(context.Background(), f.command); err != nil {
		t.Fatal(err)
	}
	var code string
	if err := f.pool.QueryRow(context.Background(), `SELECT error_code FROM observation_candidates WHERE kind='model'`).Scan(&code); err != nil || code != "capture_invalid" {
		t.Fatalf("model accepted basis-only safe code: %s %v", code, err)
	}
}

func TestObservationBasisFirstFreezeMissingCaptureRecordsGap(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	if err := artifactpostgres.NewFragmentJobStore(pool).Activate(ctx, "langfuse.projection.v3"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	var command workerapp.FreezeTurnSettlementCheckpointCommand
	f := prepareTerminalSettlementApplyFixture(t, pool, now, "basis", func(_ workerapp.TurnSettlement, c workerapp.FreezeTurnSettlementCheckpointCommand) { command = c })
	store, err := workerpostgres.NewTurnSettlementRepositoryWithObservation(pool, &artifactpostgres.ObservationCaptureStore{})
	if err != nil {
		t.Fatal(err)
	}
	service := workerapp.TurnSettlementService{Store: store, Now: func() time.Time { return now.Add(7 * time.Minute) }}
	frozen, err := service.FreezeCheckpoint(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.PhaseStateDigest == "" || frozen.Revision != f.Settlement.Revision+1 {
		t.Fatal("checkpoint did not freeze")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates WHERE kind IN ('run_basis','turn_basis') AND error_code='basis_scan_unavailable' AND frame IS NULL AND receipt IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("first freeze left missing basis unrecorded: got %d gaps, want 2", count)
	}
}
