package capacitysnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

const qualificationInstallDirectory = "/mnt/lima-sbquota/core/capacity-publisher-config"
const qualificationObserverDirectory = "/etc/switchback/created-layer-observer"
const qualificationObserverWriter = qualificationGuestCandidateRoot + "/observer-env-writer"

var ErrQualificationInstallUnknown = errors.New("qualification_install_commit_unknown")

// All injection points are private. The public command supplies only the fixed
// Linux filesystem, real semantic validator, original clock and current roles.
type qualificationInstallOps struct {
	claim                 func(context.Context) error
	remaining             func(context.Context) error
	load                  func(context.Context) (map[string][]byte, error)
	directory             func(context.Context, string) (*os.File, []string, error)
	stat                  func(context.Context, int) (unix.Stat_t, error)
	leaf                  func(context.Context, *os.File, string) (qualificationFile, error)
	view                  func(context.Context, *os.File, *os.File) error
	state                 func(context.Context, map[string][]byte) error
	current               func(context.Context) error
	write                 func(*os.File, []byte) (int, error)
	sync                  func(*os.File) error
	rename                func(int, string, int, string) error
	final, public, writer string
}

type qualificationInstallDirectoryHandle struct {
	file *os.File
	path string
	ids  []string
}

func installQualification(ctx context.Context, k qualificationInstallOps) (err error) {
	if k.claim == nil || k.remaining == nil {
		return errQualification
	}
	if err = k.claim(ctx); err != nil {
		return err
	}
	check := func() error {
		if e := qualificationContext(ctx); e != nil {
			return e
		}
		return k.remaining(ctx)
	}
	if err = check(); err != nil {
		return err
	}
	files, err := k.load(ctx)
	if err != nil {
		return err
	}
	var trust Trust
	if strictJSON(files["trust.json"], &trust) != nil {
		return errQualification
	}
	if _, err = validateDeploymentQualification(trust, files); err != nil {
		return err
	}
	if err = k.state(ctx, files); err != nil {
		return err
	}
	var dirs []*qualificationInstallDirectoryHandle
	defer func() {
		for _, d := range dirs {
			_ = d.file.Close()
		}
	}()
	open := func(path string) (*qualificationInstallDirectoryHandle, error) {
		f, ids, e := k.directory(ctx, path)
		if e != nil {
			return nil, e
		}
		d := &qualificationInstallDirectoryHandle{f, path, qualificationStableDirectories(ids)}
		dirs = append(dirs, d)
		s, e := k.stat(ctx, int(f.Fd()))
		if e != nil || s.Uid != 0 || s.Gid != 0 || s.Mode&unix.S_IFMT != unix.S_IFDIR || s.Mode&0777 != 0700 || s.Nlink == 0 {
			return nil, fmt.Errorf("installation directory unsafe")
		}
		return d, nil
	}
	final, err := open(k.final)
	if err != nil {
		return err
	}
	public, err := open(k.public)
	if err != nil {
		return err
	}
	writer, err := open(k.writer)
	if err != nil {
		return err
	}
	if err = k.view(ctx, public.file, writer.file); err != nil {
		return err
	}
	stable := func() error {
		if e := check(); e != nil {
			return e
		}
		for _, d := range dirs {
			s, e := k.stat(ctx, int(d.file.Fd()))
			if e != nil || qualificationStableDirectory(qualificationIdentity(s)) != d.ids[len(d.ids)-1] {
				return fmt.Errorf("installation held directory drift")
			}
			f, ids, e := k.directory(ctx, d.path)
			if e != nil {
				return e
			}
			_ = f.Close()
			if !reflect.DeepEqual(d.ids, qualificationStableDirectories(ids)) {
				return fmt.Errorf("installation canonical directory drift")
			}
		}
		if e := k.view(ctx, public.file, writer.file); e != nil {
			return e
		}
		return check()
	}
	if err = qualificationInstallNames(final.file, nil); err != nil {
		return err
	}
	// The IPC key is provisioned separately before roles start. Installation never
	// opens its contents and allows no other observer-directory material.
	names, err := qualificationInstallList(public.file)
	if err != nil {
		return err
	}
	if len(names) > 1 || (len(names) == 1 && names[0] != "key") {
		return fmt.Errorf("observer configuration already present or unknown")
	}
	if err = stable(); err != nil {
		return err
	}
	temp := func(d *qualificationInstallDirectoryHandle, name string, raw []byte) error {
		if e := stable(); e != nil {
			return e
		}
		tmp := ".qualification-" + name + ".next"
		fd, e := unix.Openat(int(d.file.Fd()), tmp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), tmp)
		defer f.Close()
		before, e := k.stat(ctx, fd)
		if e != nil || before.Uid != 0 || before.Gid != 0 || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size != 0 {
			return fmt.Errorf("installation temporary unsafe")
		}
		for off := 0; off < len(raw); {
			if e = check(); e != nil {
				return e
			}
			end := min(off+32768, len(raw))
			n, e := k.write(f, raw[off:end])
			if e != nil {
				return e
			}
			if n != end-off {
				return io.ErrShortWrite
			}
			off += n
		}
		if e = f.Chmod(0400); e != nil {
			return e
		}
		if e = k.sync(f); e != nil {
			return e
		}
		s, e := k.stat(ctx, fd)
		if e != nil || s.Dev != before.Dev || s.Ino != before.Ino || s.Nlink != 1 || s.Uid != 0 || s.Gid != 0 || s.Mode&07777 != 0400 || s.Size != int64(len(raw)) {
			return fmt.Errorf("installation temporary changed")
		}
		leaf, e := k.leaf(ctx, d.file, tmp)
		if e != nil || leaf.identity != qualificationIdentity(s) || !bytes.Equal(leaf.data, raw) {
			return fmt.Errorf("installation temporary readback failed")
		}
		return stable()
	}
	// Prepare all eight leaves before exposing any final name.
	for _, name := range qualificationLeaves {
		d := final
		target := name
		if name == "observer-deployment.env" {
			d = writer
			target = "deployment.env"
		}
		if err = temp(d, target, files[name]); err != nil {
			return err
		}
	}
	expected := []string{}
	for _, name := range qualificationLeaves {
		if name == "admission-manifest.json" || name == "observer-deployment.env" {
			continue
		}
		if err = stable(); err != nil {
			return err
		}
		if err = k.rename(int(final.file.Fd()), ".qualification-"+name+".next", int(final.file.Fd()), name); err != nil {
			return err
		}
		expected = append(expected, name)
	}
	if err = stable(); err != nil {
		return err
	}
	if err = k.rename(int(writer.file.Fd()), ".qualification-deployment.env.next", int(writer.file.Fd()), "deployment.env"); err != nil {
		return err
	}
	if err = k.sync(writer.file); err != nil {
		return err
	}
	actualEnv, e := k.leaf(ctx, public.file, "deployment.env")
	if e != nil || !bytes.Equal(actualEnv.data, files["observer-deployment.env"]) {
		return fmt.Errorf("actual installed observer env differs")
	}
	if err = k.sync(final.file); err != nil {
		return err
	}
	expected = append(expected, ".qualification-admission-manifest.json.next")
	if err = qualificationInstallNames(final.file, expected); err != nil {
		return err
	}
	// Validate immutable source and current role/process/CID/cleanup again at the
	// last precommit boundary. No journal/key/PG initialization happens here.
	again, e := k.load(ctx)
	if e != nil || !reflect.DeepEqual(files, again) {
		return fmt.Errorf("installation source changed")
	}
	if err = k.state(ctx, files); err != nil {
		return err
	}
	if err = k.current(ctx); err != nil {
		return err
	}
	if err = stable(); err != nil {
		return err
	}
	// From the rename attempt onward even an error may have made the manifest
	// visible. Never retry, repair, delete it, or report a proven precommit failure.
	unknown := func(e error) error { return fmt.Errorf("%w: %v", ErrQualificationInstallUnknown, e) }
	if err = k.rename(int(final.file.Fd()), ".qualification-admission-manifest.json.next", int(final.file.Fd()), "admission-manifest.json"); err != nil {
		return unknown(err)
	}
	if err = k.sync(final.file); err != nil {
		return unknown(err)
	}
	if err = stable(); err != nil {
		return unknown(err)
	}
	expected[len(expected)-1] = "admission-manifest.json"
	if err = qualificationInstallNames(final.file, expected); err != nil {
		return unknown(err)
	}
	for _, name := range qualificationLeaves {
		d := final
		leaf := name
		if name == "observer-deployment.env" {
			d = public
			leaf = "deployment.env"
		}
		f, e := k.leaf(ctx, d.file, leaf)
		if e != nil || !bytes.Equal(f.data, files[name]) {
			return unknown(fmt.Errorf("committed leaf readback: %s", name))
		}
	}
	return nil
}
func qualificationInstallList(dir *os.File) ([]string, error) {
	fd, e := unix.Openat(int(dir.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), dir.Name())
	defer f.Close()
	names, e := f.Readdirnames(129)
	if e != nil && e != io.EOF {
		return nil, e
	}
	if len(names) > 128 {
		return nil, errQualification
	}
	sort.Strings(names)
	return names, nil
}
func qualificationInstallNames(dir *os.File, want []string) error {
	names, e := qualificationInstallList(dir)
	if e != nil {
		return e
	}
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) && !(len(names) == 0 && len(want) == 0) {
		return fmt.Errorf("installation extra/missing leaf")
	}
	return nil
}

