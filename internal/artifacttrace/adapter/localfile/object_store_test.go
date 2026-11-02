package localfile_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestObjectStoreScopesAtomicallyFinalizesAndVerifiesContent(t *testing.T) {
	store := newStore(t)
	identity := objectIdentity()
	payload := []byte("trace line one\ntrace line two\n")
	temporary, err := store.PutTemporary(context.Background(), identity, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(temporary.UploadID, "/") || temporary.Size != int64(len(payload)) ||
		temporary.Checksum != "sha256:819477988e16cebcc627a22c35bcfcab7894a82dd62d16cc95dece1112a685a6" {
		t.Fatalf("temporary object=%+v", temporary)
	}
	stored, err := store.Finalize(context.Background(), identity, temporary)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "objects/tenants/tenant-local/users/user-local/workspaces/workspace-demo/" +
		"sessions/session-1/runs/run-1/turns/turn-1/trace-normalized/sha256/"
	if !strings.HasPrefix(stored.Key, wantPrefix) || stored.Size != temporary.Size || stored.Checksum != temporary.Checksum {
		t.Fatalf("stored object=%+v", stored)
	}
	if err := store.Verify(context.Background(), stored.Key, stored.Checksum); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(context.Background(), stored.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("opened payload=%q error=%v", got, err)
	}
}

func TestObservationFragmentRetention(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	identity := objectIdentity()
	identity.Kind = artifactapp.ObservationFragmentObjectKind
	temporary, err := store.PutTemporary(ctx, identity, strings.NewReader("immutable fragment"))
	if err != nil {
		t.Fatal(err)
	}
	assertRetained := func(candidate artifactapp.ObjectCandidate) {
		t.Helper()
		inventory, err := store.ListCandidates(ctx, time.Now().Add(time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range inventory {
			if item.Key == candidate.Key {
				t.Error("retained observation appears in orphan inventory")
			}
		}
		for _, key := range []string{candidate.Key, strings.Replace(candidate.Key, "turn-1", "turn-absent", 1)} {
			candidate.Key = key
			deleted, err := store.DeleteCandidate(ctx, candidate)
			if err == nil || err.Error() != "observation_fragment_retained" || deleted.Deleted || deleted.AlreadyAbsent {
				t.Errorf("retained deletion = %+v, %v", deleted, err)
			}
		}
	}
	temporaryKey := "temporary/tenants/tenant-local/users/user-local/workspaces/workspace-demo/sessions/session-1/runs/run-1/turns/turn-1/observation-fragment/" + temporary.UploadID + ".upload"
	assertRetained(artifactapp.ObjectCandidate{Key: temporaryKey, State: artifactapp.ObjectStateTemporary, Size: temporary.Size, Checksum: temporary.Checksum})
	object, err := store.Finalize(ctx, identity, temporary)
	if err != nil {
		t.Fatal(err)
	}
	assertRetained(artifactapp.ObjectCandidate{Key: temporaryKey, State: artifactapp.ObjectStateTemporary, Size: temporary.Size, Checksum: temporary.Checksum})
	assertRetained(artifactapp.ObjectCandidate{Key: object.Key, State: artifactapp.ObjectStateFinalized, Size: object.Size, Checksum: object.Checksum})
	if err := store.Verify(ctx, object.Key, object.Checksum); err != nil {
		t.Fatal(err)
	}
}

func TestObjectStoreRejectsCrossIdentityFinalizeAndChecksumTampering(t *testing.T) {
	store := newStore(t)
	identity := objectIdentity()
	temporary, err := store.PutTemporary(context.Background(), identity, strings.NewReader("report"))
	if err != nil {
		t.Fatal(err)
	}
	other := identity
	other.WorkspaceID = "workspace-other"
	if _, err := store.Finalize(context.Background(), other, temporary); err == nil {
		t.Fatal("cross-Workspace finalize unexpectedly succeeded")
	}
	tampered := temporary
	tampered.Checksum = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := store.Finalize(context.Background(), identity, tampered); !errors.Is(err, application.ErrObjectIntegrity) {
		t.Fatalf("tampered checksum error = %v, want integrity violation", err)
	}
	stored, err := store.Finalize(context.Background(), identity, temporary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PutTemporary(context.Background(), identity, strings.NewReader("report"))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Finalize(context.Background(), identity, second)
	if err != nil || replayed.Key != stored.Key {
		t.Fatalf("duplicate content replay=%+v error=%v", replayed, err)
	}
}

func TestObjectStoreIntegrityErrorsExcludeReadFailuresAndCancellation(t *testing.T) {
	root := t.TempDir()
	store, err := localfile.NewObjectStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	identity := objectIdentity()
	temporary, err := store.PutTemporary(ctx, identity, strings.NewReader("original bytes"))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Finalize(cancelled, identity, temporary); !errors.Is(err, context.Canceled) || errors.Is(err, application.ErrObjectIntegrity) {
		t.Fatalf("cancelled temporary read: %v", err)
	}
	stored, err := store.Finalize(ctx, identity, temporary)
	if err != nil {
		t.Fatal(err)
	}
	for name, operation := range map[string]func(context.Context) error{
		"existing finalize": func(ctx context.Context) error { _, err := store.Finalize(ctx, identity, temporary); return err },
		"verify":            func(ctx context.Context) error { return store.Verify(ctx, stored.Key, stored.Checksum) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(cancelled); !errors.Is(err, context.Canceled) || errors.Is(err, application.ErrObjectIntegrity) {
				t.Fatalf("cancelled existing content read: %v", err)
			}
		})
	}
	path := filepath.Join(root, filepath.FromSlash(stored.Key))
	t.Run("unreadable existing content", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses file read permissions")
		}
		if err := os.Chmod(path, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0600) })
		_, err := store.Finalize(ctx, identity, temporary)
		if !errors.Is(err, os.ErrPermission) || errors.Is(err, application.ErrObjectIntegrity) {
			t.Fatalf("unreadable existing finalize: %v", err)
		}
		if err := store.Verify(ctx, stored.Key, stored.Checksum); !errors.Is(err, os.ErrPermission) || errors.Is(err, application.ErrObjectIntegrity) {
			t.Fatalf("unreadable existing verify: %v", err)
		}
	})
	if err := os.WriteFile(path, []byte("corrupted bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(ctx, identity, temporary); !errors.Is(err, application.ErrObjectIntegrity) {
		t.Fatalf("existing immutable conflict: %v", err)
	}
	if err := store.Verify(ctx, stored.Key, stored.Checksum); !errors.Is(err, application.ErrObjectIntegrity) {
		t.Fatalf("verified checksum mismatch: %v", err)
	}
}

