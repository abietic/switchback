package qualificationio

import (
	"fmt"
	"strings"
	"testing"
)

func syntheticStat(pid uint64) []byte {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[1] = "1"
	fields[19] = "7"
	return []byte(fmt.Sprintf("%d (fixture) %s\n", pid, strings.Join(fields, " ")))
}
func syntheticLocal(c Control) LocalProcess {
	return LocalProcess{PID: 41, TGID: 41, TID: 42, StartTicks: 7, ThreadStartTicks: 7, Boot: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Snapshot: ProcSnapshot{Stat: syntheticStat(41), ThreadStat: syntheticStat(42), Status: []byte("Pid:\t42\nTgid:\t41\nUid:\t10002 10002 10002 10002\nGid:\t10002 10002 10002 10002\nCapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000000000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n"), Cgroup: []byte("0::/\n"), MountInfo: []byte("19 1 8:1 " + c.MountRoot + " /qualification-data rw,relatime - xfs /dev/vda rw\n"), ExeDigest: c.BinaryDigest}}
}
func TestProcessIdentityRawDriftAndMountRootMapping(t *testing.T) {
	c := testControl()
	c.HostTestRoot = "/mnt/lima-sbquota/product/.qualification/" + c.WindowID
	c.MountRoot = "/product/.qualification/" + c.WindowID
	p := syntheticLocal(c)
	if err := verifyProcess(c, p, 0, c.BinaryDigest, true); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*LocalProcess){"uid": func(p *LocalProcess) {
		p.Snapshot.Status = []byte(strings.ReplaceAll(string(p.Snapshot.Status), "10002", "0"))
	}, "cap": func(p *LocalProcess) {
		p.Snapshot.Status = []byte(strings.Replace(string(p.Snapshot.Status), "CapEff:\t0000000000000000", "CapEff:\t0000000000000001", 1))
	}, "nnp": func(p *LocalProcess) {
		p.Snapshot.Status = []byte(strings.Replace(string(p.Snapshot.Status), "NoNewPrivs:\t1", "NoNewPrivs:\t0", 1))
	}, "seccomp": func(p *LocalProcess) {
		p.Snapshot.Status = []byte(strings.Replace(string(p.Snapshot.Status), "Seccomp:\t2", "Seccomp:\t0", 1))
	}, "pid": func(p *LocalProcess) { p.PID++ }, "ticks": func(p *LocalProcess) { p.ThreadStartTicks++ }, "cgroup": func(p *LocalProcess) { p.Snapshot.Cgroup = []byte("0::/unrelated\n") }, "filesystem-relative-root": func(p *LocalProcess) {
		p.Snapshot.MountInfo = []byte(strings.Replace(string(p.Snapshot.MountInfo), c.MountRoot, c.HostTestRoot, 1))
	}, "readonly-bind": func(p *LocalProcess) {
		p.Snapshot.MountInfo = []byte(strings.Replace(string(p.Snapshot.MountInfo), " rw,", " ro,", 1))
	}, "wrong-filesystem": func(p *LocalProcess) {
		p.Snapshot.MountInfo = []byte(strings.Replace(string(p.Snapshot.MountInfo), "- xfs ", "- ext4 ", 1))
	}} {
		t.Run(name, func(t *testing.T) {
			bad := p
			change(&bad)
			if verifyProcess(c, bad, 0, c.BinaryDigest, true) == nil {
				t.Fatal("identity drift accepted")
			}
		})
	}
}
