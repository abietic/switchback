package localproduct

import (
	"context"
	"errors"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacityobserver"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"testing"
)

type capacityAssemblyQualification struct {
	events *[]string
	fail   bool
}

func (q *capacityAssemblyQualification) metadata() capacitysnapshot.AdmissionManifestMetadata {
	return capacitysnapshot.AdmissionManifestMetadata{Inventory: capacitysnapshot.DeploymentInventory{WorkerImage: "worker"}}
}
func (q *capacityAssemblyQualification) check(ctx context.Context) error {
	*q.events = append(*q.events, "recheck/current-role")
	if q.fail {
		return errors.New("role refused")
	}
	return ctx.Err()
}

type capacityAssemblyStore struct {
	events      *[]string
	initialized bool
}

func (s *capacityAssemblyStore) InitializeTrust(context.Context, capacitysnapshot.Trust) error {
	*s.events = append(*s.events, "initialize-stale")
	s.initialized = true
	return nil
}
func (s *capacityAssemblyStore) ReconcileCapacity(context.Context, *capacitysnapshot.Reader) error {
	*s.events = append(*s.events, "reconcile")
	return nil
}
func TestRuntimeCapacityAssemblyStaleBeforeFirstSample(t *testing.T) {
	events := []string{}
	q := &capacityAssemblyQualification{events: &events}
	store := &capacityAssemblyStore{events: &events}
	assembly := initialCapacityAssembly{
		qualification: func(context.Context, string, capacitysnapshot.Trust) (initialDeploymentQualification, error) {
			events = append(events, "open")
			return q, nil
		},
		reader: func(string) (*capacitysnapshot.Reader, error) { events = append(events, "reader"); return nil, nil },
		observer: func(capacitysnapshot.Trust) (*capacityobserver.Checker, error) {
			events = append(events, "observer")
			return nil, nil
		},
		store: func(context.Context, *pgxpool.Pool) (initialCapacityStore, error) {
			events = append(events, "store")
			return store, nil
		}}
	source, e := assembleInitialCapacitySource(context.Background(), Config{WorkerImage: "worker"}, nil, capacitysnapshot.Trust{}, assembly)
	if e != nil {
		t.Fatal(e)
	}
	if !store.initialized || !reflect.DeepEqual(events, []string{"open", "recheck/current-role", "reader", "observer", "store", "initialize-stale"}) {
		t.Fatal(events)
	}
	events = nil
	if e = source.checkRuntimeStart(context.Background()); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(events, []string{"recheck/current-role", "reconcile"}) {
		t.Fatal(events)
	}
	q.fail = true
	events = nil
	if e = source.checkRuntimeStart(context.Background()); e == nil {
		t.Fatal("failed recheck reconciled")
	}
	if !reflect.DeepEqual(events, []string{"recheck/current-role"}) {
		t.Fatal(events)
	}
	events = nil
	if _, e = assembleInitialCapacitySource(context.Background(), Config{WorkerImage: "worker"}, nil, capacitysnapshot.Trust{}, assembly); e == nil {
		t.Fatal("failed role assembled")
	}
	if !reflect.DeepEqual(events, []string{"open", "recheck/current-role"}) {
		t.Fatal(events)
	}
}
