package capacitysnapshot

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const qualificationKitRoot = "/opt/switchback/qualification-kit"
const qualificationMaterialSchema = "switchback.qualification.kit_inputs.v1"

// Kit contains build outputs and source metadata only. Runtime identities are
// absent; the material generator obtains them from current OS/Engine readbacks.
type qualificationKit struct {
	Schema       string                     `json:"schema"`
	Architecture string                     `json:"architecture"`
	Toolchain    string                     `json:"toolchain"`
	Source       []qualificationSourceFile  `json:"source"`
	Artifacts    []qualificationKitArtifact `json:"artifacts"`
	Templates    []qualificationSourceFile  `json:"templates"`
}
type qualificationKitArtifact struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type qualificationPublicIdentity struct {
	Schema    string `json:"schema"`
	KeyID     string `json:"key_id"`
	PublicKey []byte `json:"public_key"`
}
type qualificationHostMaterial struct {
	Schema           string                        `json:"schema"`
	Host             qualificationHostTarget       `json:"host"`
	Writers          []qualificationWriterArtifact `json:"writers"`
	Config           qualificationSourceFile       `json:"config"`
	ExecutableDigest string                        `json:"executable_digest"`
	Toolchain        string                        `json:"toolchain"`
}
type qualificationCreatedRole struct {
	Role  string `json:"role"`
	CID   string `json:"cid"`
	Image string `json:"image"`
}
type qualificationCreated struct {
	Schema       string                     `json:"schema"`
	StaticDigest string                     `json:"static_digest"`
	Nonce        string                     `json:"nonce"`
	Roles        []qualificationCreatedRole `json:"roles"`
}

