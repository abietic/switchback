package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type FragmentJobStore struct{ pool *pgxpool.Pool }

func NewFragmentJobStore(pool *pgxpool.Pool) *FragmentJobStore { return &FragmentJobStore{pool: pool} }

// Activate freezes the Runs visible to this single statement, including live
// Runs with no manifest. A later commit is eligible regardless of business time.
func (s *FragmentJobStore) Activate(ctx context.Context, version string) error {
	if s == nil || s.pool == nil || version == "" {
		return fmt.Errorf("fragment store and version are required")
	}
	_, err := s.pool.Exec(ctx, `WITH activated AS (
 INSERT INTO observation_fragment_activations(version) VALUES ($1)
 ON CONFLICT DO NOTHING RETURNING version
 ) INSERT INTO observation_fragment_run_exclusions
 (version,worker_run_id,tenant_id,user_id,workspace_id,worker_session_id)
 SELECT activated.version,run.worker_run_id,session.tenant_id,session.user_id,
 session.workspace_id,session.worker_session_id
 FROM activated CROSS JOIN worker_runs run
 JOIN worker_sessions session ON session.worker_session_id=run.worker_session_id`, version)
	return err
}
