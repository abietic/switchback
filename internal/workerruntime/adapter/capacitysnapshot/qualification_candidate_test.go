package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
)

func TestQualificationAssemblyUsesProductionValidator(t *testing.T) {
	tr, files := qualificationFixture(t)
	var source qualificationSource
	var inventory DeploymentInventory
	var binding qualificationBinding
	var host qualificationHostReceipt
	var native qualificationNativeReceipt
	for _, v := range []struct {
		name string
		dst  any
	}{{"source-manifest.json", &source}, {"deployment-inventory.json", &inventory}, {"publisher-bindings.json", &binding}, {"host-boundary-receipt.json", &host}, {"native-enforcement-receipt.json", &native}} {
		if e := strictJSON(files[v.name], v.dst); e != nil {
			t.Fatal(e)
		}
	}
	c := qualificationCandidateRecord{Schema: candidateSchema, Fingerprint: host.Candidate, TestProject: 1234, Source: source, Inventory: inventory, Trust: tr, Binding: DeploymentPublisherBinding{Core: binding.Core, WorkerImageDigest: binding.WorkerImageDigest}}
	quota := func(id uint32) []byte {
		b := make([]byte, 112)
		b[0], b[1] = 1, 2
		binary.LittleEndian.PutUint32(b[4:8], id)
		return b
	}
	cleanup := qualificationCleanup{Complete: true, Project: 1234, Initial: quota(1234), Successor: quota(1235), Final: quota(1234), RootRemoved: true, FDsClosed: true}
	for j, name := range []string{"empty", "nonempty", "private", "sticky", "without-chown", "without-dac-override", "without-fowner", "worker-handoff", "worker"} {
		cleanup.Helpers = append(cleanup.Helpers, qualificationHelperCleanup{Case: name, CID: strings.Repeat(string(rune('1'+j)), 64), Exit: 0, DeleteStatus: 204, InspectStatus: 404})
	}
	n := qualificationNativeObservation{Schema: nativeObservationSchema, Candidate: host.Candidate, WindowID: host.Window.ID, Checks: native.Checks, GuestBootBefore: host.Window.GuestBootBefore, GuestBootAfter: host.Window.GuestBootAfter, Cleanup: cleanup}
	assembled, e := assembleQualification(c, host, n)
	if e != nil {
		t.Fatal(e)
	}
	var finalTrust Trust
	if e = strictJSON(assembled["trust.json"], &finalTrust); e != nil {
		t.Fatal(e)
	}
	if _, e = validateDeploymentQualification(finalTrust, assembled); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*qualificationCandidateRecord, *qualificationHostReceipt, *qualificationNativeObservation){
		func(c *qualificationCandidateRecord, _ *qualificationHostReceipt, _ *qualificationNativeObservation) {
			c.Source.Inputs.Architecture = "wrong"
		},
		func(c *qualificationCandidateRecord, _ *qualificationHostReceipt, _ *qualificationNativeObservation) {
			c.Source.Inputs.Roles[0].Role = "wrong"
		},
		func(c *qualificationCandidateRecord, _ *qualificationHostReceipt, _ *qualificationNativeObservation) {
			c.Source.Builds[0].ImageDigest = Digest([]byte("wrong image"))
		},
		func(_ *qualificationCandidateRecord, h *qualificationHostReceipt, _ *qualificationNativeObservation) {
			h.Window.GuestBootAfter = "00000000-0000-0000-0000-000000000001"
		},
		func(_ *qualificationCandidateRecord, _ *qualificationHostReceipt, n *qualificationNativeObservation) {
			n.Cleanup.Complete = false
		},
		func(_ *qualificationCandidateRecord, _ *qualificationHostReceipt, n *qualificationNativeObservation) {
			n.WindowID = "older-window"
		},
		func(_ *qualificationCandidateRecord, _ *qualificationHostReceipt, n *qualificationNativeObservation) {
			n.Checks[0].Observation.Errno = 1
		},
		func(_ *qualificationCandidateRecord, _ *qualificationHostReceipt, n *qualificationNativeObservation) {
			n.Checks[0].Observation.Data[0] ^= 1
		},
	} {
		x, h, y := qualificationClone(c), qualificationClone(host), qualificationClone(n)
		mutate(&x, &h, &y)
		if _, e := assembleQualification(x, h, y); e == nil {
			t.Fatal("mixed/unknown material accepted")
		}
	}
}

