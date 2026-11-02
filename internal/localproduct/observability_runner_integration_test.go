//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestFragmentCutoverActivationPrecedesHandlerAndDispatch(t *testing.T) {
	for _, mode := range []string{"disabled", "partial", "enabled"} {
		t.Run(mode, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			c := Config{Profile: profile, NetworkProfile: integrationNetworkProfile(t), DataRoot: t.TempDir(), ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID, WorkspaceRevisionID: defaultWorkspaceRevisionID, GRPCAddress: defaultGRPCAddress}
			if mode != "disabled" {
				c.LangfuseBaseURL = server.URL
			}
			if mode == "enabled" {
				c.LangfusePublicKey = "public"
				c.LangfuseSecretKey = "secret"
			}
			if _, err = pool.Exec(ctx, `INSERT INTO observability_projection_activations(schema_version) VALUES('langfuse.projection.v2'); INSERT INTO observability_projection_jobs(schema_version,trace_stream_id,manifest_checksum) VALUES('langfuse.projection.v2','old-source','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`); err != nil {
				t.Fatal(err)
			}
			var before, after []byte
			if err = pool.QueryRow(ctx, `SELECT to_jsonb(j) FROM observability_projection_jobs j`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			app, err := newWithPool(ctx, c, pool)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			// No App.Run has happened. Activation already precedes any dispatch
			// owner goroutine and the Handler becoming available to the caller.
			var active bool
			if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observation_fragment_activations WHERE version='langfuse.projection.v3')`).Scan(&active); err != nil || active != (mode == "enabled") {
				t.Fatalf("activation=%v err=%v", active, err)
			}
			if app.Handler() == nil || app.fragmentHistory == nil || app.fragmentCoverage == nil {
				t.Fatal("composition missing internal boundaries")
			}
			if app.observability != nil {
				runCtx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() { app.observability.Run(runCtx); close(done) }()
				time.Sleep(1100 * time.Millisecond)
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("fragment workers did not join cancellation")
				}
			}
			if err = pool.QueryRow(ctx, `SELECT to_jsonb(j) FROM observability_projection_jobs j`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || calls.Load() != 0 {
				t.Fatalf("v2 claimed or HTTP invoked calls=%d", calls.Load())
			}
		})
	}
}
