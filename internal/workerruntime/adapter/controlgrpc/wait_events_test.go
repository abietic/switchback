package controlgrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestWaitEventsRequiresExactLiveResumeAndReturnsSharedSubscription(t *testing.T) {
	hub := NewHub()
	identity := RuntimeIdentity{
		WorkerSessionID:   "session-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		ChannelEpoch:      7,
	}
	if err := hub.RegisterCredential(Credential{
		Identity: identity,
		Token:    "worker-control-token",
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type result struct {
		events <-chan workerapp.WorkerControlEvent
		err    error
	}
	completed := make(chan result, 1)
	go func() {
		events, err := hub.WaitEvents(ctx, identity)
		completed <- result{events: events, err: err}
	}()

	active, err := hub.attachClaimed(identity, workerapp.RuntimeControlAuthority{
		WorkerSessionID:   identity.WorkerSessionID,
		RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration),
		ChannelEpoch:      identity.ChannelEpoch,
		ProcessNonce:      "process-1",
		StreamAttempt:     1,
		OwnerFence:        1,
	}, "connection-1")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-completed:
		t.Fatalf("WaitEvents returned before ResumeComplete: %+v", value)
	case <-time.After(20 * time.Millisecond):
	}

	active.markLive(0)
	select {
	case value := <-completed:
		if value.err != nil {
			t.Fatal(value.err)
		}
		expected, err := hub.Events(context.Background(), identity.RuntimeInstanceID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if value.events != expected {
			t.Fatal("WaitEvents returned a different event subscription")
		}
	case <-time.After(time.Second):
		t.Fatal("WaitEvents did not wake after ResumeComplete")
	}
}

func TestWaitEventsFailsClosedForWrongEpochAndWakesOnRevoke(t *testing.T) {
	hub := NewHub()
	identity := RuntimeIdentity{
		WorkerSessionID:   "session-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		ChannelEpoch:      7,
	}
	if err := hub.RegisterCredential(Credential{
		Identity: identity,
		Token:    "worker-control-token",
	}); err != nil {
		t.Fatal(err)
	}
	wrong := identity
	wrong.ChannelEpoch++
	if _, err := hub.WaitEvents(context.Background(), wrong); !errors.Is(err, ErrStaleRuntime) {
		t.Fatalf("wrong epoch error=%v, want ErrStaleRuntime", err)
	}

	completed := make(chan error, 1)
	go func() {
		_, err := hub.WaitEvents(context.Background(), identity)
		completed <- err
	}()
	hub.Revoke(identity)
	select {
	case err := <-completed:
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("revoked wait error=%v, want ErrUnauthenticated", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Revoke did not wake WaitEvents")
	}
}

func TestWaitEventsWakesAndFailsClosedOnDurableFence(t *testing.T) {
	hub := newHub(&scriptedControlJournal{})
	identity := RuntimeIdentity{
		WorkerSessionID:   "session-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		ChannelEpoch:      7,
	}
	if err := hub.RegisterCredential(Credential{
		Identity: identity,
		Token:    "worker-control-token",
	}); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		_, err := hub.WaitEvents(context.Background(), identity)
		completed <- err
	}()
	if err := hub.Fence(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
		"startup_reconnect_timeout",
	); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if !errors.Is(err, ErrStaleRuntime) {
			t.Fatalf("fenced wait error=%v, want ErrStaleRuntime", err)
		}
	case <-time.After(time.Second):
		t.Fatal("durable fence did not wake WaitEvents")
	}
}

func TestFenceIfNotLiveLetsDurableResumeCompleteWinnerPublishLive(t *testing.T) {
	journal := &scriptedControlJournal{
		fence: func(
			_ context.Context,
			command workerapp.FenceRuntimeControlChannelCommand,
		) (workerapp.RuntimeControlChannelState, error) {
			if !command.OnlyIfNotConnected {
				t.Fatal("startup timeout used an unconditional durable fence")
			}
			return workerapp.RuntimeControlChannelState{Status: "connected"}, nil
		},
	}
	hub := newHub(journal)
	identity := RuntimeIdentity{
		WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1, ChannelEpoch: 7,
	}
	if err := hub.RegisterCredential(Credential{
		Identity: identity, Token: "worker-control-token",
	}); err != nil {
		t.Fatal(err)
	}
	active, err := hub.attachClaimed(identity, workerapp.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "process-1", StreamAttempt: 1, OwnerFence: 1,
	}, "connection-1")
	if err != nil {
		t.Fatal(err)
	}

	fenced, err := hub.FenceIfNotLive(
		context.Background(),
		identity.RuntimeInstanceID,
		1,
		"startup_reconnect_timeout",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if fenced {
		t.Fatal("connected ResumeComplete winner was fenced")
	}
	active.markLive(0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := hub.WaitEvents(ctx, identity); err != nil {
		t.Fatalf("ResumeComplete winner did not publish live: %v", err)
	}
}
