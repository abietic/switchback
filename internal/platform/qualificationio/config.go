package qualificationio

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"

	"github.com/abietic/switchback/internal/platform/config"
)

const (
	ControlPath      = "/qualification/control.json"
	DataPath         = "/qualification-data"
	NativeHelperPath = "/usr/local/bin/switchback-qualification-native"
	CompatHelperPath = "/usr/local/bin/switchback-qualification-compat"
	ControlSchema    = "switchback.qualification.control.v1"
	FrameSchema      = "switchback.qualification.frame.v1"
	MaxFrameBytes    = 256 << 10
	QuotaWriteBytes  = 8192
)

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var hexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var labelRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var deviceRE = regexp.MustCompile(`^(0|[1-9][0-9]*):(0|[1-9][0-9]*)$`)

// Object is expected identity from the trusted producer's fresh object setup.
// A helper always reads the actual FD again; this is never an observation.
type Object struct {
	Device    string `json:"device"`
	FSUUID    string `json:"fs_uuid"`
	Inode     uint64 `json:"inode"`
	Owner     uint64 `json:"owner"`
	Mode      uint64 `json:"mode"`
	ProjectID uint64 `json:"project_id"`
}

// Control contains no host PID, key, endpoint, executable path, shell, arbitrary
// command or resource number. It is written once after exact ContainerCreate
// and before Start into a root-owned 0444 leaf on a read-only directory bind.
type Control struct {
	Schema       string                           `json:"schema"`
	Candidate    string                           `json:"candidate"`
	WindowID     string                           `json:"window_id"`
	Nonce        string                           `json:"nonce"`
	Token        string                           `json:"token"`
	Architecture string                           `json:"architecture"`
	Role         string                           `json:"role"`
	ContainerID  string                           `json:"container_id"`
	ImageDigest  string                           `json:"image_digest"`
	BinaryDigest string                           `json:"binary_digest"`
	Profile      config.RuntimeResourceProfileRef `json:"profile"`
	Network      config.RuntimeNetworkEvidence    `json:"network"`
	HostTestRoot string                           `json:"host_test_root"`
	MountRoot    string                           `json:"mount_root"`
	Root         Object                           `json:"root"`
	Target       Object                           `json:"target"`
	Child        Object                           `json:"child"`
	NativeDigest string                           `json:"native_digest"`
	CompatDigest string                           `json:"compat_digest"`
	BackendCase  string                           `json:"backend_case"`
}

