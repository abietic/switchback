package controlgrpc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestFenceIdentityUsesCommittedJournalWithoutCredential(t *testing.T) {
	identity := testCredential().Identity
	want := workerapp.RuntimeControlChannelState{WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID, RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch, Status: "fenced", FencedAt: time.Now(), Revision: 3, FenceRevision: 3, OwnerFence: 2, FenceOwnerFence: 2}
	hub := newHub(&scriptedControlJournal{fence: func(_ context.Context, command workerapp.FenceRuntimeControlChannelCommand) (workerapp.RuntimeControlChannelState, error) {
		if command.WorkerSessionID != identity.WorkerSessionID || command.RuntimeInstanceID != identity.RuntimeInstanceID || command.RuntimeGeneration != uint64(identity.RuntimeGeneration) || command.ChannelEpoch != identity.ChannelEpoch || command.OnlyIfNotConnected {
			t.Fatal("lost exact persistent identity")
		}
		return want, nil
	}})
	got, err := hub.FenceIdentity(context.Background(), identity)
	if err != nil || got.FenceRevision != want.FenceRevision || got.FencedAt != want.FencedAt {
		t.Fatalf("committed state not returned: %+v %v", got, err)
	}
	if _, ok := hub.fenced[identity]; !ok {
		t.Fatal("committed fence not reflected in ephemeral routing")
	}
}

func TestFenceIdentityDoesNotTreatNilErrorOrStatusAsProof(t *testing.T) {
	for _, status := range []string{"connected", "fenced", ""} {
		t.Run(status, func(t *testing.T) {
			identity := testCredential().Identity
			hub := newHub(&scriptedControlJournal{fence: func(context.Context, workerapp.FenceRuntimeControlChannelCommand) (workerapp.RuntimeControlChannelState, error) {
				return workerapp.RuntimeControlChannelState{Status: status}, nil
			}})
			if _, err := hub.FenceIdentity(context.Background(), identity); err != nil {
				t.Fatal(err)
			}
			if _, ok := hub.fenced[identity]; ok {
				t.Fatal("unproven journal result revoked routing")
			}
		})
	}
}

func TestWakePendingCommandsSignalsLiveAndResumingExactConnectionsWithoutJournal(
	t *testing.T,
) {
	t.Parallel()

	for _, test := range []struct {
		name string
		live bool
	}{
		{name: "live", live: true},
		{name: "attached but not live"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var journalCalls atomic.Int64
			journal := &scriptedControlJournal{
				replay: func(
					context.Context,
					workerapp.RuntimeControlAuthority,
				) (workerapp.RuntimeControlReplayPlan, error) {
					journalCalls.Add(1)
					return workerapp.RuntimeControlReplayPlan{}, nil
				},
			}
			hub := newHub(journal)
			identity := memoryAPITestIdentity()
			active := mustAttachClaimed(
				t,
				hub,
				identity,
				memoryAPITestAuthority(identity),
				"connection-1",
			)
			if test.live {
				active.markLive(0)
			}

			if !hub.WakePendingCommands(identity) {
				t.Fatal("exact attached connection was not woken")
			}
			select {
			case <-active.sendWake:
			default:
				t.Fatal("wake edge was not queued")
			}
			if journalCalls.Load() != 0 {
				t.Fatalf(
					"pure-memory wake touched durable journal %d times",
					journalCalls.Load(),
				)
			}
		})
	}
}

func TestWakePendingCommandsRequiresFullIdentityAndCoalesces(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	identity := memoryAPITestIdentity()
	active := mustAttachClaimed(
		t,
		hub,
		identity,
		memoryAPITestAuthority(identity),
		"connection-1",
	)

	wrongIdentities := []RuntimeIdentity{
		func() RuntimeIdentity {
			wrong := identity
			wrong.WorkerSessionID = "session-other"
			return wrong
		}(),
		func() RuntimeIdentity {
			wrong := identity
			wrong.RuntimeInstanceID = "runtime-other"
			return wrong
		}(),
		func() RuntimeIdentity {
			wrong := identity
			wrong.RuntimeGeneration++
			return wrong
		}(),
		func() RuntimeIdentity {
			wrong := identity
			wrong.ChannelEpoch++
			return wrong
		}(),
	}
	for _, wrong := range wrongIdentities {
		if hub.WakePendingCommands(wrong) {
			t.Fatalf("wrong identity was reported as woken: %+v", wrong)
		}
	}
	select {
	case <-active.sendWake:
		t.Fatal("wrong identity queued a wake edge")
	default:
	}

	if !hub.WakePendingCommands(identity) ||
		!hub.WakePendingCommands(identity) {
		t.Fatal("duplicate exact wake did not find the connection")
	}
	if got := len(active.sendWake); got != 1 {
		t.Fatalf("coalesced wake edge count=%d, want 1", got)
	}
	<-active.sendWake

	active.close(errors.New("stream closed"))
	if hub.WakePendingCommands(identity) {
		t.Fatal("closed connection was reported as connected")
	}
	select {
	case <-active.sendWake:
		t.Fatal("closed connection received a wake edge")
	default:
	}
}

