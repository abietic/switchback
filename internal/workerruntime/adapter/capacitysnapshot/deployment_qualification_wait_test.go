package capacitysnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAwaitDeploymentQualificationCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q, err := AwaitDeploymentQualification(ctx, "/etc/switchback/admission-manifest.json", "/etc/switchback/trust.json")
	if err == nil || q != nil {
		t.Fatal("cancelled wait returned a handle")
	}
}

type qualificationWaitFixture struct {
	t                              *testing.T
	dir                            string
	trust                          Trust
	raw                            map[string][]byte
	files                          qualificationWaitFiles
	clock                          qualificationWaitClock
	now                            time.Time
	polls, trustReads, frozenReads int
	onPoll                         func()
}

func newQualificationWaitFixture(t *testing.T) *qualificationWaitFixture {
	t.Helper()
	h := &qualificationWaitFixture{t: t, dir: filepath.Join(t.TempDir(), "qualification"), now: time.Unix(0, 0)}
	if err := os.Mkdir(h.dir, 0700); err != nil {
		t.Fatal(err)
	}
	h.trust, h.raw = qualificationFixture(t)
	stat := func(ctx context.Context, fd int) (unix.Stat_t, error) {
		s, err := qualificationStat(ctx, fd)
		s.Uid = 0
		return s, err
	}
	h.files.stat = stat
	h.files.directory = func(ctx context.Context, p string) (*os.File, []string, error) {
		if err := qualificationContext(ctx); err != nil {
			return nil, nil, err
		}
		fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, nil, err
		}
		f := os.NewFile(uintptr(fd), p)
		s, err := stat(ctx, fd)
		if err != nil || s.Mode&0022 != 0 || s.Mode&unix.S_IFMT != unix.S_IFDIR {
			f.Close()
			return nil, nil, fmt.Errorf("unsafe test parent")
		}
		return f, []string{qualificationIdentity(s)}, nil
	}
	h.files.present = qualificationManifestPresent
	h.files.leaf = func(ctx context.Context, d *os.File, name string) (qualificationFile, error) {
		if name == "trust.json" {
			h.trustReads++
		}
		return readQualificationLeafWithStat(ctx, d, name, stat)
	}
	h.files.frozen = func(ctx context.Context, p string) (qualificationFiles, error) {
		h.frozenReads++
		d, ids, err := h.files.directory(ctx, filepath.Dir(p))
		if err != nil {
			return qualificationFiles{}, err
		}
		defer d.Close()
		out := qualificationFiles{path: p, directories: ids, leaves: map[string]qualificationFile{}}
		for _, name := range qualificationLeaves {
			leaf, err := h.files.leaf(ctx, d, name)
			if err != nil {
				return qualificationFiles{}, err
			}
			out.leaves[name] = leaf
		}
		return out, nil
	}
	h.clock = qualificationWaitClock{now: func() time.Time { return h.now }, pause: func(ctx context.Context, d time.Duration) error {
		if d <= 0 || d > qualificationWaitPoll {
			t.Fatalf("fixed-policy poll=%s", d)
		}
		h.polls++
		h.now = h.now.Add(d)
		if h.onPoll != nil {
			h.onPoll()
		}
		return qualificationContext(ctx)
	}}
	return h
}

func (h *qualificationWaitFixture) write(name string, raw []byte) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, name), raw, 0400); err != nil {
		h.t.Fatal(err)
	}
}
func (h *qualificationWaitFixture) commit() {
	h.t.Helper()
	for _, name := range qualificationLeaves {
		if name != "admission-manifest.json" {
			h.write(name, h.raw[name])
		}
	}
	h.write("admission-manifest.json", h.raw["admission-manifest.json"])
}
func (h *qualificationWaitFixture) await(ctx context.Context) (*DeploymentQualification, error) {
	return awaitDeploymentQualification(ctx, filepath.Join(h.dir, "admission-manifest.json"), filepath.Join(h.dir, "trust.json"), h.clock, h.files)
}

func TestAwaitDeploymentQualificationManifestLast(t *testing.T) {
	h := newQualificationWaitFixture(t)
	h.onPoll = func() {
		if h.trustReads != 0 {
			t.Fatal("Trust read before commit")
		}
		h.commit()
		h.onPoll = nil
	}
	q, err := h.await(context.Background())
	if err != nil || q == nil {
		t.Fatal("same held directory commit failed", err)
	}
	// One full real validator attempt, followed only by its frozen file recheck.
	if h.polls != 1 || h.frozenReads != 2 {
		t.Fatalf("polls=%d frozen reads=%d", h.polls, h.frozenReads)
	}
	if q.Metadata().Manifest.Guarantee.ID != "m1_shared_volume_capacity_v1" {
		t.Fatal("did not reach full validator")
	}
}

