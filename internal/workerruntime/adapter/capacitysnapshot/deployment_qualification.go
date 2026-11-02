package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const qualificationLifecycle = "qualification_then_frozen_deployment_v1"

var qualificationUUIDPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var errQualification = fmt.Errorf("deployment qualification invalid or unsupported")

type DeploymentQualification struct {
	files  qualificationFiles
	result qualificationResult
}
type qualificationResult struct {
	private  []DeploymentPrivateDirectoryBinding
	metadata AdmissionManifestMetadata
	binding  DeploymentPublisherBinding
	inputs   qualificationInputs
	trust    Trust
}

func OpenDeploymentQualification(ctx context.Context, manifestPath string, trust Trust) (*DeploymentQualification, error) {
	return openDeploymentQualificationWithFiles(ctx, manifestPath, trust, readQualificationFiles)
}

// Private file seam only; the strict validator itself is never replaceable.
func openDeploymentQualificationWithFiles(ctx context.Context, manifestPath string, trust Trust, read func(context.Context, string) (qualificationFiles, error)) (*DeploymentQualification, error) {
	files, e := read(ctx, manifestPath)
	if e != nil {
		return nil, e
	}
	raw := map[string][]byte{}
	for n, f := range files.leaves {
		raw[n] = f.data
	}
	result, e := validateDeploymentQualification(trust, raw)
	if e != nil {
		return nil, e
	}
	current, e := read(ctx, manifestPath)
	if e != nil {
		return nil, e
	}
	if !reflect.DeepEqual(files, current) {
		return nil, fmt.Errorf("qualification frozen files changed")
	}
	if e = qualificationContext(ctx); e != nil {
		return nil, e
	}
	return &DeploymentQualification{files: files, result: result}, nil
}
func (q *DeploymentQualification) Metadata() AdmissionManifestMetadata {
	if q == nil {
		return AdmissionManifestMetadata{}
	}
	return qualificationClone(q.result.metadata)
}
func (q *DeploymentQualification) PublisherBinding() DeploymentPublisherBinding {
	if q == nil {
		return DeploymentPublisherBinding{}
	}
	return qualificationClone(q.result.binding)
}
func (q *DeploymentQualification) Recheck(ctx context.Context) error {
	if q == nil {
		return errQualification
	}
	return q.files.recheck(ctx)
}
func qualificationClone[T any](in T) T {
	raw, _ := json.Marshal(in)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func qualificationDigest(v any) string { b, _ := json.Marshal(v); return Digest(b) }

func validateDeploymentQualification(trust Trust, files map[string][]byte) (qualificationResult, error) {
	return validateDeploymentQualificationSchema(trust, files, false)
}

// Historical bytes are replayable only through this private, pure legacy path.
// Production Open/Await never call it and cannot select a legacy schema.
func validateLegacyDeploymentQualificationV1(trust Trust, files map[string][]byte) (qualificationResult, error) {
	return validateDeploymentQualificationSchema(trust, files, true)
}

func validateDeploymentQualificationSchema(trust Trust, files map[string][]byte, legacy bool) (qualificationResult, error) {
	var out qualificationResult
	fail := func() (qualificationResult, error) { return qualificationResult{}, errQualification }
	if len(files) != len(qualificationLeaves) || ValidateTrust(trust) != nil || trust.AdmissionPolicy == nil {
		return fail()
	}
	guarantee := config.M1SharedVolumeCapacityGuarantee()
	if *trust.AdmissionPolicy != (workerapp.CapacityGuaranteeRef{ID: guarantee.ID, Revision: guarantee.Revision, Digest: guarantee.Digest}) {
		return fail()
	}
	m, e := ParseAdmissionManifest(files["admission-manifest.json"], trust)
	if e != nil {
		return fail()
	}
	inventory, e := bindAdmissionEvidence(m, files["source-manifest.json"], files["deployment-inventory.json"], files["host-boundary-receipt.json"], files["native-enforcement-receipt.json"])
	if e != nil {
		return fail()
	}
	var installed Trust
	var binding qualificationBinding
	var source qualificationSource
	if strictJSON(files["trust.json"], &installed) != nil || !reflect.DeepEqual(trust, installed) || strictJSON(files["publisher-bindings.json"], &binding) != nil || strictJSON(files["source-manifest.json"], &source) != nil {
		return fail()
	}
	if binding.Schema != "switchback.capacity.publisher_bindings.v1" || binding.ManifestDigest != trust.ManifestDigest || binding.WorkerImageDigest != inventory.WorkerImageDigest {
		return fail()
	}
	core, e := normalizeQualificationCore(binding.Core)
	if e != nil {
		return fail()
	}
	if validateQualificationSource(source, inventory, trust, core) != nil || (!legacy && validateQualificationV2Source(source, inventory) != nil) {
		return fail()
	}
	// Actual fixed EnvironmentFile bytes, not an unused JSON readiness assertion.
	expected := []byte("SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST=" + trust.ManifestDigest + "\n")
	if !bytes.Equal(files["observer-deployment.env"], expected) {
		return fail()
	}
	out = qualificationResult{metadata: AdmissionManifestMetadata{Manifest: m, Inventory: inventory}, binding: DeploymentPublisherBinding{Core: core, WorkerImageDigest: inventory.WorkerImageDigest}, inputs: source.Inputs, trust: qualificationClone(trust)}
	candidate, e := qualificationCandidate(trust, m, out.binding)
	if e != nil {
		return fail()
	}
	var host qualificationHostReceipt
	var native qualificationNativeReceipt
	if strictJSON(files["host-boundary-receipt.json"], &host) != nil || strictJSON(files["native-enforcement-receipt.json"], &native) != nil {
		return fail()
	}
	nativeSchema := qualificationNativeV2
	if legacy {
		nativeSchema = "switchback.capacity.native_enforcement_receipt.v1"
	}
	if host.Schema != "switchback.capacity.host_volume_receipt.v1" || host.Kind != "apfs_volume_quota_and_reserve_v1" || native.Schema != nativeSchema || native.Kind != "linux_xfs_enforcement_v1" || host.Window != native.Window {
		return fail()
	}
	for _, r := range []qualificationHostReceipt{host, qualificationHostReceipt(native)} {
		if r.Lifecycle != qualificationLifecycle || r.Candidate != candidate || validateQualificationWindow(r.Window, trust.Identity.GuestBootID) != nil || validateQualificationProducer(r.Producer, source) != nil {
			return fail()
		}
	}
	if host.Producer.Role != "host-producer" || native.Producer.Role != "native-producer" || validateQualificationHost(host, source.Inputs.Host) != nil || validateQualificationNative(native, out) != nil {
		return fail()
	}
	var reach qualificationReachability
	for _, check := range native.Checks {
		if check.ID == "mounts-and-writers" {
			if strictJSON(check.Observation.Data, &reach) != nil {
				return fail()
			}
		}
	}
	out.private = qualificationClone(reach.Private)
	return out, nil
}
func normalizeQualificationCore(in []DeploymentCoreBinding) ([]DeploymentCoreBinding, error) {
	if len(in) == 0 || len(in) > 32 {
		return nil, errQualification
	}
	out := append([]DeploymentCoreBinding(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].CoreID == out[j].CoreID {
			return out[i].ContainerID < out[j].ContainerID
		}
		return out[i].CoreID < out[j].CoreID
	})
	seen := map[string]bool{}
	for _, c := range out {
		if !containerIDPattern.MatchString(c.ContainerID) || !digestPattern.MatchString(c.ImageDigest) || !label(c.CoreID) || seen[c.ContainerID] {
			return nil, errQualification
		}
		seen[c.ContainerID] = true
	}
	return out, nil
}

