package localproduct

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// workspaceSourcePlan bounds logical copy inputs. It is neither an atomic
// filesystem snapshot nor a physical storage reservation or execution grant.
// Only the root stays open; per-entry descriptors are closed as they are read.
type workspaceSourcePlan struct {
	root                             *os.Root
	path                             string
	identity                         os.FileInfo
	entries                          []workspaceSourceEntry
	files, directories, logicalBytes int64
	digest                           string
}

type workspaceSourceEntry struct {
	relative string
	info     os.FileInfo
	digest   string
}

// Binding converts already planned facts without reading the source again. The
// origin identifies the configured resolved root, not an immutable snapshot.
func (plan *workspaceSourcePlan) Binding() (workerapp.RuntimeSourcePlanBinding, error) {
	if plan == nil {
		return workerapp.RuntimeSourcePlanBinding{}, fmt.Errorf("Workspace source plan is required")
	}
	entries := make([]workerapp.RuntimeSourcePlanEntry, 0, len(plan.entries))
	for _, entry := range plan.entries {
		if entry.info == nil {
			return workerapp.RuntimeSourcePlanBinding{}, fmt.Errorf("Workspace source entry metadata is required")
		}
		fact := workerapp.RuntimeSourcePlanEntry{Path: filepath.ToSlash(entry.relative), Mode: uint32(entry.info.Mode().Perm()), Kind: "file", Size: entry.info.Size(), ContentDigest: entry.digest}
		if entry.info.IsDir() {
			fact.Kind, fact.Size, fact.ContentDigest = "directory", 0, ""
		} else if !entry.info.Mode().IsRegular() {
			return workerapp.RuntimeSourcePlanBinding{}, fmt.Errorf("Workspace source contains unsupported file type")
		}
		entries = append(entries, fact)
	}
	origin := sha256.Sum256([]byte("configured_workspace_source_root_v1\x00" + plan.path))
	binding, err := workerapp.NewRuntimeSourcePlanBinding("sha256:"+hex.EncodeToString(origin[:]), entries)
	if err != nil {
		return workerapp.RuntimeSourcePlanBinding{}, err
	}
	stats, err := binding.Stats()
	if err != nil {
		return workerapp.RuntimeSourcePlanBinding{}, err
	}
	legacy, err := binding.LegacyFileDigest()
	if err != nil {
		return workerapp.RuntimeSourcePlanBinding{}, err
	}
	if stats.Files != plan.files || stats.Directories != plan.directories || stats.LogicalBytes != plan.logicalBytes || legacy != plan.digest {
		return workerapp.RuntimeSourcePlanBinding{}, fmt.Errorf("Workspace source plan statistics or legacy digest mismatch")
	}
	return binding, nil
}