func TestQualificationRawFactMapperToProductionAssembly(t *testing.T) {
	tr, files := qualificationFixture(t)
	var source qualificationSource
	var inventory DeploymentInventory
	var binding qualificationBinding
	var host qualificationHostReceipt
	var fixture qualificationNativeReceipt
	for _, item := range []struct {
		name string
		into any
	}{
		{"source-manifest.json", &source}, {"deployment-inventory.json", &inventory}, {"publisher-bindings.json", &binding},
		{"host-boundary-receipt.json", &host}, {"native-enforcement-receipt.json", &fixture},
	} {
		if err := strictJSON(files[item.name], item.into); err != nil {
			t.Fatal(err)
		}
	}
	facts := qualificationNativeFacts{}
	for _, check := range fixture.Checks {
		var into any
		switch check.ID {
		case "loaded-identity":
			into = &facts.Loaded
		case "namespaces-and-roles":
			into = &facts.Namespaces
		case "core-limits":
			into = &facts.Core
		case "byte-quota":
			into = &facts.Quota
		case "worker-setters":
			facts.SettersRaw = append([]byte(nil), check.Observation.Data...)
		case "backend-handoff":
			into = &facts.Handoff
		case "mounts-and-writers":
			into = &facts.Reachability
		default:
			t.Fatalf("unknown raw check %s", check.ID)
		}
		if into != nil && strictJSON(check.Observation.Data, into) != nil {
			t.Fatalf("bad raw sample %s", check.ID)
		}
	}
	start, _ := time.Parse(time.RFC3339Nano, "2026-10-22T01:00:01Z")
	end := start.Add(time.Second)
	facts.SampleStart, facts.ReadonlyStart, facts.BackendStart, facts.QuotaStart = start, start, start, start
	facts.ReadonlyEnd, facts.BackendEnd, facts.QuotaEnd = end, end, end
	checks, err := qualificationMapNativeChecks(host.Window.ID, facts)
	if err != nil {
		t.Fatal(err)
	}
	if !qualificationSameJSON(checks, fixture.Checks) {
		t.Fatal("raw sample mapper changed the seven check contract")
	}
	c := qualificationCandidateRecord{Schema: candidateSchema, Fingerprint: host.Candidate, TestProject: 1234,
		Source: source, Inventory: inventory, Trust: tr, Binding: DeploymentPublisherBinding{Core: binding.Core, WorkerImageDigest: binding.WorkerImageDigest}}
	quota := func(id uint32) []byte {
		b := make([]byte, 112)
		b[0], b[1] = 1, 2
		binary.LittleEndian.PutUint32(b[4:8], id)
		return b
	}
	cleanup := qualificationCleanup{Complete: true, Project: 1234, Initial: quota(1234), Successor: quota(1235), Final: quota(1234), RootRemoved: true, FDsClosed: true}
	for j, name := range []string{"empty", "nonempty", "private", "sticky", "without-chown", "without-dac-override", "without-fowner", "worker-handoff", "worker"} {
		cleanup.Helpers = append(cleanup.Helpers, qualificationHelperCleanup{Case: name, CID: strings.Repeat(string(rune('1'+j)), 64), DeleteStatus: 204, InspectStatus: 404})
	}
	native := qualificationNativeObservation{Schema: nativeObservationSchema, Candidate: c.Fingerprint, WindowID: host.Window.ID,
		GuestBootBefore: host.Window.GuestBootBefore, GuestBootAfter: host.Window.GuestBootAfter, Checks: checks, Cleanup: cleanup}
	assembled, err := assembleQualification(c, host, native)
	if err != nil {
		t.Fatal(err)
	}
	var final Trust
	if err := strictJSON(assembled["trust.json"], &final); err != nil {
		t.Fatal(err)
	}
	if _, err := validateDeploymentQualification(final, assembled); err != nil {
		t.Fatal(err)
	}
	facts.SettersRaw = []byte(`{"abis":[]}`)
	badChecks, err := qualificationMapNativeChecks(host.Window.ID, facts)
	if err != nil {
		t.Fatal(err)
	}
	native.Checks = badChecks
	if _, err := assembleQualification(c, host, native); err == nil {
		t.Fatal("raw setter evidence omission accepted")
	}
	facts.SettersRaw = append([]byte(nil), fixture.Checks[4].Observation.Data...)
	facts.Quota.Denied.Errno = 0
	badChecks, err = qualificationMapNativeChecks(host.Window.ID, facts)
	if err != nil {
		t.Fatal(err)
	}
	native.Checks = badChecks
	if _, err := assembleQualification(c, host, native); err == nil {
		t.Fatal("raw quota denial errno mismatch accepted")
	}
}

