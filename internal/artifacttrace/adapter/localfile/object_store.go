// Package localfile implements the M1 filesystem-backed ObjectStore.
package localfile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type ObjectStore struct {
	root string
}

func NewObjectStore(root string) (*ObjectStore, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("ObjectStore root must be a clean absolute path")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect ObjectStore root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("ObjectStore root must be a real directory")
	}
	store := &ObjectStore{root: root}
	if _, err := store.ensureDirectory([]string{"temporary"}); err != nil {
		return nil, err
	}
	if _, err := store.ensureDirectory([]string{"objects"}); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *ObjectStore) PutTemporary(
	ctx context.Context,
	identity application.ObjectIdentity,
	reader io.Reader,
) (result application.TemporaryObject, resultErr error) {
	if reader == nil {
		return application.TemporaryObject{}, fmt.Errorf("ObjectStore reader is required")
	}
	segments, err := temporaryScope(identity)
	if err != nil {
		return application.TemporaryObject{}, err
	}
	directory, err := store.ensureDirectory(segments)
	if err != nil {
		return application.TemporaryObject{}, err
	}
	uploadID, err := randomID()
	if err != nil {
		return application.TemporaryObject{}, err
	}
	partialPath := filepath.Join(directory, "."+uploadID+".partial")
	uploadPath := filepath.Join(directory, uploadID+".upload")
	file, err := os.OpenFile(partialPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return application.TemporaryObject{}, fmt.Errorf("create temporary ObjectStore upload: %w", err)
	}
	removePartial := true
	defer func() {
		_ = file.Close()
		if removePartial {
			_ = os.Remove(partialPath)
		}
	}()
	hash := sha256.New()
	size, err := copyWithContext(ctx, io.MultiWriter(file, hash), reader)
	if err != nil {
		return application.TemporaryObject{}, fmt.Errorf("write temporary ObjectStore upload: %w", err)
	}
	if err := file.Sync(); err != nil {
		return application.TemporaryObject{}, fmt.Errorf("sync temporary ObjectStore upload: %w", err)
	}
	if err := file.Close(); err != nil {
		return application.TemporaryObject{}, fmt.Errorf("close temporary ObjectStore upload: %w", err)
	}
	if err := os.Rename(partialPath, uploadPath); err != nil {
		return application.TemporaryObject{}, fmt.Errorf("publish temporary ObjectStore upload: %w", err)
	}
	removePartial = false
	if err := syncDirectory(directory); err != nil {
		return application.TemporaryObject{}, err
	}
	return application.TemporaryObject{
		UploadID: uploadID,
		Size:     size,
		Checksum: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func (store *ObjectStore) Finalize(
	ctx context.Context,
	identity application.ObjectIdentity,
	temporary application.TemporaryObject,
) (application.StoredObject, error) {
	segments, err := temporaryScope(identity)
	if err != nil {
		return application.StoredObject{}, err
	}
	if !validHexID(temporary.UploadID, 64) || temporary.Size < 0 || !validChecksum(temporary.Checksum) {
		return application.StoredObject{}, fmt.Errorf("temporary ObjectStore identity is invalid")
	}
	keySegments, err := objectKeySegments(identity, temporary.Checksum)
	if err != nil {
		return application.StoredObject{}, err
	}
	if finalPath, existingErr := store.existingFile(keySegments); existingErr == nil {
		existingSize, existingChecksum, verifyErr := checksumFile(ctx, finalPath)
		if verifyErr != nil {
			return application.StoredObject{}, fmt.Errorf("verify existing ObjectStore content: %w", verifyErr)
		}
		if existingSize != temporary.Size || existingChecksum != temporary.Checksum {
			return application.StoredObject{}, fmt.Errorf("immutable ObjectStore key conflict: %w", application.ErrObjectIntegrity)
		}
		if err := store.removeFinalizedTemporary(segments, temporary.UploadID); err != nil {
			return application.StoredObject{}, err
		}
		return application.StoredObject{
			Key: strings.Join(keySegments, "/"), Size: existingSize, Checksum: existingChecksum,
		}, nil
	} else if !errors.Is(existingErr, os.ErrNotExist) {
		return application.StoredObject{}, existingErr
	}
	temporaryDirectory, err := store.existingDirectory(segments)
	if err != nil {
		return application.StoredObject{}, err
	}
	temporaryPath := filepath.Join(temporaryDirectory, temporary.UploadID+".upload")
	actualSize, actualChecksum, err := checksumFile(ctx, temporaryPath)
	if err != nil {
		return application.StoredObject{}, fmt.Errorf("verify temporary ObjectStore upload: %w", err)
	}
	if actualSize != temporary.Size || actualChecksum != temporary.Checksum {
		return application.StoredObject{}, fmt.Errorf("temporary ObjectStore checksum mismatch: %w", application.ErrObjectIntegrity)
	}
	finalDirectory, err := store.ensureDirectory(keySegments[:len(keySegments)-1])
	if err != nil {
		return application.StoredObject{}, err
	}
	finalPath := filepath.Join(finalDirectory, keySegments[len(keySegments)-1])
	if err := os.Link(temporaryPath, finalPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return application.StoredObject{}, fmt.Errorf("atomically finalize ObjectStore content: %w", err)
		}
		existingSize, existingChecksum, verifyErr := checksumFile(ctx, finalPath)
		if verifyErr != nil {
			return application.StoredObject{}, fmt.Errorf("verify existing ObjectStore content: %w", verifyErr)
		}
		if existingSize != actualSize || existingChecksum != actualChecksum {
			return application.StoredObject{}, fmt.Errorf("immutable ObjectStore key conflict: %w", application.ErrObjectIntegrity)
		}
	} else if err := syncDirectory(finalDirectory); err != nil {
		return application.StoredObject{}, err
	}
	if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return application.StoredObject{}, fmt.Errorf("remove finalized temporary upload: %w", err)
	}
	if err := syncDirectory(temporaryDirectory); err != nil {
		return application.StoredObject{}, err
	}
	return application.StoredObject{
		Key: strings.Join(keySegments, "/"), Size: actualSize, Checksum: actualChecksum,
	}, nil
}

