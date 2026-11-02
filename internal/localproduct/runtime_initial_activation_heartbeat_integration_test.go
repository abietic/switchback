//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// Freeze the exact Bind command before real Activate IO, then let the real
// authenticated heartbeat/ACK transport commit without refreshing that command.
type activationHeartbeatBarrier struct {
	workerapp.RuntimeResidencyStore
	entered chan workerapp.ActivateResidentRuntimeCommand
	release chan struct{}
}

func (s *activationHeartbeatBarrier) ActivateResidentRuntime(ctx context.Context, r workerapp.ActivateResidentRuntimeRecord) (workerapp.ResidentRuntimeState, error) {
	select {
	case s.entered <- r.Command:
	case <-ctx.Done():
		return workerapp.ResidentRuntimeState{}, ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return workerapp.ResidentRuntimeState{}, ctx.Err()
	}
	return s.RuntimeResidencyStore.ActivateResidentRuntime(ctx, r)
}

type activationHeartbeatProvisioner struct {
	workerapp.RuntimeProvisioner
	checks int
}

func (p *activationHeartbeatProvisioner) Provision(ctx context.Context, request workerapp.RuntimeProvisionRequest, check workerapp.RuntimePreStartCheck) (workerapp.ProvisionedRuntime, error) {
	return p.RuntimeProvisioner.Provision(ctx, request, func(ctx context.Context, identity workerapp.CreatedRuntimeIdentity) error {
		p.checks++
		return check(ctx, identity)
	})
}

type activationHeartbeatSnapshot struct {
	SessionID, RunID, TurnID, RuntimeID, AgentID, Container, SessionStatus, RunStatus, TurnStatus, RuntimeStatus string
	SessionRev, RunRev, TurnRev, RuntimeRev, Generation, Epoch, LastEvent                                        uint64
	ChannelRev, Committed, Acknowledged, Applied, Commands, CommandAck, LastCommand                              uint64
	Heartbeats, HeartbeatSequence, HeartbeatRevision, HeartbeatControl                                           uint64
}

