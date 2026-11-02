//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	pg "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

type transportRevisionSnapshot struct {
	runtime, command, event, channel, commands, events uint64
	all                                                []byte
}

func transportRevisionState(t *testing.T, pool *pgxpool.Pool, id string) transportRevisionSnapshot {
	t.Helper()
	var v transportRevisionSnapshot
	err := pool.QueryRow(context.Background(), `SELECT i.revision,i.last_command_sequence,i.last_event_sequence,c.revision,c.command_committed_sequence,c.event_committed_sequence,
 jsonb_build_object('runtime',to_jsonb(i),'channel',to_jsonb(c),'messages',
 COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY direction,sequence) FROM runtime_control_messages m WHERE m.runtime_instance_id=i.runtime_instance_id),'[]'::jsonb))
 FROM runtime_instances i JOIN runtime_control_channels c USING(runtime_instance_id) WHERE i.runtime_instance_id=$1`, id).Scan(&v.runtime, &v.command, &v.event, &v.channel, &v.commands, &v.events, &v.all)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func transportRevisionUnchanged(t *testing.T, pool *pgxpool.Pool, id string, before transportRevisionSnapshot) {
	t.Helper()
	after := transportRevisionState(t, pool, id)
	if !bytes.Equal(before.all, after.all) {
		t.Fatalf("journal/cursor/lifecycle changed\nbefore=%s\nafter=%s", before.all, after.all)
	}
}
func transportRevisionHeartbeat(started app.StartedWorkerRun, a app.RuntimeControlAuthority, sequence uint64, id string) app.AcceptRuntimeControlEventCommand {
	e := decodeRuntimeControlEnvelopeForTransport(runtimeControlEnvelope(started, "heartbeat", sequence, id, id, "runtime.heartbeat"))
	e.RuntimeGeneration = uint32(a.RuntimeGeneration)
	e.ChannelEpoch = a.ChannelEpoch
	e.Payload = json.RawMessage(`{"heartbeat_sequence":1}`)
	b, _ := json.Marshal(e)
	return app.AcceptRuntimeControlEventCommand{Authority: a, Sequence: sequence, MessageID: id, IdempotencyKey: id, Envelope: b}
}
func decodeRuntimeControlEnvelopeForTransport(b json.RawMessage) contractsv1.RuntimeControlEnvelope {
	var e contractsv1.RuntimeControlEnvelope
	if err := json.Unmarshal(b, &e); err != nil {
		panic(err)
	}
	return e
}

func TestRuntimeControlTransportRevisionAccounting(t *testing.T) {
	pool, started, service, a := initialControlCapacityFixture(t)
	ctx := context.Background()
	before := transportRevisionState(t, pool, started.RuntimeInstanceID)
	hb := transportRevisionHeartbeat(started, a, 1, "heartbeat-1")
	accepted, err := service.AcceptEvent(ctx, hb)
	if err != nil {
		t.Fatal(err)
	}
	after := transportRevisionState(t, pool, started.RuntimeInstanceID)
	if after.runtime != before.runtime || after.event != before.event+1 || after.events != before.events+1 || after.channel != before.channel+1 || after.command != before.command {
		t.Fatalf("heartbeat transport delta before=%+v after=%+v", before, after)
	}
	duplicate, err := service.AcceptEvent(ctx, hb)
	if err != nil || !duplicate.Message.Duplicate || !bytes.Equal(duplicate.Message.Envelope, accepted.Message.Envelope) || duplicate.Message.Fingerprint != accepted.Message.Fingerprint || duplicate.Message.CursorDigest != accepted.Message.CursorDigest || !duplicate.Message.CreatedAt.Equal(accepted.Message.CreatedAt) {
		t.Fatalf("heartbeat exact replay=%+v %v", duplicate, err)
	}
	transportRevisionUnchanged(t, pool, started.RuntimeInstanceID, after)
	ack := app.ReserveRuntimeControlCommandCommand{Authority: a, MessageID: "ack-1", IdempotencyKey: "ack-1", Envelope: runtimeControlEnvelope(started, "ack", 0, "ack-1", "ack-1", "runtime.control.ack")}
	reserved, err := service.ReserveCommand(ctx, ack)
	if err != nil {
		t.Fatal(err)
	}
	next := transportRevisionState(t, pool, started.RuntimeInstanceID)
	if next.runtime != after.runtime || next.command != after.command+1 || next.commands != after.commands+1 || next.channel != after.channel+1 || next.event != after.event {
		t.Fatalf("ACK transport delta before=%+v after=%+v", after, next)
	}
	replay, err := service.ReserveCommand(ctx, ack)
	if err != nil || !replay.Duplicate || !bytes.Equal(replay.Envelope, reserved.Envelope) || replay.Fingerprint != reserved.Fingerprint || replay.CursorDigest != reserved.CursorDigest || !replay.CreatedAt.Equal(reserved.CreatedAt) {
		t.Fatalf("ACK exact replay=%+v %v", replay, err)
	}
	transportRevisionUnchanged(t, pool, started.RuntimeInstanceID, next)
	ordinary := app.ReserveRuntimeControlCommandCommand{Authority: a, MessageID: "inspect", IdempotencyKey: "inspect", Envelope: runtimeControlEnvelope(started, "command", 0, "inspect", "inspect", "runtime.inspect")}
	if _, err = service.ReserveCommand(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	business := transportRevisionState(t, pool, started.RuntimeInstanceID)
	if business.runtime != next.runtime+1 || business.command != next.command+1 {
		t.Fatal("ordinary command lost lifecycle revision")
	}
	update := app.AcceptRuntimeControlEventCommand{Authority: a, Sequence: 2, MessageID: "update", IdempotencyKey: "update", Envelope: runtimeControlEnvelopeWithAck(t, runtimeControlEnvelope(started, "event", 2, "update", "update", "runtime.update"), 1)}
	if _, err = service.AcceptEvent(ctx, update); err != nil {
		t.Fatal(err)
	}
	final := transportRevisionState(t, pool, started.RuntimeInstanceID)
	if final.runtime != business.runtime+1 || final.event != business.event+1 || final.channel != business.channel+2 {
		t.Fatal("ordinary event or piggyback ACK lost accounting")
	}
}

func TestRuntimeControlTransportRevisionRejectsStaleAuthorityWithoutWrites(t *testing.T) {
	for _, mode := range []string{"owner", "generation", "epoch", "fenced"} {
		t.Run(mode, func(t *testing.T) {
			pool, started, service, a := initialControlCapacityFixture(t)
			ctx := context.Background()
			want := app.ErrRuntimeControlStaleOwner
			switch mode {
			case "owner":
				a.OwnerFence++
			case "generation":
				a.RuntimeGeneration++
				want = app.ErrRuntimeControlIdentityConflict
			case "epoch":
				a.ChannelEpoch++
				want = app.ErrRuntimeControlIdentityConflict
			case "fenced":
				_, err := service.Fence(ctx, app.FenceRuntimeControlChannelCommand{WorkerSessionID: a.WorkerSessionID, RuntimeInstanceID: a.RuntimeInstanceID, RuntimeGeneration: a.RuntimeGeneration, ChannelEpoch: a.ChannelEpoch})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := transportRevisionState(t, pool, started.RuntimeInstanceID)
			hb := transportRevisionHeartbeat(started, a, 1, "rejected-heartbeat")
			if got, err := service.AcceptEvent(ctx, hb); !errors.Is(err, want) || len(got.Message.Envelope) > 0 {
				t.Fatalf("heartbeat rejection=%+v %v want %v", got, err, want)
			}
			transportRevisionUnchanged(t, pool, started.RuntimeInstanceID, before)
			e := decodeRuntimeControlEnvelopeForTransport(runtimeControlEnvelope(started, "ack", 0, "rejected-ack", "rejected-ack", "runtime.control.ack"))
			e.RuntimeGeneration = uint32(a.RuntimeGeneration)
			e.ChannelEpoch = a.ChannelEpoch
			b, _ := json.Marshal(e)
			if got, err := service.ReserveCommand(ctx, app.ReserveRuntimeControlCommandCommand{Authority: a, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: b}); !errors.Is(err, want) || len(got.Envelope) > 0 {
				t.Fatalf("ACK rejection=%+v %v want %v", got, err, want)
			}
			transportRevisionUnchanged(t, pool, started.RuntimeInstanceID, before)
		})
	}
}

// Capture the actual original owner once; transport must never refresh this CAS.
func transportRevisionActivationCommand(t *testing.T, pool *pgxpool.Pool, started app.StartedWorkerRun, cid string) app.ActivateResidentRuntimeCommand {
	t.Helper()
	c := app.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: cid, RuntimeGeneration: started.RuntimeGeneration, ControlChannelEpoch: started.ControlChannelEpoch}
	if err := pool.QueryRow(context.Background(), `SELECT s.session_revision,r.revision,a.revision,i.revision
 FROM worker_sessions s JOIN worker_runs r ON r.worker_session_id=s.worker_session_id
 JOIN agent_turns a ON a.worker_run_id=r.worker_run_id JOIN runtime_instances i ON i.worker_session_id=s.worker_session_id
 WHERE s.worker_session_id=$1 AND r.worker_run_id=$2 AND a.agent_turn_id=$3 AND i.runtime_instance_id=$4`, c.WorkerSessionID, c.WorkerRunID, c.AgentTurnID, c.RuntimeInstanceID).Scan(&c.ExpectedSessionRevision, &c.ExpectedRunRevision, &c.ExpectedTurnRevision, &c.ExpectedRuntimeRevision); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRuntimeControlTransportRevisionActivationCASGuards(t *testing.T) {
	for _, mode := range []string{"valid", "cid", "generation", "epoch", "session_revision", "run_revision", "turn_revision", "runtime_revision", "terminal", "ownership", "business_event"} {
		t.Run(mode, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Now().UTC()
			started := startInitialRuntimeBeforeActivation(t, pool, now)
			runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "initial-cas", started.RuntimeGeneration)
			service, a := bindAndClaimRuntimeControl(t, pool, started, now.Add(5*time.Minute))
			c := transportRevisionActivationCommand(t, pool, started, "initial-cas")
			if _, err := service.AcceptEvent(ctx, transportRevisionHeartbeat(started, a, 1, "pre-activate-heartbeat")); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ReserveCommand(ctx, app.ReserveRuntimeControlCommandCommand{Authority: a, MessageID: "pre-activate-ack", IdempotencyKey: "pre-activate-ack", Envelope: runtimeControlEnvelope(started, "ack", 0, "pre-activate-ack", "pre-activate-ack", "runtime.control.ack")}); err != nil {
				t.Fatal(err)
			}
			// Preserve the original command captured before any transport progress.
			want := error(app.CapacityBindingMismatch)
			var sql string
			switch mode {
			case "cid":
				c.ContainerID = "wrong-cid"
			case "generation":
				c.RuntimeGeneration++
			case "epoch":
				c.ControlChannelEpoch++
			case "session_revision":
				c.ExpectedSessionRevision++
				want = platformpostgres.ErrRevisionConflict
			case "run_revision":
				c.ExpectedRunRevision++
				want = platformpostgres.ErrRevisionConflict
			case "turn_revision":
				c.ExpectedTurnRevision++
				want = platformpostgres.ErrRevisionConflict
			case "runtime_revision":
				c.ExpectedRuntimeRevision++
				want = platformpostgres.ErrRevisionConflict
			case "terminal":
				sql = `UPDATE runtime_instances SET status='terminated',ended_at=clock_timestamp()`
			case "ownership":
				sql = `UPDATE worker_sessions SET active_worker_run_id=NULL`
				want = platformpostgres.ErrRevisionConflict
			case "business_event":
				_, err := service.AcceptEvent(ctx, app.AcceptRuntimeControlEventCommand{Authority: a, Sequence: 2, MessageID: "business-update", IdempotencyKey: "business-update", Envelope: runtimeControlEnvelope(started, "event", 2, "business-update", "business-update", "runtime.update")})
				if err != nil {
					t.Fatal(err)
				}
				want = platformpostgres.ErrRevisionConflict
			}
			if sql != "" {
				if _, err := pool.Exec(ctx, sql); err != nil {
					t.Fatal(err)
				}
			}
			before := transportRevisionState(t, pool, started.RuntimeInstanceID)
			result, err := pg.NewRuntimeResidencyStore(pool).ActivateResidentRuntime(ctx, app.ActivateResidentRuntimeRecord{Command: c, Now: now.Add(6 * time.Minute)})
			if mode == "valid" {
				if err != nil || result.RuntimeRevision != c.ExpectedRuntimeRevision+1 {
					t.Fatalf("transport-only Activate=%+v %v", result, err)
				}
				return
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s Activate=%+v %v want %v", mode, result, err, want)
			}
			transportRevisionUnchanged(t, pool, started.RuntimeInstanceID, before)
		})
	}
}

// A health transition is a real lifecycle change even though a transport-only
// heartbeat is not. Exact activation replay must still fail after that change.
func TestRuntimeControlTransportRevisionHealthTransitionStillConflicts(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	started := startInitialRuntimeBeforeActivation(t, pool, now)
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "health-cas", started.RuntimeGeneration)
	service, a := bindAndClaimRuntimeControl(t, pool, started, now.Add(5*time.Minute))
	c := transportRevisionActivationCommand(t, pool, started, "health-cas")
	record := app.ActivateResidentRuntimeRecord{Command: c, Now: now.Add(6 * time.Minute)}
	store := pg.NewRuntimeResidencyStore(pool)
	if _, err := store.ActivateResidentRuntime(ctx, record); err != nil {
		t.Fatal(err)
	}
	// Establish an existing suspect health state, then invoke the real observer.
	if _, err := pool.Exec(ctx, `UPDATE runtime_instances SET status='suspect' WHERE runtime_instance_id=$1`, started.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcceptEvent(ctx, transportRevisionHeartbeat(started, a, 1, "health-heartbeat")); err != nil {
		t.Fatal(err)
	}
	before := transportRevisionState(t, pool, started.RuntimeInstanceID)
	received := now.Add(6*time.Minute + 15*time.Second)
	got, err := pg.NewRuntimeHeartbeatStore(pool).RecordRuntimeHeartbeat(ctx, app.RecordRuntimeHeartbeat{Command: app.ObserveRuntimeHeartbeatCommand{WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: started.RuntimeGeneration, ChannelEpoch: started.ControlChannelEpoch, HeartbeatSequence: 1, ControlSequence: 1, ReceivedAt: received}, ScheduledCommandID: "health-suspect", SuspectDueAt: received.Add(45 * time.Second), UnhealthyDueAt: received.Add(90 * time.Second)})
	if err != nil || got.RuntimeRevision != before.runtime+1 {
		t.Fatalf("suspect->active observer=%+v %v", got, err)
	}
	after := transportRevisionState(t, pool, started.RuntimeInstanceID)
	if after.runtime != before.runtime+1 || after.command != before.command || after.event != before.event || after.channel != before.channel {
		t.Fatal("health transition lost lifecycle boundary")
	}
	if _, err = store.ActivateResidentRuntime(ctx, record); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("health change accepted old activation replay: %v", err)
	}
	transportRevisionUnchanged(t, pool, started.RuntimeInstanceID, after)
}