func TestAwaitDeploymentQualificationRejectsCommittedFailures(t *testing.T) {
	for _, name := range []string{"malformed-manifest", "missing-sibling", "unsafe-mode", "symlink", "hardlink", "legacy-v1", "semantic-v2", "disappearing-manifest", "wrong-trust-sibling"} {
		t.Run(name, func(t *testing.T) {
			h := newQualificationWaitFixture(t)
			switch name {
			case "malformed-manifest":
				h.raw["admission-manifest.json"] = []byte("{")
				h.trust.ManifestDigest = Digest(h.raw["admission-manifest.json"])
				h.raw["trust.json"] = qualificationJSON(h.trust)
			case "legacy-v1":
				h.trust, h.raw = qualificationLegacyFixture(t)
			case "semantic-v2":
				var n qualificationNativeReceipt
				json.Unmarshal(h.raw["native-enforcement-receipt.json"], &n)
				n.Checks[0] = n.Checks[1]
				h.raw["native-enforcement-receipt.json"] = qualificationJSON(n)
				h.trust = qualificationRebind(h.trust, h.raw)
			}
			h.commit()
			mp := filepath.Join(h.dir, "admission-manifest.json")
			switch name {
			case "missing-sibling":
				os.Remove(filepath.Join(h.dir, "source-manifest.json"))
			case "unsafe-mode":
				os.Chmod(mp, 0600)
			case "symlink":
				os.Rename(mp, mp+"-source")
				os.Symlink(mp+"-source", mp)
			case "hardlink":
				os.Link(mp, mp+"-link")
			case "disappearing-manifest":
				h.files.present = func(ctx context.Context, d *os.File) error {
					err := qualificationManifestPresent(ctx, d)
					os.Remove(mp)
					return err
				}
			case "wrong-trust-sibling":
				h.files.leaf = func(ctx context.Context, d *os.File, n string) (qualificationFile, error) {
					if n == "trust.json" {
						return qualificationFile{}, os.ErrNotExist
					}
					return readQualificationLeafWithStat(ctx, d, n, h.files.stat)
				}
			}
			q, err := h.await(context.Background())
			if err == nil || q != nil || h.polls != 0 {
				t.Fatalf("bad commit retry/handle: %v polls=%d", err, h.polls)
			}
			want := 0
			if name == "malformed-manifest" || name == "missing-sibling" || name == "semantic-v2" || name == "legacy-v1" {
				want = 1
			}
			if h.frozenReads != want {
				t.Fatalf("validation file reads=%d want=%d", h.frozenReads, want)
			}
		})
	}
}

func TestAwaitDeploymentQualificationParentAndPath(t *testing.T) {
	for _, name := range []string{"missing-parent", "parent-inode", "parent-mode", "parent-symlink", "wrong-path", "wrong-trust-path"} {
		t.Run(name, func(t *testing.T) {
			h := newQualificationWaitFixture(t)
			if name == "missing-parent" {
				os.Remove(h.dir)
			}
			if strings.HasPrefix(name, "parent-") {
				h.onPoll = func() {
					switch name {
					case "parent-inode":
						os.Rename(h.dir, h.dir+"-old")
						os.Mkdir(h.dir, 0700)
					case "parent-mode":
						os.Chmod(h.dir, 0500)
					case "parent-symlink":
						os.Rename(h.dir, h.dir+"-old")
						os.Symlink(h.dir+"-old", h.dir)
					}
					h.onPoll = nil
				}
			}
			mp, tp := filepath.Join(h.dir, "admission-manifest.json"), filepath.Join(h.dir, "trust.json")
			if name == "wrong-path" {
				mp = filepath.Join(h.dir, "alternate.json")
			}
			if name == "wrong-trust-path" {
				tp = filepath.Join(h.dir, "other-trust.json")
			}
			q, err := awaitDeploymentQualification(context.Background(), mp, tp, h.clock, h.files)
			if err == nil || q != nil || h.frozenReads != 0 || h.trustReads != 0 {
				t.Fatal("unsafe pending path accepted", err)
			}
			if h.polls > 1 {
				t.Fatal("parent drift retried")
			}
		})
	}
}

func TestAwaitDeploymentQualificationDeadlineAndCancellation(t *testing.T) {
	for _, name := range []string{"deadline", "cancel"} {
		t.Run(name, func(t *testing.T) {
			h := newQualificationWaitFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "cancel" {
				h.onPoll = cancel
			}
			q, err := h.await(ctx)
			if err == nil || q != nil || h.trustReads != 0 || h.frozenReads != 0 {
				t.Fatal("pending wait read or validated", err)
			}
			if name == "deadline" && (h.polls != 3000 || h.now.Sub(time.Unix(0, 0)) != 300*time.Second) {
				t.Fatalf("unbounded wait: polls=%d elapsed=%s", h.polls, h.now.Sub(time.Unix(0, 0)))
			}
			if name == "cancel" && (!errors.Is(err, context.Canceled) || h.polls != 1) {
				t.Fatal("cancel not propagated", err, h.polls)
			}
		})
	}
}

