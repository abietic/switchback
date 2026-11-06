//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/abietic/switchback/internal/platform/qualificationio"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
)

type qualificationWorkerRunner struct {
	backend  *qualificationBackendRunner
	handoff  *qualificationWorkerHandoff
	poolFD   int
	worker   *qualificationHelperSession
	parent   qualificationio.ProcessEvidence
	leaders  [2][]byte
	identity qualificationWorker
	quota    qualificationQuota
	setters  qualificationio.SetterPayload
}

func qualificationReadWorkerLeader(ctx context.Context, p qualificationio.ProcessEvidence, cid, boot string) ([]byte, error) {
	status, err := qualificationReadProc(ctx, p.PID, "status", 65536)
	if err != nil {
		return nil, err
	}
	stat, err := qualificationReadProc(ctx, p.PID, "stat", 4096)
	if err != nil {
		return nil, err
	}
	cgroup, err := qualificationReadProc(ctx, p.PID, "cgroup", 4096)
	if err != nil || !bytes.Equal(cgroup, p.HostBefore.Cgroup) {
		return nil, errQualification
	}
	if err := qualificationValidateWorkerLeader(status, stat, p, cid, boot); err != nil {
		return nil, err
	}
	return status, qualificationContext(ctx)
}

func (r *qualificationWorkerRunner) workerFrame(ctx context.Context, seq uint64, phase, name string) (qualificationio.Frame, error) {
	f, err := r.worker.attached.receive(seq, phase, name)
	if err != nil {
		return f, err
	}
	if f.Process == nil || f.Target == nil || f.Child == nil || f.Parent != nil || f.Backend != nil || f.ELF != nil || f.Setter != nil || f.ExecErrno != 0 || f.UnsupportedCause != "" {
		return f, errQualification
	}
	if seq == 0 && f.Write != nil || seq == 8 && f.Write != nil {
		return f, errQualification
	}
	if f.Process.Snapshot.ExeDigest != r.worker.attached.Control.BinaryDigest || f.Process.Boot != r.backend.window.GuestBoot {
		return f, errQualification
	}
	actual, err := qualificationCaptureProcess(ctx, r.worker.attached.Control, *f.Process, 0)
	if err != nil {
		return f, err
	}
	if r.parent.PID == 0 {
		inspected, err := r.backend.engine.ContainerInspect(ctx, r.worker.cid, client.ContainerInspectOptions{})
		if err != nil || inspected.Container.State == nil || !inspected.Container.State.Running || inspected.Container.State.Pid <= 0 {
			return f, errQualification
		}
		if err := qualificationProcessDescendsFrom(ctx, actual.PID, uint64(inspected.Container.State.Pid)); err != nil {
			return f, err
		}
		r.parent = actual
	} else if actual.PID != r.parent.PID || actual.StartTicks != r.parent.StartTicks || actual.TID != r.parent.TID {
		return f, errQualification
	}
	for _, item := range []struct {
		local *qualificationio.FDIdentity
		fd    int
		path  string
	}{{f.Target, r.handoff.targetFD, r.handoff.targetPath}, {f.Child, r.handoff.childFD, r.handoff.childPath}} {
		if _, err := qualificationReadFDInfo(ctx, actual.PID, item.local.FD); err != nil {
			return f, err
		}
		dup, live, err := qualificationDuplicateHeldFD(ctx, actual.PID, *item.local)
		if err != nil {
			return f, err
		}
		unix.Close(dup)
		host, err := qualificationNativeFD(ctx, item.fd, item.path)
		if err != nil || host.Device != live.Device || host.FSUUID != live.FSUUID || host.Inode != live.Inode || host.Owner != live.Owner || host.Mode != live.Mode || host.ProjectID != live.ProjectID {
			return f, errQualification
		}
	}
	return f, qualificationContext(ctx)
}

