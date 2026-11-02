package fake

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

type Clock struct {
	Value time.Time
}

func (clock Clock) Now() time.Time { return clock.Value }

type RuntimeProvisioner struct {
	mu                sync.Mutex
	ProvisionRequests []application.RuntimeProvisionRequest
	InspectionIDs     []string
	CleanupRequests   []application.RuntimeCleanupRequest
	ProvisionResult   application.ProvisionedRuntime
	Inspection        application.RuntimeInspection
	CleanupResult     application.RuntimeCleanupObservation
	Err               error
}

func (fake *RuntimeProvisioner) Provision(_ context.Context, request application.RuntimeProvisionRequest) (application.ProvisionedRuntime, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.ProvisionRequests = append(fake.ProvisionRequests, request)
	return fake.ProvisionResult, fake.Err
}

func (fake *RuntimeProvisioner) Inspect(_ context.Context, id string) (application.RuntimeInspection, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.InspectionIDs = append(fake.InspectionIDs, id)
	return fake.Inspection, fake.Err
}

func (fake *RuntimeProvisioner) Cleanup(_ context.Context, request application.RuntimeCleanupRequest) (application.RuntimeCleanupObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.CleanupRequests = append(fake.CleanupRequests, request)
	return fake.CleanupResult, fake.Err
}

type WorkerControl struct {
	mu       sync.Mutex
	Commands []application.WorkerControlCommand
	Fences   []string
	EventC   chan application.WorkerControlEvent
	Err      error
}

func (fake *WorkerControl) Send(_ context.Context, command application.WorkerControlCommand) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.Commands = append(fake.Commands, command)
	return fake.Err
}

func (fake *WorkerControl) Events(context.Context, string, uint64) (<-chan application.WorkerControlEvent, error) {
	if fake.EventC == nil {
		fake.EventC = make(chan application.WorkerControlEvent)
	}
	return fake.EventC, fake.Err
}

func (fake *WorkerControl) Fence(_ context.Context, sessionID string, generation uint64, reason string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.Fences = append(fake.Fences, fmt.Sprintf("%s/%d/%s", sessionID, generation, reason))
	return fake.Err
}

type AgentAdapter struct {
	Capabilities application.AgentCapabilities
	SessionID    string
	Updates      chan application.AgentUpdate
	Err          error
}

func (fake *AgentAdapter) Initialize(context.Context, application.AgentInitializeRequest) (application.AgentCapabilities, error) {
	return fake.Capabilities, fake.Err
}

func (fake *AgentAdapter) Authenticate(context.Context, string) error { return fake.Err }

func (fake *AgentAdapter) OpenSession(context.Context, application.AgentSessionRequest) (string, error) {
	return fake.SessionID, fake.Err
}

func (fake *AgentAdapter) Prompt(context.Context, application.AgentPromptRequest) (<-chan application.AgentUpdate, error) {
	if fake.Updates == nil {
		fake.Updates = make(chan application.AgentUpdate)
	}
	return fake.Updates, fake.Err
}

func (fake *AgentAdapter) Cancel(context.Context, string, string) error { return fake.Err }

func (fake *AgentAdapter) Close(context.Context) error { return fake.Err }

type ObjectStore struct {
	mu      sync.Mutex
	Objects map[string][]byte
	Err     error
}

func (fake *ObjectStore) PutTemporary(_ context.Context, identity application.ObjectIdentity, reader io.Reader) (application.TemporaryObject, error) {
	if fake.Err != nil {
		return application.TemporaryObject{}, fake.Err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return application.TemporaryObject{}, err
	}
	key := identity.WorkerSessionID + "/" + identity.Kind
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.Objects == nil {
		fake.Objects = make(map[string][]byte)
	}
	fake.Objects[key] = data
	return application.TemporaryObject{UploadID: key, Size: int64(len(data)), Checksum: "fake"}, nil
}

func (fake *ObjectStore) Finalize(_ context.Context, _ application.ObjectIdentity, object application.TemporaryObject) (application.StoredObject, error) {
	return application.StoredObject{Key: object.UploadID, Size: object.Size, Checksum: object.Checksum}, fake.Err
}

func (fake *ObjectStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	data, ok := fake.Objects[key]
	if !ok {
		return nil, fmt.Errorf("object %s not found", key)
	}
	return io.NopCloser(bytes.NewReader(data)), fake.Err
}

func (fake *ObjectStore) Verify(context.Context, string, string) error { return fake.Err }

type ModelObservationSink struct {
	mu           sync.Mutex
	Observations []application.ModelObservation
	Err          error
}

func (fake *ModelObservationSink) Record(_ context.Context, observation application.ModelObservation) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.Observations = append(fake.Observations, observation)
	return fake.Err
}
