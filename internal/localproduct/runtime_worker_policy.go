package localproduct

import (
	"context"
	"encoding/json"
	"fmt"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
)

// This admission observes physical policy only. The original journal remains
// the independent credential, channel, active graph and capacity authority.
// A resident CID pin or verified Create handoff is identity evidence, never a Start permit.
type workerPolicyAdmission struct {
	handoff func(workerapp.CreatedRuntimeIdentity) (workerapp.CreatedRuntimeIdentity, bool, error)
	lookup  func(context.Context, string) (workerPolicyOwner, error)
	inspect func(context.Context, workerapp.CreatedRuntimeIdentity) error
}

type workerPolicyOwner struct {
	identity  workerapp.CreatedRuntimeIdentity
	status    string
	residents []workerPolicyResident
}

type workerPolicyResident struct {
	Session    string `json:"session"`
	Runtime    string `json:"runtime"`
	Generation uint64 `json:"generation"`
	Container  string `json:"container"`
	Origin     string `json:"origin"`
	Removed    bool   `json:"removed"`
}

type workerPolicyQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// One read-only statement provides a consistent owner/pin snapshot. Include
// conflicting associations as evidence to reject, rather than filtering them
// away into an apparently unambiguous identity. No pin is written here.
func loadWorkerPolicyOwner(ctx context.Context, db workerPolicyQuery, runtimeID string) (workerPolicyOwner, error) {
	var owner workerPolicyOwner
	if db == nil {
		return owner, fmt.Errorf("Worker policy owner lookup is unavailable")
	}
	var residents []byte
	err := db.QueryRow(ctx, `SELECT i.worker_session_id,i.runtime_instance_id,i.generation,i.status,COALESCE(i.container_id,''),
 COALESCE((SELECT jsonb_agg(jsonb_build_object(
 'session',c.worker_session_id,'runtime',COALESCE(c.runtime_instance_id,''),
 'generation',c.generation,'container',COALESCE(c.container_id,''),
 'origin',c.origin,'removed',c.removal_sequence IS NOT NULL))
 FROM runtime_capacity_residents c WHERE c.runtime_instance_id=i.runtime_instance_id
 OR (c.worker_session_id=i.worker_session_id AND c.generation=i.generation)), '[]'::jsonb)
 FROM runtime_instances i WHERE i.runtime_instance_id=$1`, runtimeID).Scan(
		&owner.identity.WorkerSessionID, &owner.identity.RuntimeInstanceID, &owner.identity.Generation,
		&owner.status, &owner.identity.ContainerID, &residents)
	if err != nil {
		return owner, fmt.Errorf("read Worker policy owner: %w", err)
	}
	if err := json.Unmarshal(residents, &owner.residents); err != nil {
		return owner, fmt.Errorf("decode Worker policy owner: %w", err)
	}
	return owner, nil
}

func (owner workerPolicyOwner) target(request workerapp.CreatedRuntimeIdentity, handoff *workerapp.CreatedRuntimeIdentity) (workerapp.CreatedRuntimeIdentity, error) {
	fail := func() (workerapp.CreatedRuntimeIdentity, error) {
		return workerapp.CreatedRuntimeIdentity{}, fmt.Errorf("Worker policy owner identity is missing or conflicting")
	}
	id := owner.identity
	if request.WorkerSessionID == "" || request.RuntimeInstanceID == "" || request.Generation == 0 ||
		id.WorkerSessionID != request.WorkerSessionID || id.RuntimeInstanceID != request.RuntimeInstanceID || id.Generation != request.Generation {
		return fail()
	}
	if owner.status != "starting" && owner.status != "active" && owner.status != "suspect" {
		return fail()
	}
	if len(owner.residents) > 1 {
		return fail()
	}
	for _, pin := range owner.residents {
		if pin.Session != id.WorkerSessionID || pin.Runtime != id.RuntimeInstanceID || pin.Generation != id.Generation || pin.Removed || (pin.Origin != "reserved" && pin.Origin != "legacy") {
			return fail()
		}
		if pin.Container == "" {
			// Normal first Claim precedes runtime.ready/ObservePool/Activate: both
			// durable CIDs are empty. Only the verified Create handoff can bridge
			// this identity gap, and it still grants no journal authority.
			if owner.status != "starting" || id.ContainerID != "" || pin.Origin != "reserved" || handoff == nil {
				return fail()
			}
			id.ContainerID = handoff.ContainerID
		} else {
			if id.ContainerID != "" && id.ContainerID != pin.Container {
				return fail()
			}
			if id.ContainerID == "" {
				if owner.status != "starting" || pin.Origin != "reserved" {
					return fail()
				}
				id.ContainerID = pin.Container
			}
		}
	}
	if handoff != nil && (handoff.ContainerID == "" || handoff.WorkerSessionID != id.WorkerSessionID || handoff.RuntimeInstanceID != id.RuntimeInstanceID || handoff.Generation != id.Generation || handoff.ContainerID != id.ContainerID) {
		return fail()
	}
	if id.ContainerID == "" || (request.ContainerID != "" && request.ContainerID != id.ContainerID) {
		return fail()
	}
	return id, nil
}

