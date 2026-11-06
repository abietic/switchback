package capacitysnapshot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/qualificationio"
)

func qualificationThreadedStatus(hostPID, hostTID, nsPID, nsTID, caps, uid uint64, local bool) []byte {
	pid, tid := hostPID, hostTID
	nspid, nstgid := fmt.Sprintf("%d %d", hostTID, nsTID), fmt.Sprintf("%d %d", hostPID, nsPID)
	if local {
		pid, tid = nsPID, nsTID
		nspid, nstgid = fmt.Sprint(nsTID), fmt.Sprint(nsPID)
	}
	return []byte(fmt.Sprintf("Pid:\t%d\nTgid:\t%d\nUid:\t%d %d %d %d\nGid:\t%d %d %d %d\nCapInh:\t0000000000000000\nCapPrm:\t%016x\nCapEff:\t%016x\nCapBnd:\t%016x\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\nNSpid:\t%s\nNStgid:\t%s\n",
		tid, pid, uid, uid, uid, uid, uid, uid, uid, uid, caps, caps, caps, nspid, nstgid))
}

func TestQualificationBackendHostLocalThreadIdentity(t *testing.T) {
	p := qualificationio.ProcessEvidence{PID: 200, TID: 201, TGID: 200, NamespacePID: 1, NamespaceTID: 2, NamespaceTGID: 1,
		StartTicks: 1000, ThreadStartTicks: 1100, Boot: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
	cid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	p.HostBefore = qualificationio.ProcSnapshot{Stat: qualificationV2Stat(200, 90, 1000), ThreadStat: qualificationV2Stat(201, 90, 1100), Status: qualificationThreadedStatus(200, 201, 1, 2, 0xb, 0, false)}
	p.NamespaceBefore = qualificationio.ProcSnapshot{Stat: qualificationV2Stat(1, 0, 1000), ThreadStat: qualificationV2Stat(2, 0, 1100), Status: qualificationThreadedStatus(200, 201, 1, 2, 0xb, 0, true)}
	p.HostBefore.Cgroup = []byte("0::/switchback.slice/switchback-workers.slice/docker-" + cid + ".scope\n")
	p.NamespaceBefore.Cgroup = []byte("0::/\n")
	if err := qualificationBackendIdentityStatus(p, 0xb, cid, p.Boot); err != nil {
		t.Fatal("valid host/local PID and TID mapping rejected", err)
	}
	for name, mutate := range map[string]func(*qualificationio.ProcessEvidence){
		"namespace mapping": func(x *qualificationio.ProcessEvidence) {
			x.HostBefore.Status = qualificationThreadedStatus(200, 201, 1, 3, 0xb, 0, false)
		},
		"thread id":    func(x *qualificationio.ProcessEvidence) { x.TID++ },
		"thread start": func(x *qualificationio.ProcessEvidence) { x.ThreadStartTicks++ },
		"capability drift": func(x *qualificationio.ProcessEvidence) {
			x.NamespaceBefore.Status = qualificationThreadedStatus(200, 201, 1, 2, 0xa, 0, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := qualificationClone(p)
			mutate(&x)
			if err := qualificationBackendIdentityStatus(x, 0xb, cid, p.Boot); err == nil {
				t.Fatal("identity drift accepted")
			}
		})
	}
}

func qualificationThreadedFixture(t *testing.T) (Trust, map[string][]byte) {
	t.Helper()
	tr, f := qualificationFixture(t)
	var native qualificationNativeReceipt
	if err := json.Unmarshal(f["native-enforcement-receipt.json"], &native); err != nil {
		t.Fatal(err)
	}
	var loaded qualificationLoaded
	var setters qualificationSettersV2
	for _, check := range native.Checks {
		switch check.ID {
		case "loaded-identity":
			if err := strictJSON(check.Observation.Data, &loaded); err != nil {
				t.Fatal(err)
			}
		case "worker-setters":
			var err error
			setters, err = decodeQualificationSettersV2(check.Observation.Data)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	loaded.Worker.StatusBefore = qualificationThreadedStatus(42, 42, 1, 1, 0, 10002, false)
	loaded.Worker.StatusAfter = append([]byte(nil), loaded.Worker.StatusBefore...)
	for i := range setters.ABIs {
		s := &setters.ABIs[i]
		p := &s.ParentProcess
		p.TID, p.NamespaceTID, p.ThreadStartTicks = 43, 2, 124
		for _, snap := range []*qualificationio.ProcSnapshot{&p.HostBefore, &p.HostAfter} {
			snap.ThreadStat = qualificationV2Stat(43, 2, 124)
			snap.Status = qualificationThreadedStatus(42, 43, 1, 2, 0, 10002, false)
		}
		for _, snap := range []*qualificationio.ProcSnapshot{&p.NamespaceBefore, &p.NamespaceAfter} {
			snap.ThreadStat = qualificationV2Stat(2, 0, 124)
			snap.Status = qualificationThreadedStatus(42, 43, 1, 2, 0, 10002, true)
		}
		s.Worker = qualificationio.WorkerParent(loaded.Worker)
	}
	for i, check := range native.Checks {
		switch check.ID {
		case "loaded-identity":
			native.Checks[i] = qualificationCheckValue(check.ID, loaded)
		case "worker-setters":
			native.Checks[i] = qualificationCheckValue(check.ID, setters)
		}
	}
	f["native-enforcement-receipt.json"] = qualificationJSON(native)
	return qualificationRebind(tr, f), f
}

func TestQualificationThreadedWorkerFullValidator(t *testing.T) {
	tr, f := qualificationThreadedFixture(t)
	if _, err := validateDeploymentQualification(tr, f); err != nil {
		t.Fatal("valid nonleader emitting thread rejected", err)
	}
}

func TestQualificationWorkerLeaderReadbackBindsThread(t *testing.T) {
	_, f := qualificationThreadedFixture(t)
	var native qualificationNativeReceipt
	if err := strictJSON(f["native-enforcement-receipt.json"], &native); err != nil {
		t.Fatal(err)
	}
	var loaded qualificationLoaded
	var setters qualificationSettersV2
	for _, check := range native.Checks {
		switch check.ID {
		case "loaded-identity":
			if err := strictJSON(check.Observation.Data, &loaded); err != nil {
				t.Fatal(err)
			}
		case "worker-setters":
			var err error
			setters, err = decodeQualificationSettersV2(check.Observation.Data)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	p := setters.ABIs[0].ParentProcess
	if err := qualificationValidateWorkerLeader(loaded.Worker.StatusBefore, p.HostBefore.Stat, p, loaded.Worker.ContainerID, loaded.Worker.Boot); err != nil {
		t.Fatal("actual leader status rejected", err)
	}
	bad := []byte(strings.Replace(string(loaded.Worker.StatusBefore), "NSpid:\t42 1\n", "NSpid:\t42 3\n", 1))
	if err := qualificationValidateWorkerLeader(bad, p.HostBefore.Stat, p, loaded.Worker.ContainerID, loaded.Worker.Boot); err == nil {
		t.Fatal("wrong leader namespace mapping accepted")
	}
	if err := qualificationValidateWorkerLeader(loaded.Worker.StatusBefore, qualificationV2Stat(42, 2, 124), p, loaded.Worker.ContainerID, loaded.Worker.Boot); err == nil {
		t.Fatal("wrong leader start accepted")
	}
}