func planWorkspaceSource(ctx context.Context, sourceRoot string) (_ *workspaceSourcePlan, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, info, path, err := openWorkspaceDirectory("Workspace source root", sourceRoot)
	if err != nil {
		return nil, err
	}
	plan := &workspaceSourcePlan{root: root, path: path, identity: info}
	defer func() {
		if resultErr != nil {
			_ = plan.Close()
		}
	}()
	err = walkWorkspaceSource(ctx, root, func(relative string, info os.FileInfo) error {
		entry := workspaceSourceEntry{relative: relative, info: info}
		if info.IsDir() {
			if err := addWorkspaceSourceCount(&plan.directories, 1); err != nil {
				return err
			}
		} else {
			if err := addWorkspaceSourceCount(&plan.files, 1); err != nil {
				return err
			}
			if err := addWorkspaceSourceCount(&plan.logicalBytes, info.Size()); err != nil {
				return err
			}
			checksum := sha256.New()
			if err := readWorkspaceSourceFile(ctx, root, entry, checksum); err != nil {
				return err
			}
			entry.digest = hex.EncodeToString(checksum.Sum(nil))
		}
		plan.entries = append(plan.entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(plan.entries, func(i, j int) bool { return plan.entries[i].relative < plan.entries[j].relative })
	if err := plan.validate(ctx); err != nil {
		return nil, err
	}
	tree := sha256.New()
	for _, entry := range plan.entries {
		if entry.info.Mode().IsRegular() {
			_, _ = fmt.Fprintf(tree, "%s\x00%o\x00%s\n", filepath.ToSlash(entry.relative), entry.info.Mode().Perm(), entry.digest)
		}
	}
	plan.digest = hex.EncodeToString(tree.Sum(nil))
	return plan, nil
}

func addWorkspaceSourceCount(total *int64, amount int64) error {
	if amount < 0 || *total > math.MaxInt64-amount {
		return fmt.Errorf("Workspace source statistics overflow")
	}
	*total += amount
	return nil
}

func (plan *workspaceSourcePlan) Close() error {
	if plan == nil || plan.root == nil {
		return nil
	}
	return plan.root.Close()
}

func openWorkspaceDirectory(name, path string) (*os.Root, os.FileInfo, string, error) {
	if err := absolutePath(name, path); err != nil {
		return nil, nil, "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, "", fmt.Errorf("%s must be a real directory", name)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve %s: %w", name, err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open %s: %w", name, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, nil, "", fmt.Errorf("%s changed while opening", name)
	}
	return root, info, resolved, nil
}

// Read directory entries incrementally so validation stops at the first unknown
// path, rather than accumulating an arbitrarily enlarged directory listing.
func walkWorkspaceSource(ctx context.Context, root *os.Root, visit func(string, os.FileInfo) error) error {
	var walk func(string) error
	walk = func(directory string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, err := root.Lstat(directory)
		if err != nil || !before.IsDir() {
			return fmt.Errorf("Workspace source directory changed")
		}
		input, err := root.OpenFile(directory, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("open Workspace source directory: %w", err)
		}
		defer input.Close()
		opened, err := input.Stat()
		if err != nil || !sameWorkspaceSourceInfo(before, opened) {
			return fmt.Errorf("Workspace source directory changed")
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			names, readErr := input.Readdirnames(1)
			for _, name := range names {
				relative := filepath.Join(directory, name)
				info, err := root.Lstat(relative)
				if err != nil {
					return fmt.Errorf("inspect Workspace source entry: %w", err)
				}
				if !info.IsDir() && !info.Mode().IsRegular() {
					return fmt.Errorf("Workspace source contains unsupported file type")
				}
				if err := visit(relative, info); err != nil {
					return err
				}
				if info.IsDir() {
					if err := walk(relative); err != nil {
						return err
					}
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return fmt.Errorf("read Workspace source directory: %w", readErr)
			}
		}
		after, err := root.Lstat(directory)
		if err != nil || !sameWorkspaceSourceInfo(before, after) {
			return fmt.Errorf("Workspace source directory changed")
		}
		return nil
	}
	return walk(".")
}

func sameWorkspaceSourceInfo(expected, current os.FileInfo) bool {
	return expected != nil && current != nil && os.SameFile(expected, current) && expected.Mode() == current.Mode() &&
		(expected.IsDir() || expected.Size() == current.Size() && expected.ModTime().Equal(current.ModTime()))
}

func (plan *workspaceSourcePlan) validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The descriptor pins the original root, but a renamed/replaced configured
	// path would invalidate the path-based overlap check used by Copy.
	named, err := os.Lstat(plan.root.Name())
	if err != nil || !sameWorkspaceSourceInfo(plan.identity, named) {
		return fmt.Errorf("Workspace source root path changed")
	}
	current, err := plan.root.Stat(".")
	if err != nil || !sameWorkspaceSourceInfo(plan.identity, current) {
		return fmt.Errorf("Workspace source root changed")
	}
	expected := make(map[string]os.FileInfo, len(plan.entries))
	for _, entry := range plan.entries {
		expected[entry.relative] = entry.info
	}
	err = walkWorkspaceSource(ctx, plan.root, func(relative string, info os.FileInfo) error {
		original, ok := expected[relative]
		if !ok || !sameWorkspaceSourceInfo(original, info) {
			return fmt.Errorf("Workspace source structure or metadata changed")
		}
		delete(expected, relative)
		return nil
	})
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("Workspace source entry disappeared")
	}
	return ctx.Err()
}

type workspaceSourceReader struct {
	ctx   context.Context
	input io.Reader
}

func (reader workspaceSourceReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.input.Read(buffer)
}

// CopyN never writes more than the planned size. The extra byte is a read-only
// growth probe and is never passed to writer.
func readWorkspaceSourceFile(ctx context.Context, root *os.Root, entry workspaceSourceEntry, writer io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := root.Lstat(entry.relative)
	if err != nil || !sameWorkspaceSourceInfo(entry.info, before) || !before.Mode().IsRegular() {
		return fmt.Errorf("Workspace source file changed")
	}
	input, err := root.OpenFile(entry.relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open Workspace source file: %w", err)
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !sameWorkspaceSourceInfo(entry.info, opened) {
		return fmt.Errorf("Workspace source file changed")
	}
	reader := workspaceSourceReader{ctx: ctx, input: input}
	written, err := io.CopyN(writer, reader, entry.info.Size())
	if err != nil {
		return fmt.Errorf("read Workspace source file: %w", err)
	}
	if written != entry.info.Size() {
		return fmt.Errorf("Workspace source file size changed")
	}
	var extra [1]byte
	count, err := reader.Read(extra[:])
	if err != io.EOF || count != 0 {
		if err != nil {
			return fmt.Errorf("Workspace source file changed: %w", err)
		}
		return fmt.Errorf("Workspace source file grew")
	}
	after, err := input.Stat()
	if err != nil || !sameWorkspaceSourceInfo(entry.info, after) {
		return fmt.Errorf("Workspace source file changed")
	}
	pathInfo, err := root.Lstat(entry.relative)
	if err != nil || !sameWorkspaceSourceInfo(entry.info, pathInfo) {
		return fmt.Errorf("Workspace source file path changed")
	}
	return ctx.Err()
}

func workspacePathsOverlap(first, second string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	}
	return contains(first, second) || contains(second, first)
}

// workspaceDestination tracks identities, not an fd for every created directory.
// Each child operation opens a short-lived chain of checked parent capabilities;
// renaming a pathname cannot redirect IO through a different parent identity.
// Copy requires a fresh Backend-private root until it returns. This excludes
// Worker writers during mkdir-to-first-identity capture; it does not isolate
// malicious Backend-UID writers or host administrators, or prevent mounts.
type workspaceDestination struct {
	root     *os.Root
	identity os.FileInfo
	entries  map[string]os.FileInfo
}

func sameWorkspaceDestinationInfo(expected, current os.FileInfo) bool {
	return expected != nil && current != nil && os.SameFile(expected, current) && expected.Mode().Type() == current.Mode().Type()
}

func (destination *workspaceDestination) validateRoot() error {
	named, err := os.Lstat(destination.root.Name())
	if err != nil || !sameWorkspaceDestinationInfo(destination.identity, named) {
		return fmt.Errorf("WorkingCopy root path changed")
	}
	opened, err := destination.root.Stat(".")
	if err != nil || !sameWorkspaceDestinationInfo(destination.identity, opened) {
		return fmt.Errorf("WorkingCopy root changed")
	}
	return validateWorkspaceBootstrapRoot(opened)
}

// Permission checks detect contract violations; they do not prove freshness.
// Production freshness and deferred handoff are provided by Materializer.
func validateWorkspaceBootstrapRoot(info os.FileInfo) error {
	stat, _ := info.Sys().(*syscall.Stat_t)
	return validateWorkspaceBootstrapOwner(info.Mode(), stat, os.Geteuid())
}

func validateWorkspaceBootstrapOwner(mode fs.FileMode, stat *syscall.Stat_t, backendUID int) error {
	if backendUID == runtimesecret.AgentUID || stat == nil || stat.Uid != uint32(backendUID) || !mode.IsDir() || mode.Perm() != 0o700 {
		return fmt.Errorf("WorkingCopy bootstrap root must remain Backend-owned 0700 with a distinct Worker UID")
	}
	return nil
}

func (destination *workspaceDestination) openParent(relative string) (_ *os.Root, resultErr error) {
	if err := destination.validateRoot(); err != nil {
		return nil, err
	}
	parent, err := destination.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = parent.Close()
		}
	}()
	directory := filepath.Dir(relative)
	if directory == "." {
		return parent, nil
	}
	prefix := ""
	for _, name := range strings.Split(directory, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, name)
		expected := destination.entries[prefix]
		info, err := parent.Lstat(name)
		if err != nil || expected == nil || !expected.IsDir() || !sameWorkspaceDestinationInfo(expected, info) {
			return nil, fmt.Errorf("WorkingCopy parent directory changed")
		}
		next, err := parent.OpenRoot(name)
		if err != nil {
			return nil, fmt.Errorf("open WorkingCopy parent: %w", err)
		}
		opened, statErr := next.Stat(".")
		if statErr != nil || !sameWorkspaceDestinationInfo(expected, opened) {
			_ = next.Close()
			return nil, fmt.Errorf("WorkingCopy parent identity changed")
		}
		closeErr := parent.Close()
		parent = next
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return parent, nil
}

