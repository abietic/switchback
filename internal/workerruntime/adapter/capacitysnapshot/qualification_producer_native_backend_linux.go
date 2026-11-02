//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"github.com/abietic/switchback/internal/platform/qualificationio"
	"golang.org/x/sys/unix"
)

type qualificationBackendRunner struct {
	engine    qualificationEngine
	dir       qualificationCandidateDirectory
	candidate qualificationCandidateRecord
	window    qualificationWindowOpen
	test      qualificationTestDirectory
	mountRoot string
	cleanups  []qualificationHelperCleanup
}

func (r *qualificationBackendRunner) start(ctx context.Context, name string, target, child qualificationio.Object) (*qualificationHelperSession, error) {
	root := qualificationControlObject(r.test.Identity)
	spec := qualificationHelperSpec{Case: name, Candidate: r.candidate, Window: r.window, TestRoot: r.test.Path}
	return qualificationStartHelper(ctx, r.engine, r.dir, spec, r.mountRoot, root, target, child)
}

func qualificationBackendFrame(ctx context.Context, s *qualificationHelperSession, seq uint64, phase, testCase string,
	targetFD, childFD int, targetPath, childPath string) (qualificationio.Frame, qualificationio.ProcessEvidence, error) {
	var process qualificationio.ProcessEvidence
	f, err := s.attached.receive(seq, phase, testCase)
	if err != nil {
		return f, process, err
	}
	if f.Process == nil || f.Backend == nil || f.Target == nil || f.Parent != nil || f.Write != nil || f.Setter != nil || f.ELF != nil || f.ExecErrno != 0 || f.UnsupportedCause != "" {
		return f, process, errQualification
	}
	if targetFD < 3 || (childFD >= 3) != (f.Child != nil) {
		return f, process, errQualification
	}
	mask := uint64(0xb)
	switch testCase {
	case "without-chown":
		mask = 0xa
	case "without-dac-override":
		mask = 0x9
	case "without-fowner":
		mask = 0x3
	}
	st, err := procstatus.Parse(f.Process.Snapshot.Status)
	if err != nil || !roleMatches(st, mask) {
		return f, process, errQualification
	}
	process, err = qualificationCaptureProcess(ctx, s.attached.Control, *f.Process, 0)
	if err != nil {
		return f, process, err
	}
	if err := qualificationBackendIdentityStatus(process, mask, s.cid, s.boot); err != nil {
		return f, process, errQualification
	}
	if _, err = qualificationReadFDInfo(ctx, process.PID, f.Target.FD); err != nil {
		return f, process, err
	}
	// The limited-DAC case uses O_PATH. Its attributes are explicitly
	// unavailable; the retained full-cap holder FD supplies that object's
	// independent host identity and entries instead.
	if testCase != "without-dac-override" {
		dup, live, e := qualificationDuplicateHeldFD(ctx, process.PID, *f.Target)
		if e != nil {
			return f, process, e
		}
		unix.Close(dup)
		host, e := qualificationNativeFD(ctx, targetFD, targetPath)
		if e != nil || host.Device != live.Device || host.FSUUID != live.FSUUID || host.Inode != live.Inode || host.Owner != live.Owner || host.Mode != live.Mode || host.ProjectID != live.ProjectID {
			return f, process, errQualification
		}
	} else {
		host, e := qualificationNativeFD(ctx, targetFD, targetPath)
		if e != nil || host.Device != f.Target.Device || host.Inode != f.Target.Inode || host.Owner != f.Target.Owner || host.Mode != f.Target.Mode {
			return f, process, errQualification
		}
	}
	if childFD >= 3 {
		if _, err = qualificationReadFDInfo(ctx, process.PID, f.Child.FD); err != nil {
			return f, process, err
		}
		host, e := qualificationNativeFDAny(ctx, childFD)
		allowedPath := host.Path == childPath
		if phase == "backend-after" && (testCase == "private" || testCase == "sticky") {
			allowedPath = host.Path == childPath+" (deleted)"
		}
		if e != nil || !allowedPath || host.Device != f.Child.Device || host.Inode != f.Child.Inode || host.Owner != f.Child.Owner || host.Mode != f.Child.Mode {
			return f, process, errQualification
		}
	}
	return f, process, qualificationContext(ctx)
}

