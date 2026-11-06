package capacitysnapshot

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"golang.org/x/sys/unix"
)

const AdmissionManifestVersion = "switchback.capacity.admission_manifest.v1"
const DeploymentInventoryVersion = "switchback.capacity.deployment_inventory.v1"

// AdmissionManifest contains structural metadata only. Neither successful JSON
// parsing nor evidence byte digests prove host safety/native enforcement or grant
// capacity. All JSON names are fixed, required, and case sensitive.
type AdmissionManifest struct {
	Schema                         string                           `json:"schema"`
	Guarantee                      workerapp.CapacityGuaranteeRef   `json:"guarantee"`
	Identity                       Identity                         `json:"identity"`
	ResourceProfile                config.RuntimeResourceProfileRef `json:"resource_profile"`
	NetworkProfile                 config.RuntimeResourceProfileRef `json:"network_profile"`
	KeyID                          string                           `json:"key_id"`
	PublicKey                      []byte                           `json:"public_key"`
	SourceManifestDigest           string                           `json:"source_manifest_digest"`
	DeploymentInventoryDigest      string                           `json:"deployment_inventory_digest"`
	HostBoundaryReceiptDigest      string                           `json:"host_boundary_receipt_digest"`
	NativeEnforcementReceiptDigest string                           `json:"native_enforcement_receipt_digest"`
}

type AdmissionBinary struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// DeploymentInventory binds a deployment candidate and its image/binary
// inventory. The input digest is metadata, not an assertion of native validity.
type DeploymentInventory struct {
	Schema                 string            `json:"schema"`
	SourceManifestDigest   string            `json:"source_manifest_digest"`
	DeploymentInputsDigest string            `json:"deployment_inputs_digest"`
	WorkerImage            string            `json:"worker_image"`
	WorkerImageDigest      string            `json:"worker_image_digest"`
	Binaries               []AdmissionBinary `json:"binaries"`
}

type AdmissionManifestMetadata struct {
	Manifest  AdmissionManifest
	Inventory DeploymentInventory
}

func ParseAdmissionManifest(raw []byte, trust Trust) (AdmissionManifest, error) {
	var m AdmissionManifest
	invalid := fmt.Errorf("runtime capacity admission manifest invalid")
	if strictJSON(raw, &m) != nil || ValidateTrust(trust) != nil || trust.AdmissionPolicy == nil {
		return AdmissionManifest{}, invalid
	}
	if m.Schema != AdmissionManifestVersion || validateTrustAdmissionPolicy(&m.Guarantee) != nil || m.Guarantee != *trust.AdmissionPolicy || m.Identity != trust.Identity || m.KeyID != trust.KeyID || !bytes.Equal(m.PublicKey, trust.PublicKey) || trust.ManifestDigest != Digest(raw) {
		return AdmissionManifest{}, invalid
	}
	if _, err := config.ProductionProfileRegistry().ResolveRuntimeResource(m.ResourceProfile); err != nil {
		return AdmissionManifest{}, invalid
	}
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil || m.NetworkProfile != (config.RuntimeResourceProfileRef{ID: network.ID, Revision: network.Revision, Digest: network.Digest}) {
		return AdmissionManifest{}, invalid
	}
	for _, d := range []string{m.SourceManifestDigest, m.DeploymentInventoryDigest, m.HostBoundaryReceiptDigest, m.NativeEnforcementReceiptDigest} {
		if !digestPattern.MatchString(d) {
			return AdmissionManifest{}, invalid
		}
	}
	return m, nil
}

