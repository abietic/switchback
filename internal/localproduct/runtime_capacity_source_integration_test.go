//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCapacityCommonAssemblyRejectsPartialWhenExecutionDisabled(t *testing.T) {
	app := newStartGuardApp(t)
	c := app.configuration
	c.RuntimeCapacityTrustFile = "/private/tmp/nonexistent-trust.json"
	got, err := newWithPool(context.Background(), c, app.pool)
	if got != nil {
		got.Close()
	}
	if err == nil {
		t.Fatal("partial configured trust accepted by common assembly with execution disabled")
	}
}

func TestConfiguredCapacitySourceHTTPQueuedDenials(t *testing.T) {
	for _, name := range []string{"missing", "bad", "stale", "drift", "inventory"} {
		t.Run(name, func(t *testing.T) {
			base := newStartGuardApp(t)
			files := runtimecapacity.NewFileInventory(t)
			c := base.configuration
			c.RuntimeCapacityTrustFile, c.RuntimeCapacitySnapshotDirectory = files.TrustPath, files.Directory
			app, err := newWithPool(context.Background(), base.configuration, base.pool)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			source, err := newSyntheticInitialCapacitySource(context.Background(), c, base.pool)
			if err != nil {
				t.Fatal(err)
			}
			app.initialPolicy = source
			if err := source.checkRuntimeStart(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := workerapp.CapacityEvidenceInvalid
			switch name {
			case "missing":
				if err := os.Remove(filepath.Join(files.Directory, "capacity.json")); err != nil {
					t.Fatal(err)
				}
			case "bad":
				if err := os.WriteFile(filepath.Join(files.Directory, "capacity.json"), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "stale":
				want = workerapp.CapacityEvidenceStale
				files.Publish(func(f *capacitysnapshot.Facts) { f.ObservedAt = time.Now().Add(-time.Minute) })
			case "drift":
				want = workerapp.CapacityIdentityDrift
				files.Publish(func(f *capacitysnapshot.Facts) { f.Identity.EngineID = "different-engine" })
			case "inventory":
				want = workerapp.CapacityInventoryUnknown
				files.Publish(func(f *capacitysnapshot.Facts) { f.Anchor.MountOptions = []string{} })
			}
			task := createRuntimeRunnerTask(t, app.Handler(), app.pool, "source-"+name, "Remain queued without effects")
			runner := dispatchWaitRunner(app)
			runner.initialPolicy = source
			if err := runner.startNextQueuedTask(context.Background()); !errors.Is(err, want) {
				t.Fatalf("refusal=%v want %v", err, want)
			}
			for i := 0; i < 3; i++ {
				if _, err := app.outbox.RunOnce(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			assertPublicDispatchWait(t, app, task, string(want))
			var status string
			if err := app.pool.QueryRow(context.Background(), `SELECT status FROM runtime_capacity_pools WHERE pool_id='local'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != string(want) {
				t.Fatalf("old good sample not durably denied: %s", status)
			}
			for _, table := range []string{"worker_sessions", "session_dispatch_permits", "worker_runs", "agent_turns", "runtime_instances", "runtime_capacity_executions", "runtime_capacity_residents", "scheduled_commands"} {
				var count int
				if err := app.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("created %s", table)
				}
			}
		})
	}
}

func TestConfiguredCapacityTrustImmutableAndInvalidAssembly(t *testing.T) {
	base := newStartGuardApp(t)
	files := runtimecapacity.NewFileInventory(t)
	c := base.configuration
	c.RuntimeCapacityTrustFile, c.RuntimeCapacitySnapshotDirectory = files.TrustPath, files.Directory
	source, err := newSyntheticInitialCapacitySource(context.Background(), c, base.pool)
	if err != nil {
		t.Fatal(err)
	}
	if err = source.checkRuntimeStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	var before string
	if err = base.pool.QueryRow(context.Background(), `SELECT to_jsonb(p)::text FROM runtime_capacity_pools p`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(files.TrustPath)
	if err != nil {
		t.Fatal(err)
	}
	var trust capacitysnapshot.Trust
	if err = json.Unmarshal(raw, &trust); err != nil {
		t.Fatal(err)
	}
	trust.KeyID = "different-trust"
	changed, _ := json.Marshal(trust)
	for _, content := range [][]byte{changed, []byte("malformed secret path /private/unsafe")} {
		if err = os.WriteFile(files.TrustPath, content, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = newSyntheticInitialCapacitySource(context.Background(), c, base.pool); err == nil {
			t.Fatal("invalid/changed original Trust accepted by synthetic PG assembly")
		}
		if _, err = newWithPool(context.Background(), c, base.pool); err == nil || strings.Contains(err.Error(), files.Directory) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("configured invalid trust assembly=%v", err)
		}
		var after string
		if err = base.pool.QueryRow(context.Background(), `SELECT to_jsonb(p)::text FROM runtime_capacity_pools p`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatal("trust rejection changed ledger/epoch/sample")
		}
	}
}

func TestConfiguredCapacityBadReadExpiresOnlyEligibleUnconsumed(t *testing.T) {
	var files *runtimecapacity.FileInventory
	f := newSupplementFixture(t, func(f *supplementFixture) {
		files = enableInitialCapacityFixture(f)
		f.publishInitial = nil
		f.runtime.heartbeatStart = make(chan struct{})
	})
	r := f.app.runtime
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "capacity-consumed", "Keep consumed resident")
	if err := r.startNextQueuedTask(f.ctx); !errors.Is(err, workerapp.CapacityContainerProofPending) {
		t.Fatal(err)
	}
	second := createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "capacity-unconsumed", "Expire only unused token")
	_, command := enqueueDispatchWaitTask(t, f.app, r, second)
	permit, err := r.dispatch.IssueTaskPermit(f.ctx, command)
	supplementMust(t, err)
	// Only a test clock boundary is changed; no signed physics/ready fact is SQL-seeded.
	_, err = f.pool.Exec(f.ctx, `UPDATE runtime_capacity_executions SET expires_at=clock_timestamp()-interval '1 second' WHERE permit_id=$1 AND consumed_at IS NULL`, permit.PermitID)
	supplementMust(t, err)
	before := f.count(`SELECT count(*) FROM runtime_capacity_residents WHERE removal_sequence IS NULL`)
	supplementMust(t, os.WriteFile(filepath.Join(files.Directory, "capacity.json"), []byte("bad after good"), 0600))
	if err = r.checkInitialCapacity(f.ctx); !errors.Is(err, workerapp.CapacityEvidenceInvalid) {
		t.Fatal(err)
	}
	if f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE consumed_at IS NULL AND released_at IS NOT NULL AND release_reason='permit_expired'`) != 1 || f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE consumed_at IS NOT NULL AND released_at IS NULL`) != 1 || f.count(`SELECT count(*) FROM runtime_capacity_residents WHERE removal_sequence IS NULL`) != before {
		t.Fatal("bad-read reconcile expired/released the wrong authority")
	}
	var status string
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT status FROM runtime_capacity_pools`).Scan(&status))
	if status != "capacity_evidence_invalid" {
		t.Fatal("bad sample did not durably deny old good")
	}
}

func TestConfiguredCapacitySameSignedBytesBecomeDurablyStale(t *testing.T) {
	base := newStartGuardApp(t)
	files := runtimecapacity.NewFileInventory(t)
	c := base.configuration
	c.RuntimeCapacityTrustFile, c.RuntimeCapacitySnapshotDirectory = files.TrustPath, files.Directory
	source, err := newSyntheticInitialCapacitySource(context.Background(), c, base.pool)
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().Add(-28 * time.Second)
	files.Publish(func(f *capacitysnapshot.Facts) { f.ObservedAt = observed })
	if err = source.checkRuntimeStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	var before, beforeRevision int64
	var epoch string
	if err = base.pool.QueryRow(context.Background(), `SELECT latest_sequence,epoch::text,revision FROM runtime_capacity_pools`).Scan(&before, &epoch, &beforeRevision); err != nil {
		t.Fatal(err)
	}
	// Advance natural time beyond the signed source's freshness boundary. No
	// database fact, signature, payload or receive timestamp is manufactured.
	timer := time.NewTimer(time.Until(observed.Add(30*time.Second + 50*time.Millisecond)))
	defer timer.Stop()
	<-timer.C
	if err = source.checkRuntimeStart(context.Background()); !errors.Is(err, workerapp.CapacityEvidenceStale) {
		t.Fatal(err)
	}
	var status, afterEpoch string
	var after, afterRevision int64
	if err = base.pool.QueryRow(context.Background(), `SELECT status,latest_sequence,epoch::text,revision FROM runtime_capacity_pools`).Scan(&status, &after, &afterEpoch, &afterRevision); err != nil {
		t.Fatal(err)
	}
	if status != "capacity_evidence_stale" || after != before || epoch != afterEpoch || afterRevision != beforeRevision+1 {
		t.Fatal("same signed bytes became stale without durable denial or original snapshot identity")
	}
	if err = source.checkRuntimeStart(context.Background()); !errors.Is(err, workerapp.CapacityEvidenceStale) {
		t.Fatal(err)
	}
	var replayRevision int64
	if err = base.pool.QueryRow(context.Background(), `SELECT revision FROM runtime_capacity_pools`).Scan(&replayRevision); err != nil || replayRevision != afterRevision {
		t.Fatal("same stale replay changed the already blocked pool revision", err)
	}
}
