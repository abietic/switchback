//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestWorkerDiagnosticTargetRequiresScopedLangfuseReadback(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
INSERT INTO worker_sessions
(worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at,terminal_at)
VALUES ('session-1','tenant-1','user-1','workspace-1','revision-1','copy-1','closed',1,'2026-10-01','2026-10-10','2026-09-28','2026-09-29','2026-09-29'),
('session-2','tenant-1','user-1','workspace-1','revision-1','copy-2','closed',1,'2026-10-01','2026-10-10','2026-09-28','2026-09-29','2026-09-29');
INSERT INTO worker_runs
(worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,created_at,terminal_at)
VALUES ('run-1','session-1','direct_agent_turn','{}','profile','1','sha256:'||repeat('a',64),'completed','2026-09-28','2026-09-29'),
('run-2','session-2','direct_agent_turn','{}','profile','1','sha256:'||repeat('a',64),'completed','2026-09-28','2026-09-29');
INSERT INTO agent_turns
(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,terminal_at)
VALUES ('turn-1','session-1','run-1','agent-1','prompt','{}','completed','one','2026-09-28','2026-09-29');`)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(pool)
	viewer := projectionapp.Viewer{TenantID: "tenant-1", UserID: "user-1"}
	read := func(session, run, turn string) string {
		t.Helper()
		target, err := store.ReadWorkerDiagnosticTarget(ctx, projectionapp.WorkerDiagnosticQuery{
			Viewer: viewer, WorkspaceID: "workspace-1", WorkerSessionID: session, WorkerRunID: run, AgentTurnID: turn,
		})
		if err != nil {
			t.Fatal(err)
		}
		return target.LangfuseTraceID
	}
	if got := read("session-1", "run-1", ""); got != "" {
		t.Fatalf("advertised trace before read-back: %s", got)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO observation_candidates
  (kind,source_id,frame_digest,frame,receipt,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,provenance,status)
VALUES ('run_basis','test-run-basis','sha256:'||encode(sha256('fixture'::bytea),'hex'),'fixture','receipt',
        'tenant-1','user-1','workspace-1','session-1','run-1','{"schema_version":"observation.provenance.v1"}','published');
INSERT INTO observation_publications
  (kind,source_id,frame_digest,object_key,object_size,object_checksum,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id)
SELECT kind,source_id,frame_digest,'fixture',7,frame_digest,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id
FROM observation_candidates WHERE source_id='test-run-basis';
INSERT INTO observation_fragment_jobs (version,kind,source_id,frame_digest,status)
SELECT 'langfuse.projection.v3',kind,source_id,frame_digest,'passed'
FROM observation_publications WHERE source_id='test-run-basis';`)
	if err != nil {
		t.Fatal(err)
	}
	verified := langfuse.FragmentTraceID(artifactdomain.ObservationScope{
		TenantID: "tenant-1", UserID: "user-1", WorkspaceID: "workspace-1", WorkerSessionID: "session-1", WorkerRunID: "run-1",
	})
	for _, tc := range []struct {
		name, session, run, turn, want string
	}{
		{"session latest Run", "session-1", "", "", verified},
		{"explicit Run", "session-1", "run-1", "", verified},
		{"unverified Turn", "session-1", "run-1", "turn-1", ""},
		{"different Session", "session-2", "run-2", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := read(tc.session, tc.run, tc.turn); got != tc.want {
				t.Fatalf("Langfuse Trace ID = %q, want %q", got, tc.want)
			}
		})
	}
}