func (destination *workspaceDestination) validate(ctx context.Context) error {
	if err := destination.validateRoot(); err != nil {
		return err
	}
	for relative, expected := range destination.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		parent, err := destination.openParent(relative)
		if err != nil {
			return err
		}
		current, statErr := parent.Lstat(filepath.Base(relative))
		closeErr := parent.Close()
		if statErr != nil || !sameWorkspaceDestinationInfo(expected, current) {
			return errors.Join(fmt.Errorf("WorkingCopy entry changed"), closeErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return ctx.Err()
}

func (destination *workspaceDestination) createDirectory(parent *os.Root, relative string, chown func(*os.File, int, int) error) (resultErr error) {
	name := filepath.Base(relative)
	if err := parent.Mkdir(name, 0o750); err != nil {
		return fmt.Errorf("create WorkingCopy directory: %w", err)
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("WorkingCopy directory changed")
	}
	directory, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open WorkingCopy directory: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, directory.Close()) }()
	opened, err := directory.Stat()
	if err != nil || !sameWorkspaceDestinationInfo(info, opened) {
		return fmt.Errorf("WorkingCopy directory identity changed")
	}
	destination.entries[relative] = opened
	return chown(directory, runtimesecret.AgentUID, runtimesecret.AgentUID)
}

// Copy initializes a fresh, unexposed Backend-owned 0700 root synchronously.
// The caller must keep it inaccessible to Worker writers until Copy returns;
// owner/mode validation is defensive and does not establish that provenance.
func (plan *workspaceSourcePlan) Copy(ctx context.Context, destinationRoot string, chown func(*os.File, int, int) error) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if chown == nil {
		return "", fmt.Errorf("WorkingCopy ownership operation is required")
	}
	root, info, path, err := openWorkspaceDirectory("WorkingCopy root", destinationRoot)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := validateWorkspaceBootstrapRoot(info); err != nil {
		return "", err
	}
	if os.SameFile(plan.identity, info) || workspacePathsOverlap(plan.path, path) {
		return "", fmt.Errorf("Workspace source and WorkingCopy roots overlap")
	}
	if err := plan.validate(ctx); err != nil {
		return "", err
	}
	destination := workspaceDestination{root: root, identity: info, entries: make(map[string]os.FileInfo, len(plan.entries))}
	for _, entry := range plan.entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		parent, err := destination.openParent(entry.relative)
		if err != nil {
			return "", err
		}
		if entry.info.IsDir() {
			err = destination.createDirectory(parent, entry.relative, chown)
		} else {
			err = plan.copyFile(ctx, parent, entry, chown, destination.entries)
		}
		if err := errors.Join(err, parent.Close()); err != nil {
			return "", err
		}
	}
	if err := plan.validate(ctx); err != nil {
		return "", err
	}
	if err := destination.validate(ctx); err != nil {
		return "", err
	}
	return plan.digest, nil
}

