package capacitysnapshot

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
)

// One raw-fact fixture feeds both the producer state-machine assertions and the
// installer/bootstrap assertions. It always runs real collect/assemble code.
type qualificationCollectFixture struct {
	ctx                          context.Context
	t0                           time.Time
	input, staged                qualificationStaged
	static                       qualificationStaticIntent
	opened                       qualificationWindowOpen
	candidate                    qualificationCandidateRecord
	host                         qualificationHostReceipt
	before                       qualificationHostBefore
	facts                        qualificationNativeFacts
	cleanup                      qualificationCleanup
	dir                          qualificationCandidateDirectory
	ops                          qualificationNativeCollectOps
	monoCalls, nowCalls, samples int
}

func newQualificationCollectFixture(t *testing.T) *qualificationCollectFixture {
	t.Helper()
	ctx := context.Background()
	tr, files := qualificationThreadedFixture(t)
	var fixture qualificationNativeReceipt
	var host qualificationHostReceipt
	if err := strictJSON(files["native-enforcement-receipt.json"], &fixture); err != nil {
		t.Fatal(err)
	}
	if err := strictJSON(files["host-boundary-receipt.json"], &host); err != nil {
		t.Fatal(err)
	}
	staged := qualificationIntentFixture(t)
	for i := range staged.Source.Inputs.Roles {
		for j := range staged.Source.Inputs.Roles[i].Mounts {
			staged.Source.Inputs.Roles[i].Mounts[j].Device = ""
			staged.Source.Inputs.Roles[i].Mounts[j].Root = ""
			staged.Source.Inputs.Roles[i].Mounts[j].MountID = 0
		}
	}
	staged.Artifacts = nil
	for _, build := range staged.Source.Builds {
		if build.Role != "host-producer" && !strings.HasPrefix(build.Role, "writer-") {
			staged.Artifacts = append(staged.Artifacts, qualificationArtifact{Role: build.Role, Path: "/fixed/" + build.Role, Digest: build.BinaryDigest})
		}
	}
	if qualificationValidateStaged(staged) != nil || qualificationValidateCreateBoundStaged(staged) != nil {
		t.Fatal("synthetic frozen stage invalid")
	}
	input := qualificationStaticIntentProjection(staged)
	static := qualificationStaticIntent{Schema: staticIntentSchema, WindowID: staged.WindowID, Nonce: staged.Nonce,
		InputDigest: qualificationDigest(input), Digest: qualificationStaticIntentDigest(input)}
	t0 := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	opened := qualificationWindowOpen{Schema: windowOpenSchema, ID: staged.WindowID, Nonce: staged.Nonce,
		StagedDigest: qualificationDigest(staged), GuestBoot: tr.Identity.GuestBootID, Started: t0.Format(time.RFC3339Nano), MonotonicNS: 100}
	c := qualificationCandidateRecord{Schema: candidateSchema, OpenDigest: qualificationDigest(opened), StagedDigest: qualificationDigest(staged),
		StaticIntentDigest: static.Digest, TestProject: staged.TestProject, Source: qualificationClone(staged.Source),
		Inventory: qualificationClone(staged.Inventory), Trust: qualificationClone(staged.Trust), Binding: qualificationClone(staged.Binding)}
	for i := range c.Source.Inputs.Roles {
		for j := range c.Source.Inputs.Roles[i].Mounts {
			c.Source.Inputs.Roles[i].Mounts[j].Device = "8:2"
			c.Source.Inputs.Roles[i].Mounts[j].Root = "/etc/switchback/created-layer-observer"
			c.Source.Inputs.Roles[i].Mounts[j].MountID = 2
		}
	}
	c.Inventory.DeploymentInputsDigest = qualificationDigest(c.Source.Inputs)
	c.Inventory.SourceManifestDigest = qualificationDigest(c.Source)
	for i, role := range c.Source.Inputs.Roles {
		c.Processes = append(c.Processes, qualificationFrozenProcess{Role: role.Role, PID: uint64(100 + i), StartTicks: uint64(1000 + i),
			Snapshot: qualificationProcReadback{Stat: []byte("synthetic stat\n"), Status: qualificationStatus(role.Capabilities),
				Cgroup: []byte("0::" + strings.TrimPrefix(role.CgroupPath, "/sys/fs/cgroup") + "\n"), MountInfo: []byte("synthetic mountinfo\n"), ExecutableDigest: role.BinaryDigest, Boot: opened.GuestBoot}})
	}
	manifest, err := qualificationManifestPrefix(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Fingerprint, err = qualificationCandidate(c.Trust, manifest, c.Binding)
	if err != nil {
		t.Fatal(err)
	}
	host.Candidate = c.Fingerprint
	host.Window.Started = opened.Started
	host.Window.Ended = t0.Add(8 * time.Second).Format(time.RFC3339Nano)
	for i := range host.Checks {
		start, end := t0.Add(time.Second), t0.Add(2*time.Second)
		if i >= 2 {
			start, end = t0.Add(6*time.Second), t0.Add(7*time.Second)
		}
		host.Checks[i].Observation.Started = start.Format(time.RFC3339Nano)
		host.Checks[i].Observation.Ended = end.Format(time.RFC3339Nano)
	}
	before := qualificationHostBefore{Schema: "switchback.qualification.host_before.v1", Candidate: c.Fingerprint, OpenDigest: c.OpenDigest,
		HostBoot: host.Window.HostBootBefore, Started: t0.Add(2 * time.Second).Format(time.RFC3339Nano), Checks: qualificationClone(host.Checks[:2])}
	facts := qualificationNativeFacts{}
	for _, check := range fixture.Checks {
		var out any
		switch check.ID {
		case "loaded-identity":
			out = &facts.Loaded
		case "namespaces-and-roles":
			out = &facts.Namespaces
		case "core-limits":
			out = &facts.Core
		case "byte-quota":
			out = &facts.Quota
		case "worker-setters":
			facts.SettersRaw = append([]byte(nil), check.Observation.Data...)
		case "backend-handoff":
			out = &facts.Handoff
		case "mounts-and-writers":
			out = &facts.Reachability
		default:
			t.Fatalf("unexpected check %s", check.ID)
		}
		if out != nil {
			if err := strictJSON(check.Observation.Data, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	facts.Loaded.Roles = qualificationClone(c.Source.Inputs.Roles)
	facts.Reachability.Roles = qualificationClone(c.Source.Inputs.Roles)
	for i, role := range c.Source.Inputs.Roles {
		if facts.Namespaces.Roles[i].Role != role.Role {
			t.Fatal("role order")
		}
		facts.Namespaces.Roles[i].CgroupBefore = strings.TrimPrefix(role.CgroupPath, "/sys/fs/cgroup")
		facts.Namespaces.Roles[i].CgroupAfter = facts.Namespaces.Roles[i].CgroupBefore
	}
	facts.SampleStart, facts.ReadonlyStart, facts.BackendStart, facts.QuotaStart = t0.Add(3*time.Second), t0.Add(3*time.Second), t0.Add(3*time.Second), t0.Add(3*time.Second)
	facts.ReadonlyEnd, facts.BackendEnd, facts.QuotaEnd = t0.Add(4*time.Second), t0.Add(4*time.Second), t0.Add(4*time.Second)
	quota := func(id uint32) []byte {
		b := make([]byte, 112)
		b[0], b[1] = 1, 2
		binary.LittleEndian.PutUint32(b[4:8], id)
		return b
	}
	cleanup := qualificationCleanup{Complete: true, Project: staged.TestProject, Initial: quota(staged.TestProject), Successor: quota(staged.TestProject + 1), Final: quota(staged.TestProject), RootRemoved: true, FDsClosed: true}
	for j, name := range []string{"empty", "nonempty", "private", "sticky", "without-chown", "without-dac-override", "without-fowner", "worker-handoff", "worker"} {
		cleanup.Helpers = append(cleanup.Helpers, qualificationHelperCleanup{Case: name, CID: strings.Repeat(string(rune('1'+j)), 64), DeleteStatus: 204, InspectStatus: 404})
	}
	f, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	dir := qualificationCandidateDirectory{file: f, uid: uint32(os.Getuid())}
	for _, item := range []struct {
		name string
		v    any
	}{{"intent-input.json", input}, {"static-intent.json", static}, {"staged.json", staged}, {"window-open.json", opened}, {"candidate.json", c}, {"host-before.json", before}} {
		if err := dir.put(ctx, item.name, item.v); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		name string
		into any
	}{{"intent-input.json", &qualificationStaged{}}, {"static-intent.json", &qualificationStaticIntent{}}, {"staged.json", &qualificationStaged{}}, {"window-open.json", &qualificationWindowOpen{}}, {"candidate.json", &qualificationCandidateRecord{}}, {"host-before.json", &qualificationHostBefore{}}} {
		if err := dir.decode(ctx, item.name, item.into); err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
	}
	fxt := &qualificationCollectFixture{}
	ops := qualificationNativeCollectOps{
		architecture: c.Source.Inputs.Architecture,
		boot:         func(context.Context) (string, error) { return opened.GuestBoot, nil },
		mono: func() (int64, error) {
			fxt.monoCalls++
			if fxt.monoCalls == 1 {
				return 100 + int64(2*time.Second), nil
			}
			return 100 + int64(5*time.Second), nil
		},
		now: func() time.Time {
			fxt.nowCalls++
			if fxt.nowCalls == 1 {
				return t0.Add(2 * time.Second)
			}
			return t0.Add(5 * time.Second)
		},
		self: func(context.Context, qualificationSource, string) error { return nil },
		hash: func(_ context.Context, path string, _ int64) (string, error) {
			for _, a := range staged.Artifacts {
				if a.Path == path {
					return a.Digest, nil
				}
			}
			return "", errQualification
		},
		waitHost: func(_ context.Context, _ qualificationCandidateDirectory, candidate qualificationCandidateRecord, _ qualificationWindowOpen) error {
			if candidate.Fingerprint != before.Candidate {
				return errQualification
			}
			return nil
		},
		sample: func(_ context.Context, _ qualificationCandidateDirectory, candidate qualificationCandidateRecord, _ qualificationWindowOpen) (qualificationNativeFacts, qualificationCleanup, error) {
			fxt.samples++
			if candidate.Fingerprint != c.Fingerprint {
				return qualificationNativeFacts{}, qualificationCleanup{}, errQualification
			}
			return facts, cleanup, nil
		},
	}

	fxt.ctx, fxt.t0, fxt.input, fxt.staged, fxt.static, fxt.opened = ctx, t0, input, staged, static, opened
	fxt.candidate, fxt.host, fxt.before, fxt.facts, fxt.cleanup, fxt.dir, fxt.ops = c, host, before, facts, cleanup, dir, ops
	return fxt
}

func (f *qualificationCollectFixture) collect(t *testing.T) (qualificationNativeObservation, qualificationCommonEnd) {
	t.Helper()
	if err := collectNativeQualificationWith(f.ctx, f.dir, f.ops); err != nil {
		t.Fatal(err)
	}
	var native qualificationNativeObservation
	if err := f.dir.decode(f.ctx, "native-observations.json", &native); err != nil {
		t.Fatal(err)
	}
	common := qualificationCommonEnd{Schema: "switchback.qualification.common_end.v1", Candidate: f.candidate.Fingerprint, HostBeforeDigest: qualificationDigest(f.before), NativeDigest: qualificationDigest(native), HostDigest: qualificationDigest(f.host), Window: f.host.Window}
	for _, item := range []struct {
		name string
		v    any
	}{{"host-observations.json", f.host}, {"common-window.json", common}} {
		if err := f.dir.put(f.ctx, item.name, item.v); err != nil {
			t.Fatal(err)
		}
	}
	return native, common
}

func (f *qualificationCollectFixture) assemble(t *testing.T) {
	t.Helper()
	if err := assembleQualificationFromDirectory(f.ctx, f.dir, func(context.Context) (string, error) { return f.opened.GuestBoot, nil }, func() (int64, error) { return 100 + int64(9*time.Second), nil }, func() time.Time { return f.t0.Add(9 * time.Second) }); err != nil {
		t.Fatal(err)
	}
}

func (f *qualificationCollectFixture) inputDirectory(t *testing.T, candidate qualificationCandidateRecord) qualificationCandidateDirectory {
	t.Helper()
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	d := qualificationCandidateDirectory{file: root, uid: uint32(os.Getuid())}
	for _, item := range []struct {
		name string
		v    any
	}{{"intent-input.json", f.input}, {"static-intent.json", f.static}, {"staged.json", f.staged}, {"window-open.json", f.opened}, {"candidate.json", candidate}, {"host-before.json", f.before}} {
		if err := d.put(f.ctx, item.name, item.v); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func qualificationCompletedInstallCandidate(t *testing.T) qualificationCandidateDirectory {
	t.Helper()
	f := newQualificationCollectFixture(t)
	f.collect(t)
	f.assemble(t)
	return f.dir
}