func qualificationQuotaBefore(ctx context.Context, poolFD int, h qualificationWorkerHandoff, project uint32) (qualificationQuotaTrial, error) {
	var t qualificationQuotaTrial
	target, err := qualificationNativeFD(ctx, h.targetFD, h.targetPath)
	if err != nil {
		return t, err
	}
	child, err := qualificationNativeFD(ctx, h.childFD, h.childPath)
	if err != nil {
		return t, err
	}
	quota, err := qualificationQuotaRead(ctx, poolFD, project)
	if err != nil {
		return t, err
	}
	state, err := qualificationQuotaState(ctx, poolFD)
	if err != nil {
		return t, err
	}
	v, err := qualificationQuotaValue(quota, state, project)
	if err != nil || v.HardBlocks512 == 0 || v.HardBlocks512 > 32768 || v.UsedBlocks512 > v.HardBlocks512 {
		return t, errQualification
	}
	free, err := qualificationFreeBytes(ctx, poolFD)
	if err != nil {
		return t, err
	}
	t = qualificationQuotaTrial{ObjectBefore: qualificationFDObject(target), ChildBefore: qualificationFDObject(child),
		Inode: target.Inode, ChildInode: child.Inode, ChildFSXAttr: append([]byte(nil), child.FSXAttr...),
		Device: target.Device, FSUUID: target.FSUUID, ProjectID: uint64(project), Before: v,
		QStatV: state, QuotaBefore: quota, FSXAttr: append([]byte(nil), target.FSXAttr...),
		XFlags: uint64(binary.LittleEndian.Uint32(target.FSXAttr[:4])), ChildProjectID: child.ProjectID,
		RequestedBytes: qualificationio.QuotaWriteBytes, PoolFreeBytes: free}
	return t, qualificationContext(ctx)
}

func qualificationQuotaAfter(ctx context.Context, poolFD int, h qualificationWorkerHandoff, t *qualificationQuotaTrial, written qualificationio.WriteResult) error {
	if t == nil || written.Requested != qualificationio.QuotaWriteBytes || written.Written > written.Requested {
		return errQualification
	}
	target, err := qualificationNativeFD(ctx, h.targetFD, h.targetPath)
	if err != nil {
		return err
	}
	child, err := qualificationNativeFD(ctx, h.childFD, h.childPath)
	if err != nil {
		return err
	}
	if qualificationFDObject(target) != t.ObjectBefore || qualificationFDObject(child) != t.ChildBefore {
		return errQualification
	}
	quota, err := qualificationQuotaRead(ctx, poolFD, uint32(t.ProjectID))
	if err != nil {
		return err
	}
	v, err := qualificationQuotaValue(quota, t.QStatV, uint32(t.ProjectID))
	if err != nil {
		return err
	}
	t.ObjectAfter, t.ChildAfter = qualificationFDObject(target), qualificationFDObject(child)
	t.QuotaAfter, t.After, t.WrittenBytes = quota, v, written.Written
	switch {
	case written.WriteErrno == 0 && written.SyncErrno == 0:
		t.Errno = 0
	case written.WriteErrno == uint64(unix.EDQUOT) && written.SyncErrno == 0:
		t.Errno = uint64(unix.EDQUOT)
	case written.WriteErrno == 0 && written.SyncErrno == uint64(unix.EDQUOT):
		t.Errno = uint64(unix.EDQUOT)
	default:
		return errQualification
	}
	return qualificationContext(ctx)
}