func TestAwaitDeploymentQualificationDeadlineAcrossLookup(t *testing.T) {
	for _, name := range []string{"late-commit", "pending-lookup-cost"} {
		t.Run(name, func(t *testing.T) {
			h := newQualificationWaitFixture(t)
			present := h.files.present
			h.files.present = func(ctx context.Context, dir *os.File) error {
				if name == "late-commit" && h.polls == 1 {
					h.now = time.Unix(0, 0).Add(qualificationWaitLimit + time.Nanosecond)
					h.commit()
				} else if name == "pending-lookup-cost" {
					h.now = h.now.Add(time.Millisecond)
				}
				return present(ctx, dir)
			}
			clipped := false
			var lastPause time.Duration
			h.clock.pause = func(ctx context.Context, d time.Duration) error {
				if d <= 0 || d > qualificationWaitPoll {
					t.Fatalf("invalid fixed-policy pause: %s", d)
				}
				remaining := qualificationWaitLimit - h.now.Sub(time.Unix(0, 0))
				if d != min(qualificationWaitPoll, remaining) {
					t.Fatalf("pause=%s remaining=%s", d, remaining)
				}
				lastPause = d
				clipped = clipped || d < qualificationWaitPoll
				h.polls++
				h.now = h.now.Add(d)
				return qualificationContext(ctx)
			}
			q, err := h.await(context.Background())
			if q != nil || !errors.Is(err, context.DeadlineExceeded) || h.trustReads != 0 || h.frozenReads != 0 {
				t.Fatalf("expired lookup proceeded: handle=%v err=%v trust=%d validation reads=%d", q != nil, err, h.trustReads, h.frozenReads)
			}
			t.Logf("elapsed=%s polls=%d final-pause=%s handle=%v trust=%d frozen=%d", h.now.Sub(time.Unix(0, 0)), h.polls, lastPause, q != nil, h.trustReads, h.frozenReads)
			if name == "pending-lookup-cost" && (!clipped || h.now.Sub(time.Unix(0, 0)) != qualificationWaitLimit) {
				t.Fatalf("lookup cost not charged: elapsed=%s clipped=%v", h.now.Sub(time.Unix(0, 0)), clipped)
			}
		})
	}
}

func TestAwaitDeploymentQualificationDeadlineAcrossCommittedStages(t *testing.T) {
	for _, name := range []string{"parent-open", "parent-stat", "parent-reopen", "manifest-read", "trust-read", "frozen-read", "validation-before-recheck", "freeze-recheck", "final-parent-stat"} {
		t.Run(name, func(t *testing.T) {
			h := newQualificationWaitFixture(t)
			h.commit()
			expire := func() { h.now = time.Unix(0, 0).Add(qualificationWaitLimit + time.Nanosecond) }
			// First post-read clock sample is before the real validator. The next
			// sample is before its frozen recheck; advance there to model time
			// spent in full validation without replacing that validator.
			readSamples, originalNow := 0, h.clock.now
			h.clock.now = func() time.Time {
				if name == "validation-before-recheck" && h.frozenReads == 1 {
					readSamples++
					if readSamples == 2 {
						expire()
					}
				}
				return originalNow()
			}
			directory, stat, leaf, frozen := h.files.directory, h.files.stat, h.files.leaf, h.files.frozen
			opens := 0
			h.files.directory = func(ctx context.Context, p string) (*os.File, []string, error) {
				file, ids, err := directory(ctx, p)
				opens++
				if (name == "parent-open" && opens == 1) || (name == "parent-reopen" && opens == 2) {
					expire()
				}
				return file, ids, err
			}
			h.files.stat = func(ctx context.Context, fd int) (unix.Stat_t, error) {
				s, err := stat(ctx, fd)
				if name == "parent-stat" || (name == "final-parent-stat" && h.frozenReads == 2) {
					expire()
				}
				return s, err
			}
			h.files.leaf = func(ctx context.Context, dir *os.File, n string) (qualificationFile, error) {
				file, err := leaf(ctx, dir, n)
				if (name == "manifest-read" && n == "admission-manifest.json") || (name == "trust-read" && n == "trust.json") {
					expire()
				}
				return file, err
			}
			h.files.frozen = func(ctx context.Context, p string) (qualificationFiles, error) {
				files, err := frozen(ctx, p)
				if (name == "frozen-read" && h.frozenReads == 1) || (name == "freeze-recheck" && h.frozenReads == 2) {
					expire()
				}
				return files, err
			}
			q, err := h.await(context.Background())
			if q != nil || !errors.Is(err, context.DeadlineExceeded) || h.polls != 0 {
				t.Fatalf("expired %s returned authority: handle=%v err=%v polls=%d", name, q != nil, err, h.polls)
			}
			wantReads := 0
			if name == "frozen-read" || name == "validation-before-recheck" {
				wantReads = 1
			} else if name == "freeze-recheck" || name == "final-parent-stat" {
				wantReads = 2
			}
			if h.frozenReads != wantReads {
				t.Fatalf("work continued after %s deadline: frozen reads=%d want=%d", name, h.frozenReads, wantReads)
			}
			if (strings.HasPrefix(name, "parent-") || name == "manifest-read") && h.trustReads != 0 {
				t.Fatal("Trust read after expired parent/manifest operation")
			}
		})
	}
}
