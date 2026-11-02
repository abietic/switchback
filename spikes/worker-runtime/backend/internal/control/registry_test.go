package control

import (
	"context"
	"errors"
	"testing"
	"time"

	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
)

func TestRegistryRejectsSecondConnectionForRuntime(t *testing.T) {
	registry := NewRegistry()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}

	first, err := registry.attach(identity)
	if err != nil {
		t.Fatalf("first attach() error = %v", err)
	}
	t.Cleanup(func() { registry.detach(identity, first) })

	if _, err := registry.attach(identity); !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("second attach() error = %v, want ErrAlreadyConnected", err)
	}
	if got := registry.connectionCount(); got != 1 {
		t.Fatalf("connection count = %d, want 1", got)
	}
}

func TestRegistryWaitReadyRequiresRuntimeReady(t *testing.T) {
	registry := NewRegistry()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	connection, err := registry.attach(identity)
	if err != nil {
		t.Fatalf("attach() error = %v", err)
	}
	t.Cleanup(func() { registry.detach(identity, connection) })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- registry.WaitReady(ctx, identity) }()

	select {
	case err := <-result:
		t.Fatalf("WaitReady() returned before RuntimeReady: %v", err)
	default:
	}
	connection.markReady()
	connection.markReady()
	if err := <-result; err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
}

func TestRegistryBackpressureClosesFullRuntimeQueue(t *testing.T) {
	registry := NewRegistry()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	connection, err := registry.attach(identity)
	if err != nil {
		t.Fatalf("attach() error = %v", err)
	}
	t.Cleanup(func() { registry.detach(identity, connection) })
	command := &workercontrolv1.BackendEnvelope{
		Payload: &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "work"},
		},
	}

	for index := 0; index < commandQueueCapacity; index++ {
		if err := registry.Send(context.Background(), identity, command); err != nil {
			t.Fatalf("Send() #%d error = %v", index+1, err)
		}
	}
	if err := registry.Send(context.Background(), identity, command); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("Send() full queue error = %v, want ErrBackpressure", err)
	}
	select {
	case <-connection.done:
	default:
		t.Fatal("full queue did not close the runtime connection")
	}
	if got := len(connection.Commands); got != commandQueueCapacity {
		t.Fatalf("queued commands = %d, want bounded capacity %d", got, commandQueueCapacity)
	}
	if _, err := registry.admit(identity, connection); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("stale admission after backpressure error = %v, want ErrBackpressure", err)
	}
	if err := registry.Send(context.Background(), identity, command); err == nil {
		t.Fatal("Send() after backpressure fencing error = nil")
	}
}

func TestRegistryRevokeWakesWaitReadyBeforeConnection(t *testing.T) {
	registry := NewRegistry()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- registry.WaitReady(ctx, identity) }()

	registry.Revoke(identity)
	if err := <-result; !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("WaitReady() after revoke error = %v, want ErrUnauthenticated", err)
	}
	if _, err := registry.attach(identity); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("attach() after revoke error = %v, want ErrUnauthenticated", err)
	}
}

func TestRegistryRevokeWaitsForAdmittedMutationAndFencesStaleConnection(t *testing.T) {
	registry := NewRegistry()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	connection, err := registry.attach(identity)
	if err != nil {
		t.Fatalf("attach() error = %v", err)
	}
	operation, err := registry.admit(identity, connection)
	if err != nil {
		t.Fatalf("admit() error = %v", err)
	}

	revokeDone := make(chan struct{})
	go func() {
		registry.Revoke(identity)
		close(revokeDone)
	}()
	select {
	case <-connection.done:
	case <-time.After(time.Second):
		t.Fatal("revoke did not fence the connection")
	}
	select {
	case <-revokeDone:
		t.Fatal("Revoke() returned while an admitted mutation was half-committed")
	default:
	}
	if _, err := registry.admit(identity, connection); err == nil {
		t.Fatal("stale connection admitted work after revoke fencing")
	}
	if err := registry.Send(context.Background(), identity, &workercontrolv1.BackendEnvelope{
		Payload: &workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "must be fenced"},
		},
	}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Send() after revoke error = %v, want ErrUnauthenticated", err)
	}

	operation.release()
	select {
	case <-revokeDone:
	case <-time.After(time.Second):
		t.Fatal("Revoke() did not return after the admitted mutation committed")
	}
}

