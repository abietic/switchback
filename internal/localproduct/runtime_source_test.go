package localproduct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type sourcePreflightJournal struct {
	deadlineCleanupJournal
	binds int
}

func (j *sourcePreflightJournal) BindRuntimeControlChannel(ctx context.Context, record workerapp.BindRuntimeControlChannelRecord) (workerapp.RuntimeControlChannelState, error) {
	j.binds++
	return j.deadlineCleanupJournal.BindRuntimeControlChannel(ctx, record)
}

func TestRuntimeEnvironmentSourcePreflight(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "symlink", true: "cancelled"}[cancelled], func(t *testing.T) {
			journal := &sourcePreflightJournal{}
			environment := &runtimeEnvironment{hub: controlgrpc.NewHubWithJournal(workerapp.RuntimeControlJournalService{Store: journal, Now: time.Now}), storage: localstorage.Materializer{BackendDataRoot: t.TempDir(), EngineDataRoot: filepath.Join(t.TempDir(), "engine"), Ownership: localstorage.OwnershipFunc(func(string, int, int) error { return nil })}, prepared: make(map[string]preparedRuntime)}
			configureCleanupPrepare(t, environment)
			sourceWrite(t, filepath.Join(environment.configuration.WorkspaceSourceRoot, "a.txt"), "normal", 0o644)
			if !cancelled {
				if err := os.Symlink("a.txt", filepath.Join(environment.configuration.WorkspaceSourceRoot, "z-link")); err != nil {
					t.Fatal(err)
				}
			}
			profile, _ := environment.profiles.Agent(config.KimiCodexAnthropicV1)
			network, err := environment.networkProfiles.ResolveForDeployment(config.SingleUserLocalProfileName)
			if err != nil {
				t.Fatal(err)
			}
			request := workerapp.RuntimePreparationRequest{Ownership: workerapp.StartedWorkerRun{WorkerSessionID: "session-preflight", WorkerRunID: "run-preflight", RuntimeInstanceID: "runtime-preflight", RuntimeGeneration: 1, ControlChannelEpoch: 1}, Command: workerapp.StartWorkerRunCommand{AgentProfileID: profile.ID, AgentProfileDigest: profile.Digest, ResourceProfileID: config.LocalStandardV1, NetworkPolicyID: network.ID, NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest, NetworkEnforcementStatus: config.NetworkEnforcementActive}}
			sessions := filepath.Join(environment.storage.BackendDataRoot, "sessions")
			assertSourceAbsent(t, sessions)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			_, err = environment.PrepareRuntime(ctx, request)
			if err == nil {
				t.Error("source preflight accepted invalid input")
			}
			if cancelled && !errors.Is(err, context.Canceled) {
				t.Errorf("cancel error=%v", err)
			}
			assertSourceAbsent(t, sessions)
			if journal.binds != 0 {
				t.Errorf("credential binds=%d", journal.binds)
			}
		})
	}
}