func (admission *workerPolicyAdmission) verify(ctx context.Context, request workerapp.CreatedRuntimeIdentity) error {
	if admission == nil || admission.lookup == nil || admission.inspect == nil {
		return fmt.Errorf("Worker policy admission is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, err := admission.lookup(ctx, request.RuntimeInstanceID)
	if err != nil {
		return err
	}
	var handoff *workerapp.CreatedRuntimeIdentity
	if admission.handoff != nil {
		identity, found, lookupErr := admission.handoff(owner.identity)
		if lookupErr != nil {
			return lookupErr
		}
		if found {
			handoff = &identity
		}
	}
	target, err := owner.target(request, handoff)
	if err != nil {
		return err
	}
	return admission.inspect(ctx, target)
}

type workerPolicyJournal struct {
	workerapp.RuntimeControlJournal
	admission *workerPolicyAdmission
}

func (admission *workerPolicyAdmission) guardJournal(store workerapp.RuntimeControlJournal) workerapp.RuntimeControlJournal {
	return workerPolicyJournal{RuntimeControlJournal: store, admission: admission}
}

func (journal workerPolicyJournal) ClaimRuntimeControlStream(ctx context.Context, record workerapp.ClaimRuntimeControlStreamRecord) (workerapp.RuntimeControlReplayPlan, error) {
	command := record.Command
	if err := journal.admission.verify(ctx, workerapp.CreatedRuntimeIdentity{
		WorkerSessionID: command.WorkerSessionID, RuntimeInstanceID: command.RuntimeInstanceID, Generation: command.RuntimeGeneration,
	}); err != nil {
		return workerapp.RuntimeControlReplayPlan{}, err
	}
	if journal.RuntimeControlJournal == nil {
		return workerapp.RuntimeControlReplayPlan{}, fmt.Errorf("Worker control journal is unavailable")
	}
	return journal.RuntimeControlJournal.ClaimRuntimeControlStream(ctx, record)
}

func (admission *workerPolicyAdmission) prepare(ctx context.Context, request liveRuntimeRestoreRequest, restore func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error)) (preparedRuntime, error) {
	if err := admission.verify(ctx, workerapp.CreatedRuntimeIdentity{
		WorkerSessionID: request.WorkerSessionID, RuntimeInstanceID: request.RuntimeInstanceID, Generation: request.RuntimeGeneration,
	}); err != nil {
		return preparedRuntime{}, err
	}
	if restore == nil {
		return preparedRuntime{}, fmt.Errorf("Worker environment restore is unavailable")
	}
	return restore(ctx, request)
}

func (runner *runtimeRunner) restorePolicyVerifiedLiveRuntime(ctx context.Context, request liveRuntimeRestoreRequest) (preparedRuntime, error) {
	return runner.workerPolicy.prepare(ctx, request, runner.environment.RestoreLiveRuntime)
}
