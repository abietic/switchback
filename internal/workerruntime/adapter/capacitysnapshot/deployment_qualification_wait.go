package capacitysnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const qualificationWaitLimit = 300 * time.Second
const qualificationWaitPoll = 100 * time.Millisecond

// AwaitDeploymentQualification waits for the final manifest commit under the
// already-safe, fixed directory. Trust is not read before that commit. Existing
// malformed files and any error except the manifest's initial ENOENT fail closed.
// The current process must separately verify its own role before business work.
func AwaitDeploymentQualification(ctx context.Context, manifestPath, trustPath string) (*DeploymentQualification, error) {
	started := time.Now()
	if err := qualificationContext(ctx); err != nil {
		return nil, err
	}
	// The same fixed deadline also bounds the real filesystem reader/Open.
	// Checks below reject late results even before timer cancellation is observed.
	bounded, cancel := context.WithDeadline(ctx, started.Add(qualificationWaitLimit))
	defer cancel()
	return awaitDeploymentQualification(bounded, manifestPath, trustPath, qualificationWaitClock{now: time.Now, pause: qualificationPause}, qualificationWaitFiles{directory: openQualificationDirectory, leaf: readQualificationLeaf, present: qualificationManifestPresent, stat: qualificationStat})
}

type qualificationWaitClock struct {
	now   func() time.Time
	pause func(context.Context, time.Duration) error
}

// Test seams are private, filesystem/clock-only and cannot replace validation.
// frozen is nil in production, so production calls real Open exactly once.
type qualificationWaitFiles struct {
	directory func(context.Context, string) (*os.File, []string, error)
	present   func(context.Context, *os.File) error
	leaf      func(context.Context, *os.File, string) (qualificationFile, error)
	stat      func(context.Context, int) (unix.Stat_t, error)
	frozen    func(context.Context, string) (qualificationFiles, error)
}

func qualificationPause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return qualificationContext(ctx)
	}
}

// Directory content timestamps/size change during manifest-last installation.
// Preserve type, inode/device, owner, mode and link identity while pending. The
// committed full reader/Recheck continue to compare the original full identity.
func qualificationStableDirectory(identity string) string {
	var b strings.Builder
	for _, field := range strings.Split(identity, ";") {
		name, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch name {
		case "Nlink":
			// APFS counts regular directory entries in st_nlink. Installation
			// may change that count; unlinked vs linked remains significant.
			if value != "0" {
				field = "Nlink=linked"
			}
			b.WriteString(field)
			b.WriteByte(';')
		case "Dev", "Ino", "Mode", "Uid", "Gid":
			b.WriteString(field)
			b.WriteByte(';')
		}
	}
	return b.String()
}

func qualificationStableDirectories(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = qualificationStableDirectory(id)
	}
	return out
}