// Fixed-order canonical encoding deliberately omits outer ManifestDigest. The
// input tree hashes regular source entries only, never its own build receipt.
func qualificationCandidate(t Trust, m AdmissionManifest, b DeploymentPublisherBinding) (string, error) {
	t = qualificationClone(t)
	t.ManifestDigest = ""
	sort.Slice(t.Core, func(i, j int) bool { return t.Core[i].ID < t.Core[j].ID })
	sort.Slice(t.Storage, func(i, j int) bool { return t.Storage[i].ID < t.Storage[j].ID })
	sort.Slice(t.Coverage, func(i, j int) bool { return t.Coverage[i].Role < t.Coverage[j].Role })
	for _, ids := range [][]string{coreIDs(t.Core), storageIDs(t.Storage), coverageIDs(t.Coverage)} {
		for i, id := range ids {
			if id == "" || (i > 0 && id == ids[i-1]) {
				return "", errQualification
			}
		}
	}
	core, e := normalizeQualificationCore(b.Core)
	if e != nil {
		return "", e
	}
	v := struct {
		Schema    string                           `json:"schema"`
		Guarantee workerapp.CapacityGuaranteeRef   `json:"guarantee"`
		Resource  config.RuntimeResourceProfileRef `json:"resource"`
		Network   config.RuntimeResourceProfileRef `json:"network"`
		KeyID     string                           `json:"key_id"`
		PublicKey []byte                           `json:"public_key"`
		Identity  Identity                         `json:"identity"`
		Anchor    string                           `json:"anchor_path"`
		Core      []CoreDomain                     `json:"core"`
		Storage   []StorageDomain                  `json:"storage"`
		Coverage  []StorageCoverage                `json:"coverage"`
		Source    string                           `json:"source_manifest_digest"`
		Inventory string                           `json:"deployment_inventory_digest"`
		Bindings  []DeploymentCoreBinding          `json:"bindings"`
		Worker    string                           `json:"worker_image_digest"`
	}{"switchback.capacity.deployment_candidate.v1", m.Guarantee, m.ResourceProfile, m.NetworkProfile, t.KeyID, t.PublicKey, t.Identity, t.AnchorPath, t.Core, t.Storage, t.Coverage, m.SourceManifestDigest, m.DeploymentInventoryDigest, core, b.WorkerImageDigest}
	return qualificationDigest(v), nil
}
func coreIDs(v []CoreDomain) (out []string) {
	for _, x := range v {
		out = append(out, x.ID)
	}
	return
}
func storageIDs(v []StorageDomain) (out []string) {
	for _, x := range v {
		out = append(out, x.ID)
	}
	return
}
func coverageIDs(v []StorageCoverage) (out []string) {
	for _, x := range v {
		out = append(out, x.Role)
	}
	return
}
func validateQualificationSource(s qualificationSource, i DeploymentInventory, t Trust, bindings []DeploymentCoreBinding) error {
	if s.Schema != "switchback.capacity.source_manifest.v1" || len(s.Files) == 0 || len(s.Files) > 4096 || s.Inputs.Schema != "switchback.capacity.deployment_inputs.v1" || qualificationDigest(s.Inputs) != i.DeploymentInputsDigest {
		return errQualification
	}
	for n, f := range s.Files {
		if f.Type != "regular" || f.Mode&^0777 != 0 || f.Mode&0022 != 0 || f.Bytes == 0 || !digestPattern.MatchString(f.Digest) || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || strings.HasPrefix(f.Path, "../") || f.Path == "." || strings.ContainsAny(f.Path, "\\\x00") || (n > 0 && s.Files[n-1].Path >= f.Path) {
			return errQualification
		}
		for _, part := range strings.Split(f.Path, "/") {
			switch strings.ToLower(part) {
			case ".git", ".scratch", ".lima", ".env", "data", "journals", "keys", "private", "secrets", "secret", "state", "qualification", "trust.json", "publisher-bindings.json", "admission-manifest.json", "deployment.env":
				return errQualification
			}
		}
	}
	if (s.Inputs.Architecture != "amd64" && s.Inputs.Architecture != "arm64") || s.Inputs.Maintenance != "stop_admission_and_fence_before_change_v1" {
		return errQualification
	}
	for _, d := range []qualificationDerived{s.Inputs.TrustManifest, s.Inputs.PublisherManifest, s.Inputs.ObserverManifest} {
		if d.Reference != "final_manifest_digest" {
			return errQualification
		}
	}
	inputRoot := qualificationDigest(s.Files)
	builds := map[string]qualificationBuild{}
	binaries := map[string]string{}
	for _, b := range i.Binaries {
		binaries[b.Name] = b.Digest
	}
	for n, b := range s.Builds {
		if !label(b.Role) || b.Toolchain == "" || len(b.Toolchain) > 256 || strings.Contains(b.Toolchain, "${") || b.InputRoot != inputRoot || !digestPattern.MatchString(b.BinaryDigest) || (b.ImageDigest != "" && !digestPattern.MatchString(b.ImageDigest)) || binaries[b.Role] != b.BinaryDigest || (n > 0 && s.Builds[n-1].Role >= b.Role) {
			return errQualification
		}
		builds[b.Role] = b
	}
	for _, role := range []string{"backend", "publisher", "observer", "worker", "host-producer", "native-producer", "engine", "kernel"} {
		if _, ok := builds[role]; !ok {
			return errQualification
		}
	}
	if len(builds) > 32 || len(binaries) != len(builds) || builds["worker"].ImageDigest != i.WorkerImageDigest {
		return errQualification
	}
	if len(s.Inputs.Roles) < len(bindings) || len(s.Inputs.Roles) > 32 {
		return errQualification
	}
	roles := map[string]bool{}
	seen := map[string]bool{}
	for n, r := range s.Inputs.Roles {
		if (n > 0 && s.Inputs.Roles[n-1].Role >= r.Role) || roles[r.Role] || !label(r.Role) || r.UID > uint64(^uint32(0)) || ((r.Kind == "host_process" || r.Role == "backend" || r.Role == "publisher" || r.Role == "observer") && r.UID != 0) || r.NoNewPrivileges != 1 || r.Seccomp != 2 || !absolute(r.CgroupPath) {
			return errQualification
		}
		roles[r.Role] = true
		build, ok := builds[r.Role]
		if !ok || build.BinaryDigest != r.BinaryDigest || build.ImageDigest != r.ImageDigest {
			return errQualification
		}
		if r.Kind == "container" {
			found := false
			for _, b := range bindings {
				if b.ContainerID == r.ContainerID && b.ImageDigest == r.ImageDigest && b.CoreID == r.CoreID {
					found = true
				}
			}
			if !found || seen[r.ContainerID] {
				return errQualification
			}
			seen[r.ContainerID] = true
		} else if r.Kind != "host_process" || r.ContainerID != "" || r.ImageDigest != "" {
			return errQualification
		}
		domain := false
		for _, c := range t.Core {
			if c.ID == r.CoreID && (r.CgroupPath == c.CgroupPath || strings.HasPrefix(r.CgroupPath, c.CgroupPath+"/")) {
				domain = true
			}
		}
		if !domain {
			return errQualification
		}
		switch r.Role {
		case "backend":
			if r.Capabilities != 0xb {
				return errQualification
			}
		case "publisher":
			if r.Capabilities != 0x280000 {
				return errQualification
			}
		}
		for j, m := range r.Mounts {
			if !absolute(m.Source) || !absolute(m.Target) || !absolute(m.Root) || m.Device == "" || m.MountID == 0 || (j > 0 && r.Mounts[j-1].Target >= m.Target) {
				return errQualification
			}
		}
		for j, v := range r.Environment {
			if !label(v.Name) || strings.Contains(v.Value, "${") || strings.Contains(v.Value, "final_manifest_digest") || strings.ContainsAny(v.Value, "\x00\n\r") || (j > 0 && r.Environment[j-1].Name >= v.Name) || v.Name == "SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST" {
				return errQualification
			}
		}
	}
	if len(seen) != len(bindings) || !roles["backend"] || !roles["publisher"] || !roles["observer"] {
		return errQualification
	}
	// Both readers see the actual host observer EnvironmentFile via the same
	// read-only directory bind, not a per-container copy with matching text.
	var envMount *qualificationMount
	for _, r := range s.Inputs.Roles {
		if r.Role != "backend" && r.Role != "publisher" {
			continue
		}
		found := false
		for _, m := range r.Mounts {
			if m.Target != "/etc/switchback/created-layer-observer" {
				continue
			}
			if !m.ReadOnly || m.Source != "/etc/switchback/created-layer-observer" {
				return errQualification
			}
			if envMount != nil && (m.Source != envMount.Source || m.Root != envMount.Root || m.Device != envMount.Device) {
				return errQualification
			}
			copy := m
			envMount = &copy
			found = true
		}
		if !found {
			return errQualification
		}
	}
	return validateQualificationHostTarget(s.Inputs.Host)
}
func validateQualificationWindow(w qualificationWindow, boot string) error {
	start, e := time.Parse(time.RFC3339Nano, w.Started)
	if e != nil {
		return errQualification
	}
	end, e := time.Parse(time.RFC3339Nano, w.Ended)
	if e != nil || !end.After(start) || end.Sub(start) > time.Hour || !label(w.ID) || !qualificationUUIDPattern.MatchString(w.HostBootBefore) || !qualificationUUIDPattern.MatchString(w.GuestBootBefore) || w.HostBootBefore != w.HostBootAfter || w.GuestBootBefore != boot || w.GuestBootAfter != boot {
		return errQualification
	}
	return nil
}
func validateQualificationProducer(p qualificationProducer, s qualificationSource) error {
	for _, b := range s.Builds {
		if b.Role == p.Role && p.ArtifactDigest == b.BinaryDigest && p.BuildDigest == qualificationDigest(b) && p.InputRoot == b.InputRoot {
			return nil
		}
	}
	return errQualification
}
func qualificationChecks(checks []qualificationCheck, ids []string, window qualificationWindow) (map[string][]byte, error) {
	if len(checks) != len(ids) {
		return nil, errQualification
	}
	out := map[string][]byte{}
	for _, c := range checks {
		ok := false
		for _, id := range ids {
			if c.ID == id {
				ok = true
			}
		}
		r := c.Observation
		start, se := time.Parse(time.RFC3339Nano, r.Started)
		end, ee := time.Parse(time.RFC3339Nano, r.Ended)
		ws, _ := time.Parse(time.RFC3339Nano, window.Started)
		we, _ := time.Parse(time.RFC3339Nano, window.Ended)
		if r.WindowID != window.ID || se != nil || ee != nil || !end.After(start) || start.Before(ws) || end.After(we) {
			return nil, errQualification
		}
		if !ok || out[c.ID] != nil || r.Source != c.ID || r.Exit != 0 || r.Errno != 0 || len(r.Data) == 0 || len(r.Data) > 256<<10 || Digest(r.Data) != r.Digest {
			return nil, errQualification
		}
		out[c.ID] = r.Data
	}
	return out, nil
}
func roleMatches(p procstatus.Status, cap uint64) bool {
	return roleMatchesUID(p, 0, cap)
}
func roleMatchesUID(p procstatus.Status, uid, cap uint64) bool {
	if uid > uint64(^uint32(0)) {
		return false
	}
	want := uint32(uid)
	mask := fmt.Sprintf("%016x", cap)
	return p.UIDs == [4]uint32{want, want, want, want} && p.CapPrm == mask && p.CapEff == mask && p.CapBnd == mask && p.CapAmb == "0000000000000000" && p.CapInh == "0000000000000000" && p.NoNewPrivs == 1 && p.Seccomp == 2
}
func (q *DeploymentQualification) role(name string) (qualificationRole, bool) {
	if q != nil {
		for _, r := range q.result.inputs.Roles {
			if r.Role == name {
				return r, true
			}
		}
	}
	return qualificationRole{}, false
}

// DeploymentRoleBinding is a copied expected identity, never observed evidence.
type DeploymentRoleBinding struct {
	Private                                                             []DeploymentPrivateDirectoryBinding
	BinaryDigest, GuestBootID, CgroupPath, MountNamespace, Architecture string
	Capabilities                                                        uint64
	Core                                                                []CoreDomain
}

func (q *DeploymentQualification) RoleBinding(name string) (DeploymentRoleBinding, error) {
	r, ok := q.role(name)
	if !ok || q.files.path == "" {
		return DeploymentRoleBinding{}, errQualification
	}
	return DeploymentRoleBinding{Private: qualificationClone(q.result.private), BinaryDigest: r.BinaryDigest, GuestBootID: q.result.trust.Identity.GuestBootID, CgroupPath: r.CgroupPath, Capabilities: r.Capabilities, MountNamespace: q.result.trust.Identity.MountNamespace, Architecture: q.result.inputs.Architecture, Core: qualificationClone(q.result.trust.Core)}, nil
}