func qualificationMaterialKit(ctx context.Context, read func(context.Context, string, int64) ([]byte, error)) (qualificationKit, error) {
	var k qualificationKit
	b, e := read(ctx, qualificationKitRoot+"/kit-inputs.json", MaxBytes)
	if e != nil {
		return k, e
	}
	if strictJSON(b, &k) != nil || k.Schema != qualificationMaterialSchema || (k.Architecture != "arm64" && k.Architecture != "amd64") || k.Toolchain != "go1.26.4" || len(k.Source) == 0 || len(k.Artifacts) == 0 {
		return k, errQualification
	}
	if runtime.GOOS == "linux" && k.Architecture != runtime.GOARCH {
		return k, errQualification
	}
	for _, a := range k.Artifacts {
		if !label(a.Role) || !absolute(a.Path) || !strings.HasPrefix(a.Path, qualificationKitRoot+"/") || !digestPattern.MatchString(a.Digest) {
			return k, errQualification
		}
		if runtime.GOOS == "linux" && a.Role != "host-producer" && !strings.HasPrefix(a.Role, "writer-") {
			h, e := qualificationHashFile(ctx, a.Path, 512<<20)
			if e != nil || h != a.Digest {
				return k, fmt.Errorf("kit artifact differs: %s", a.Role)
			}
		}
	}
	return k, nil
}
func qualificationBuildIntent(k qualificationKit, h qualificationHostMaterial, tr Trust, roles []qualificationRole, external []qualificationArtifact, images map[string]string, nonce string) (qualificationStaged, error) {
	if k.Schema != qualificationMaterialSchema || h.Schema != "switchback.qualification.host_material.v1" || !containerIDPattern.MatchString(nonce) || validateQualificationHostTarget(h.Host) != nil || len(h.Writers) != len(qualificationWriterIDs) {
		return qualificationStaged{}, errQualification
	}
	files := append([]qualificationSourceFile(nil), k.Source...)
	files = append(files, h.Config)
	for _, t := range k.Templates {
		t.Path = "deployment-material/" + strings.ReplaceAll(t.Path, "/", "_")
		files = append(files, t)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for i, f := range files {
		if f.Type != "regular" || f.Bytes == 0 || f.Mode&0022 != 0 || !digestPattern.MatchString(f.Digest) || (i > 0 && files[i-1].Path >= f.Path) {
			return qualificationStaged{}, errQualification
		}
	}
	builds := []qualificationBuild{}
	artifacts := []qualificationArtifact{}
	seen := map[string]bool{}
	add := func(role, path, digest, image string) error {
		if seen[role] || !digestPattern.MatchString(digest) {
			return errQualification
		}
		seen[role] = true
		toolchain := k.Toolchain
		if strings.HasPrefix(role, "writer-") {
			toolchain = h.Toolchain
		}
		if role == "engine" || role == "kernel" || role == "postgres" || role == "web" {
			toolchain = "preexisting OS/image artifact; current digest verified"
		}
		if toolchain == "" {
			return errQualification
		}
		builds = append(builds, qualificationBuild{role, toolchain, qualificationDigest(files), digest, image})
		if role != "host-producer" && !strings.HasPrefix(role, "writer-") {
			artifacts = append(artifacts, qualificationArtifact{role, path, digest})
		}
		return nil
	}
	for _, a := range k.Artifacts {
		if e := add(a.Role, a.Path, a.Digest, images[a.Role]); e != nil {
			return qualificationStaged{}, e
		}
	}
	for _, a := range external {
		if e := add(a.Role, a.Path, a.Digest, images[a.Role]); e != nil {
			return qualificationStaged{}, e
		}
	}
	for _, w := range h.Writers {
		if e := add("writer-"+w.ID, w.Executable, w.ExecutableDigest, ""); e != nil {
			return qualificationStaged{}, e
		}
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].Role < builds[j].Role })
	sort.Slice(roles, func(i, j int) bool { return roles[i].Role < roles[j].Role })
	inventory := DeploymentInventory{Schema: DeploymentInventoryVersion, WorkerImage: "switchback-worker:qualification-candidate", WorkerImageDigest: images["worker"]}
	for _, b := range builds {
		inventory.Binaries = append(inventory.Binaries, AdmissionBinary{b.Role, b.BinaryDigest})
	}
	binding := DeploymentPublisherBinding{WorkerImageDigest: images["worker"]}
	for _, r := range roles {
		if r.Kind == "container" {
			binding.Core = append(binding.Core, DeploymentCoreBinding{ImageDigest: r.ImageDigest, CoreID: r.CoreID})
		}
	}
	s := qualificationStaged{Schema: stagedSchema, WindowID: "qualification-" + nonce[:24], Nonce: nonce, TestProject: 2147483000, Source: qualificationSource{Schema: "switchback.capacity.source_manifest.v1", Files: files, Builds: builds, Inputs: qualificationInputs{Architecture: k.Architecture, Schema: "switchback.capacity.deployment_inputs.v1", TrustManifest: qualificationDerived{"final_manifest_digest"}, PublisherManifest: qualificationDerived{"final_manifest_digest"}, ObserverManifest: qualificationDerived{"final_manifest_digest"}, Roles: roles, Host: h.Host, Maintenance: "stop_admission_and_fence_before_change_v1"}}, Inventory: inventory, Trust: tr, Binding: binding, Artifacts: artifacts, Writers: h.Writers}
	if e := qualificationValidateIntentInput(s); e != nil {
		return qualificationStaged{}, e
	}
	return s, nil
}
func qualificationApplyCreated(input qualificationStaged, created qualificationCreated) (qualificationStaged, error) {
	if qualificationValidateIntentInput(input) != nil || created.Schema != "switchback.qualification.created.v1" || created.StaticDigest != qualificationStaticIntentDigest(input) || created.Nonce != input.Nonce {
		return qualificationStaged{}, errQualification
	}
	s := qualificationClone(input)
	seen := map[string]bool{}
	for i := range s.Source.Inputs.Roles {
		r := &s.Source.Inputs.Roles[i]
		if r.Kind != "container" {
			continue
		}
		found := false
		for _, c := range created.Roles {
			if c.Role == r.Role {
				if found || seen[c.CID] || !containerIDPattern.MatchString(c.CID) || c.Image != r.ImageDigest {
					return qualificationStaged{}, errQualification
				}
				found = true
				seen[c.CID] = true
				r.ContainerID = c.CID
				r.CgroupPath = qualificationContainerCgroupPath(r.CgroupPath, c.CID)
				for j := range s.Binding.Core {
					if s.Binding.Core[j].ContainerID == "" && s.Binding.Core[j].ImageDigest == r.ImageDigest && s.Binding.Core[j].CoreID == r.CoreID {
						s.Binding.Core[j].ContainerID = c.CID
						break
					}
				}
			}
		}
		if !found {
			return qualificationStaged{}, errQualification
		}
	}
	if len(seen) != len(created.Roles) {
		return qualificationStaged{}, errQualification
	}
	s.Inventory.DeploymentInputsDigest = qualificationDigest(s.Source.Inputs)
	s.Inventory.SourceManifestDigest = qualificationDigest(s.Source)
	if qualificationValidateCreateBoundStaged(s) != nil || !qualificationSameJSON(input, qualificationStaticIntentProjection(s)) {
		return qualificationStaged{}, errQualification
	}
	return s, nil
}
func qualificationMaterialRead(ctx context.Context, path string, limit int64) ([]byte, error) {
	if !absolute(path) || limit <= 0 || limit > MaxBytes {
		return nil, errQualification
	}
	parent, e := qualificationOpenArtifactParent(ctx, filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	defer parent.Close()
	fd, e := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	before, e := qualificationStat(ctx, fd)
	if e != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Mode&0022 != 0 || before.Size <= 0 || before.Size > limit {
		return nil, errQualification
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) != before.Size {
		return nil, errQualification
	}
	after, e := qualificationStat(ctx, fd)
	if e != nil || qualificationIdentity(before) != qualificationIdentity(after) {
		return nil, errQualification
	}
	fresh, e := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	again, e := qualificationStat(ctx, fresh)
	unix.Close(fresh)
	if e != nil || qualificationIdentity(again) != qualificationIdentity(before) {
		return nil, errQualification
	}

	return b, qualificationContext(ctx)
}

// All phases have fixed roots/finite operations. No complete runtime JSON,
// resource values, paths, timeouts or injected adapters are accepted by CLI.
func GenerateQualificationMaterial(ctx context.Context, phase string) error {
	return qualificationGenerateMaterialFixed(ctx, phase)
}

func qualificationWriterToolchain(path string) (string, error) {
	i, e := buildinfo.ReadFile(path)
	if e != nil {
		return "", e
	}
	return i.GoVersion, nil
}