// Reconstruct from raw immutable same-window observations, then compare all
// prepared bytes. A caller-created prepared Trust is never a success token.
func qualificationInstallMaterial(ctx context.Context, d qualificationCandidateDirectory) (map[string][]byte, error) {
	var c qualificationCandidateRecord
	var h qualificationHostReceipt
	var n qualificationNativeObservation
	var common qualificationCommonEnd
	var w qualificationWindowOpen
	var s qualificationStaged
	var before qualificationHostBefore
	for _, x := range []struct {
		name string
		v    any
	}{{"candidate.json", &c}, {"host-observations.json", &h}, {"native-observations.json", &n}, {"common-window.json", &common}, {"window-open.json", &w}, {"staged.json", &s}, {"host-before.json", &before}} {
		if e := d.decode(ctx, x.name, x.v); e != nil {
			return nil, e
		}
	}
	if qualificationValidateStaged(s) != nil || qualificationValidateCreateBoundStaged(s) != nil {
		return nil, errQualification
	}
	static, e := qualificationValidateStaticIntent(ctx, d.decode, s)
	if e != nil {
		return nil, e
	}
	if c.StaticIntentDigest != static || c.StagedDigest != qualificationDigest(s) || c.OpenDigest != qualificationDigest(w) || w.StagedDigest != c.StagedDigest || w.ID != s.WindowID || w.Nonce != s.Nonce || c.TestProject != s.TestProject || common.Candidate != c.Fingerprint || common.Schema != "switchback.qualification.common_end.v1" || common.HostDigest != qualificationDigest(h) || common.NativeDigest != qualificationDigest(n) || common.HostBeforeDigest != qualificationDigest(before) || !reflect.DeepEqual(common.Window, h.Window) || h.Window.Started != w.Started || h.Window.ID != w.ID || before.Candidate != c.Fingerprint || before.OpenDigest != c.OpenDigest || before.HostBoot != h.Window.HostBootBefore || len(before.Checks) != 2 || len(h.Checks) != 5 || !reflect.DeepEqual(before.Checks, h.Checks[:2]) {
		return nil, errQualification
	}
	projected := qualificationClone(s)
	projected.Source = c.Source
	projected.Inventory = c.Inventory
	projected.Trust = c.Trust
	projected.Binding = c.Binding
	if !qualificationSameJSON(qualificationStaticIntentProjection(projected), qualificationStaticIntentProjection(s)) {
		return nil, errQualification
	}
	start, se := time.Parse(time.RFC3339Nano, w.Started)
	end, ee := time.Parse(time.RFC3339Nano, h.Window.Ended)
	completed, ce := time.Parse(time.RFC3339Nano, n.Completed)
	beforeEnd, be := time.Parse(time.RFC3339Nano, before.Started)
	if se != nil || ee != nil || ce != nil || be != nil || !beforeEnd.After(start) || !completed.After(beforeEnd) || !end.After(completed) || end.Sub(start) > qualificationBudget || n.GuestBootBefore != w.GuestBoot || n.GuestBootAfter != w.GuestBoot {
		return nil, errQualification
	}
	for _, check := range n.Checks {
		at, e := time.Parse(time.RFC3339Nano, check.Observation.Started)
		if e != nil || at.Before(beforeEnd) {
			return nil, errQualification
		}
	}
	for _, check := range h.Checks[2:] {
		at, e := time.Parse(time.RFC3339Nano, check.Observation.Started)
		if e != nil || !at.After(completed) {
			return nil, errQualification
		}
	}
	files, e := assembleQualification(c, h, n)
	if e != nil {
		return nil, e
	}
	for _, name := range qualificationLeaves {
		raw, e := d.read(ctx, "prepared-"+name)
		if e != nil || !bytes.Equal(raw, files[name]) {
			return nil, fmt.Errorf("prepared material differs: %s", name)
		}
	}
	return files, nil
}