func TestRegistryDetachWaitsForAdmittedOperationBeforeReattach(t *testing.T) {
	registry := NewRegistry()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	connection, err := registry.attach(identity)
	if err != nil {
		t.Fatalf("attach() error = %v", err)
	}
	connection.markReady()
	operation, err := registry.admit(identity, connection)
	if err != nil {
		t.Fatalf("admit() error = %v", err)
	}

	detachDone := make(chan struct{})
	go func() {
		registry.detach(identity, connection)
		close(detachDone)
	}()
	select {
	case <-connection.done:
	case <-time.After(time.Second):
		t.Fatal("detach did not fence the connection")
	}
	if _, err := registry.admit(identity, connection); err == nil {
		t.Fatal("stale connection admitted work after detach fencing")
	}
	if err := registry.Send(context.Background(), identity, &workercontrolv1.BackendEnvelope{
		Payload: &workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "must be fenced"},
		},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send() during detach drain error = %v, want context.Canceled", err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := registry.WaitReady(waitContext, identity); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady() during detach drain error = %v, want context.Canceled", err)
	}
	if replacement, err := registry.attach(identity); !errors.Is(err, ErrAlreadyConnected) {
		operation.release()
		<-detachDone
		if replacement != nil {
			registry.detach(identity, replacement)
		}
		t.Fatalf("reattach() during detach drain error = %v, want ErrAlreadyConnected", err)
	}
	select {
	case <-detachDone:
		t.Fatal("detach returned while an admitted operation was in flight")
	default:
	}

	operation.release()
	select {
	case <-detachDone:
	case <-time.After(time.Second):
		t.Fatal("detach did not finish after the admitted operation completed")
	}
	reconnected, err := registry.attach(identity)
	if err != nil {
		t.Fatalf("reattach() after drained detach error = %v", err)
	}
	registry.detach(identity, reconnected)
}

func TestRegistryBackpressureRetainsReservationUntilAdmittedOperationDrains(t *testing.T) {
	registry := NewRegistry()
	identity := Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	connection, err := registry.attach(identity)
	if err != nil {
		t.Fatalf("attach() error = %v", err)
	}
	operation, err := registry.admit(identity, connection)
	if err != nil {
		t.Fatalf("admit() error = %v", err)
	}
	command := &workercontrolv1.BackendEnvelope{
		Payload: &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "work"},
		},
	}
	for index := 0; index < commandQueueCapacity; index++ {
		if err := registry.Send(context.Background(), identity, command); err != nil {
			operation.release()
			t.Fatalf("Send() #%d error = %v", index+1, err)
		}
	}

	backpressureDone := make(chan error, 1)
	go func() {
		backpressureDone <- registry.Send(context.Background(), identity, command)
	}()
	select {
	case <-connection.done:
	case <-time.After(time.Second):
		operation.release()
		t.Fatal("backpressure did not publish the connection fence")
	}
	replacement, reattachErr := registry.attach(identity)
	if !errors.Is(reattachErr, ErrAlreadyConnected) {
		operation.release()
		<-backpressureDone
		if replacement != nil {
			registry.detach(identity, replacement)
		}
		t.Fatalf("reattach() during backpressure drain error = %v, want ErrAlreadyConnected", reattachErr)
	}
	select {
	case err := <-backpressureDone:
		operation.release()
		t.Fatalf("backpressure Send() returned before admitted operation drained: %v", err)
	default:
	}

	operation.release()
	if err := <-backpressureDone; !errors.Is(err, ErrBackpressure) {
		t.Fatalf("backpressure Send() error = %v, want ErrBackpressure", err)
	}
	reconnected, err := registry.attach(identity)
	if err != nil {
		t.Fatalf("reattach() after backpressure drain error = %v", err)
	}
	registry.detach(identity, reconnected)
}