func (r *qualificationWorkerRunner) quotaRun(ctx context.Context) error {
	if r.handoff == nil || r.handoff.targetFD < 3 || r.handoff.childFD < 3 {
		return errQualification
	}
	target := qualificationControlObject(r.handoff.target)
	child := qualificationControlObject(r.handoff.child)
	spec := qualificationHelperSpec{Case: "worker", Candidate: r.backend.candidate, Window: r.backend.window, TestRoot: r.backend.test.Path}
	s, err := qualificationStartHelper(ctx, r.backend.engine, r.backend.dir, spec, r.backend.mountRoot, qualificationControlObject(r.backend.test.Identity), target, child)
	if err != nil {
		return err
	}
	r.worker = s
	ready, err := r.workerFrame(ctx, 0, "worker-ready", "worker")
	if err != nil || ready.Write != nil {
		return errQualification
	}
	r.leaders[0], err = qualificationReadWorkerLeader(ctx, r.parent, s.cid, r.backend.window.GuestBoot)
	if err != nil {
		return err
	}
	project := r.backend.candidate.TestProject
	current, err := qualificationQuotaRead(ctx, r.poolFD, project)
	if err != nil {
		return err
	}
	used := binary.LittleEndian.Uint64(current[40:48])
	if used >= 15 {
		return errQualification
	}
	deniedHard := used + 1
	if err = qualificationQuotaSetHard(ctx, r.poolFD, project, deniedHard); err != nil {
		return err
	}
	r.quota.Denied, err = qualificationQuotaBefore(ctx, r.poolFD, *r.handoff, project)
	if err != nil {
		return err
	}
	if r.quota.Denied.Before.HardBlocks512 != deniedHard || r.quota.Denied.PoolFreeBytes < 2*qualificationio.QuotaWriteBytes {
		return errQualification
	}
	if err = s.attached.send(1, "quota-denied", "quota-denied"); err != nil {
		return err
	}
	denied, err := r.workerFrame(ctx, 1, "quota-denied-after", "quota-denied")
	if err != nil || denied.Write == nil {
		return errQualification
	}
	if err = qualificationQuotaAfter(ctx, r.poolFD, *r.handoff, &r.quota.Denied, *denied.Write); err != nil || r.quota.Denied.Errno != uint64(unix.EDQUOT) || r.quota.Denied.WrittenBytes >= qualificationio.QuotaWriteBytes {
		return errQualification
	}
	if r.quota.Denied.After.UsedBlocks512 > 32736 {
		return errQualification
	}
	successHard := r.quota.Denied.After.UsedBlocks512 + 32
	if err = qualificationQuotaSetHard(ctx, r.poolFD, project, successHard); err != nil {
		return err
	}
	r.quota.Succeeded, err = qualificationQuotaBefore(ctx, r.poolFD, *r.handoff, project)
	if err != nil || r.quota.Succeeded.Before.HardBlocks512 != successHard {
		return errQualification
	}
	if err = s.attached.send(2, "quota-success", "quota-success"); err != nil {
		return err
	}
	succeeded, err := r.workerFrame(ctx, 2, "quota-success-after", "quota-success")
	if err != nil || succeeded.Write == nil {
		return errQualification
	}
	if err = qualificationQuotaAfter(ctx, r.poolFD, *r.handoff, &r.quota.Succeeded, *succeeded.Write); err != nil || r.quota.Succeeded.Errno != 0 || r.quota.Succeeded.WrittenBytes != qualificationio.QuotaWriteBytes {
		return errQualification
	}
	if r.quota.Succeeded.After.UsedBlocks512 <= r.quota.Succeeded.Before.UsedBlocks512 {
		return errQualification
	}
	if err = s.attached.send(3, "setters-begin", "worker"); err != nil {
		return err
	}
	if err = r.setterRuns(ctx); err != nil {
		return err
	}
	after, err := r.workerFrame(ctx, 8, "worker-after", "worker")
	if err != nil || after.Write != nil {
		return errQualification
	}
	if err = qualificationFinishProcess(ctx, &r.parent, *after.Process); err != nil {
		return err
	}
	r.leaders[1], err = qualificationReadWorkerLeader(ctx, r.parent, s.cid, r.backend.window.GuestBoot)
	if err != nil {
		return err
	}
	c := s.attached.Control
	r.identity = qualificationWorker{ContainerID: s.cid, ImageDigest: c.ImageDigest, BinaryDigest: c.BinaryDigest, Profile: c.Profile,
		Boot: r.backend.window.GuestBoot, PID: r.parent.PID, StartTicks: r.parent.StartTicks,
		Cgroup:       "/switchback.slice/switchback-workers.slice/docker-" + s.cid + ".scope",
		StatusBefore: r.leaders[0], StatusAfter: r.leaders[1]}
	if err = s.attached.send(8, "worker-release", "worker"); err != nil {
		return err
	}
	clean, err := s.finish(ctx, r.backend.engine)
	if err != nil {
		return err
	}
	r.backend.cleanups = append(r.backend.cleanups, clean)
	return qualificationContext(ctx)
}