func TestObjectStoreRejectsTraversalAndSymlinkPrefixes(t *testing.T) {
	root := t.TempDir()
	store, err := localfile.NewObjectStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*application.ObjectIdentity){
		"tenant traversal": func(identity *application.ObjectIdentity) { identity.TenantID = "../escape" },
		"workspace slash":  func(identity *application.ObjectIdentity) { identity.WorkspaceID = "a/b" },
		"absolute kind":    func(identity *application.ObjectIdentity) { identity.Kind = "/tmp/report" },
	} {
		t.Run(name, func(t *testing.T) {
			identity := objectIdentity()
			mutate(&identity)
			if _, err := store.PutTemporary(context.Background(), identity, strings.NewReader("payload")); err == nil {
				t.Fatal("unsafe identity unexpectedly accepted")
			}
		})
	}
	outside := t.TempDir()
	tenants := filepath.Join(root, "temporary", "tenants")
	if err := os.Symlink(outside, tenants); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutTemporary(context.Background(), objectIdentity(), strings.NewReader("payload")); err == nil {
		t.Fatal("symlink storage prefix unexpectedly accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed entries=%v error=%v", entries, err)
	}
}

func TestObjectStoreOpenRejectsNonCanonicalKey(t *testing.T) {
	store := newStore(t)
	for _, key := range []string{"/absolute", "../outside", "objects/../outside", "objects//bad"} {
		if _, err := store.Open(context.Background(), key); err == nil {
			t.Fatalf("unsafe key %q unexpectedly opened", key)
		}
	}
}

func TestObjectStoreListsAndDeletesOnlyStableOrphanCandidates(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	temporary, err := store.PutTemporary(ctx, objectIdentity(), strings.NewReader("temporary orphan"))
	if err != nil {
		t.Fatal(err)
	}
	finalIdentity := objectIdentity()
	finalIdentity.Kind = "report"
	finalTemporary, err := store.PutTemporary(ctx, finalIdentity, strings.NewReader("final orphan"))
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := store.Finalize(ctx, finalIdentity, finalTemporary)
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := store.ListCandidates(ctx, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want temporary and finalized", len(candidates))
	}
	byState := make(map[string]artifactapp.ObjectCandidate, len(candidates))
	for _, candidate := range candidates {
		byState[candidate.State] = candidate
	}
	if byState[artifactapp.ObjectStateTemporary].Checksum != temporary.Checksum ||
		byState[artifactapp.ObjectStateFinalized].Key != finalized.Key {
		t.Fatalf("candidates = %#v", candidates)
	}
	tampered := byState[artifactapp.ObjectStateFinalized]
	tampered.Checksum = "sha256:" + strings.Repeat("f", 64)
	if _, err := store.DeleteCandidate(ctx, tampered); err == nil {
		t.Fatal("candidate with stale checksum unexpectedly deleted")
	}
	for _, candidate := range candidates {
		deleted, err := store.DeleteCandidate(ctx, candidate)
		if err != nil || !deleted.Deleted {
			t.Fatalf("delete candidate %s = %#v error=%v", candidate.Key, deleted, err)
		}
		replayed, err := store.DeleteCandidate(ctx, candidate)
		if err != nil || !replayed.AlreadyAbsent {
			t.Fatalf("replay delete candidate %s = %#v error=%v", candidate.Key, replayed, err)
		}
	}
	if _, err := store.Open(ctx, finalized.Key); err == nil {
		t.Fatal("deleted finalized object remains readable")
	}
	if _, err := store.Finalize(ctx, objectIdentity(), temporary); err == nil {
		t.Fatal("deleted temporary upload remains finalizable")
	}
}

func newStore(t *testing.T) *localfile.ObjectStore {
	t.Helper()
	store, err := localfile.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func objectIdentity() application.ObjectIdentity {
	return application.ObjectIdentity{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1",
		Kind: "trace-normalized",
	}
}