func (plan *workspaceSourcePlan) copyFile(ctx context.Context, parent *os.Root, entry workspaceSourceEntry, chown func(*os.File, int, int) error, identities map[string]os.FileInfo) (resultErr error) {
	mode := fs.FileMode(0o640)
	if entry.info.Mode()&0o111 != 0 {
		mode = 0o750
	}
	name := filepath.Base(entry.relative)
	output, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create WorkingCopy file: %w", err)
	}
	// Close on every path, including read/hash/ownership errors. Cleanup uses the
	// same pinned parent as creation, never the potentially redirected pathname.
	defer func() {
		resultErr = errors.Join(resultErr, output.Close())
		if resultErr != nil {
			_ = parent.Remove(name)
		}
	}()
	checksum := sha256.New()
	if err := readWorkspaceSourceFile(ctx, plan.root, entry, io.MultiWriter(output, checksum)); err != nil {
		return err
	}
	if hex.EncodeToString(checksum.Sum(nil)) != entry.digest {
		return fmt.Errorf("Workspace source content changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := chown(output, runtimesecret.AgentUID, runtimesecret.AgentUID); err != nil {
		return fmt.Errorf("handoff WorkingCopy file: %w", err)
	}
	info, err := output.Stat()
	if err != nil {
		return err
	}
	identities[entry.relative] = info
	return ctx.Err()
}

func copyWorkspaceSource(sourceRoot, destinationRoot string, chown func(*os.File, int, int) error) (string, error) {
	plan, err := planWorkspaceSource(context.Background(), sourceRoot)
	if err != nil {
		return "", err
	}
	defer plan.Close()
	return plan.Copy(context.Background(), destinationRoot, chown)
}

func workspaceSourceDigest(sourceRoot string) (string, error) {
	plan, err := planWorkspaceSource(context.Background(), sourceRoot)
	if err != nil {
		return "", err
	}
	defer plan.Close()
	return plan.digest, nil
}
