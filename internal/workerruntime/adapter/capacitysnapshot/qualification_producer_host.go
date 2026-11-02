package capacitysnapshot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type qualificationHostOps struct {
	now   func() time.Time
	boot  func(context.Context) (string, error)
	run   func(context.Context, string, ...string) ([]byte, error)
	paths func(context.Context, qualificationStaged) (qualificationHostPaths, error)
	self  func(context.Context, qualificationSource, string) error
	read  func(context.Context, string, any) error
	put   func(context.Context, string, any) error
	claim func(context.Context, string) error
}

// CollectHostQualification reads only the fixed deployment-owned host root.
// No endpoint, path, timeout, skip or test adapter is accepted by production.
func CollectHostQualification(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("host qualification requires darwin")
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	d, e := openQualificationCandidate(ctx, filepath.Join(home, ".switchback", "qualification-candidate"), uint32(os.Getuid()))
	if e != nil {
		return e
	}
	defer d.close()
	return collectHostQualification(ctx, qualificationHostOps{now: time.Now, boot: qualificationHostBoot, run: qualificationHostExec, paths: qualificationHostObservePaths, self: qualificationOwnExecutable, read: d.decode, put: d.put, claim: d.claim})
}
func collectHostQualification(ctx context.Context, k qualificationHostOps) error {
	if qualificationContext(ctx) != nil {
		return errQualification
	}
	var staged qualificationStaged
	var c qualificationCandidateRecord
	var w qualificationWindowOpen
	if e := k.read(ctx, "staged.json", &staged); e != nil {
		return e
	}
	if e := k.read(ctx, "candidate.json", &c); e != nil {
		return e
	}
	if e := k.read(ctx, "window-open.json", &w); e != nil {
		return e
	}
	start, e := time.Parse(time.RFC3339Nano, w.Started)
	if e != nil || c.OpenDigest != qualificationDigest(w) || c.StagedDigest != qualificationDigest(staged) || c.Schema != candidateSchema {
		return errQualification
	}
	if c.StaticIntentDigest != qualificationStaticIntentDigest(staged) {
		return errQualification
	}
	remaining := qualificationBudget - k.now().Sub(start)
	if remaining <= 0 || remaining > qualificationBudget {
		return errQualification
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	if e = k.claim(ctx, "host"); e != nil {
		return e
	}
	if e = k.self(ctx, c.Source, "host-producer"); e != nil {
		return e
	}
	boot, e := k.boot(ctx)
	if e != nil || !qualificationUUIDPattern.MatchString(boot) {
		return errQualification
	}
	checks := []qualificationCheck{}
	observe := func(phase string) error {
		for _, op := range []string{"diskutil-info", "apfs-list"} {
			s := k.now()
			var raw []byte
			var e error
			if op == "diskutil-info" {
				raw, e = k.run(ctx, "/usr/sbin/diskutil", "info", "-plist", c.Source.Inputs.Host.Mount)
			} else {
				raw, e = k.run(ctx, "/usr/sbin/diskutil", "apfs", "list", "-plist")
			}
			if e != nil {
				return e
			}
			if _, e = qualificationPlist(raw); e != nil {
				return e
			}
			check, e := qualificationCheckRaw(op+"-"+phase, w.ID, s, k.now(), raw)
			if e != nil {
				return e
			}
			checks = append(checks, check)
		}
		return nil
	}
	if e = validateQualificationHostTarget(c.Source.Inputs.Host); e != nil {
		return e
	}
	if e = observe("before"); e != nil {
		return e
	}
	beforePaths, e := k.paths(ctx, staged)
	if e != nil {
		return e
	}
	before := qualificationHostBefore{Schema: "switchback.qualification.host_before.v1", Candidate: c.Fingerprint, OpenDigest: c.OpenDigest, HostBoot: boot, Started: k.now().UTC().Format(time.RFC3339Nano), Checks: qualificationClone(checks)}
	if e = k.put(ctx, "host-before.json", before); e != nil {
		return e
	}
	// The driver transfers native-observations.json exclusively only after its
	// producer exits successfully. Only ENOENT under this already-open safe root
	// is pending; malformed, old or unsafe signals stop immediately.
	var native qualificationNativeObservation
	for {
		e = k.read(ctx, "native-observations.json", &native)
		if e == nil {
			break
		}
		if !os.IsNotExist(e) {
			return e
		}
		if e = qualificationPoll(ctx); e != nil {
			return e
		}
	}
	if native.Schema != nativeObservationSchema || native.Candidate != c.Fingerprint || native.WindowID != w.ID || native.OpenDigest != c.OpenDigest || validateQualificationCleanup(native.Cleanup, c.TestProject) != nil {
		return errQualification
	}
	completed, e := time.Parse(time.RFC3339Nano, native.Completed)
	if e != nil || completed.Before(start) || completed.After(k.now()) {
		return errQualification
	}
	if e = observe("after"); e != nil {
		return e
	}
	s := k.now()
	afterPaths, e := k.paths(ctx, staged)
	if e != nil {
		return e
	}
	if !qualificationStableHostPaths(beforePaths, afterPaths) {
		return fmt.Errorf("host writer mapping changed")
	}
	raw, e := qualificationEncode(afterPaths)
	if e != nil {
		return e
	}
	check, e := qualificationCheckRaw("writer-paths", w.ID, s, k.now(), raw)
	if e != nil {
		return e
	}
	checks = append(checks, check)
	afterBoot, e := k.boot(ctx)
	if e != nil || boot != afterBoot {
		return errQualification
	}
	end := k.now()
	if !end.After(start) || end.Sub(start) > qualificationBudget {
		return errQualification
	}
	window := qualificationWindow{ID: w.ID, Started: w.Started, Ended: end.UTC().Format(time.RFC3339Nano), HostBootBefore: boot, HostBootAfter: afterBoot, GuestBootBefore: native.GuestBootBefore, GuestBootAfter: native.GuestBootAfter}
	p, e := qualificationProduced("host-producer", c.Source)
	if e != nil {
		return e
	}
	h := qualificationHostReceipt{Schema: "switchback.capacity.host_volume_receipt.v1", Kind: "apfs_volume_quota_and_reserve_v1", Lifecycle: qualificationLifecycle, Candidate: c.Fingerprint, Window: window, Producer: p, Checks: checks}
	if e = validateQualificationHost(h, c.Source.Inputs.Host); e != nil {
		return e
	}
	if e = k.put(ctx, "host-observations.json", h); e != nil {
		return e
	}
	return k.put(ctx, "common-window.json", qualificationCommonEnd{Schema: "switchback.qualification.common_end.v1", Candidate: c.Fingerprint, HostBeforeDigest: qualificationDigest(before), NativeDigest: qualificationDigest(native), HostDigest: qualificationDigest(h), Window: window})
}
func qualificationPoll(ctx context.Context) error {
	t := time.NewTimer(100 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type qualificationBoundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *qualificationBoundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("qualification command output oversized")
	}
	return b.Buffer.Write(p)
}
func qualificationHostExec(ctx context.Context, program string, args ...string) ([]byte, error) {
	if qualificationContext(ctx) != nil {
		return nil, errQualification
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	cmd.WaitDelay = time.Second
	out := &qualificationBoundedOutput{limit: 256 << 10}
	stderr := &qualificationBoundedOutput{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if e := cmd.Run(); e != nil {
		return nil, fmt.Errorf("qualification fixed command failed: %w", e)
	}
	if stderr.Len() != 0 {
		return nil, fmt.Errorf("qualification fixed command stderr")
	}
	return out.Bytes(), qualificationContext(ctx)
}
func qualificationHostBoot(ctx context.Context) (string, error) {
	b, e := qualificationHostExec(ctx, "/usr/sbin/sysctl", "-n", "kern.bootsessionuuid")
	if e != nil {
		return "", e
	}
	v := strings.TrimSpace(string(b))
	if !qualificationUUIDPattern.MatchString(v) {
		return "", errQualification
	}
	return v, nil
}
func qualificationStableHostPaths(a, b qualificationHostPaths) bool {
	if a.Mount != b.Mount || a.VolumeUUID != b.VolumeUUID || a.StatDevice != b.StatDevice || a.StatInode != b.StatInode || len(a.Writers) != len(b.Writers) {
		return false
	}
	for i := range a.Writers {
		x, y := a.Writers[i], b.Writers[i]
		x.AllocatedBlocks512 = 0
		y.AllocatedBlocks512 = 0
		if x != y {
			return false
		}
	}
	return true
}
func qualificationHostObservePaths(ctx context.Context, s qualificationStaged) (qualificationHostPaths, error) {
	h := s.Source.Inputs.Host
	root, e := qualificationHostStat(ctx, h.Mount)
	if e != nil {
		return qualificationHostPaths{}, e
	}
	out := qualificationHostPaths{Mount: h.Mount, VolumeUUID: h.VolumeUUID, StatDevice: uint64(root.Dev), StatInode: root.Ino}
	for _, w := range h.Writers {
		var artifact *qualificationWriterArtifact
		for i := range s.Writers {
			if s.Writers[i].ID == w.ID {
				if artifact != nil {
					return out, errQualification
				}
				artifact = &s.Writers[i]
			}
		}
		if artifact == nil || w.ArtifactDigest != qualificationDigest(*artifact) {
			return out, fmt.Errorf("writer provenance missing")
		}
		a := *artifact
		// Executable provenance is an explicit frozen build. Configuration is an
		// immutable source input. Mutable disk/state paths are never content-hashed.
		built := false
		configPinned := false
		for _, b := range s.Source.Builds {
			if b.Role == "writer-"+w.ID && b.BinaryDigest == a.ExecutableDigest && b.InputRoot == qualificationDigest(s.Source.Files) {
				built = true
			}
		}
		for _, f := range s.Source.Files {
			if f.Digest == a.ConfigDigest {
				configPinned = true
			}
		}
		if !built || !configPinned {
			return out, errQualification
		}
		x, e := qualificationHashFile(ctx, a.Executable, 512<<20)
		if e != nil || x != a.ExecutableDigest {
			return out, fmt.Errorf("writer executable differs")
		}
		x, e = qualificationHashFile(ctx, a.Config, MaxBytes)
		if e != nil || x != a.ConfigDigest {
			return out, fmt.Errorf("writer config differs")
		}
		r := qualificationWriterReadback{ID: w.ID, Path: w.Path, ArtifactDigest: w.ArtifactDigest, Policy: w.Policy}
		if w.Policy == "volume_mapped" {
			st, e := qualificationHostStat(ctx, w.Path)
			if e != nil || uint64(st.Dev) != out.StatDevice || st.Blocks < 0 {
				return out, fmt.Errorf("writer path not on frozen volume")
			}
			r.StatDevice = uint64(st.Dev)
			r.StatInode = st.Ino
			r.AllocatedBlocks512 = uint64(st.Blocks)
			r.VolumeUUID = h.VolumeUUID
		}
		out.Writers = append(out.Writers, r)
	}
	return out, nil
}
func qualificationHostStat(ctx context.Context, p string) (unix.Stat_t, error) {
	var out unix.Stat_t
	parent, e := qualificationOpenArtifactParent(ctx, filepath.Dir(p))
	if e != nil {
		return out, e
	}
	defer parent.Close()
	if e = unix.Fstatat(int(parent.Fd()), filepath.Base(p), &out, unix.AT_SYMLINK_NOFOLLOW); e != nil {
		return out, e
	}
	if out.Mode&unix.S_IFMT == unix.S_IFLNK || out.Ino == 0 {
		return out, errQualification
	}
	return out, qualificationContext(ctx)
}