func activationHeartbeatSnapshotNow(t *testing.T, f *supplementFixture, c workerapp.ActivateResidentRuntimeCommand, label string) activationHeartbeatSnapshot {
	t.Helper()
	raw := f.jsonRow(`SELECT jsonb_build_object(
 'SessionID',s.worker_session_id,'RunID',r.worker_run_id,'TurnID',a.agent_turn_id,'RuntimeID',i.runtime_instance_id,
 'AgentID',a.agent_session_id,'Container',COALESCE(i.container_id,''),
 'SessionStatus',s.status,'RunStatus',r.status,'TurnStatus',a.status,'RuntimeStatus',i.status,
 'SessionRev',s.session_revision,'RunRev',r.revision,'TurnRev',a.revision,'RuntimeRev',i.revision,
 'Generation',i.generation,'Epoch',ch.channel_epoch,'LastEvent',i.last_event_sequence,
 'LastCommand',i.last_command_sequence,'CommandAck',ch.command_acknowledged_sequence,'ChannelRev',ch.revision,'Committed',ch.event_committed_sequence,'Acknowledged',ch.event_acknowledged_sequence,'Applied',ch.event_applied_sequence,'Commands',ch.command_committed_sequence,
 'Heartbeats',(SELECT count(*) FROM runtime_heartbeat_observations WHERE runtime_instance_id=i.runtime_instance_id),
 'HeartbeatSequence',COALESCE(h.heartbeat_sequence,0),'HeartbeatRevision',COALESCE(h.revision,0),'HeartbeatControl',COALESCE(h.last_control_sequence,0))
 FROM runtime_instances i JOIN worker_sessions s ON s.worker_session_id=i.worker_session_id
 JOIN worker_runs r ON r.worker_run_id=$2 JOIN agent_turns a ON a.agent_turn_id=$3
 JOIN runtime_control_channels ch USING(runtime_instance_id)
 LEFT JOIN runtime_heartbeat_observations h USING(runtime_instance_id)
 WHERE i.runtime_instance_id=$1`, c.RuntimeInstanceID, c.WorkerRunID, c.AgentTurnID)
	t.Logf("[activation-heartbeat] snapshot %s=%s", label, raw)
	var v activationHeartbeatSnapshot
	supplementMust(t, json.Unmarshal(raw, &v))
	return v
}
func activationHeartbeatAssertOwner(t *testing.T, f *supplementFixture, c workerapp.ActivateResidentRuntimeCommand) {
	t.Helper()
	for label, q := range map[string]string{
		"session":            `SELECT count(*) FROM worker_sessions WHERE worker_session_id=$1`,
		"run":                `SELECT count(*) FROM worker_runs WHERE worker_session_id=$1`,
		"turn":               `SELECT count(*) FROM agent_turns WHERE worker_session_id=$1`,
		"runtime":            `SELECT count(*) FROM runtime_instances WHERE worker_session_id=$1`,
		"consumed execution": `SELECT count(*) FROM runtime_capacity_executions WHERE worker_session_id=$1 AND consumed_at IS NOT NULL AND released_at IS NULL`,
		"resident":           `SELECT count(*) FROM runtime_capacity_residents WHERE worker_session_id=$1 AND removal_sequence IS NULL`,
	} {
		if n := f.count(q, c.WorkerSessionID); n != 1 {
			t.Fatalf("%s count=%d", label, n)
		}
	}
	if n := f.count(`SELECT count(*) FROM runtime_capacity_executions WHERE worker_session_id=$1 AND run_id=$2 AND turn_id=$3 AND runtime_id=$4`, c.WorkerSessionID, c.WorkerRunID, c.AgentTurnID, c.RuntimeInstanceID); n != 1 {
		t.Fatalf("original consumed owner=%d", n)
	}
	f.runtime.inventory.mu.Lock()
	n := len(f.runtime.inventory.requests)
	request, ok := f.runtime.inventory.requests[c.RuntimeInstanceID]
	f.runtime.inventory.mu.Unlock()
	if n != 1 || !ok || request.WorkerSessionID != c.WorkerSessionID || request.WorkerRunID != c.WorkerRunID || c.ContainerID != "fixture-"+c.RuntimeInstanceID {
		t.Fatalf("created CID/owner mismatch request=%+v command=%+v", request, c)
	}
	if n := f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`); n != 0 {
		t.Fatalf("prompt before explicit executeNextDeliveredTurn=%d", n)
	}
	select {
	case <-f.runtime.starts:
		t.Fatal("fixture prompt before explicit execute")
	default:
	}
}
func activationHeartbeatHeartbeat(t *testing.T, f *supplementFixture, ticks chan time.Time, c workerapp.ActivateResidentRuntimeCommand, observe bool) contractsv1.RuntimeControlEnvelope {
	t.Helper()
	select {
	case ticks <- time.Now():
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	var envelope contractsv1.RuntimeControlEnvelope
	for {
		accepted := supplementReceive(t, f.ctx, f.journal.accepted)
		supplementMust(t, json.Unmarshal(accepted.Message.Envelope, &envelope))
		if envelope.MessageType != "runtime.heartbeat" {
			continue
		}
		if string(envelope.WorkerSessionID) != c.WorkerSessionID || string(envelope.RuntimeInstanceID) != c.RuntimeInstanceID || uint64(envelope.RuntimeGeneration) != c.RuntimeGeneration || envelope.ChannelEpoch != c.ControlChannelEpoch || envelope.MessageID != "heartbeat-observation-fixture-1" {
			t.Fatalf("heartbeat identity=%+v", envelope)
		}
		t.Logf("[activation-heartbeat] accepted-heartbeat=%s", accepted.Message.Envelope)
		break
	}
	// Complete the real transport ACK reservation too, so channel revisions are stable.
	for f.count(`SELECT count(*) FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='command' AND sequence=2 AND envelope->>'message_type'='runtime.control.ack'`, c.RuntimeInstanceID) != 1 {
		select {
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		default:
			runtime.Gosched()
		}
	}
	// Before Activate, runtime.ready blocks this ordered observer pump. The
	// revised brief explicitly requires observing that absence, not waiting past it.
	if !observe {
		return envelope
	}
	for {
		if f.count(`SELECT count(*) FROM runtime_heartbeat_observations WHERE runtime_instance_id=$1 AND worker_session_id=$2 AND runtime_generation=$3 AND channel_epoch=$4 AND heartbeat_sequence=1 AND revision=1 AND last_control_sequence=$5`, c.RuntimeInstanceID, c.WorkerSessionID, c.RuntimeGeneration, c.ControlChannelEpoch, envelope.Sequence) == 1 {
			break
		}
		select {
		case err := <-f.runtime.failures:
			t.Fatal(err)
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		default:
			runtime.Gosched()
		}
	}
	return envelope
}
func activationHeartbeatAssertHeartbeatDelta(t *testing.T, before, after activationHeartbeatSnapshot, envelope contractsv1.RuntimeControlEnvelope) {
	t.Helper()
	want := before
	want.LastEvent++
	want.LastCommand++
	want.CommandAck++
	want.ChannelRev += 3
	want.Commands++
	want.Committed++
	want.Acknowledged++
	if envelope.Sequence != before.LastEvent+1 {
		t.Fatalf("heartbeat sequence=%d", envelope.Sequence)
	}
	// Exact expected absence: ordered runtime.ready delivery remains unapplied.
	if !reflect.DeepEqual(want, after) {
		t.Errorf("unexpected heartbeat delta\nwant=%+v\ngot=%+v", want, after)
	}
}
func TestInitialActivationHeartbeatTransportOrdering(t *testing.T) {
	for _, name := range []string{"heartbeat_before_activate", "no_heartbeat", "heartbeat_after_activate"} {
		t.Run(name, func(t *testing.T) {
			ticks := make(chan time.Time)
			f := newSupplementFixture(t, func(f *supplementFixture) { enableInitialCapacityFixture(f); f.runtime.heartbeatTicks = ticks })
			defer close(ticks)
			prepared := &countedInitialPreparation{RuntimeEnvironmentPreparer: f.app.runtime.starts.Environment}
			f.app.runtime.starts.Environment = prepared
			provisioner := &activationHeartbeatProvisioner{RuntimeProvisioner: f.app.runtime.starts.Provisioner}
			f.app.runtime.starts.Provisioner = provisioner
			var schema string
			supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT current_schema()`).Scan(&schema))
			t.Logf("[activation-heartbeat] isolated-schema=%s", schema)
			barrier := &activationHeartbeatBarrier{RuntimeResidencyStore: f.app.runtime.residency.Store, entered: make(chan workerapp.ActivateResidentRuntimeCommand, 1), release: make(chan struct{})}
			f.app.runtime.residency.Store = barrier
			released := false
			defer func() {
				if !released {
					close(barrier.release)
				}
			}()
			createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "activation-heartbeat-"+name, "Activate through real heartbeat transport")
			done := make(chan error, 1)
			go func() { done <- f.app.runtime.startNextQueuedTask(f.ctx) }()
			c := supplementReceive(t, f.ctx, barrier.entered)
			frozen := c
			command, _ := json.Marshal(c)
			t.Logf("[activation-heartbeat] frozen-command=%s", command)
			before := activationHeartbeatSnapshotNow(t, f, c, "bound_before_activate")
			if before.SessionID != c.WorkerSessionID || before.RunID != c.WorkerRunID || before.TurnID != c.AgentTurnID || before.RuntimeID != c.RuntimeInstanceID || before.Generation != c.RuntimeGeneration || before.Epoch != c.ControlChannelEpoch || before.SessionRev != c.ExpectedSessionRevision || before.RunRev != c.ExpectedRunRevision || before.TurnRev != c.ExpectedTurnRevision || before.RuntimeRev != c.ExpectedRuntimeRevision || before.AgentID != "agent-observation-fixture" || before.Container != "" || before.SessionStatus != "running" || before.RunStatus != "starting" || before.TurnStatus != "delivered" || before.RuntimeStatus != "starting" || before.LastEvent != 1 || before.Committed != 1 || before.Acknowledged != 1 || before.Applied != 0 || before.Commands != 1 || before.CommandAck != 0 || before.LastCommand != 1 || before.Heartbeats != 0 {
				t.Fatalf("unexpected bound state=%+v", before)
			}
			activationHeartbeatAssertOwner(t, f, c)
			current := before
			if name == "heartbeat_before_activate" {
				e := activationHeartbeatHeartbeat(t, f, ticks, c, false)
				current = activationHeartbeatSnapshotNow(t, f, c, "heartbeat_committed_before_activate")
				activationHeartbeatAssertHeartbeatDelta(t, before, current, e)
			}
			close(barrier.release)
			released = true
			err := supplementReceive(t, f.ctx, done)
			supplementMust(t, err)
			if c != frozen || prepared.calls.Load() != 1 || provisioner.checks != 1 {
				t.Fatal("activation changed command or repeated pre-start preparation")
			}
			after := activationHeartbeatSnapshotNow(t, f, c, "after_activate_return")
			activationHeartbeatAssertOwner(t, f, c)
			{
				want := current
				want.SessionRev++
				want.RunRev++
				want.RuntimeRev++
				want.RunStatus = "running"
				want.RuntimeStatus = "active"
				want.Container = c.ContainerID
				want.ChannelRev++
				want.Applied = 1
				if name != "heartbeat_before_activate" && !reflect.DeepEqual(want, after) {
					t.Fatalf("activation control delta want=%+v got=%+v", want, after)
				}
				if name != "no_heartbeat" {
					var e contractsv1.RuntimeControlEnvelope
					if name == "heartbeat_after_activate" {
						e = activationHeartbeatHeartbeat(t, f, ticks, c, true)
					} else {
						supplementMust(t, json.Unmarshal(f.jsonRow(`SELECT envelope FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='event' AND envelope->>'message_type'='runtime.heartbeat'`, c.RuntimeInstanceID), &e))
					}
					f.waitApplied(e.MessageID)
					got := activationHeartbeatSnapshotNow(t, f, c, "heartbeat_applied_after_activate")
					want := current
					if name == "heartbeat_after_activate" {
						want.LastEvent++
						want.LastCommand++
						want.CommandAck++
						want.ChannelRev += 3
						want.Commands++
						want.Committed++
						want.Acknowledged++
					}
					want.SessionRev++
					want.RunRev++
					want.RuntimeRev++
					want.RunStatus = "running"
					want.RuntimeStatus = "active"
					want.Container = c.ContainerID
					want.ChannelRev += 2
					want.Applied = 2
					want.Heartbeats = 1
					want.HeartbeatSequence = 1
					want.HeartbeatRevision = 1
					want.HeartbeatControl = e.Sequence
					if !reflect.DeepEqual(want, got) {
						t.Fatalf("heartbeat/apply delta want=%+v got=%+v", want, got)
					}
					// Exact response-loss replay after transport progress: original command/revisions.
					_, replayErr := barrier.RuntimeResidencyStore.ActivateResidentRuntime(f.ctx, workerapp.ActivateResidentRuntimeRecord{Command: frozen, Now: time.Now()})
					supplementMust(t, replayErr)
					if replayed := activationHeartbeatSnapshotNow(t, f, c, "exact_activate_replay"); !reflect.DeepEqual(got, replayed) {
						t.Fatal("exact activation replay mutated state")
					}
				}
				activationHeartbeatAssertOwner(t, f, c)
				supplementMust(t, f.app.runtime.executeNextDeliveredTurn(f.ctx))
				turn := f.runtime.next(t)
				if string(turn.start.Envelope.WorkerRunID) != c.WorkerRunID || string(turn.start.Envelope.AgentTurnID) != c.AgentTurnID || string(turn.start.Envelope.RuntimeInstanceID) != c.RuntimeInstanceID {
					t.Fatal("explicit execute changed owner")
				}
				if f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND envelope->>'message_type'='agent_turn.start'`) != 1 {
					t.Fatal("explicit execute did not send exactly one prompt")
				}
			}
			wantHB := 0
			if name != "no_heartbeat" {
				wantHB = 1
			}
			if n := f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='event' AND envelope->>'message_type'='runtime.heartbeat'`); n != wantHB {
				t.Fatalf("heartbeat event count=%d want=%d", n, wantHB)
			}
			t.Logf("[activation-heartbeat] control=%s PASS single_original_owner=true accepted_heartbeat_events=%d", name, wantHB)
		})
	}
}
