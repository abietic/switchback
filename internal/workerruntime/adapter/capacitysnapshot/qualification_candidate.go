package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

const (
	qualificationGuestCandidateRoot = "/var/lib/switchback/qualification-candidate"
	candidateSchema                 = "switchback.qualification.candidate.v1"
	stagedSchema                    = "switchback.qualification.staged.v1"
	windowOpenSchema                = "switchback.qualification.window_open.v1"
	nativeObservationSchema         = "switchback.qualification.native_observations.v1"
	qualificationBudget             = 240 * time.Second
)

// Deployment-owned, untrusted preparation materials. No constructor here grants
// a qualification handle. Actual mount IDs/processes are frozen only by prepare.
type qualificationStaged struct {
	Schema      string                        `json:"schema"`
	WindowID    string                        `json:"window_id"`
	Nonce       string                        `json:"nonce"`
	TestProject uint32                        `json:"test_project"`
	Source      qualificationSource           `json:"source"`
	Inventory   DeploymentInventory           `json:"inventory"`
	Trust       Trust                         `json:"trust_prefix"`
	Binding     DeploymentPublisherBinding    `json:"bindings"`
	Artifacts   []qualificationArtifact       `json:"artifacts"`
	Writers     []qualificationWriterArtifact `json:"writer_artifacts"`
}
type qualificationArtifact struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type qualificationWriterArtifact struct {
	ID               string `json:"id"`
	Executable       string `json:"executable"`
	Config           string `json:"config"`
	ExecutableDigest string `json:"executable_digest"`
	ConfigDigest     string `json:"config_digest"`
}
type qualificationFrozenProcess struct {
	Role       string                    `json:"role"`
	PID        uint64                    `json:"pid"`
	StartTicks uint64                    `json:"start_ticks"`
	Snapshot   qualificationProcReadback `json:"snapshot"`
}
type qualificationProcReadback struct {
	Stat             []byte `json:"stat"`
	Status           []byte `json:"status"`
	Cgroup           []byte `json:"cgroup"`
	MountInfo        []byte `json:"mountinfo"`
	ExecutableDigest string `json:"executable_digest"`
	Boot             string `json:"boot"`
}
type qualificationCandidateRecord struct {
	Schema             string                       `json:"schema"`
	Fingerprint        string                       `json:"fingerprint"`
	OpenDigest         string                       `json:"open_digest"`
	StagedDigest       string                       `json:"staged_digest"`
	StaticIntentDigest string                       `json:"static_intent_digest"`
	TestProject        uint32                       `json:"test_project"`
	Source             qualificationSource          `json:"source"`
	Inventory          DeploymentInventory          `json:"inventory"`
	Trust              Trust                        `json:"trust_prefix"`
	Binding            DeploymentPublisherBinding   `json:"bindings"`
	Processes          []qualificationFrozenProcess `json:"processes"`
}
type qualificationWindowOpen struct {
	Schema       string `json:"schema"`
	ID           string `json:"id"`
	Nonce        string `json:"nonce"`
	StagedDigest string `json:"staged_digest"`
	GuestBoot    string `json:"guest_boot"`
	Started      string `json:"started"`
	MonotonicNS  int64  `json:"monotonic_ns"`
}
type qualificationNativeObservation struct {
	Schema          string               `json:"schema"`
	Candidate       string               `json:"candidate"`
	OpenDigest      string               `json:"open_digest"`
	WindowID        string               `json:"window_id"`
	GuestBootBefore string               `json:"guest_boot_before"`
	GuestBootAfter  string               `json:"guest_boot_after"`
	Completed       string               `json:"completed"`
	Checks          []qualificationCheck `json:"checks"`
	Cleanup         qualificationCleanup `json:"cleanup"`
}
type qualificationCleanup struct {
	Complete    bool                         `json:"complete"`
	Project     uint32                       `json:"project"`
	Initial     []byte                       `json:"initial"`
	Successor   []byte                       `json:"successor"`
	Final       []byte                       `json:"final"`
	Helpers     []qualificationHelperCleanup `json:"helpers"`
	RootRemoved bool                         `json:"root_removed"`
	FDsClosed   bool                         `json:"fds_closed"`
}
type qualificationHelperCleanup struct {
	Case          string `json:"case"`
	CID           string `json:"cid"`
	Exit          int64  `json:"exit"`
	DeleteStatus  uint64 `json:"delete_status"`
	InspectStatus uint64 `json:"inspect_status"`
}
type qualificationHostBefore struct {
	Schema     string               `json:"schema"`
	Candidate  string               `json:"candidate"`
	OpenDigest string               `json:"open_digest"`
	HostBoot   string               `json:"host_boot"`
	Started    string               `json:"started"`
	Checks     []qualificationCheck `json:"checks"`
}
type qualificationCommonEnd struct {
	Schema           string              `json:"schema"`
	Candidate        string              `json:"candidate"`
	HostBeforeDigest string              `json:"host_before_digest"`
	NativeDigest     string              `json:"native_digest"`
	HostDigest       string              `json:"host_digest"`
	Window           qualificationWindow `json:"window"`
}