func (r *qualificationWorkerRunner) setterRuns(ctx context.Context) error {
	r.setters = qualificationio.SetterPayload{HostTestRoot: r.backend.test.Path}
	for i, item := range []struct{ abi, command string }{{"native", "FS_IOC_FSSETXATTR"}, {"native", "FS_IOC_SETFLAGS"}, {"compat32", "FS_IOC_FSSETXATTR"}, {"compat32", "FS_IOC_SETFLAGS"}} {
		seq := uint64(4 + i)
		name := item.abi + ":" + item.command
		first, err := qualificationio.ReadFrame(r.worker.attached.Reader)
		if err != nil {
			return err
		}
		if first.Match(r.worker.attached.Control, seq, first.Phase, name) != nil || (first.Phase != "setter-before" && (item.abi != "compat32" || first.Phase != "setter-unsupported")) {
			return errQualification
		}
		if first.Process == nil {
			if first.Phase != "setter-unsupported" {
				return errQualification
			}
		} else if first.ELF == nil || first.Parent == nil || first.Target == nil || first.Child == nil || first.Setter != nil || first.ExecErrno != 0 || first.UnsupportedCause != "" {
			return errQualification
		}
		if first.Phase == "setter-unsupported" {
			if item.abi != "compat32" || first.ExecErrno != 8 || first.UnsupportedCause != "execve_ENOEXEC" || first.ELF == nil || first.Parent == nil || first.Process != nil || first.Setter != nil || first.Target != nil || first.Child != nil {
				return errQualification
			}
			parent, err := qualificationCaptureProcess(ctx, r.worker.attached.Control, *first.Parent, 0)
			if err != nil {
				return err
			}
			if err = qualificationFinishProcess(ctx, &parent, *first.Parent); err != nil {
				return err
			}
			r.setters.ABIs = append(r.setters.ABIs, qualificationio.SetterCase{Worker: qualificationio.WorkerParent{}, ParentProcess: parent,
				Architecture: r.backend.candidate.Source.Inputs.Architecture, ABI: item.abi, Command: item.command,
				Execution: qualificationio.SetterExecution{ELF: *first.ELF, ExecErrno: first.ExecErrno, UnsupportedCause: first.UnsupportedCause}})
			if err := r.worker.attached.send(seq, "setter-release", name); err != nil {
				return err
			}
			continue
		}
		if first.Phase != "setter-before" {
			return errQualification
		}
		parent, err := qualificationCaptureProcess(ctx, r.worker.attached.Control, *first.Parent, 0)
		if err != nil {
			return err
		}
		executor, err := qualificationCaptureProcess(ctx, r.worker.attached.Control, *first.Process, parent.PID)
		if err != nil {
			return err
		}
		targetProof, err := qualificationCaptureHeld(ctx, executor.PID, *first.Target, r.handoff.targetFD, r.handoff.targetPath)
		if err != nil {
			return err
		}
		childProof, err := qualificationCaptureHeld(ctx, executor.PID, *first.Child, r.handoff.childFD, r.handoff.childPath)
		if err != nil {
			targetProof.close()
			return err
		}
		if err = r.worker.attached.send(seq, "setter-operate", name); err != nil {
			targetProof.close()
			childProof.close()
			return err
		}
		last, err := r.worker.attached.receive(seq, "setter-after", name)
		if err != nil || last.Process == nil || last.Parent == nil || last.Target == nil || last.Child == nil || last.Setter == nil || last.ELF == nil || !reflect.DeepEqual(first.ELF, last.ELF) {
			targetProof.close()
			childProof.close()
			return errQualification
		}
		if err = qualificationFinishProcess(ctx, &parent, *last.Parent); err != nil {
			targetProof.close()
			childProof.close()
			return err
		}
		if err = qualificationFinishProcess(ctx, &executor, *last.Process); err != nil {
			targetProof.close()
			childProof.close()
			return err
		}
		target, err := targetProof.finish(ctx, *last.Target)
		if err != nil {
			targetProof.close()
			childProof.close()
			return err
		}
		child, err := childProof.finish(ctx, *last.Child)
		targetProof.close()
		childProof.close()
		if err != nil {
			return err
		}
		if !bytes.Equal(first.Target.FSXAttr, last.Target.FSXAttr) || first.Target.Inode != last.Target.Inode || first.Child.Inode != last.Child.Inode || last.Setter.Set.Errno != uint64(unix.EPERM) {
			return errQualification
		}
		r.setters.ABIs = append(r.setters.ABIs, qualificationio.SetterCase{Worker: qualificationio.WorkerParent{}, ParentProcess: parent,
			Architecture: r.backend.candidate.Source.Inputs.Architecture, ABI: item.abi, Command: item.command,
			Execution: qualificationio.SetterExecution{ELF: *first.ELF, Executors: []qualificationio.ProcessEvidence{executor},
				Facts: []qualificationio.SetterFacts{{TargetFD: target, IOFD: child, IO: *last.Setter}}}})
		if err := r.worker.attached.send(seq, "setter-release", name); err != nil {
			return err
		}
	}
	return qualificationContext(ctx)
}