func TestRevokeDurablyFencedOnlyClosesExactConnectionWithoutJournal(t *testing.T) {
	t.Parallel()

	var durableFenceCalls atomic.Int64
	journal := &scriptedControlJournal{
		fence: func(
			context.Context,
			workerapp.FenceRuntimeControlChannelCommand,
		) (workerapp.RuntimeControlChannelState, error) {
			durableFenceCalls.Add(1)
			return workerapp.RuntimeControlChannelState{}, nil
		},
	}
	hub := newHub(journal)
	identity := memoryAPITestIdentity()
	active := mustAttachClaimed(
		t,
		hub,
		identity,
		memoryAPITestAuthority(identity),
		"connection-1",
	)

	wrong := identity
	wrong.ChannelEpoch++
	if hub.RevokeDurablyFenced(wrong) {
		t.Fatal("wrong identity reported an exact local revocation")
	}
	if got := hub.connections[keyFor(identity)]; got != active {
		t.Fatal("wrong identity removed the current connection")
	}
	select {
	case <-active.done:
		t.Fatal("wrong identity closed the current connection")
	default:
	}

	if !hub.RevokeDurablyFenced(identity) {
		t.Fatal("exact connection was not revoked")
	}
	if _, found := hub.connections[keyFor(identity)]; found {
		t.Fatal("exact connection remains routed after revocation")
	}
	select {
	case <-active.done:
	default:
		t.Fatal("exact connection was removed without being closed")
	}
	if !errors.Is(active.terminalError(), ErrNotConnected) {
		t.Fatalf(
			"revoked connection error=%v, want ErrNotConnected",
			active.terminalError(),
		)
	}
	if durableFenceCalls.Load() != 0 {
		t.Fatalf(
			"pure-memory revoke called durable fence %d times",
			durableFenceCalls.Load(),
		)
	}
	if hub.RevokeDurablyFenced(identity) {
		t.Fatal("duplicate revoke reported a second closed connection")
	}
}

func TestRevokeDurablyFencedPreventsAttachRaceForExactIdentityOnly(
	t *testing.T,
) {
	t.Parallel()

	hub := NewHub()
	identity := memoryAPITestIdentity()
	if hub.RevokeDurablyFenced(identity) {
		t.Fatal("revocation without a local connection reported one closed")
	}
	if _, err := hub.attachClaimed(
		identity,
		memoryAPITestAuthority(identity),
		"connection-after-fence",
	); !errors.Is(err, ErrStaleRuntime) {
		t.Fatalf("exact post-fence attach error=%v, want ErrStaleRuntime", err)
	}

	otherEpoch := identity
	otherEpoch.ChannelEpoch++
	if _, err := hub.attachClaimed(
		otherEpoch,
		memoryAPITestAuthority(otherEpoch),
		"connection-other-epoch",
	); err != nil {
		t.Fatalf("exact fence affected another ChannelEpoch: %v", err)
	}
}

func TestWakeAndDurableRevokeRaceLeavesNoRoutedConnection(t *testing.T) {
	t.Parallel()

	for iteration := 0; iteration < 100; iteration++ {
		hub := NewHub()
		identity := memoryAPITestIdentity()
		active := mustAttachClaimed(
			t,
			hub,
			identity,
			memoryAPITestAuthority(identity),
			"connection-1",
		)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			hub.WakePendingCommands(identity)
		}()
		go func() {
			defer wait.Done()
			<-start
			hub.RevokeDurablyFenced(identity)
		}()
		close(start)
		wait.Wait()

		hub.mu.RLock()
		routed := hub.connections[keyFor(identity)]
		hub.mu.RUnlock()
		if routed != nil {
			t.Fatalf(
				"iteration %d retained a routed connection after revoke",
				iteration,
			)
		}
		select {
		case <-active.done:
		default:
			t.Fatalf(
				"iteration %d left the revoked connection open",
				iteration,
			)
		}
		if hub.WakePendingCommands(identity) {
			t.Fatalf(
				"iteration %d woke a connection after revocation",
				iteration,
			)
		}
	}
}

func TestAttachAndDurableRevokeRaceCannotPublishAfterFence(t *testing.T) {
	t.Parallel()

	for iteration := 0; iteration < 100; iteration++ {
		hub := NewHub()
		identity := memoryAPITestIdentity()
		start := make(chan struct{})
		var (
			wait      sync.WaitGroup
			attached  *connection
			attachErr error
		)
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			attached, attachErr = hub.attachClaimed(
				identity,
				memoryAPITestAuthority(identity),
				"connection-racing-fence",
			)
		}()
		go func() {
			defer wait.Done()
			<-start
			hub.RevokeDurablyFenced(identity)
		}()
		close(start)
		wait.Wait()

		if attachErr != nil && !errors.Is(attachErr, ErrStaleRuntime) {
			t.Fatalf(
				"iteration %d attach error=%v, want nil or ErrStaleRuntime",
				iteration,
				attachErr,
			)
		}
		if attached != nil {
			select {
			case <-attached.done:
			default:
				t.Fatalf(
					"iteration %d published an open post-fence connection",
					iteration,
				)
			}
		}
		hub.mu.RLock()
		routed := hub.connections[keyFor(identity)]
		hub.mu.RUnlock()
		if routed != nil {
			t.Fatalf(
				"iteration %d retained a post-fence route",
				iteration,
			)
		}
	}
}

func memoryAPITestIdentity() RuntimeIdentity {
	return RuntimeIdentity{
		WorkerSessionID:   "session-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		ChannelEpoch:      7,
	}
}

func memoryAPITestAuthority(
	identity RuntimeIdentity,
) workerapp.RuntimeControlAuthority {
	return workerapp.RuntimeControlAuthority{
		WorkerSessionID:   identity.WorkerSessionID,
		RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration),
		ChannelEpoch:      identity.ChannelEpoch,
		ProcessNonce:      "process-1",
		StreamAttempt:     1,
		OwnerFence:        1,
	}
}