// ReadAdmissionManifest accepts only a deployment-configured canonical path.
// Evidence paths cannot be chosen by JSON: these four fixed sibling leaves are
// the complete mapping. Digests authenticate bytes only, not receipt semantics.
func ReadAdmissionManifest(path string, trust Trust) (AdmissionManifestMetadata, error) {
	invalid := fmt.Errorf("runtime capacity admission manifest or evidence invalid or unreadable")
	if !absolute(path) {
		return AdmissionManifestMetadata{}, invalid
	}
	dir, err := openAdmissionDirectory(filepath.Dir(path))
	if err != nil {
		return AdmissionManifestMetadata{}, invalid
	}
	defer dir.Close()
	raw, err := readAdmissionFile(dir, filepath.Base(path))
	if err != nil {
		return AdmissionManifestMetadata{}, invalid
	}
	m, err := ParseAdmissionManifest(raw, trust)
	if err != nil {
		return AdmissionManifestMetadata{}, invalid
	}
	evidence := make([][]byte, 0, 4)
	for _, leaf := range []string{"source-manifest.json", "deployment-inventory.json", "host-boundary-receipt.json", "native-enforcement-receipt.json"} {
		data, err := readAdmissionFile(dir, leaf)
		if err != nil {
			return AdmissionManifestMetadata{}, invalid
		}
		evidence = append(evidence, data)
	}
	inventory, err := bindAdmissionEvidence(m, evidence[0], evidence[1], evidence[2], evidence[3])
	if err != nil {
		return AdmissionManifestMetadata{}, invalid
	}

	return AdmissionManifestMetadata{Manifest: m, Inventory: inventory}, nil
}

func parseDeploymentInventory(raw []byte, sourceDigest string) (DeploymentInventory, error) {
	var v DeploymentInventory
	invalid := fmt.Errorf("runtime capacity deployment inventory invalid")
	if strictJSON(raw, &v) != nil || v.Schema != DeploymentInventoryVersion || v.SourceManifestDigest != sourceDigest || !digestPattern.MatchString(v.DeploymentInputsDigest) || !digestPattern.MatchString(v.WorkerImageDigest) || strings.TrimSpace(v.WorkerImage) != v.WorkerImage || v.WorkerImage == "" || len(v.WorkerImage) > 512 || strings.ContainsAny(v.WorkerImage, "\x00\n\r\t") || len(v.Binaries) == 0 {
		return DeploymentInventory{}, invalid
	}
	names := map[string]bool{}
	for _, b := range v.Binaries {
		if !label(b.Name) || names[b.Name] || !digestPattern.MatchString(b.Digest) {
			return DeploymentInventory{}, invalid
		}
		names[b.Name] = true
	}
	return v, nil
}

// A distinct reader preserves legacy reader compatibility. Every opened
// ancestor is root owned with no group/world write bits; every leaf is root
// owned, regular, bounded, and read-only. NOFOLLOW applies at every hop.
func openAdmissionDirectory(path string) (*os.File, error) {
	if path != "/" && !absolute(path) {
		return nil, fmt.Errorf("unsafe admission directory")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	check := func(fd int) error {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		if st.Uid != 0 || st.Mode&0022 != 0 {
			return fmt.Errorf("unsafe admission ancestor")
		}
		return nil
	}
	if err = check(fd); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if path == "/" {
		return os.NewFile(uintptr(fd), path), nil
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
		if e = check(fd); e != nil {
			unix.Close(fd)
			return nil, e
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}
func readAdmissionFile(dir *os.File, leaf string) ([]byte, error) {
	if leaf == "" || leaf == "." || leaf == ".." || filepath.Base(leaf) != leaf {
		return nil, fmt.Errorf("unsafe admission leaf")
	}
	fd, err := unix.Openat(int(dir.Fd()), leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), leaf)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&0222 != 0 {
		return nil, fmt.Errorf("unsafe admission file")
	}
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > MaxBytes {
		return nil, fmt.Errorf("unbounded admission file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || len(raw) > MaxBytes || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, fmt.Errorf("admission file changed")
	}
	return raw, nil
}

// This pure comparison returns metadata only; it cannot establish deployment
// authority. Production supplies bytes exclusively from the fixed safe reader.
func bindAdmissionEvidence(m AdmissionManifest, source, inventory, host, native []byte) (DeploymentInventory, error) {
	for _, e := range []struct {
		data   []byte
		digest string
	}{{source, m.SourceManifestDigest}, {inventory, m.DeploymentInventoryDigest}, {host, m.HostBoundaryReceiptDigest}, {native, m.NativeEnforcementReceiptDigest}} {
		if len(e.data) == 0 || len(e.data) > MaxBytes || Digest(e.data) != e.digest {
			return DeploymentInventory{}, fmt.Errorf("admission evidence digest mismatch")
		}
	}
	return parseDeploymentInventory(inventory, m.SourceManifestDigest)
}