func (c Control) Validate() error {
	if c.Schema != ControlSchema || !digestRE.MatchString(c.Candidate) || !safeWindow(c.WindowID) || !hexRE.MatchString(c.Nonce) || !hexRE.MatchString(c.Token) || (c.Architecture != "amd64" && c.Architecture != "arm64") || !hexRE.MatchString(c.ContainerID) || !digestRE.MatchString(c.ImageDigest) || !digestRE.MatchString(c.BinaryDigest) {
		return fmt.Errorf("qualification control identity invalid")
	}
	profile, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil || c.Profile != profile.Ref() || c.Network != network.Evidence() {
		return fmt.Errorf("qualification profile invalid")
	}
	if !path.IsAbs(c.HostTestRoot) || path.Clean(c.HostTestRoot) != c.HostTestRoot || path.Base(c.HostTestRoot) != c.WindowID || path.Base(path.Dir(c.HostTestRoot)) != ".qualification" || !path.IsAbs(c.MountRoot) || path.Clean(c.MountRoot) != c.MountRoot {
		return fmt.Errorf("qualification product root invalid")
	}
	empty := c.Role == "backend" && c.BackendCase == "empty"
	if !validObject(c.Root, true) || !validObject(c.Target, true) || (!empty && !validObject(c.Child, false)) || (empty && c.Child != (Object{})) || c.Root.Device != c.Target.Device || c.Root.FSUUID != c.Target.FSUUID || (!empty && (c.Child.Device != c.Target.Device || c.Child.FSUUID != c.Target.FSUUID || c.Target.Inode == c.Child.Inode || c.Root.Inode == c.Child.Inode)) || c.Root.Inode == c.Target.Inode {
		return fmt.Errorf("qualification objects invalid")
	}
	if c.Role == "worker" {
		if c.BackendCase != "" || !digestRE.MatchString(c.NativeDigest) || !digestRE.MatchString(c.CompatDigest) || c.NativeDigest == c.CompatDigest || c.NativeDigest == c.BinaryDigest || c.CompatDigest == c.BinaryDigest || c.Root.Owner != 0 || c.Target.Owner != 10002 || c.Child.Owner != 10002 || c.Root.Mode != 0040755 || c.Target.Mode != 0040700 || c.Child.Mode != 0100600 || c.Target.ProjectID == 0 || c.Target.ProjectID >= 0xfffffffe || c.Child.ProjectID != c.Target.ProjectID {
			return fmt.Errorf("qualification worker control invalid")
		}
	} else if c.Role == "backend" {
		if !backendCase(c.BackendCase) || c.NativeDigest != "" || c.CompatDigest != "" {
			return fmt.Errorf("qualification backend control invalid")
		}
		if c.Root.Mode != 0040755 || c.Root.Owner != 0 || (!empty && c.Child.Mode != 0100600) || (!negativeCase(c.BackendCase) && c.Target.Mode != 0040755) {
			return fmt.Errorf("qualification backend initial modes invalid")
		}
		if c.BackendCase == "worker-handoff" {
			if c.Root.Owner != 0 || c.Target.Owner != 0 || c.Child.Owner != 0 || c.Target.ProjectID == 0 || c.Target.ProjectID >= 0xfffffffe || c.Child.ProjectID != c.Target.ProjectID {
				return fmt.Errorf("qualification handoff control invalid")
			}
		} else if negativeCase(c.BackendCase) {
			if c.Target.Owner != 10002 || c.Child.Owner != 10002 || c.Target.Mode != uint64(0040000|backendMode(c.BackendCase)) {
				return fmt.Errorf("qualification negative ownership invalid")
			}
		} else if c.Target.Owner != 0 || (!empty && c.Child.Owner != 0) {
			return fmt.Errorf("qualification holder initial ownership invalid")
		}
	} else {
		return fmt.Errorf("qualification role invalid")
	}
	return nil
}

func validObject(o Object, directory bool) bool {
	mode := uint64(0100000)
	if directory {
		mode = 0040000
	}
	return deviceRE.MatchString(o.Device) && uuidRE.MatchString(o.FSUUID) && o.Inode > 0 && (o.Owner == 0 || o.Owner == 10002) && o.Mode&0170000 == mode && o.Mode & ^uint64(0177777) == 0 && o.ProjectID < 0xffffffff
}
func safeWindow(s string) bool { return labelRE.MatchString(s) && s != "." && s != ".." }
func backendCase(s string) bool {
	switch s {
	case "empty", "nonempty", "private", "sticky", "without-chown", "without-dac-override", "without-fowner", "worker-handoff":
		return true
	}
	return false
}
func negativeCase(s string) bool {
	return s == "without-chown" || s == "without-dac-override" || s == "without-fowner"
}
func positiveCase(s string) string {
	switch s {
	case "without-chown":
		return "nonempty"
	case "without-dac-override":
		return "private"
	case "without-fowner":
		return "sticky"
	}
	return s
}
func targetPath(c Control) string {
	if c.Role == "worker" || c.BackendCase == "worker-handoff" {
		return DataPath + "/target"
	}
	return DataPath + "/handoff/" + positiveCase(c.BackendCase)
}

// EncodeControl and DecodeControl are untrusted canonical codecs, not authority.
func EncodeControl(c Control) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}
func DecodeControl(raw []byte) (Control, error) {
	var c Control
	if err := strictCanonical(raw, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// Canonical exact re-encoding rejects duplicates, omitted members, unknown
// members, mixed numeric spellings and multiple JSON values in this private ABI.
func strictCanonical(raw []byte, dst any) error {
	if len(raw) == 0 || len(raw) > MaxFrameBytes {
		return fmt.Errorf("qualification JSON size invalid")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("qualification JSON invalid")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("qualification JSON trailing data")
	}
	canonical, err := json.Marshal(dst)
	if err != nil || !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		return fmt.Errorf("qualification JSON is not canonical")
	}
	return nil
}

func bytesDigest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