func (r *qualificationWorkerRunner) setterPayload() ([]byte, error) {
	if len(r.setters.ABIs) != 4 || r.identity.PID == 0 {
		return nil, errQualification
	}
	w := qualificationio.WorkerParent{ContainerID: r.identity.ContainerID, ImageDigest: r.identity.ImageDigest,
		BinaryDigest: r.identity.BinaryDigest, Profile: r.identity.Profile, Boot: r.identity.Boot,
		PID: r.identity.PID, StartTicks: r.identity.StartTicks, Cgroup: r.identity.Cgroup,
		StatusBefore: r.identity.StatusBefore, StatusAfter: r.identity.StatusAfter}
	for i := range r.setters.ABIs {
		r.setters.ABIs[i].Worker = w
	}
	encoded, err := qualificationio.EncodeSetterPayload(r.setters)
	if err != nil {
		return nil, fmt.Errorf("qualification setter evidence invalid: %w", err)
	}
	return qualificationEncode(encoded)
}

func qualificationWorkerMountRoot(ctx context.Context, testRoot string, expected qualificationio.FDIdentity) (string, error) {
	raw, err := qualificationReadProc(ctx, uint64(unix.Getpid()), "mountinfo", 65536)
	if err != nil {
		return "", err
	}
	mounts, err := qualificationMountInfo(raw)
	if err != nil {
		return "", err
	}
	var selected qualificationMount
	for _, m := range mounts {
		if (testRoot == m.Target || strings.HasPrefix(testRoot, strings.TrimSuffix(m.Target, "/")+"/")) && len(m.Target) > len(selected.Target) {
			selected = m
		}
	}
	if selected.Target == "" || selected.MountID != expected.MountID || selected.Device != expected.Device || selected.ReadOnly || !qualificationMountXFS(raw, selected) {
		return "", errQualification
	}
	return filepath.Join(selected.Root, strings.TrimPrefix(strings.TrimPrefix(testRoot, selected.Target), "/")), nil
}
