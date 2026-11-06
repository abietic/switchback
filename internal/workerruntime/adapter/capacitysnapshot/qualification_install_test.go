package capacitysnapshot

import (
	"bytes"
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQualificationInstallFailureIsConsumed(t *testing.T) {
	calls := 0
	ops := qualificationInstallOps{claim: func(context.Context) error {
		calls++
		if calls > 1 {
			return errors.New("consumed")
		}
		return nil
	}, remaining: func(context.Context) error { return errors.New("expired") }}
	if err := installQualification(context.Background(), ops); err == nil {
		t.Fatal("expired window installed")
	}
	if err := installQualification(context.Background(), ops); err == nil {
		t.Fatal("resumed expired window")
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

type qualificationInstallFixture struct {
	t                *testing.T
	root, final, env string
	files            map[string][]byte
	ops              qualificationInstallOps
	committed        bool
}

func newQualificationInstallFixture(t *testing.T) *qualificationInstallFixture {
	t.Helper()
	f := &qualificationInstallFixture{t: t, root: t.TempDir()}
	f.final = filepath.Join(f.root, "final")
	f.env = filepath.Join(f.root, "env")
	for _, p := range []string{f.final, f.env} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	_, f.files = qualificationFixture(t)
	stat := func(ctx context.Context, fd int) (unix.Stat_t, error) {
		s, e := qualificationStat(ctx, fd)
		s.Uid = 0
		s.Gid = 0
		return s, e
	}
	directory := func(ctx context.Context, p string) (*os.File, []string, error) {
		fd, e := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return nil, nil, e
		}
		file := os.NewFile(uintptr(fd), p)
		s, e := stat(ctx, fd)
		if e != nil || s.Mode&0022 != 0 {
			file.Close()
			return nil, nil, errors.New("unsafe")
		}
		return file, []string{qualificationIdentity(s)}, nil
	}
	claimed := false
	f.ops = qualificationInstallOps{
		claim: func(context.Context) error {
			if claimed {
				return errors.New("consumed")
			}
			claimed = true
			return nil
		}, remaining: func(context.Context) error { return nil },
		load: func(context.Context) (map[string][]byte, error) { return f.files, nil }, directory: directory, stat: stat,
		leaf: func(ctx context.Context, d *os.File, n string) (qualificationFile, error) {
			return readQualificationLeafWithStat(ctx, d, n, stat)
		},
		view: func(ctx context.Context, p, w *os.File) error {
			a, e := stat(ctx, int(p.Fd()))
			if e != nil {
				return e
			}
			b, e := stat(ctx, int(w.Fd()))
			if e != nil {
				return e
			}
			if a.Dev != b.Dev || a.Ino != b.Ino {
				return errors.New("not same inode")
			}
			return nil
		},
		state: func(context.Context, map[string][]byte) error { return nil }, current: func(context.Context) error { return nil }, write: func(f *os.File, b []byte) (int, error) { return f.Write(b) }, sync: func(f *os.File) error { return f.Sync() },
		rename: func(old int, from string, to int, name string) error {
			if e := unix.Linkat(old, from, to, name, 0); e != nil {
				return e
			}
			if e := unix.Unlinkat(old, from, 0); e != nil {
				return e
			}
			return nil
		}, final: f.final, public: f.env, writer: f.env,
	}
	return f
}
func (f *qualificationInstallFixture) frozen(ctx context.Context, path string) (qualificationFiles, error) {
	d, ids, e := f.ops.directory(ctx, f.final)
	if e != nil {
		return qualificationFiles{}, e
	}
	defer d.Close()
	out := qualificationFiles{path: path, directories: ids, leaves: map[string]qualificationFile{}}
	env, envIDs, e := f.ops.directory(ctx, f.env)
	if e != nil {
		return out, e
	}
	defer env.Close()
	for _, name := range qualificationLeaves {
		where, leaf := d, name
		if name == "observer-deployment.env" {
			where, leaf = env, "deployment.env"
		}
		v, e := f.ops.leaf(ctx, where, leaf)
		if e != nil {
			return out, e
		}
		out.leaves[name] = v
	}
	out.directories = append(out.directories, envIDs...)
	return out, nil
}
func TestQualificationInstallFullReaderAndThreeBarriers(t *testing.T) {
	f := newQualificationInstallFixture(t)
	ctx := context.Background()
	calls := [3]int{}
	// Each barrier traverses the real manifest presence/leaf reader and full
	// semantic validator. The filesystem seam only maps its fixed public env root.
	for role := 0; role < 3; role++ {
		clock := qualificationWaitClock{now: time.Now, pause: func(context.Context, time.Duration) error {
			if !f.committed {
				if e := installQualification(ctx, f.ops); e != nil {
					t.Fatal(e)
				}
				f.committed = true
			}
			return nil
		}}
		q, e := awaitDeploymentQualification(ctx, filepath.Join(f.final, "admission-manifest.json"), filepath.Join(f.final, "trust.json"), clock, qualificationWaitFiles{directory: f.ops.directory, stat: f.ops.stat, present: qualificationManifestPresent, leaf: f.ops.leaf, frozen: f.frozen})
		if e != nil || q == nil {
			t.Fatal(e)
		}
		calls[role]++
	}
	if calls != [3]int{1, 1, 1} {
		t.Fatal(calls)
	}
	names, e := os.ReadDir(f.final)
	if e != nil || len(names) != 7 {
		t.Fatal("config must have seven leaves", e, len(names))
	}
	actual, e := os.ReadFile(filepath.Join(f.env, "deployment.env"))
	if e != nil || !bytes.Equal(actual, f.files["observer-deployment.env"]) {
		t.Fatal("actual env missing", e)
	}
	// A matching copy in the config directory cannot substitute for the real env.
	_ = os.Chmod(filepath.Join(f.env, "deployment.env"), 0600)
	if e = os.WriteFile(filepath.Join(f.env, "deployment.env"), []byte("wrong"), 0400); e != nil {
		t.Fatal(e)
	}
	_ = os.Chmod(filepath.Join(f.env, "deployment.env"), 0400)
	var tr Trust
	_ = strictJSON(f.files["trust.json"], &tr)
	if _, e = openDeploymentQualificationWithFiles(ctx, filepath.Join(f.final, "admission-manifest.json"), tr, f.frozen); e == nil {
		t.Fatal("actual env ignored")
	}
}
func TestQualificationInstallMaterialFailures(t *testing.T) {
	cases := []string{"short-write", "file-sync", "directory-sync", "rename", "noreplace", "symlink", "hardlink", "mode", "parent-drift", "extra-leaf", "missing-leaf", "wrong-window", "existing-trust", "existing-manifest", "concurrent", "state-unknown", "role-drift", "expired-precommit", "commit-unknown"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := newQualificationInstallFixture(t)
			unknown := false
			switch name {
			case "short-write":
				f.ops.write = func(_ *os.File, b []byte) (int, error) { return len(b) - 1, nil }
			case "file-sync":
				f.ops.sync = func(file *os.File) error {
					info, _ := file.Stat()
					if info.Mode().IsRegular() {
						return errors.New("sync")
					}
					return file.Sync()
				}
			case "directory-sync":
				f.ops.sync = func(file *os.File) error {
					info, _ := file.Stat()
					if info.IsDir() {
						return errors.New("sync")
					}
					return file.Sync()
				}
			case "rename":
				f.ops.rename = func(int, string, int, string) error { return errors.New("rename") }
			case "noreplace":
				f.ops.rename = func(int, string, int, string) error { return unix.EEXIST }
			case "symlink":
				_ = os.Symlink("/nonexistent", filepath.Join(f.final, "unknown"))
			case "hardlink":
				p := filepath.Join(f.root, "data")
				_ = os.WriteFile(p, []byte("x"), 0400)
				_ = os.Link(p, filepath.Join(f.final, "trust.json"))
			case "mode":
				_ = os.Chmod(f.final, 0777)
			case "parent-drift":
				base := f.ops.view
				count := 0
				f.ops.view = func(ctx context.Context, p, w *os.File) error {
					count++
					if count == 3 {
						_ = os.Rename(f.final, f.final+"-old")
						_ = os.Mkdir(f.final, 0700)
					}
					return base(ctx, p, w)
				}
			case "extra-leaf", "existing-trust", "existing-manifest":
				leaf := "unknown"
				if name == "existing-trust" {
					leaf = "trust.json"
				}
				if name == "existing-manifest" {
					leaf = "admission-manifest.json"
				}
				_ = os.WriteFile(filepath.Join(f.final, leaf), []byte("old"), 0400)
			case "missing-leaf":
				delete(f.files, "native-enforcement-receipt.json")
			case "wrong-window":
				var native qualificationNativeReceipt
				_ = strictJSON(f.files["native-enforcement-receipt.json"], &native)
				native.Window.ID = "wrong"
				f.files["native-enforcement-receipt.json"] = qualificationJSON(native)
			case "concurrent":
				f.ops.claim = func(context.Context) error { return unix.EWOULDBLOCK }
			case "state-unknown":
				f.ops.state = func(context.Context, map[string][]byte) error { return errors.New("unknown state") }
			case "role-drift":
				f.ops.current = func(context.Context) error { return errors.New("changed process") }
			case "expired-precommit":
				f.ops.current = func(context.Context) error {
					f.ops.remaining = func(context.Context) error { return context.DeadlineExceeded }
					return context.DeadlineExceeded
				}
			case "commit-unknown":
				base := f.ops.rename
				f.ops.rename = func(old int, from string, to int, name string) error {
					if e := base(old, from, to, name); e != nil {
						return e
					}
					if name == "admission-manifest.json" {
						return errors.New("lost commit result")
					}
					return nil
				}
				unknown = true
			}
			e := installQualification(context.Background(), f.ops)
			if e == nil {
				t.Fatal("failure accepted")
			}
			if errors.Is(e, ErrQualificationInstallUnknown) != unknown {
				t.Fatal(e)
			}
			if name != "existing-manifest" && !unknown {
				if _, e := os.Lstat(filepath.Join(f.final, "admission-manifest.json")); !os.IsNotExist(e) {
					t.Fatal("precommit failure exposed manifest", e)
				}
			}
			if name != "concurrent" {
				if e = installQualification(context.Background(), f.ops); e == nil {
					t.Fatal("attempted resume succeeded")
				}
			}
		})
	}
}