func TestWorkspaceSourceGrowthDuringCopy(t *testing.T) {
	source, destination := t.TempDir(), sourcePrivateDestination(t)
	if err := os.Mkdir(filepath.Join(source, "a-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, filepath.Join(source, "z-file"), "small", 0o644)
	_, err := copyWorkspaceSource(source, destination, func(openedFile *os.File, _, _ int) error {
		path := openedFile.Name()
		if filepath.Base(path) == "a-dir" {
			sourceWrite(t, filepath.Join(source, "z-file"), "small-and-grown", 0o644)
		}
		return nil
	})
	if err == nil {
		t.Error("growing source was accepted")
	}
	info, statErr := os.Lstat(filepath.Join(destination, "z-file"))
	if statErr == nil && info.Size() > int64(len("small")) {
		t.Errorf("wrote %d bytes beyond original size", info.Size())
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}
func sourceWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
func assertSourceAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("path %s exists or stat failed: %v", path, err)
	}
}

func TestWorkspaceSourcePlanCopiesCompatibleDigestAndModes(t *testing.T) {
	source, destination := t.TempDir(), sourcePrivateDestination(t)
	for _, directory := range []string{"nested", "empty"} {
		if err := os.Mkdir(filepath.Join(source, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sourceWrite(t, filepath.Join(source, "nested", "file"), "actual bytes\n", 0o644)
	sourceWrite(t, filepath.Join(source, "empty-file"), "", 0o600)
	sourceWrite(t, filepath.Join(source, "run"), "#!/bin/sh\n", 0o755)
	plan, err := planWorkspaceSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	if plan.files != 3 || plan.directories != 2 || plan.logicalBytes != 23 {
		t.Fatalf("statistics files=%d directories=%d bytes=%d", plan.files, plan.directories, plan.logicalBytes)
	}
	tree := sha256.New()
	for _, file := range []struct {
		path, content string
		mode          os.FileMode
	}{{"empty-file", "", 0o600}, {"nested/file", "actual bytes\n", 0o644}, {"run", "#!/bin/sh\n", 0o755}} {
		checksum := sha256.Sum256([]byte(file.content))
		fmt.Fprintf(tree, "%s\x00%o\x00%s\n", file.path, file.mode, hex.EncodeToString(checksum[:]))
	}
	want := hex.EncodeToString(tree.Sum(nil))
	got, err := plan.Copy(context.Background(), destination, sourceNoopChown)
	if err != nil || got != want {
		t.Fatalf("digest=%s want=%s err=%v", got, want, err)
	}
	helperDigest, err := workspaceSourceDigest(source)
	if err != nil || helperDigest != want {
		t.Fatalf("helper digest=%s want=%s err=%v", helperDigest, want, err)
	}
	for _, entry := range []struct {
		path, content string
		mode          os.FileMode
		directory     bool
	}{{"empty", "", 0o750, true}, {"nested", "", 0o750, true}, {"empty-file", "", 0o640, false}, {"nested/file", "actual bytes\n", 0o640, false}, {"run", "#!/bin/sh\n", 0o750, false}} {
		path := filepath.Join(destination, entry.path)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() != entry.directory || info.Mode().Perm() != entry.mode {
			t.Errorf("path=%s mode=%v", entry.path, info.Mode())
		}
		if !entry.directory {
			content, err := os.ReadFile(path)
			if err != nil || string(content) != entry.content {
				t.Errorf("path=%s content=%q err=%v", entry.path, content, err)
			}
		}
	}
}

func TestWorkspaceSourcePlanRejectsChanges(t *testing.T) {
	for _, change := range []string{"growth", "shrink", "content", "mode", "symlink", "special", "identity", "added-file", "added-directory", "missing-file", "directory-mode", "directory-symlink"} {
		t.Run(change, func(t *testing.T) {
			source, destination := t.TempDir(), sourcePrivateDestination(t)
			file := filepath.Join(source, "z-file")
			sourceWrite(t, file, "original", 0o644)
			if err := os.Mkdir(filepath.Join(source, "a-dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			plan, err := planWorkspaceSource(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			switch change {
			case "growth":
				sourceWrite(t, file, "original-growth", 0o644)
			case "shrink":
				sourceWrite(t, file, "short", 0o644)
			case "content":
				sourceWrite(t, file, "replaced", 0o644)
			case "mode":
				if err := os.Chmod(file, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink", "special", "identity":
				if err := os.Rename(file, filepath.Join(t.TempDir(), "original")); err != nil {
					t.Fatal(err)
				}
				if change == "symlink" {
					err = os.Symlink("a-dir", file)
				} else if change == "special" {
					err = syscall.Mkfifo(file, 0o600)
				} else {
					sourceWrite(t, file, "original", 0o644)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "added-file":
				sourceWrite(t, filepath.Join(source, "new-file"), "new", 0o644)
			case "added-directory":
				if err := os.Mkdir(filepath.Join(source, "new-dir"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "directory-mode":
				if err := os.Chmod(filepath.Join(source, "a-dir"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				if err := os.Remove(filepath.Join(source, "a-dir")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(".", filepath.Join(source, "a-dir")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := plan.Copy(context.Background(), destination, sourceNoopChown); err == nil {
				t.Fatal("changed source accepted")
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("precopy rejection wrote entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestWorkspaceSourcePlanRejectsSameSizeContentDuringCopy(t *testing.T) {
	source, destination := t.TempDir(), sourcePrivateDestination(t)
	if err := os.Mkdir(filepath.Join(source, "a-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, "z-file")
	sourceWrite(t, file, "original", 0o644)
	plan, err := planWorkspaceSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = plan.Copy(context.Background(), destination, func(openedFile *os.File, _, _ int) error {
		path := openedFile.Name()
		if filepath.Base(path) == "a-dir" {
			sourceWrite(t, file, "replaced", 0o644)
			if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("same metadata with changed content was accepted")
	}
	assertSourceAbsent(t, filepath.Join(destination, "z-file"))
}

func TestWorkspaceSourceBoundedReadDetectsGrowthAndCancellation(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "growth", true: "cancel"}[cancelRead], func(t *testing.T) {
			source := t.TempDir()
			file := filepath.Join(source, "file")
			sourceWrite(t, file, "planned", 0o644)
			plan, err := planWorkspaceSource(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var written bytes.Buffer
			writer := sourceWriteFunc(func(p []byte) (int, error) {
				n, err := written.Write(p)
				if cancelRead {
					cancel()
				} else {
					sourceWrite(t, file, "planned-and-grown", 0o644)
				}
				return n, err
			})
			err = readWorkspaceSourceFile(ctx, plan.root, plan.entries[0], writer)
			if err == nil {
				t.Fatal("changing source accepted")
			}
			if cancelRead && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if written.String() != "planned" {
				t.Fatalf("unbounded or altered bytes %q", written.String())
			}
		})
	}
}

type sourceWriteFunc func([]byte) (int, error)

func (f sourceWriteFunc) Write(p []byte) (int, error) { return f(p) }
func sourceNoopChown(*os.File, int, int) error        { return nil }

func TestWorkspaceSourcePlanCancellationStopsFurtherWrites(t *testing.T) {
	for _, afterFile := range []bool{false, true} {
		t.Run(fmt.Sprint(afterFile), func(t *testing.T) {
			source, destination := t.TempDir(), sourcePrivateDestination(t)
			if err := os.Mkdir(filepath.Join(source, "a-dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			sourceWrite(t, filepath.Join(source, "b-file"), "first", 0o644)
			sourceWrite(t, filepath.Join(source, "z-file"), "later", 0o644)
			plan, err := planWorkspaceSource(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			_, err = plan.Copy(ctx, destination, func(openedFile *os.File, _, _ int) error {
				path := openedFile.Name()
				calls++
				if !afterFile || filepath.Base(path) == "b-file" {
					cancel()
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			assertSourceAbsent(t, filepath.Join(destination, "z-file"))
			if !afterFile {
				assertSourceAbsent(t, filepath.Join(destination, "b-file"))
				if calls != 1 {
					t.Fatalf("ownership after cancellation=%d", calls)
				}
			} else {
				assertSourceAbsent(t, filepath.Join(destination, "b-file"))
			}
		})
	}
}

func TestWorkspaceSourcePlanRejectsNewPathDuringCopy(t *testing.T) {
	source, destination := t.TempDir(), sourcePrivateDestination(t)
	if err := os.Mkdir(filepath.Join(source, "a-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, filepath.Join(source, "z-file"), "file", 0o644)
	plan, err := planWorkspaceSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	_, err = plan.Copy(context.Background(), destination, func(openedFile *os.File, _, _ int) error {
		path := openedFile.Name()
		if filepath.Base(path) == "a-dir" {
			sourceWrite(t, filepath.Join(source, "new-file"), "unplanned", 0o644)
		}
		return nil
	})
	if err == nil {
		t.Fatal("new path after validation accepted")
	}
	assertSourceAbsent(t, filepath.Join(destination, "new-file"))
}

func TestWorkspaceSourcePlanDestinationBoundaries(t *testing.T) {
	t.Run("overlap", func(t *testing.T) {
		parent := t.TempDir()
		source := filepath.Join(parent, "source")
		nested := filepath.Join(source, "nested")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		plan, err := planWorkspaceSource(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		defer plan.Close()
		for _, destination := range []string{source, parent, nested} {
			if err := os.Chmod(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := plan.Copy(context.Background(), destination, sourceNoopChown); err == nil || !strings.Contains(err.Error(), "Workspace source and WorkingCopy roots overlap") {
				t.Errorf("expected overlap error for %s: %v", destination, err)
			}
		}
	})
	t.Run("existing", func(t *testing.T) {
		source, destination := t.TempDir(), sourcePrivateDestination(t)
		sourceWrite(t, filepath.Join(source, "file"), "source", 0o644)
		sourceWrite(t, filepath.Join(destination, "file"), "keep", 0o600)
		if _, err := copyWorkspaceSource(source, destination, sourceNoopChown); err == nil {
			t.Fatal("existing file accepted")
		}
		content, err := os.ReadFile(filepath.Join(destination, "file"))
		if err != nil || string(content) != "keep" {
			t.Fatalf("existing file overwritten: %q %v", content, err)
		}
	})
	t.Run("symlink-root", func(t *testing.T) {
		source := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		if _, err := copyWorkspaceSource(source, link, sourceNoopChown); err == nil {
			t.Fatal("destination symlink accepted")
		}
		if _, err := planWorkspaceSource(context.Background(), link); err == nil {
			t.Fatal("source symlink accepted")
		}
	})
	t.Run("ownership-required", func(t *testing.T) {
		if _, err := copyWorkspaceSource(t.TempDir(), t.TempDir(), nil); err == nil {
			t.Fatal("missing ownership accepted")
		}
	})
	t.Run("canonical", func(t *testing.T) {
		if _, err := planWorkspaceSource(context.Background(), "relative"); err == nil {
			t.Fatal("relative source accepted")
		}
	})
	t.Run("ownership-failure-cleans-file", func(t *testing.T) {
		source, destination := t.TempDir(), sourcePrivateDestination(t)
		sourceWrite(t, filepath.Join(source, "file"), "source", 0o644)
		if _, err := copyWorkspaceSource(source, destination, func(*os.File, int, int) error { return errors.New("controlled ownership failure") }); err == nil {
			t.Fatal("ownership failure ignored")
		}
		assertSourceAbsent(t, filepath.Join(destination, "file"))
	})
}

func TestWorkspaceSourcePlanRejectsSpecialFiles(t *testing.T) {
	source := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(source, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := planWorkspaceSource(context.Background(), source); err == nil {
		t.Fatal("FIFO accepted")
	}

}

func TestWorkspaceSourceStatisticsOverflow(t *testing.T) {
	total := int64(math.MaxInt64)
	if err := addWorkspaceSourceCount(&total, 1); err == nil || total != math.MaxInt64 {
		t.Fatalf("overflow not rejected: %d %v", total, err)
	}
	if err := addWorkspaceSourceCount(&total, -1); err == nil {
		t.Fatal("negative size accepted")
	}
}

func TestWorkspaceSourcePlanRejectsRootReplacement(t *testing.T) {
	source, destination := t.TempDir(), sourcePrivateDestination(t)
	sourceWrite(t, filepath.Join(source, "file"), "original", 0o644)
	plan, err := planWorkspaceSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	// Move the pinned source beneath the destination: stale path comparison alone
	// would miss this overlap even though Root still points at the old directory.
	if err := os.Rename(source, filepath.Join(destination, "moved-source")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, filepath.Join(source, "file"), "replacement", 0o644)
	if _, err := plan.Copy(context.Background(), destination, sourceNoopChown); err == nil {
		t.Fatal("replaced configured root accepted")
	}
	assertSourceAbsent(t, filepath.Join(destination, "file"))
}

func TestWorkspaceSourcePlanConfinesRedirectedPaths(t *testing.T) {
	for _, redirectSource := range []bool{false, true} {
		t.Run(fmt.Sprint(redirectSource), func(t *testing.T) {
			source, destination, outside := t.TempDir(), sourcePrivateDestination(t), t.TempDir()
			if err := os.Mkdir(filepath.Join(source, "a-dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			sourceWrite(t, filepath.Join(source, "a-dir", "file"), "planned", 0o644)
			sourceWrite(t, filepath.Join(outside, "sentinel"), "keep", 0o600)
			plan, err := planWorkspaceSource(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			_, err = plan.Copy(context.Background(), destination, func(openedFile *os.File, _, _ int) error {
				path := openedFile.Name()
				if filepath.Base(path) == "a-dir" {
					redirect := filepath.Join(destination, "a-dir")
					if redirectSource {
						redirect = filepath.Join(source, "a-dir")
					}
					if err := os.Rename(redirect, filepath.Join(t.TempDir(), "old-directory")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, redirect); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("redirected path accepted")
			}
			assertSourceAbsent(t, filepath.Join(outside, "file"))
			content, err := os.ReadFile(filepath.Join(outside, "sentinel"))
			if err != nil || string(content) != "keep" {
				t.Fatalf("outside changed: %q %v", content, err)
			}
		})
	}
}

// These deterministic ownership hooks reproduce destination redirection after
// creation without a privileged chown or a concurrent timing loop.
func TestWorkspaceSourceDestinationOwnershipStaysOnOpenedObject(t *testing.T) {
	for _, target := range []string{"root", "parent", "file"} {
		t.Run(target, func(t *testing.T) {
			source, destination, outside := t.TempDir(), sourcePrivateDestination(t), t.TempDir()
			if err := os.Mkdir(filepath.Join(source, "a-dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			sourceWrite(t, filepath.Join(source, "a-dir", "file"), "planned", 0o644)
			if err := os.Mkdir(filepath.Join(outside, "a-dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			sourceWrite(t, filepath.Join(outside, "a-dir", "file"), "outside", 0o600)
			outsideTarget := filepath.Join(outside, "a-dir")
			trigger := "a-dir"
			if target == "file" {
				outsideTarget = filepath.Join(outsideTarget, "file")
				trigger = "file"
			}
			before, err := os.Stat(outsideTarget)
			if err != nil {
				t.Fatal(err)
			}
			var intended os.FileInfo
			moved := filepath.Join(t.TempDir(), "moved")
			changed := false
			_, err = copyWorkspaceSource(source, destination, func(openedFile *os.File, _, _ int) error {
				path := openedFile.Name()
				if filepath.Base(path) != trigger {
					return nil
				}
				intended, err = openedFile.Stat()
				if err != nil {
					return err
				}
				redirect, replacement := filepath.Join(destination, "a-dir"), filepath.Join(outside, "a-dir")
				if target == "root" {
					redirect, replacement = destination, outside
				}
				if target == "file" {
					redirect, replacement = filepath.Join(destination, "a-dir", "file"), outsideTarget
				}
				if err := os.Rename(redirect, moved); err != nil {
					return err
				}
				if err := os.Symlink(replacement, redirect); err != nil {
					return err
				}
				changed = true
				return openedFile.Chmod(0o710)
			})
			if !changed {
				t.Fatal("redirection hook did not run")
			}
			if err == nil {
				t.Error("redirected destination accepted")
			}
			after, statErr := os.Stat(outsideTarget)
			if statErr != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() {
				t.Errorf("ownership affected outside object: before=%v after=%v err=%v", before.Mode(), after, statErr)
			}
			actualPath := moved
			if target == "root" {
				actualPath = filepath.Join(moved, "a-dir")
			}
			actual, statErr := os.Stat(actualPath)
			if statErr != nil || !os.SameFile(intended, actual) || actual.Mode().Perm() != 0o710 {
				t.Errorf("ownership missed intended opened identity: info=%v err=%v", actual, statErr)
			}
			content, readErr := os.ReadFile(filepath.Join(outside, "a-dir", "file"))
			if readErr != nil || string(content) != "outside" {
				t.Errorf("outside bytes=%q err=%v", content, readErr)
			}
		})
	}
}

func TestWorkspaceSourceDestinationRejectsInternalDirectoryRedirect(t *testing.T) {
	for _, replacement := range []string{"symlink", "directory"} {
		t.Run(replacement, func(t *testing.T) {
			source, destination := t.TempDir(), sourcePrivateDestination(t)
			if err := os.Mkdir(filepath.Join(source, "a-dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			sourceWrite(t, filepath.Join(source, "a-dir", "file"), "planned", 0o644)
			spare := filepath.Join(destination, "spare")
			if err := os.Mkdir(spare, 0o755); err != nil {
				t.Fatal(err)
			}
			sourceWrite(t, filepath.Join(spare, "sentinel"), "keep", 0o600)
			moved := filepath.Join(t.TempDir(), "original")
			var original os.FileInfo
			_, err := copyWorkspaceSource(source, destination, func(openedFile *os.File, _, _ int) error {
				path := openedFile.Name()
				if filepath.Base(path) != "a-dir" {
					return nil
				}
				var err error
				original, err = openedFile.Stat()
				if err != nil {
					return err
				}
				if err := os.Rename(filepath.Join(destination, "a-dir"), moved); err != nil {
					return err
				}
				if replacement == "symlink" {
					return os.Symlink("spare", filepath.Join(destination, "a-dir"))
				}
				return os.Rename(spare, filepath.Join(destination, "a-dir"))
			})
			if err == nil {
				t.Error("replaced destination directory accepted")
			}
			actual, statErr := os.Stat(moved)
			if statErr != nil || !os.SameFile(original, actual) {
				t.Fatalf("original identity lost: %v", statErr)
			}
			assertSourceAbsent(t, filepath.Join(moved, "file"))
			if replacement == "directory" {
				spare = filepath.Join(destination, "a-dir")
			}
			assertSourceAbsent(t, filepath.Join(spare, "file"))
			content, readErr := os.ReadFile(filepath.Join(spare, "sentinel"))
			if readErr != nil || string(content) != "keep" {
				t.Errorf("replacement bytes=%q err=%v", content, readErr)
			}
		})
	}
}

func TestWorkspaceSourceDestinationFinalValidation(t *testing.T) {
	for _, target := range []string{"root", "empty-directory"} {
		t.Run(target, func(t *testing.T) {
			source, destination := t.TempDir(), sourcePrivateDestination(t)
			if err := os.Mkdir(filepath.Join(source, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(t.TempDir(), "moved")
			calls := 0
			_, err := copyWorkspaceSource(source, destination, func(*os.File, int, int) error {
				calls++
				redirect := destination
				if target == "empty-directory" {
					redirect = filepath.Join(destination, "empty")
				}
				if err := os.Rename(redirect, moved); err != nil {
					return err
				}
				return os.Mkdir(redirect, 0o750)
			})
			if calls != 1 || err == nil {
				t.Fatalf("last-entry replacement accepted: calls=%d err=%v", calls, err)
			}
			replacement := destination
			if target == "empty-directory" {
				replacement = filepath.Join(destination, "empty")
			}
			before, statErr := os.Stat(moved)
			if statErr != nil {
				t.Fatal(statErr)
			}
			after, statErr := os.Stat(replacement)
			if statErr != nil || os.SameFile(before, after) {
				t.Fatalf("replacement fixture identity: %v", statErr)
			}
		})
	}
}

func TestWorkspaceSourceOwnershipDescriptorLifetime(t *testing.T) {
	source, destination := t.TempDir(), sourcePrivateDestination(t)
	if err := os.Mkdir(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, filepath.Join(source, "nested", "file"), "planned", 0o644)
	var handed []*os.File
	_, err := copyWorkspaceSource(source, destination, func(file *os.File, uid, gid int) error {
		info, err := file.Stat()
		if err != nil {
			return fmt.Errorf("ownership received closed fd: %w", err)
		}
		if uid != runtimesecret.AgentUID || gid != runtimesecret.AgentUID {
			t.Errorf("ownership uid=%d gid=%d", uid, gid)
		}
		if info.Mode().IsRegular() && info.Size() != int64(len("planned")) {
			t.Errorf("ownership before copy: size=%d", info.Size())
		}
		handed = append(handed, file)
		return nil
	})
	if err != nil || len(handed) != 2 {
		t.Fatalf("copy err=%v ownership calls=%d", err, len(handed))
	}
	for _, file := range handed {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("ownership fd not closed after copy: %v", err)
		}
	}
}

func TestWorkspaceSourceOwnershipCloseFailure(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			source, destination := t.TempDir(), sourcePrivateDestination(t)
			path := filepath.Join(source, "entry")
			if directory {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				sourceWrite(t, path, "planned", 0o644)
			}
			_, err := copyWorkspaceSource(source, destination, func(file *os.File, _, _ int) error { return file.Close() })
			if !errors.Is(err, os.ErrClosed) {
				t.Fatalf("close failure ignored: %v", err)
			}
			if !directory {
				assertSourceAbsent(t, filepath.Join(destination, "entry"))
			}
		})
	}
}

func TestWorkspaceSourceF1RejectsNonPrivateRoot(t *testing.T) {
	source, destination := t.TempDir(), sourcePrivateDestination(t)
	sourceWrite(t, filepath.Join(source, "file"), "private input", 0o600)
	if err := os.Chmod(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	calls := 0
	digest, err := copyWorkspaceSource(source, destination, func(*os.File, int, int) error { calls++; return nil })
	if err == nil || digest != "" || calls != 0 {
		t.Errorf("nonprivate root accepted: digest=%q error=%v ownership_calls=%d", digest, err, calls)
	}
	assertSourceAbsent(t, filepath.Join(destination, "file"))
}

func TestWorkspaceSourceF1RejectsPrivateBoundaryLoss(t *testing.T) {
	source, destination := t.TempDir(), sourcePrivateDestination(t)
	sourceWrite(t, filepath.Join(source, "a-file"), "first", 0o600)
	sourceWrite(t, filepath.Join(source, "z-file"), "later", 0o600)
	calls := 0
	digest, err := copyWorkspaceSource(source, destination, func(*os.File, int, int) error { calls++; return os.Chmod(destination, 0o750) })
	if err == nil || digest != "" || calls != 1 {
		t.Fatalf("boundary loss accepted: digest=%q error=%v calls=%d", digest, err, calls)
	}
	assertSourceAbsent(t, filepath.Join(destination, "z-file"))
}

func TestWorkspaceSourceF1ValidatesOwnerFacts(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    os.FileMode
		stat    *syscall.Stat_t
		backend int
		valid   bool
	}{
		{"private-backend", os.ModeDir | 0o700, &syscall.Stat_t{Uid: 501}, 501, true},
		{"worker-owner", os.ModeDir | 0o700, &syscall.Stat_t{Uid: runtimesecret.AgentUID}, 501, false},
		{"foreign-owner", os.ModeDir | 0o700, &syscall.Stat_t{Uid: 502}, 501, false},
		{"same-backend-worker", os.ModeDir | 0o700, &syscall.Stat_t{Uid: runtimesecret.AgentUID}, runtimesecret.AgentUID, false},
		{"nonprivate-mode", os.ModeDir | 0o750, &syscall.Stat_t{Uid: 501}, 501, false},
		{"nondirectory", 0o700, &syscall.Stat_t{Uid: 501}, 501, false},
		{"unknown-owner", os.ModeDir | 0o700, nil, 501, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateWorkspaceBootstrapOwner(test.mode, test.stat, test.backend); (err == nil) != test.valid {
				t.Fatalf("error=%v valid=%v", err, test.valid)
			}
		})
	}
}

func sourcePrivateDestination(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