func (store *ObjectStore) removeFinalizedTemporary(segments []string, uploadID string) error {
	temporaryDirectory, err := store.existingDirectory(segments)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	temporaryPath := filepath.Join(temporaryDirectory, uploadID+".upload")
	if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove finalized temporary upload: %w", err)
	}
	if err := syncDirectory(temporaryDirectory); err != nil {
		return err
	}
	return nil
}

func (store *ObjectStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	segments, err := validateObjectKey(key)
	if err != nil {
		return nil, err
	}
	path, err := store.existingFile(segments)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open ObjectStore content: %w", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (store *ObjectStore) Verify(ctx context.Context, key, checksum string) error {
	if !validChecksum(checksum) {
		return fmt.Errorf("ObjectStore checksum is invalid")
	}
	segments, err := validateObjectKey(key)
	if err != nil {
		return err
	}
	path, err := store.existingFile(segments)
	if err != nil {
		return err
	}
	_, actual, err := checksumFile(ctx, path)
	if err != nil {
		return err
	}
	if actual != checksum || segments[len(segments)-1] != strings.TrimPrefix(checksum, "sha256:") {
		return fmt.Errorf("ObjectStore checksum mismatch: %w", application.ErrObjectIntegrity)
	}
	return nil
}

func (store *ObjectStore) ListCandidates(
	ctx context.Context,
	olderThan time.Time,
	limit int,
) ([]artifactapp.ObjectCandidate, error) {
	if olderThan.IsZero() || limit <= 0 {
		return nil, fmt.Errorf("ObjectStore candidate cutoff and limit are required")
	}
	var candidates []artifactapp.ObjectCandidate
	for _, root := range []struct {
		name  string
		state string
	}{
		{name: "temporary", state: artifactapp.ObjectStateTemporary},
		{name: "objects", state: artifactapp.ObjectStateFinalized},
	} {
		walkRoot := filepath.Join(store.root, root.name)
		err := filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("ObjectStore inventory encountered a symlink")
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("ObjectStore inventory encountered a non-regular file")
			}
			relative, err := filepath.Rel(store.root, path)
			if err != nil || !within(store.root, path) {
				return fmt.Errorf("ObjectStore inventory path escapes configured root")
			}
			key := filepath.ToSlash(relative)
			if root.state == artifactapp.ObjectStateTemporary {
				if strings.HasSuffix(key, ".partial") {
					return nil
				}
				if _, err := validateTemporaryKey(key); err != nil {
					return err
				}
			} else if _, err := validateObjectKey(key); err != nil {
				return err
			}
			if isRetainedObservationKey(strings.Split(key, "/")) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.ModTime().After(olderThan) {
				return nil
			}
			size, checksum, err := checksumFile(ctx, path)
			if err != nil {
				return err
			}
			candidates = append(candidates, artifactapp.ObjectCandidate{
				Key: key, State: root.state, Checksum: checksum, Size: size,
				ModifiedAt: info.ModTime().UTC(),
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("inventory %s ObjectStore content: %w", root.name, err)
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].ModifiedAt.Equal(candidates[right].ModifiedAt) {
			return candidates[left].Key < candidates[right].Key
		}
		return candidates[left].ModifiedAt.Before(candidates[right].ModifiedAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func (store *ObjectStore) DeleteCandidate(
	ctx context.Context,
	candidate artifactapp.ObjectCandidate,
) (artifactapp.ObjectDeletion, error) {
	var segments []string
	var err error
	switch candidate.State {
	case artifactapp.ObjectStateTemporary:
		segments, err = validateTemporaryKey(candidate.Key)
	case artifactapp.ObjectStateFinalized:
		segments, err = validateObjectKey(candidate.Key)
	default:
		err = fmt.Errorf("unsupported ObjectStore candidate state")
	}
	if err != nil || candidate.Size < 0 || !validChecksum(candidate.Checksum) {
		return artifactapp.ObjectDeletion{}, fmt.Errorf("ObjectStore deletion candidate is invalid")
	}
	if isRetainedObservationKey(segments) {
		return artifactapp.ObjectDeletion{}, errors.New("observation_fragment_retained")
	}
	path, err := store.existingFile(segments)
	if errors.Is(err, os.ErrNotExist) {
		return artifactapp.ObjectDeletion{AlreadyAbsent: true}, nil
	}
	if err != nil {
		return artifactapp.ObjectDeletion{}, err
	}
	size, checksum, err := checksumFile(ctx, path)
	if err != nil {
		return artifactapp.ObjectDeletion{}, fmt.Errorf("verify ObjectStore deletion candidate: %w", err)
	}
	if size != candidate.Size || checksum != candidate.Checksum {
		return artifactapp.ObjectDeletion{}, fmt.Errorf("ObjectStore deletion candidate changed after inventory")
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return artifactapp.ObjectDeletion{AlreadyAbsent: true}, nil
		}
		return artifactapp.ObjectDeletion{}, fmt.Errorf("delete ObjectStore candidate: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return artifactapp.ObjectDeletion{}, err
	}
	return artifactapp.ObjectDeletion{Deleted: true}, nil
}

// Call only after validateObjectKey or validateTemporaryKey succeeds.
func isRetainedObservationKey(segments []string) bool {
	return len(segments) >= 14 && segments[13] == artifactapp.ObservationFragmentObjectKind
}

func temporaryScope(identity application.ObjectIdentity) ([]string, error) {
	values := []string{
		identity.TenantID, identity.UserID, identity.WorkspaceID, identity.WorkerSessionID,
		identity.WorkerRunID, identity.AgentTurnID, identity.Kind,
	}
	for _, value := range values {
		if !validSegment(value) {
			return nil, fmt.Errorf("ObjectStore identity must contain canonical path segments")
		}
	}
	return []string{
		"temporary", "tenants", identity.TenantID, "users", identity.UserID,
		"workspaces", identity.WorkspaceID, "sessions", identity.WorkerSessionID,
		"runs", identity.WorkerRunID, "turns", identity.AgentTurnID, identity.Kind,
	}, nil
}

func objectKeySegments(identity application.ObjectIdentity, checksum string) ([]string, error) {
	scope, err := temporaryScope(identity)
	if err != nil {
		return nil, err
	}
	scope[0] = "objects"
	return append(scope, "sha256", strings.TrimPrefix(checksum, "sha256:")), nil
}

func validateObjectKey(key string) ([]string, error) {
	if key == "" || filepath.IsAbs(key) || filepath.Clean(key) != key || strings.Contains(key, "\\") {
		return nil, fmt.Errorf("ObjectStore key is not canonical")
	}
	segments := strings.Split(key, "/")
	if len(segments) != 16 || segments[0] != "objects" || segments[1] != "tenants" ||
		segments[3] != "users" || segments[5] != "workspaces" || segments[7] != "sessions" ||
		segments[9] != "runs" || segments[11] != "turns" || segments[14] != "sha256" ||
		!validHexID(segments[15], 64) {
		return nil, fmt.Errorf("ObjectStore key has an invalid schema")
	}
	for index, segment := range segments {
		if index == 15 {
			continue
		}
		if !validSegment(segment) {
			return nil, fmt.Errorf("ObjectStore key has an invalid segment")
		}
	}
	return segments, nil
}

func validateTemporaryKey(key string) ([]string, error) {
	if key == "" || filepath.IsAbs(key) || filepath.Clean(key) != key || strings.Contains(key, "\\") {
		return nil, fmt.Errorf("temporary ObjectStore key is not canonical")
	}
	segments := strings.Split(key, "/")
	if len(segments) != 15 || segments[0] != "temporary" || segments[1] != "tenants" ||
		segments[3] != "users" || segments[5] != "workspaces" || segments[7] != "sessions" ||
		segments[9] != "runs" || segments[11] != "turns" ||
		!strings.HasSuffix(segments[14], ".upload") ||
		!validHexID(strings.TrimSuffix(segments[14], ".upload"), 64) {
		return nil, fmt.Errorf("temporary ObjectStore key has an invalid schema")
	}
	for index, segment := range segments {
		if index == 14 {
			continue
		}
		if !validSegment(segment) {
			return nil, fmt.Errorf("temporary ObjectStore key has an invalid segment")
		}
	}
	return segments, nil
}

func (store *ObjectStore) ensureDirectory(segments []string) (string, error) {
	current := store.root
	for _, segment := range segments {
		if !validSegment(segment) {
			return "", fmt.Errorf("ObjectStore directory segment is invalid")
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return "", fmt.Errorf("create ObjectStore directory: %w", err)
			}
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect ObjectStore directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("ObjectStore path prefix is not a real directory")
		}
	}
	if !within(store.root, current) {
		return "", fmt.Errorf("ObjectStore directory escapes configured root")
	}
	return current, nil
}

func (store *ObjectStore) existingDirectory(segments []string) (string, error) {
	current := store.root
	for _, segment := range segments {
		if !validSegment(segment) {
			return "", fmt.Errorf("ObjectStore directory segment is invalid")
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("inspect ObjectStore directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("ObjectStore path prefix is not a real directory")
		}
	}
	if !within(store.root, current) {
		return "", fmt.Errorf("ObjectStore directory escapes configured root")
	}
	return current, nil
}

func (store *ObjectStore) existingFile(segments []string) (string, error) {
	directory, err := store.existingDirectory(segments[:len(segments)-1])
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, segments[len(segments)-1])
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect ObjectStore content: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("ObjectStore content is not a regular file")
	}
	return path, nil
}

func checksumFile(ctx context.Context, path string) (int64, string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	hash := sha256.New()
	size, err := copyWithContext(ctx, hash, file)
	if err != nil {
		return 0, "", err
	}
	return size, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func copyWithContext(ctx context.Context, writer io.Writer, reader io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			written, writeErr := writer.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func randomID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate ObjectStore upload id: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func validChecksum(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validHexID(strings.TrimPrefix(value, "sha256:"), 64)
}

func validHexID(value string, length int) bool {
	if len(value) != length {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validSegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 160 || strings.TrimSpace(value) != value {
		return false
	}
	for index, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) ||
			(index > 0 && (character == '-' || character == '_' || character == '.')) {
			continue
		}
		return false
	}
	return true
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open ObjectStore directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync ObjectStore directory: %w", err)
	}
	return nil
}

var _ application.ObjectStore = (*ObjectStore)(nil)
var _ artifactapp.ObjectInventory = (*ObjectStore)(nil)
