package capacitysnapshot

import (
	"bytes"
	"reflect"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"github.com/abietic/switchback/internal/platform/qualificationio"
)

// Permission fields have the same meaning in both PID namespaces. Pid/Tgid
// do not: they are checked through the raw NSpid/NStgid maps and stat tuples.
func qualificationSamePermissionStatus(a, b procstatus.Status) bool {
	return a.UIDs == b.UIDs && a.GIDs == b.GIDs && a.CapInh == b.CapInh &&
		a.CapPrm == b.CapPrm && a.CapEff == b.CapEff && a.CapBnd == b.CapBnd &&
		a.CapAmb == b.CapAmb && a.NoNewPrivs == b.NoNewPrivs && a.Seccomp == b.Seccomp
}

func qualificationBackendIdentityStatus(p qualificationio.ProcessEvidence, mask uint64, cid, boot string) error {
	if p.PID == 0 || p.PID != p.TGID || p.TID == 0 || p.NamespacePID == 0 ||
		p.NamespacePID != p.NamespaceTGID || p.NamespaceTID == 0 || p.StartTicks == 0 ||
		p.ThreadStartTicks == 0 || !containerIDPattern.MatchString(cid) || p.Boot != boot ||
		!qualificationUUIDPattern.MatchString(boot) {
		return errQualification
	}
	h, he := procstatus.Parse(p.HostBefore.Status)
	l, le := procstatus.Parse(p.NamespaceBefore.Status)
	hpid, hparent, hstart, hs := qualificationProcStat(p.HostBefore.Stat)
	htid, htparent, htstart, hts := qualificationProcStat(p.HostBefore.ThreadStat)
	lpid, lparent, lstart, ls := qualificationProcStat(p.NamespaceBefore.Stat)
	ltid, ltparent, ltstart, lts := qualificationProcStat(p.NamespaceBefore.ThreadStat)
	if he != nil || le != nil || hs != nil || hts != nil || ls != nil || lts != nil ||
		!roleMatches(h, mask) || !roleMatches(l, mask) || !qualificationSamePermissionStatus(h, l) ||
		h.PID != p.PID || h.TGID != p.TGID || h.TID != p.TID ||
		l.PID != p.NamespacePID || l.TGID != p.NamespaceTGID || l.TID != p.NamespaceTID ||
		hpid != p.PID || htid != p.TID || lpid != p.NamespacePID || ltid != p.NamespaceTID ||
		hparent != htparent || lparent != ltparent ||
		hstart != p.StartTicks || lstart != p.StartTicks ||
		htstart != p.ThreadStartTicks || ltstart != p.ThreadStartTicks ||
		!qualificationMapping(p.HostBefore.Status, p.NamespaceBefore.Status, "NSpid", p.TID, p.NamespaceTID) ||
		!qualificationMapping(p.HostBefore.Status, p.NamespaceBefore.Status, "NStgid", p.PID, p.NamespacePID) ||
		!bytes.Equal(p.HostBefore.Cgroup, []byte("0::/switchback.slice/switchback-workers.slice/docker-"+cid+".scope\n")) ||
		(!bytes.Equal(p.NamespaceBefore.Cgroup, []byte("0::/\n")) && !bytes.Equal(p.NamespaceBefore.Cgroup, p.HostBefore.Cgroup)) {
		return errQualification
	}
	return nil
}

func qualificationValidateWorkerLeader(raw, stat []byte, p qualificationio.ProcessEvidence, cid, boot string) error {
	if !containerIDPattern.MatchString(cid) || p.PID == 0 || p.Boot != boot || !qualificationUUIDPattern.MatchString(boot) ||
		!bytes.Equal(p.HostBefore.Cgroup, []byte("0::/switchback.slice/switchback-workers.slice/docker-"+cid+".scope\n")) {
		return errQualification
	}
	leader, le := procstatus.Parse(raw)
	thread, te := procstatus.Parse(p.HostBefore.Status)
	pid, _, ticks, se := qualificationProcStat(stat)
	nspid, pe := qualificationNamespaceIDs(raw, "NSpid")
	nstgid, ge := qualificationNamespaceIDs(raw, "NStgid")
	if le != nil || te != nil || se != nil || pe != nil || ge != nil ||
		leader.PID != p.PID || leader.TGID != p.PID || leader.TID != p.PID ||
		pid != p.PID || ticks != p.StartTicks || !qualificationSamePermissionStatus(leader, thread) ||
		len(nspid) < 2 || !reflect.DeepEqual(nspid, nstgid) || nspid[0] != p.PID ||
		nspid[len(nspid)-1] != p.NamespacePID ||
		!qualificationMapping(raw, p.NamespaceBefore.Status, "NStgid", p.PID, p.NamespacePID) {
		return errQualification
	}
	return nil
}