func qualificationBackendObjects(ctx context.Context, parent int, parentPath, name string, child bool) (int, int, qualificationio.Object, qualificationio.Object, error) {
	targetFD, target, err := qualificationCreateDirectoryAt(ctx, parent, parentPath, name, 0755)
	if err != nil {
		return -1, -1, qualificationio.Object{}, qualificationio.Object{}, err
	}
	childFD := -1
	var childIdentity qualificationio.FDIdentity
	if child {
		childFD, childIdentity, err = qualificationCreateFileAt(ctx, targetFD, filepath.Join(parentPath, name))
		if err != nil {
			unix.Close(targetFD)
			return -1, -1, qualificationio.Object{}, qualificationio.Object{}, err
		}
	}
	return targetFD, childFD, qualificationControlObject(target), qualificationControlObject(childIdentity), nil
}

func qualificationPositiveForNegative(name string) string {
	switch name {
	case "nonempty":
		return "without-chown"
	case "private":
		return "without-dac-override"
	case "sticky":
		return "without-fowner"
	}
	return ""
}

// Every positive holder owns a fresh target and, where specified, its one
// child. The reduced-cap helper runs against that same held inode before the
// holder's positive operation; the holder supplies unavailable private-entry
// facts independently, rather than a synthetic zero count.
func (r *qualificationBackendRunner) caseRun(ctx context.Context, parentFD int, parentPath, name string) (qualificationHandoffCase, *qualificationHandoffCase, error) {
	var zero qualificationHandoffCase
	targetFD, childFD, target, child, err := qualificationBackendObjects(ctx, parentFD, parentPath, name, name != "empty")
	if err != nil {
		return zero, nil, err
	}
	defer unix.Close(targetFD)
	if childFD >= 3 {
		defer unix.Close(childFD)
	}
	targetPath := filepath.Join(parentPath, name)
	childPath := filepath.Join(targetPath, "io")
	holder, err := r.start(ctx, name, target, child)
	if err != nil {
		return zero, nil, err
	}
	before, bp, err := qualificationBackendFrame(ctx, holder, 0, "backend-before", name, targetFD, childFD, targetPath, childPath)
	if err != nil {
		return zero, nil, err
	}
	if before.Backend.Errno != 0 || !before.Backend.EntriesAvailable || before.Backend.Operation == "" {
		return zero, nil, errQualification
	}
	var negative *qualificationHandoffCase
	if negName := qualificationPositiveForNegative(name); negName != "" {
		freshTarget, err := qualificationNativeFD(ctx, targetFD, targetPath)
		if err != nil {
			return zero, nil, err
		}
		freshChild, err := qualificationNativeFD(ctx, childFD, childPath)
		if err != nil {
			return zero, nil, err
		}
		neg, err := r.start(ctx, negName, qualificationControlObject(freshTarget), qualificationControlObject(freshChild))
		if err != nil {
			return zero, nil, err
		}
		negBefore, np, err := qualificationBackendFrame(ctx, neg, 0, "backend-before", negName, targetFD, -1, targetPath, "")
		if err != nil {
			return zero, nil, err
		}
		if err = neg.attached.send(1, "backend-operate", negName); err != nil {
			return zero, nil, err
		}
		negAfter, _, err := qualificationBackendFrame(ctx, neg, 1, "backend-after", negName, targetFD, -1, targetPath, "")
		if err != nil || negAfter.Backend.Errno == 0 {
			return zero, nil, errQualification
		}
		if err = qualificationFinishProcess(ctx, &np, *negAfter.Process); err != nil {
			return zero, nil, err
		}
		if err = holder.attached.send(1, "backend-observe", name); err != nil {
			return zero, nil, err
		}
		observed, _, err := qualificationBackendFrame(ctx, holder, 1, "backend-observed", name, targetFD, childFD, targetPath, childPath)
		if err != nil || observed.Backend.Errno != 0 || !observed.Backend.EntriesAvailable || !observed.Backend.ChildAvailable || observed.Target.Inode != before.Target.Inode || observed.Backend.Entries != before.Backend.Entries || observed.Backend.ChildInode != before.Backend.ChildInode {
			return zero, nil, errQualification
		}
		if err = neg.attached.send(2, "backend-release", negName); err != nil {
			return zero, nil, err
		}
		clean, err := neg.finish(ctx, r.engine)
		if err != nil {
			return zero, nil, err
		}
		r.cleanups = append(r.cleanups, clean)
		nc := qualificationHandoffCase{ID: negName, BinaryDigest: neg.attached.Control.BinaryDigest, Device: observed.Target.Device,
			TargetInode: observed.Target.Inode, ChildInode: observed.Backend.ChildInode, ChildOwner: observed.Backend.ChildOwner,
			Operation: negAfter.Backend.Operation, RequestedOwner: negAfter.Backend.RequestedOwner, Status: np.HostBefore.Status,
			OwnerBefore: before.Target.Owner, OwnerAfter: observed.Target.Owner, EntriesBefore: before.Backend.Entries,
			EntriesAfter: observed.Backend.Entries, Mode: uint64(freshTarget.Mode & 07777), Errno: negAfter.Backend.Errno}
		// The reduced helper's observations must have the exact same object and
		// intent. Its unavailable DAC fields are not substituted into nc.
		if negBefore.Target.Inode != before.Target.Inode || negAfter.Target.Inode != before.Target.Inode || negAfter.Backend.Operation != nc.Operation || negBefore.Backend.Operation != nc.Operation || np.PID == 0 {
			return zero, nil, errQualification
		}
		negative = &nc
	} else {
		if err = holder.attached.send(1, "backend-observe", name); err != nil {
			return zero, nil, err
		}
		observed, _, e := qualificationBackendFrame(ctx, holder, 1, "backend-observed", name, targetFD, childFD, targetPath, childPath)
		if e != nil || !reflect.DeepEqual(observed.Backend, before.Backend) {
			return zero, nil, errQualification
		}
	}
	if err = holder.attached.send(2, "backend-operate", name); err != nil {
		return zero, nil, err
	}
	after, _, err := qualificationBackendFrame(ctx, holder, 2, "backend-after", name, targetFD, childFD, targetPath, childPath)
	if err != nil || after.Backend.Errno != 0 {
		return zero, nil, errQualification
	}
	if err = qualificationFinishProcess(ctx, &bp, *after.Process); err != nil {
		return zero, nil, err
	}
	pc := qualificationHandoffCase{ID: name, BinaryDigest: holder.attached.Control.BinaryDigest, Device: before.Target.Device,
		TargetInode: before.Target.Inode, Operation: after.Backend.Operation, RequestedOwner: after.Backend.RequestedOwner,
		Status: bp.HostBefore.Status, OwnerBefore: before.Target.Owner, OwnerAfter: after.Target.Owner,
		EntriesBefore: before.Backend.Entries, EntriesAfter: after.Backend.Entries,
		Mode: uint64(before.Target.Mode & 07777), Errno: after.Backend.Errno}
	if childFD >= 3 {
		pc.ChildInode, pc.ChildOwner = before.Backend.ChildInode, before.Backend.ChildOwner
	}
	if err = holder.attached.send(3, "backend-cleanup", name); err != nil {
		return zero, nil, err
	}
	cleaned, err := holder.attached.receive(3, "backend-cleaned", name)
	if err != nil || cleaned.Target == nil || cleaned.Target.Inode != before.Target.Inode || cleaned.Backend != nil {
		return zero, nil, errQualification
	}
	if err = holder.attached.send(4, "backend-release", name); err != nil {
		return zero, nil, err
	}
	clean, err := holder.finish(ctx, r.engine)
	if err != nil {
		return zero, nil, err
	}
	r.cleanups = append(r.cleanups, clean)
	return pc, negative, nil
}

func (r *qualificationBackendRunner) handoffCases(ctx context.Context) (qualificationHandoff, error) {
	var out qualificationHandoff
	parentFD, _, err := qualificationCreateDirectoryAt(ctx, r.test.Root, r.test.Path, "handoff", 0755)
	if err != nil {
		return out, err
	}
	defer unix.Close(parentFD)
	for _, name := range []string{"empty", "nonempty", "private", "sticky"} {
		positive, negative, err := r.caseRun(ctx, parentFD, filepath.Join(r.test.Path, "handoff"), name)
		if err != nil {
			return out, fmt.Errorf("qualification backend %s: %w", name, err)
		}
		out.Cases = append(out.Cases, positive)
		if negative != nil {
			out.Cases = append(out.Cases, *negative)
		}
	}
	if err := unix.Unlinkat(r.test.Root, "handoff", unix.AT_REMOVEDIR); err != nil {
		return out, err
	}
	return out, qualificationContext(ctx)
}