// InstallQualification is Linux-only and has no configurable path or adapter.
func InstallQualification(ctx context.Context) error { return installQualificationFixed(ctx) }

func qualificationInstallCandidateNames(d qualificationCandidateDirectory) error {
	allowed := map[string]bool{qualificationExecutionProfileLeaf: true}
	for _, name := range []string{"runtime-preflight.json", "intent-input.json", "static-intent.json", "staged.json", "window-open.json", "candidate.json", "host-material.json", "host-before.json", "native-observations.json", "host-observations.json", "common-window.json", "public-identity.json", "publication-empty.json", "publication-bootstrap.json", "created.json", "created-backend.json", "created-postgres.json", "created-web.json", "pg-prepared.json", "pg-precommit.json", "observer-env-writer"} {
		allowed[name] = true
	}
	for _, phase := range []string{"runtime-preflight", "identity", "intent", "begin", "prepare", "collect", "assemble", "material-intent", "create-core", "material-staged", "prepare-core", "start-waiting", "pg-precommit", "install", "publication-preflight", "publication-bootstrap"} {
		allowed["claimed-"+phase+".json"] = true
	}
	for _, name := range qualificationLeaves {
		allowed["prepared-"+name] = true
	}
	for _, role := range []string{"backend", "worker", "postgres", "web", "worker-native-helper", "worker-compat-helper"} {
		allowed["image-artifact-"+role] = true
	}
	for _, c := range []string{"empty", "nonempty", "private", "sticky", "without-chown", "without-dac-override", "without-fowner", "worker-handoff", "worker"} {
		allowed["control-"+c] = true
	}
	names, e := qualificationInstallList(d.file)
	if e != nil {
		return e
	}
	for _, name := range names {
		if !allowed[name] {
			return fmt.Errorf("unknown qualification candidate material: %s", name)
		}
	}
	return nil
}
