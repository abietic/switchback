package localproduct

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeEnvironmentReplacementBindRetryClassification(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"serialization", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"database-restarting", &pgconn.PgError{Code: "57P03"}, true},
		{"connection-refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, true},
		{"response-lost", io.EOF, true},
		{"authentication", &pgconn.PgError{Code: "28P01"}, false},
		{"permission", &pgconn.PgError{Code: "42501"}, false},
		{"schema", &pgconn.PgError{Code: "42P01"}, false},
		{"identity", workerapp.ErrRuntimeControlIdentityConflict, false},
		{"credential", workerapp.ErrRuntimeControlCredentialConflict, false},
		{"epoch", workerapp.ErrRuntimeControlStaleOwner, false},
		{"cancelled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"unknown", errors.New("unknown failure"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := retryableReplacementBind(test.err); got != test.retry {
				t.Fatalf("retry=%v want=%v", got, test.retry)
			}
		})
	}
}

func TestCopyWorkspaceSourceRejectsSymlinksAndProducesStableDigest(t *testing.T) {
	source := t.TempDir()
	firstDestination := filepath.Join(t.TempDir(), "workspace")
	secondDestination := filepath.Join(t.TempDir(), "workspace")
	for _, destination := range []string{firstDestination, secondDestination} {
		if err := os.Mkdir(destination, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(source, "counter"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "counter", "counter.go"), []byte("package counter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noopChown := func(string, int, int) error { return nil }
	first, err := copyWorkspaceSource(source, firstDestination, noopChown)
	if err != nil {
		t.Fatal(err)
	}
	second, err := copyWorkspaceSource(source, secondDestination, noopChown)
	if err != nil || first != second {
		t.Fatalf("digest first=%q second=%q error=%v", first, second, err)
	}
	if content, err := os.ReadFile(filepath.Join(firstDestination, "counter", "counter.go")); err != nil || string(content) != "package counter\n" {
		t.Fatalf("WorkingCopy content=%q error=%v", content, err)
	}
	linkSource := t.TempDir()
	if err := os.Symlink(filepath.Join(source, "counter", "counter.go"), filepath.Join(linkSource, "counter.go")); err != nil {
		t.Fatal(err)
	}
	linkDestination := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(linkDestination, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := copyWorkspaceSource(linkSource, linkDestination, noopChown); err == nil {
		t.Fatal("Workspace source symlink was accepted")
	}
}

func TestCleanupDurablyFencedRuntimeRequiresExactPreparedIdentity(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*workerapp.BackendDeadlineSourceAuthority)
		wantConflict bool
	}{
		{
			name: "WorkerSession",
			mutate: func(authority *workerapp.BackendDeadlineSourceAuthority) {
				authority.WorkerSessionID = "session-other"
			},
			wantConflict: true,
		},
		{
			name: "RuntimeInstance",
			mutate: func(authority *workerapp.BackendDeadlineSourceAuthority) {
				authority.RuntimeInstanceID = "runtime-other"
			},
			wantConflict: true,
		},
		{
			name: "RuntimeGeneration",
			mutate: func(authority *workerapp.BackendDeadlineSourceAuthority) {
				authority.RuntimeGeneration++
			},
			wantConflict: true,
		},
		{
			name: "ChannelEpoch",
			mutate: func(authority *workerapp.BackendDeadlineSourceAuthority) {
				authority.ChannelEpoch++
			},
			wantConflict: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment, journal, storage, identity := newDeadlineCleanupTestEnvironment(t)
			secretRoot := createDeadlineCleanupSecretRoot(
				t,
				storage,
				identity.RuntimeInstanceID,
				uint64(identity.RuntimeGeneration),
			)
			credential := deadlineCleanupCredential(t, identity)
			if err := environment.hub.RegisterCredential(credential); err != nil {
				t.Fatal(err)
			}
			environment.prepared[identity.RuntimeInstanceID] = preparedRuntime{
				Storage:    storage,
				Credential: credential,
				Secrets: workerapp.RuntimeSecretResources{
					BackendRoot: secretRoot,
				},
			}

			authority := deadlineCleanupAuthority(identity)
			test.mutate(&authority)
			_, err := environment.cleanupDurablyFencedRuntime(authority, deadlineCleanupFence(t, environment, journal, identity))
			if test.wantConflict && err == nil {
				t.Fatal("conflicting Runtime identity was accepted")
			}
			if !test.wantConflict && err != nil {
				t.Fatalf("nonmatching Runtime cleanup: %v", err)
			}
			prepared, found := environment.get(identity.RuntimeInstanceID)
			if !found || prepared.Credential.Identity != identity {
				t.Fatal("nonexact cleanup removed the prepared Runtime")
			}
			if _, err := os.Lstat(secretRoot); err != nil {
				t.Fatalf("nonexact cleanup removed the Runtime secret root: %v", err)
			}
			if journal.fenceCalls != 0 {
				t.Fatalf(
					"nonexact cleanup attempted %d second durable fences",
					journal.fenceCalls,
				)
			}
		})
	}
}

func TestCleanupDurablyFencedRuntimeRemovesExactPreparedRuntimeWithoutSecondFence(
	t *testing.T,
) {
	environment, journal, storage, identity := newDeadlineCleanupTestEnvironment(t)
	secretRoot := createDeadlineCleanupSecretRoot(
		t,
		storage,
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	otherGenerationRoot := createDeadlineCleanupSecretRoot(
		t,
		storage,
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration)+1,
	)
	credential := deadlineCleanupCredential(t, identity)
	if err := environment.hub.RegisterCredential(credential); err != nil {
		t.Fatal(err)
	}
	environment.prepared[identity.RuntimeInstanceID] = preparedRuntime{
		Storage:    storage,
		Credential: credential,
		Secrets: workerapp.RuntimeSecretResources{
			BackendRoot: secretRoot,
		},
	}

	got, err := environment.cleanupDurablyFencedRuntime(
		deadlineCleanupAuthority(identity),
		deadlineCleanupFence(t, environment, journal, identity),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != storage {
		t.Fatalf("cleanup storage=%+v, want %+v", got, storage)
	}
	if _, found := environment.get(identity.RuntimeInstanceID); found {
		t.Fatal("exact prepared Runtime remains after cleanup")
	}
	assertDeadlineCleanupPathAbsent(t, secretRoot)
	if _, err := os.Lstat(otherGenerationRoot); err != nil {
		t.Fatalf("cleanup removed another Runtime generation: %v", err)
	}
	if journal.fenceCalls != 0 {
		t.Fatalf(
			"cleanup attempted %d second durable fences",
			journal.fenceCalls,
		)
	}
}

func TestCleanupDurablyFencedRuntimeReopensStorageAfterBackendRestart(t *testing.T) {
	environment, journal, storage, identity := newDeadlineCleanupTestEnvironment(t)
	secretRoot := createDeadlineCleanupSecretRoot(
		t,
		storage,
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	durableMarker := filepath.Join(storage.WorkingCopy.BackendPath, "keep.txt")
	if err := os.WriteFile(durableMarker, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := environment.cleanupDurablyFencedRuntime(
		deadlineCleanupAuthority(identity),
		deadlineCleanupFence(t, environment, journal, identity),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != storage {
		t.Fatalf("reopened storage=%+v, want %+v", got, storage)
	}
	assertDeadlineCleanupPathAbsent(t, secretRoot)
	if content, err := os.ReadFile(durableMarker); err != nil ||
		string(content) != "durable" {
		t.Fatalf(
			"cleanup changed durable WorkingCopy content=%q error=%v",
			content,
			err,
		)
	}
	if journal.fenceCalls != 0 {
		t.Fatalf(
			"restart cleanup attempted %d second durable fences",
			journal.fenceCalls,
		)
	}
}

type deadlineCleanupJournal struct {
	workerapp.RuntimeControlJournal
	fenceCalls int
	fenceErr   error
	bindErr    error
}

func (journal *deadlineCleanupJournal) BindRuntimeControlChannel(
	_ context.Context,
	record workerapp.BindRuntimeControlChannelRecord,
) (workerapp.RuntimeControlChannelState, error) {
	if journal.bindErr != nil {
		return workerapp.RuntimeControlChannelState{}, journal.bindErr
	}
	return workerapp.RuntimeControlChannelState{
		WorkerSessionID:   record.Command.WorkerSessionID,
		RuntimeInstanceID: record.Command.RuntimeInstanceID,
		RuntimeGeneration: record.Command.RuntimeGeneration,
		ChannelEpoch:      record.Command.ChannelEpoch,
		Status:            "bound",
		Revision:          1,
	}, nil
}

func (journal *deadlineCleanupJournal) FenceRuntimeControl(
	_ context.Context,
	command workerapp.FenceRuntimeControlChannelCommand,
	at time.Time,
) (workerapp.RuntimeControlChannelState, error) {
	journal.fenceCalls++
	if journal.fenceErr != nil {
		return workerapp.RuntimeControlChannelState{}, journal.fenceErr
	}
	return workerapp.RuntimeControlChannelState{
		WorkerSessionID:   command.WorkerSessionID,
		RuntimeInstanceID: command.RuntimeInstanceID,
		RuntimeGeneration: command.RuntimeGeneration,
		ChannelEpoch:      command.ChannelEpoch,
		Status:            "fenced",
		Revision:          2,
		FencedAt:          at, FenceRevision: 2, OwnerFence: 1, FenceOwnerFence: 1,
	}, nil
}

// Unit fixture's store boundary returns complete committed fields; the real
// PostgreSQL boundary is exercised by the integration cleanup tests.
func deadlineCleanupFence(t *testing.T, environment *runtimeEnvironment, journal *deadlineCleanupJournal, identity controlgrpc.RuntimeIdentity) workerapp.RuntimeControlChannelState {
	t.Helper()
	state, err := environment.hub.FenceIdentity(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	journal.fenceCalls = 0
	return state
}

func newDeadlineCleanupTestEnvironment(
	t *testing.T,
) (
	*runtimeEnvironment,
	*deadlineCleanupJournal,
	workerapp.SessionStorage,
	controlgrpc.RuntimeIdentity,
) {
	t.Helper()
	dataRoot := t.TempDir()
	materializer := localstorage.Materializer{
		BackendDataRoot: dataRoot,
		EngineDataRoot:  filepath.Join(t.TempDir(), "engine-data"),
		Ownership: localstorage.OwnershipFunc(
			func(string, int, int) error { return nil },
		),
	}
	storage, err := materializer.Materialize("session-1")
	if err != nil {
		t.Fatal(err)
	}
	journal := &deadlineCleanupJournal{}
	hub := controlgrpc.NewHubWithJournal(workerapp.RuntimeControlJournalService{
		Store: journal,
		Now:   time.Now,
	})
	return &runtimeEnvironment{
			hub:      hub,
			storage:  materializer,
			prepared: make(map[string]preparedRuntime),
		}, journal, storage, controlgrpc.RuntimeIdentity{
			WorkerSessionID:   "session-1",
			RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 7,
			ChannelEpoch:      11,
		}
}

func createDeadlineCleanupSecretRoot(
	t *testing.T,
	storage workerapp.SessionStorage,
	runtimeInstanceID string,
	generation uint64,
) string {
	t.Helper()
	root := filepath.Join(
		storage.BackendRoot,
		"runtimes",
		runtimeInstanceID+"-g"+strconv.FormatUint(generation, 10),
	)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "bootstrap.json"),
		[]byte(`{"secret":"present"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return root
}

func deadlineCleanupCredential(
	t *testing.T,
	identity controlgrpc.RuntimeIdentity,
) controlgrpc.Credential {
	t.Helper()
	credential, err := controlgrpc.NewCredential(identity)
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func deadlineCleanupAuthority(
	identity controlgrpc.RuntimeIdentity,
) workerapp.BackendDeadlineSourceAuthority {
	return workerapp.BackendDeadlineSourceAuthority{
		WorkerSessionID:   identity.WorkerSessionID,
		RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration),
		ChannelEpoch:      identity.ChannelEpoch,
	}
}

func assertDeadlineCleanupPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Runtime secret root remains at %q: %v", path, err)
	}
}
