//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abietic/switchback/internal/platform/qualificationio"
	"golang.org/x/sys/unix"
)

// Resolve the actual guest PID/TID through the cgroup and NSpid/NStgid maps.
// The helper's local PID is never used as a guest PID without this readback.
func qualificationResolveLocal(ctx context.Context, c qualificationio.Control, local qualificationio.LocalProcess) (uint64, uint64, error) {
	if local.PID == 0 || local.TID == 0 || local.TGID != local.PID || local.Boot == "" {
		return 0, 0, errQualification
	}
	entries, err := os.ReadDir("/proc")
	if err != nil || len(entries) > 65536 {
		return 0, 0, errQualification
	}
	wantGroup := "0::/switchback.slice/switchback-workers.slice/docker-" + c.ContainerID + ".scope\n"
	var selectedPID, selectedTID uint64
	for _, ent := range entries {
		if err := qualificationContext(ctx); err != nil {
			return 0, 0, err
		}
		pid, err := strconv.ParseUint(ent.Name(), 10, 32)
		if err != nil || pid == 0 {
			continue
		}
		group, err := qualificationReadProc(ctx, pid, "cgroup", 4096)
		if err != nil || string(group) != wantGroup {
			continue
		}
		digest, err := qualificationHashProcExecutable(ctx, pid)
		if err != nil {
			return 0, 0, err
		}
		if digest != local.Snapshot.ExeDigest {
			continue
		}
		threads, err := os.ReadDir(qualificationProcPath(pid, "task"))
		if err != nil || len(threads) > 512 {
			return 0, 0, errQualification
		}
		for _, task := range threads {
			tid, err := strconv.ParseUint(task.Name(), 10, 32)
			if err != nil || tid == 0 {
				continue
			}
			status, err := qualificationReadTask(ctx, pid, tid, "status", 65536)
			if err != nil {
				return 0, 0, err
			}
			nsPID, pe := qualificationNamespaceIDs(status, "NSpid")
			nsTGID, te := qualificationNamespaceIDs(status, "NStgid")
			if pe != nil || te != nil {
				return 0, 0, errQualification
			}
			if nsPID[len(nsPID)-1] != local.TID || nsTGID[len(nsTGID)-1] != local.TGID {
				continue
			}
			if selectedPID != 0 {
				return 0, 0, errQualification
			}
			selectedPID, selectedTID = pid, tid
		}
	}
	if selectedPID == 0 || selectedTID == 0 {
		return 0, 0, errQualification
	}
	return selectedPID, selectedTID, nil
}