func qualificationRemaining(w qualificationWindowOpen, boot string, monotonic int64, now time.Time) (time.Duration, error) {
	start, e := time.Parse(time.RFC3339Nano, w.Started)
	if e != nil || w.Schema != windowOpenSchema || !qualificationWindowLabel(w.ID) || !containerIDPattern.MatchString(w.Nonce) || boot != w.GuestBoot || !qualificationUUIDPattern.MatchString(boot) || w.MonotonicNS < 0 || monotonic < w.MonotonicNS {
		return 0, errQualification
	}
	elapsed := time.Duration(monotonic - w.MonotonicNS)
	wall := now.Sub(start)
	// Detect jumps instead of manufacturing matching timestamps. The allowance is
	// only clock-read ordering; it is never an extension of the monotonic budget.
	if elapsed >= qualificationBudget || wall < 0 || wall >= qualificationBudget || wall-elapsed > time.Second || elapsed-wall > time.Second {
		return 0, fmt.Errorf("qualification common deadline/clock invalid")
	}
	return qualificationBudget - elapsed, nil
}
func qualificationEncode(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil || len(b) == 0 || len(b) > MaxBytes {
		return nil, fmt.Errorf("qualification aggregate oversized")
	}
	return b, nil
}
func qualificationCheckRaw(id, window string, start, end time.Time, raw []byte) (qualificationCheck, error) {
	if !end.After(start) || len(raw) == 0 || len(raw) > 256<<10 {
		return qualificationCheck{}, errQualification
	}
	return qualificationCheck{ID: id, Observation: qualificationRaw{WindowID: window, Started: start.UTC().Format(time.RFC3339Nano), Ended: end.UTC().Format(time.RFC3339Nano), Source: id, Data: raw, Digest: Digest(raw)}}, nil
}
func qualificationProduced(role string, source qualificationSource) (qualificationProducer, error) {
	for _, b := range source.Builds {
		if b.Role == role {
			return qualificationProducer{Role: role, ArtifactDigest: b.BinaryDigest, BuildDigest: qualificationDigest(b), InputRoot: b.InputRoot}, nil
		}
	}
	return qualificationProducer{}, errQualification
}
func qualificationManifestPrefix(c qualificationCandidateRecord) (AdmissionManifest, error) {
	if c.Trust.AdmissionPolicy == nil {
		return AdmissionManifest{}, errQualification
	}
	p, e := qualificationProfileRefs()
	if e != nil {
		return AdmissionManifest{}, e
	}
	return AdmissionManifest{Schema: AdmissionManifestVersion, Guarantee: *c.Trust.AdmissionPolicy, Identity: c.Trust.Identity, ResourceProfile: p.resource, NetworkProfile: p.network, KeyID: c.Trust.KeyID, PublicKey: c.Trust.PublicKey, SourceManifestDigest: qualificationDigest(c.Source), DeploymentInventoryDigest: qualificationDigest(c.Inventory)}, nil
}
func assembleQualification(c qualificationCandidateRecord, h qualificationHostReceipt, n qualificationNativeObservation) (map[string][]byte, error) {
	if c.Schema != candidateSchema || n.Schema != nativeObservationSchema || n.Candidate != c.Fingerprint || h.Candidate != c.Fingerprint || n.WindowID != h.Window.ID || n.OpenDigest != c.OpenDigest || n.GuestBootBefore != h.Window.GuestBootBefore || n.GuestBootAfter != h.Window.GuestBootAfter || !n.Cleanup.Complete {
		return nil, errQualification
	}
	if e := validateQualificationCleanup(n.Cleanup, c.TestProject); e != nil {
		return nil, e
	}
	producer, e := qualificationProduced("native-producer", c.Source)
	if e != nil {
		return nil, e
	}
	native := qualificationNativeReceipt{Schema: qualificationNativeV2, Kind: "linux_xfs_enforcement_v1", Lifecycle: qualificationLifecycle, Candidate: c.Fingerprint, Window: h.Window, Producer: producer, Checks: n.Checks}
	m, e := qualificationManifestPrefix(c)
	if e != nil {
		return nil, e
	}
	f := map[string][]byte{}
	for name, v := range map[string]any{"source-manifest.json": c.Source, "deployment-inventory.json": c.Inventory, "host-boundary-receipt.json": h, "native-enforcement-receipt.json": native} {
		b, e := qualificationEncode(v)
		if e != nil {
			return nil, e
		}
		f[name] = b
	}
	m.HostBoundaryReceiptDigest = Digest(f["host-boundary-receipt.json"])
	m.NativeEnforcementReceiptDigest = Digest(f["native-enforcement-receipt.json"])
	f["admission-manifest.json"], e = qualificationEncode(m)
	if e != nil {
		return nil, e
	}
	tr := qualificationClone(c.Trust)
	tr.ManifestDigest = Digest(f["admission-manifest.json"])
	f["trust.json"], e = qualificationEncode(tr)
	if e != nil {
		return nil, e
	}
	f["publisher-bindings.json"], e = qualificationEncode(qualificationBinding{Schema: "switchback.capacity.publisher_bindings.v1", ManifestDigest: tr.ManifestDigest, Core: c.Binding.Core, WorkerImageDigest: c.Binding.WorkerImageDigest})
	if e != nil {
		return nil, e
	}
	f["observer-deployment.env"] = []byte("SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST=" + tr.ManifestDigest + "\n")
	// This is the same production semantic validator used by Open/Await, never a
	// test/DTO success factory. No final file is emitted if any predicate fails.
	if _, e = validateDeploymentQualification(tr, f); e != nil {
		return nil, e
	}
	return f, nil
}
func validateQualificationCleanup(c qualificationCleanup, project uint32) error {
	if !c.Complete || project == 0 || project >= 0xfffffffe || c.Project != project || !c.RootRemoved || !c.FDsClosed || len(c.Helpers) != 9 {
		return fmt.Errorf("qualification cleanup unknown")
	}
	if !qualificationUnusedQuota(c.Initial, project) || !qualificationUnusedQuota(c.Successor, project+1) || !qualificationUnusedQuota(c.Final, project) {
		return fmt.Errorf("qualification project not clean")
	}
	seen := map[string]bool{}
	cids := map[string]bool{}
	for _, h := range c.Helpers {
		if !qualificationHelperCase(h.Case) || seen[h.Case] || cids[h.CID] || !containerIDPattern.MatchString(h.CID) || h.Exit != 0 || h.DeleteStatus != 204 || h.InspectStatus != 404 {
			return fmt.Errorf("qualification helper cleanup unknown")
		}
		seen[h.Case] = true
		cids[h.CID] = true
	}
	return nil
}
func qualificationUnusedQuota(raw []byte, id uint32) bool {
	if !createdQuotaRecord(raw, id) {
		return false
	}
	// All limits, usage, timers and warnings (including realtime fields) must
	// be clear. Version/type/fieldmask/project ID occupy only bytes 0..7.
	for _, b := range raw[8:] {
		if b != 0 {
			return false
		}
	}
	return true
}
func qualificationHelperCase(s string) bool {
	switch s {
	case "empty", "nonempty", "private", "sticky", "without-chown", "without-dac-override", "without-fowner", "worker-handoff", "worker":
		return true
	}
	return false
}
func qualificationSameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func qualificationLeaf(n string) bool {
	return n != "" && n != "." && n != ".." && filepath.Base(n) == n
}

// Fixed-root public entrypoints are the only production construction surface.
// Platform implementations never accept paths, adapters or test switches.
func AssembleQualification(ctx context.Context) error { return assembleQualificationInstalled(ctx) }

// QualificationWindowRemaining is the fixed-root guest-side deadline check
// for the later installer. It reads the original begin leaf and the current
// guest boot/monotonic clock; the caller cannot supply or reset a window.
func QualificationWindowRemaining(ctx context.Context) (time.Duration, error) {
	dir, err := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if err != nil {
		return 0, err
	}
	defer dir.close()
	var opened qualificationWindowOpen
	if err := dir.decode(ctx, "window-open.json", &opened); err != nil {
		return 0, err
	}
	boot, err := qualificationGuestBoot(ctx)
	if err != nil {
		return 0, err
	}
	mono, err := qualificationMonotonicNS()
	if err != nil {
		return 0, err
	}
	return qualificationRemaining(opened, boot, mono, time.Now())
}