func TestQualificationExclusivePhaseCannotResumeFailure(t *testing.T) {
	f, e := os.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	d := qualificationCandidateDirectory{file: f, uid: uint32(os.Getuid())}
	ctx := context.Background()
	if e = d.claim(ctx, "collect"); e != nil {
		t.Fatal(e)
	}
	if e = d.claim(ctx, "collect"); e == nil {
		t.Fatal("failed phase resumed")
	}
	if e = d.write(ctx, "sample.json", []byte("{}")); e != nil {
		t.Fatal(e)
	}
	if e = d.write(ctx, "sample.json", []byte("{}")); e == nil {
		t.Fatal("leaf overwritten")
	}
	first, e := d.read(ctx, "sample.json")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(first, []byte("{}")) {
		t.Fatal("leaf differs")
	}
	if e = os.Symlink("sample.json", f.Name()+"/link.json"); e != nil {
		t.Fatal(e)
	}
	if _, e = d.read(ctx, "link.json"); e == nil {
		t.Fatal("symlink accepted")
	}
}

func TestQualificationBudgetNeverResets(t *testing.T) {
	start := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	w := qualificationWindowOpen{Schema: windowOpenSchema, ID: "one", Nonce: string(bytes.Repeat([]byte("a"), 64)), GuestBoot: "11111111-1111-1111-1111-111111111111", Started: start.Format(time.RFC3339Nano), MonotonicNS: 100}
	if _, e := qualificationRemaining(w, w.GuestBoot, 100+int64(239*time.Second), start.Add(239*time.Second)); e != nil {
		t.Fatal(e)
	}
	if _, e := qualificationRemaining(w, w.GuestBoot, 100+int64(240*time.Second), start.Add(240*time.Second)); e == nil {
		t.Fatal("budget reset")
	}
	if _, e := qualificationRemaining(w, w.GuestBoot, 99, start.Add(time.Second)); e == nil {
		t.Fatal("monotonic rollback accepted")
	}
}

func TestQualificationUnusedProjectRejectsRealtimeAccounting(t *testing.T) {
	raw := make([]byte, 112)
	raw[0], raw[1] = 1, 2
	binary.LittleEndian.PutUint32(raw[4:8], 1234)
	if !qualificationUnusedQuota(raw, 1234) {
		t.Fatal("zero project record rejected")
	}
	for _, offset := range []int{8, 40, 48, 56, 64, 72, 80, 88, 96, 100, 104} {
		changed := append([]byte(nil), raw...)
		changed[offset] = 1
		if qualificationUnusedQuota(changed, 1234) {
			t.Fatalf("occupied quota byte %d accepted", offset)
		}
	}
}
