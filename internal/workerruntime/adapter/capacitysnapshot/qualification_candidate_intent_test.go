package capacitysnapshot

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func qualificationIntentFixture(t *testing.T) qualificationStaged {
	t.Helper()
	tr, files := qualificationFixture(t)
	var source qualificationSource
	var inventory DeploymentInventory
	var binding qualificationBinding
	if err := json.Unmarshal(files["source-manifest.json"], &source); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(files["deployment-inventory.json"], &inventory); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(files["publisher-bindings.json"], &binding); err != nil {
		t.Fatal(err)
	}
	tr.ManifestDigest = ""
	for i := range source.Inputs.Roles {
		role := &source.Inputs.Roles[i]
		if role.Kind == "container" {
			role.CgroupPath = qualificationContainerCgroupPath(filepath.Dir(role.CgroupPath), role.ContainerID)
		}
	}
	s := qualificationStaged{Schema: stagedSchema, WindowID: "qualification-once", Nonce: strings.Repeat("a", 64), TestProject: 1234,
		Source: source, Inventory: inventory, Trust: tr, Binding: DeploymentPublisherBinding{Core: binding.Core, WorkerImageDigest: binding.WorkerImageDigest},
		Artifacts: []qualificationArtifact{{Role: "backend", Path: "/fixed/backend", Digest: source.Builds[0].BinaryDigest}},
	}
	for _, id := range qualificationWriterIDs {
		s.Writers = append(s.Writers, qualificationWriterArtifact{ID: id, Executable: "/fixed/" + id, Config: "/fixed/" + id + ".conf", ExecutableDigest: Digest([]byte(id)), ConfigDigest: Digest([]byte(id + "-config"))})
	}
	return s
}

func TestQualificationPreCreateStaticIntentAcyclic(t *testing.T) {
	actual := qualificationIntentFixture(t)
	pre := qualificationStaticIntentProjection(actual)
	if err := qualificationValidateIntentInput(pre); err != nil {
		t.Fatal(err)
	}
	base := qualificationStaticIntentDigest(pre)
	if base == "" || base != qualificationStaticIntentDigest(actual) {
		t.Fatal("runtime identity changed Create intent")
	}
	changed := qualificationClone(actual)
	changed.Source.Inputs.Roles[0].ContainerID = strings.Repeat("b", 64)
	changed.Source.Inputs.Roles[0].CgroupPath = qualificationContainerCgroupPath(filepath.Dir(actual.Source.Inputs.Roles[0].CgroupPath), strings.Repeat("b", 64))
	changed.Source.Inputs.Roles[0].Mounts[0].MountID++
	changed.Source.Inputs.Roles[0].Mounts[0].Root = "/late/root"
	changed.Binding.Core[0].ContainerID = strings.Repeat("b", 64)
	changed.Inventory.DeploymentInputsDigest = qualificationDigest(changed.Source.Inputs)
	changed.Inventory.SourceManifestDigest = qualificationDigest(changed.Source)
	if got := qualificationStaticIntentDigest(changed); got != base {
		t.Fatal("CID/late mount facts altered pre-Create intent")
	}
	cgroupDrift := qualificationClone(actual)
	cgroupDrift.Source.Inputs.Roles[0].CgroupPath = qualificationContainerCgroupPath("/sys/fs/cgroup/switchback.slice/other-parent", cgroupDrift.Source.Inputs.Roles[0].ContainerID)
	if qualificationStaticIntentDigest(cgroupDrift) == base {
		t.Fatal("static cgroup parent drift accepted")
	}
	if qualificationDigest(changed.Source) == qualificationDigest(actual.Source) {
		t.Fatal("full candidate source lost CID identity")
	}
	candidate := func(s qualificationStaged) string {
		t.Helper()
		c := qualificationCandidateRecord{Source: s.Source, Inventory: s.Inventory, Trust: s.Trust, Binding: s.Binding}
		manifest, err := qualificationManifestPrefix(c)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := qualificationCandidate(s.Trust, manifest, s.Binding)
		if err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}
	if candidate(actual) == candidate(changed) {
		t.Fatal("final candidate did not bind actual CID")
	}
	mutations := []struct {
		name   string
		mutate func(*qualificationStaged)
	}{
		{"artifact", func(s *qualificationStaged) { s.Source.Builds[0].BinaryDigest = Digest([]byte("new-binary")) }},
		{"image", func(s *qualificationStaged) { s.Source.Builds[0].ImageDigest = Digest([]byte("new-image")) }},
		{"cap", func(s *qualificationStaged) { s.Source.Inputs.Roles[0].Capabilities++ }},
		{"mount-source", func(s *qualificationStaged) { s.Source.Inputs.Roles[0].Mounts[0].Source = "/new/static/source" }},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			x := qualificationClone(actual)
			m.mutate(&x)
			if qualificationStaticIntentDigest(x) == base {
				t.Fatal("static Create input did not change intent")
			}
		})
	}
}
