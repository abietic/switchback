//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapplication "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestStartWorkerRunPersistsCurrentAndLegacyNetworkEvidence(t *testing.T) {
	current, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]config.RuntimeNetworkEvidence{
		"current": current.Evidence(),
		"legacy": {
			ID:                config.LegacyPublicWebRestrictedV1,
			EnforcementStatus: config.NetworkEnforcementLegacy,
		},
	}
	for name, evidence := range tests {
		t.Run(name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Date(2026, 7, 26, 1, 30, 0, 0, time.UTC)
			seedAllocationRequests(t, pool, 1, now)
			ids := &concurrentAllocationIDs{}
			allocated, err := (workerapplication.WorkerSessionAllocator{
				Store: workerpostgres.NewAllocatorStore(pool), IDs: ids,
				Now: func() time.Time { return now.Add(time.Minute) },
			}).Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
				TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET status='interactive',runtime_generation=1,session_revision=2,updated_at=$2
WHERE worker_session_id=$1`, allocated.WorkerSessionID, now.Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			dispatch := taskapplication.SessionDispatchService{
				Store: taskpostgres.NewStore(pool), IDs: ids,
				Now: func() time.Time { return now.Add(3 * time.Minute) }, PermitTTL: 5 * time.Minute,
			}
			if _, err := dispatch.EnqueueTask(ctx, taskapplication.EnqueueTaskCommand{
				TenantID: "tenant-local", UserID: "user-local", TaskID: "task-01",
				WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
				ExpectedSessionRevision: 2, IdempotencyKey: "enqueue-task-01",
			}); err != nil {
				t.Fatal(err)
			}
			permit, err := dispatch.IssueTaskPermit(ctx, taskapplication.IssueTaskPermitCommand{
				TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: allocated.WorkerSessionID,
				RuntimeGeneration: 1, CapacityAdmissionRef: "capacity-local", IdempotencyKey: "permit-task-01",
			})
			if err != nil {
				t.Fatal(err)
			}
			command := residentStartCommand(permit)
			command.NetworkPolicyID = evidence.ID
			command.NetworkPolicyRevision = evidence.Revision
			command.NetworkPolicyDigest = evidence.Digest
			command.NetworkEnforcementStatus = evidence.EnforcementStatus
			started, err := (workerapplication.StartWorkerRunService{
				Store: workerpostgres.NewRunStore(pool), IDs: ids,
				Now: func() time.Time { return now.Add(4 * time.Minute) },
			}).Execute(ctx, command)
			if err != nil {
				t.Fatal(err)
			}

			var policyID, digest, status string
			var revision uint64
			var snapshotID, snapshotDigest, snapshotStatus string
			var snapshotRevision uint64
			err = pool.QueryRow(ctx, `
SELECT network_policy_id,network_policy_revision,network_policy_digest,network_enforcement_status,
       start_snapshot->'runtime_network_profile'->>'id',
       (start_snapshot->'runtime_network_profile'->>'revision')::bigint,
       start_snapshot->'runtime_network_profile'->>'digest',
       start_snapshot->'runtime_network_profile'->>'enforcement_status'
FROM runtime_instances
JOIN worker_runs USING (worker_run_id)
WHERE runtime_instance_id=$1`, started.RuntimeInstanceID).Scan(
				&policyID, &revision, &digest, &status,
				&snapshotID, &snapshotRevision, &snapshotDigest, &snapshotStatus,
			)
			if err != nil {
				t.Fatal(err)
			}
			if policyID != evidence.ID || revision != evidence.Revision || digest != evidence.Digest ||
				status != evidence.EnforcementStatus || snapshotID != evidence.ID ||
				snapshotRevision != evidence.Revision || snapshotDigest != evidence.Digest ||
				snapshotStatus != evidence.EnforcementStatus {
				t.Fatalf(
					"persisted network=(%q,%d,%q,%q) snapshot=(%q,%d,%q,%q), want=%+v",
					policyID, revision, digest, status,
					snapshotID, snapshotRevision, snapshotDigest, snapshotStatus, evidence,
				)
			}
			runtimeSnapshot, err := (workerpostgres.RuntimeReader{Pool: pool}).ReadRuntimeInstance(
				ctx,
				started.RuntimeInstanceID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if runtimeSnapshot.NetworkProfile.ID != evidence.ID ||
				runtimeSnapshot.NetworkProfile.Revision != evidence.Revision ||
				runtimeSnapshot.NetworkProfile.Digest != evidence.Digest ||
				runtimeSnapshot.NetworkEnforcementStatus != evidence.EnforcementStatus {
				t.Fatalf("Runtime read model network evidence = %+v", runtimeSnapshot)
			}
		})
	}
}
