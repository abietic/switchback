//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"path/filepath"

	"github.com/abietic/switchback/internal/platform/qualificationio"
	"golang.org/x/sys/unix"
)

type qualificationWorkerHandoff struct {
	holder                *qualificationHelperSession
	ready                 qualificationio.Frame
	process               qualificationio.ProcessEvidence
	targetFD, childFD     int
	targetPath, childPath string
	target, child         qualificationio.FDIdentity
}

func (h *qualificationWorkerHandoff) closeFDs() error {
	if h.targetFD < 3 || h.childFD < 3 {
		return errQualification
	}
	if err := unix.Close(h.childFD); err != nil {
		return err
	}
	h.childFD = -1
	if err := unix.Close(h.targetFD); err != nil {
		return err
	}
	h.targetFD = -1
	return nil
}

// Both host-view FDs are opened and retained while target and child are
// root-owned. The holder's fixed Start performs the later UID10002 handoff;
// no post-chown path traversal or extra capability is required by native.
func (r *qualificationBackendRunner) beginWorkerHandoff(ctx context.Context, inherited uint32) (qualificationWorkerHandoff, error) {
	h := qualificationWorkerHandoff{targetFD: -1, childFD: -1}
	fd, _, err := qualificationCreateDirectoryAt(ctx, r.test.Root, r.test.Path, "target", 0755)
	if err != nil {
		return h, err
	}
	h.targetFD, h.childFD = fd, -1
	h.targetPath = filepath.Join(r.test.Path, "target")
	target, err := qualificationSetProject(ctx, fd, h.targetPath, inherited, r.candidate.TestProject)
	if err != nil {
		return h, err
	}
	childFD, child, err := qualificationCreateFileAt(ctx, fd, h.targetPath)
	if err != nil || child.ProjectID != uint64(r.candidate.TestProject) || child.Device != target.Device || child.FSUUID != target.FSUUID {
		return h, errQualification
	}
	h.childFD = childFD
	h.childPath = filepath.Join(h.targetPath, "io")
	h.target, h.child = target, child
	holder, err := r.start(ctx, "worker-handoff", qualificationControlObject(target), qualificationControlObject(child))
	if err != nil {
		return h, err
	}
	h.holder = holder
	h.ready, h.process, err = qualificationBackendFrame(ctx, holder, 0, "backend-before", "worker-handoff", h.targetFD, h.childFD, h.targetPath, h.childPath)
	if err != nil || h.ready.Backend.Errno != 0 || h.ready.Target.Owner != 10002 || h.ready.Child.Owner != 10002 || h.ready.Target.ProjectID != uint64(r.candidate.TestProject) || h.ready.Child.ProjectID != uint64(r.candidate.TestProject) {
		return h, errQualification
	}
	h.target, err = qualificationNativeFD(ctx, h.targetFD, h.targetPath)
	if err != nil {
		return h, err
	}
	h.child, err = qualificationNativeFD(ctx, h.childFD, h.childPath)
	return h, err
}

func (r *qualificationBackendRunner) finishWorkerHandoff(ctx context.Context, h *qualificationWorkerHandoff) error {
	if h == nil || h.holder == nil {
		return errQualification
	}
	if err := h.holder.attached.send(1, "backend-observe", "worker-handoff"); err != nil {
		return err
	}
	observed, _, err := qualificationBackendFrame(ctx, h.holder, 1, "backend-observed", "worker-handoff", h.targetFD, h.childFD, h.targetPath, h.childPath)
	if err != nil || observed.Backend.Errno != 0 || !observed.Backend.EntriesAvailable || observed.Backend.Entries != 1 || !observed.Backend.ChildAvailable || observed.Backend.ChildInode != h.ready.Backend.ChildInode || observed.Target.Inode != h.ready.Target.Inode {
		return errQualification
	}
	if err := qualificationFinishProcess(ctx, &h.process, *observed.Process); err != nil {
		return err
	}
	if err := h.holder.attached.send(2, "backend-cleanup", "worker-handoff"); err != nil {
		return err
	}
	cleaned, err := h.holder.attached.receive(2, "backend-cleaned", "worker-handoff")
	if err != nil || cleaned.Target == nil || cleaned.Target.Inode != h.ready.Target.Inode || cleaned.Backend != nil {
		return errQualification
	}
	if err := h.holder.attached.send(3, "backend-release", "worker-handoff"); err != nil {
		return err
	}
	clean, err := h.holder.finish(ctx, r.engine)
	if err != nil {
		return err
	}
	r.cleanups = append(r.cleanups, clean)
	return h.closeFDs()
}