func awaitDeploymentQualification(ctx context.Context, manifestPath, trustPath string, clock qualificationWaitClock, files qualificationWaitFiles) (*DeploymentQualification, error) {
	if err := qualificationContext(ctx); err != nil {
		return nil, err
	}
	if !absolute(manifestPath) || filepath.Base(manifestPath) != "admission-manifest.json" || trustPath != filepath.Join(filepath.Dir(manifestPath), "trust.json") {
		return nil, fmt.Errorf("qualification fixed paths invalid")
	}
	deadline := clock.now().Add(qualificationWaitLimit)
	if outer, ok := ctx.Deadline(); ok && outer.Before(deadline) {
		deadline = outer
	}
	timeout := fmt.Errorf("qualification commit wait timed out: %w", context.DeadlineExceeded)
	checkDeadline := func() error {
		if err := qualificationContext(ctx); err != nil {
			return err
		}
		if !clock.now().Before(deadline) {
			return timeout
		}
		return nil
	}
	held, ids, err := files.directory(ctx, filepath.Dir(manifestPath))
	if err != nil {
		return nil, err
	}
	defer held.Close()
	if err := checkDeadline(); err != nil {
		return nil, err
	}
	stable := qualificationStableDirectories(ids)
	if len(stable) == 0 {
		return nil, fmt.Errorf("qualification directory identity missing")
	}
	checkParent := func() error {
		if err := checkDeadline(); err != nil {
			return err
		}
		s, err := files.stat(ctx, int(held.Fd()))
		if err != nil {
			return err
		}
		if err := checkDeadline(); err != nil {
			return err
		}
		if qualificationStableDirectory(qualificationIdentity(s)) != stable[len(stable)-1] {
			return fmt.Errorf("qualification waiting parent changed")
		}
		fresh, current, err := files.directory(ctx, filepath.Dir(manifestPath))
		if err != nil {
			return err
		}
		defer fresh.Close()
		if err := checkDeadline(); err != nil {
			return err
		}
		if !reflect.DeepEqual(stable, qualificationStableDirectories(current)) {
			return fmt.Errorf("qualification waiting path replaced")
		}
		return checkDeadline()
	}
	for {
		if err := checkDeadline(); err != nil {
			return nil, err
		}
		if err := checkParent(); err != nil {
			return nil, err
		}
		err = files.present(ctx, held)
		if elapsed := checkDeadline(); elapsed != nil {
			return nil, elapsed
		}
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			if err := checkParent(); err != nil {
				return nil, err
			}
			remaining := deadline.Sub(clock.now())
			if remaining <= 0 {
				return nil, timeout
			}
			// 100ms is the fixed cadence, never a new wait budget. Charge
			// lookup/identity work and clip only the final sleep to remaining.
			if err := clock.pause(ctx, min(qualificationWaitPoll, remaining)); err != nil {
				return nil, err
			}
			if err := checkDeadline(); err != nil {
				return nil, err
			}
			continue
		}
		manifest, err := files.leaf(ctx, held, "admission-manifest.json")
		if err != nil {
			return nil, err
		}
		if err := checkDeadline(); err != nil {
			return nil, err
		}
		if err := checkParent(); err != nil {
			return nil, err
		}
		// The committed sibling uses the stronger qualification leaf reader,
		// rather than ReadTrust's more permissive historical configuration read.
		trustFile, err := files.leaf(ctx, held, "trust.json")
		if err != nil {
			return nil, err
		}
		if err := checkDeadline(); err != nil {
			return nil, err
		}
		var trust Trust
		if strictJSON(trustFile.data, &trust) != nil || ValidateTrust(trust) != nil || trust.ManifestDigest != Digest(manifest.data) {
			return nil, errQualification
		}
		if err := checkDeadline(); err != nil {
			return nil, err
		}
		if err := checkParent(); err != nil {
			return nil, err
		}
		var q *DeploymentQualification
		if files.frozen == nil {
			q, err = OpenDeploymentQualification(ctx, manifestPath, trust)
		} else {
			// The test-only file seam still runs the real full validator. Its
			// reads obey the same synthetic clock as all other file operations.
			readFrozen := func(ctx context.Context, path string) (qualificationFiles, error) {
				if err := checkDeadline(); err != nil {
					return qualificationFiles{}, err
				}
				current, err := files.frozen(ctx, path)
				if err != nil {
					return qualificationFiles{}, err
				}
				if err := checkDeadline(); err != nil {
					return qualificationFiles{}, err
				}
				return current, nil
			}
			q, err = openDeploymentQualificationWithFiles(ctx, manifestPath, trust, readFrozen)
		}
		if err != nil {
			return nil, err
		}
		if err := checkDeadline(); err != nil {
			return nil, err
		}
		if err := checkParent(); err != nil {
			return nil, err
		}
		if len(q.files.directories) < len(stable) || !reflect.DeepEqual(stable, qualificationStableDirectories(q.files.directories[:len(stable)])) {
			return nil, fmt.Errorf("qualification committed parent replaced")
		}
		for name, before := range map[string]qualificationFile{"admission-manifest.json": manifest, "trust.json": trustFile} {
			after, ok := q.files.leaves[name]
			if !ok || before.identity != after.identity || !bytes.Equal(before.data, after.data) {
				return nil, fmt.Errorf("qualification commit changed")
			}
		}
		if err := checkDeadline(); err != nil {
			return nil, err
		}
		return q, nil
	}
}

// Only this initial no-follow lookup can produce a pending ENOENT. A leaf
// disappearing after it was observed is an invalid committed file, never a retry.
func qualificationManifestPresent(ctx context.Context, dir *os.File) error {
	if err := qualificationContext(ctx); err != nil {
		return err
	}
	var s unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), "admission-manifest.json", &s, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	return qualificationContext(ctx)
}