func qualificationReadTask(ctx context.Context, pid, tid uint64, leaf string, limit int64) ([]byte, error) {
	if pid == 0 || tid == 0 || pid > 1<<31-1 || tid > 1<<31-1 || (leaf != "status" && leaf != "stat") || limit <= 0 || limit > 65536 {
		return nil, errQualification
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/task/%d/%s", pid, tid, leaf))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return qualificationReadBounded(ctx, f, limit)
}

func qualificationReadBounded(ctx context.Context, f *os.File, limit int64) ([]byte, error) {
	if err := qualificationContext(ctx); err != nil {
		return nil, err
	}
	b := make([]byte, limit+1)
	n := 0
	for n < len(b) {
		if err := qualificationContext(ctx); err != nil {
			return nil, err
		}
		m, err := f.Read(b[n:])
		n += m
		if err != nil {
			if err != io.EOF {
				return nil, err
			}
			break
		}
		if m == 0 {
			return nil, errQualification
		}
	}
	if n == 0 || int64(n) > limit || b[n-1] != '\n' {
		return nil, errQualification
	}
	return b[:n], qualificationContext(ctx)
}

func qualificationHostSnapshot(ctx context.Context, pid, tid uint64, expectedDigest string) (qualificationio.ProcSnapshot, error) {
	var out qualificationio.ProcSnapshot
	var err error
	if out.Stat, err = qualificationReadProc(ctx, pid, "stat", 4096); err != nil {
		return out, err
	}
	if out.ThreadStat, err = qualificationReadTask(ctx, pid, tid, "stat", 4096); err != nil {
		return out, err
	}
	if out.Status, err = qualificationReadTask(ctx, pid, tid, "status", 65536); err != nil {
		return out, err
	}
	if out.Cgroup, err = qualificationReadProc(ctx, pid, "cgroup", 4096); err != nil {
		return out, err
	}
	if out.MountInfo, err = qualificationReadProc(ctx, pid, "mountinfo", 65536); err != nil {
		return out, err
	}
	if out.ExeDigest, err = qualificationHashProcExecutable(ctx, pid); err != nil || out.ExeDigest != expectedDigest {
		return out, errQualification
	}
	return out, qualificationContext(ctx)
}

func qualificationCaptureProcess(ctx context.Context, c qualificationio.Control, local qualificationio.LocalProcess, parentPID uint64) (qualificationio.ProcessEvidence, error) {
	var out qualificationio.ProcessEvidence
	pid, tid, err := qualificationResolveLocal(ctx, c, local)
	if err != nil {
		return out, err
	}
	if parentPID != 0 {
		if err = qualificationProcessDescendsFrom(ctx, pid, parentPID); err != nil {
			return out, err
		}
	}
	before, err := qualificationHostSnapshot(ctx, pid, tid, local.Snapshot.ExeDigest)
	if err != nil {
		return out, err
	}
	statPID, _, ticks, pe := qualificationProcStat(before.Stat)
	statTID, _, threadTicks, te := qualificationProcStat(before.ThreadStat)
	if pe != nil || te != nil || statPID != pid || statTID != tid || ticks != local.StartTicks || threadTicks != local.ThreadStartTicks {
		return out, errQualification
	}
	group := "0::/switchback.slice/switchback-workers.slice/docker-" + c.ContainerID + ".scope\n"
	if !bytes.Equal(before.Cgroup, []byte(group)) {
		return out, errQualification
	}
	out = qualificationio.ProcessEvidence{PID: pid, TID: tid, TGID: pid, StartTicks: ticks, ThreadStartTicks: threadTicks,
		NamespacePID: local.PID, NamespaceTID: local.TID, NamespaceTGID: local.TGID, Boot: local.Boot,
		HostBefore: before, NamespaceBefore: local.Snapshot}
	return out, nil
}

func qualificationFinishProcess(ctx context.Context, p *qualificationio.ProcessEvidence, local qualificationio.LocalProcess) error {
	if p == nil || p.PID == 0 || local.PID != p.NamespacePID || local.TID != p.NamespaceTID || local.StartTicks != p.StartTicks || local.ThreadStartTicks != p.ThreadStartTicks || local.Boot != p.Boot || local.Snapshot.ExeDigest != p.NamespaceBefore.ExeDigest {
		return errQualification
	}
	after, err := qualificationHostSnapshot(ctx, p.PID, p.TID, p.HostBefore.ExeDigest)
	if err != nil {
		return err
	}
	p.HostAfter, p.NamespaceAfter = after, local.Snapshot
	return qualificationContext(ctx)
}

type qualificationHeldProof struct {
	fd    int
	path  string
	first qualificationio.FDProof
}

func qualificationCaptureHeld(ctx context.Context, pid uint64, ns qualificationio.FDIdentity, hostFD int, hostPath string) (qualificationHeldProof, error) {
	var out qualificationHeldProof
	if hostFD < 3 || !absolute(hostPath) {
		return out, errQualification
	}
	dup, live, err := qualificationDuplicateHeldFD(ctx, pid, ns)
	if err != nil {
		return out, err
	}
	defer unix.Close(dup)
	host, err := qualificationNativeFD(ctx, hostFD, hostPath)
	if err != nil || host.Device != live.Device || host.FSUUID != live.FSUUID || host.Inode != live.Inode || host.Owner != live.Owner || host.Mode != live.Mode || host.ProjectID != live.ProjectID {
		return out, errQualification
	}
	fd, err := unix.Dup(hostFD)
	if err != nil {
		return out, err
	}
	host.FD = uint64(fd)
	mounts, err := qualificationReadProc(ctx, uint64(os.Getpid()), "mountinfo", 65536)
	if err != nil {
		unix.Close(fd)
		return out, err
	}
	out = qualificationHeldProof{fd: fd, path: hostPath, first: qualificationio.FDProof{HostMountInfoBefore: mounts, HostBefore: host, NamespaceBefore: ns}}
	return out, nil
}

func (h *qualificationHeldProof) finish(ctx context.Context, ns qualificationio.FDIdentity) (qualificationio.FDProof, error) {
	var zero qualificationio.FDProof
	if h.fd < 3 || h.path == "" || h.first.NamespaceBefore.FD != ns.FD {
		return zero, errQualification
	}
	host, err := qualificationNativeFD(ctx, h.fd, h.path)
	if err != nil {
		return zero, err
	}
	mounts, err := qualificationReadProc(ctx, uint64(os.Getpid()), "mountinfo", 65536)
	if err != nil {
		return zero, err
	}
	out := h.first
	out.HostMountInfoAfter, out.HostAfter, out.NamespaceAfter = mounts, host, ns
	return out, qualificationContext(ctx)
}

func (h *qualificationHeldProof) close() {
	if h.fd >= 3 {
		_ = unix.Close(h.fd)
		h.fd = -1
	}
}

func qualificationReadFDInfo(ctx context.Context, pid uint64, fd uint64) ([]byte, error) {
	if pid == 0 || fd < 3 || pid > 1<<31-1 || fd > 1<<31-1 {
		return nil, errQualification
	}
	f, err := os.Open(filepath.Join("/proc", strconv.FormatUint(pid, 10), "fdinfo", strconv.FormatUint(fd, 10)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := qualificationReadBounded(ctx, f, 4096)
	if err != nil || !strings.Contains(string(b), "mnt_id:") || !strings.Contains(string(b), "ino:") {
		return nil, errQualification
	}
	return b, nil
}