func TestQualificationPublicationBootstrapIndependentState(t *testing.T) {
	for _, failure := range []string{"success", "preexisting", "drift", "wrong-trust", "modified-journal", "unknown-leaf", "attempted-resume"} {
		t.Run(failure, func(t *testing.T) {
			d := qualificationCompletedInstallCandidate(t)
			f := newQualificationInstallFixture(t)
			private := filepath.Join(f.root, "private")
			public := filepath.Join(f.root, "public")
			_ = os.Mkdir(private, 0700)
			_ = os.Mkdir(public, 0750)
			fs := qualificationPublicationFS{directory: f.ops.directory, stat: f.ops.stat, private: private, public: public}
			ctx := context.Background()
			if e := qualificationPublicationRecordEmpty(ctx, d, fs); e != nil {
				t.Fatal(e)
			}
			if failure == "preexisting" {
				_ = os.WriteFile(filepath.Join(private, "sequence.bin"), []byte("old"), 0400)
			}
			if failure == "drift" {
				_ = os.Rename(private, private+"old")
				_ = os.Mkdir(private, 0700)
			}
			e := qualificationBootstrapPublication(ctx, d, fs)
			if failure == "preexisting" || failure == "drift" {
				if e == nil {
					t.Fatal("existing or drift accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			files, e := qualificationInstallMaterial(ctx, d)
			if e != nil {
				t.Fatal(e)
			}
			if failure == "wrong-trust" {
				var tr Trust
				_ = strictJSON(files["trust.json"], &tr)
				tr.ManifestDigest = Digest([]byte("other"))
				files["trust.json"] = qualificationJSON(tr)
			}
			if failure == "modified-journal" {
				p := filepath.Join(private, "sequence.bin")
				_ = os.Chmod(p, 0600)
				_ = os.WriteFile(p, make([]byte, 96), 0400)
				_ = os.Chmod(p, 0400)
			}
			if failure == "unknown-leaf" {
				_ = os.WriteFile(filepath.Join(public, "unknown"), []byte("x"), 0400)
			}
			e = qualificationPublicationCheck(ctx, d, fs, files)
			if failure == "wrong-trust" || failure == "modified-journal" || failure == "unknown-leaf" {
				if e == nil {
					t.Fatal("bad state accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = qualificationBootstrapPublication(ctx, d, fs); e == nil {
				t.Fatal("bootstrap resumed")
			}
			if failure == "success" {
				f.ops.load = func(ctx context.Context) (map[string][]byte, error) { return qualificationInstallMaterial(ctx, d) }
				f.ops.state = func(ctx context.Context, files map[string][]byte) error {
					return qualificationPublicationCheck(ctx, d, fs, files)
				}
				if e = installQualification(ctx, f.ops); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestQualificationInstallRejectsUnsafeSourceMaterial(t *testing.T) {
	for _, bad := range []string{"missing", "hardlink", "symlink", "write-mode", "wrong-common-window"} {
		t.Run(bad, func(t *testing.T) {
			d := qualificationCompletedInstallCandidate(t)
			path := filepath.Join(d.file.Name(), "prepared-trust.json")
			switch bad {
			case "missing":
				_ = os.Remove(path)
			case "hardlink":
				_ = os.Link(path, path+"-other")
			case "symlink":
				_ = os.Remove(path)
				_ = os.Symlink("outside", path)
			case "write-mode":
				_ = os.Chmod(path, 0600)
			case "wrong-common-window":
				p := filepath.Join(d.file.Name(), "common-window.json")
				_ = os.Chmod(p, 0600)
				_ = os.WriteFile(p, []byte(`{"schema":"wrong"}`), 0400)
				_ = os.Chmod(p, 0400)
			}
			if _, e := qualificationInstallMaterial(context.Background(), d); e == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}
