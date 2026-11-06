package capacitysnapshot

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func Digest(b []byte) string         { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
func (v Verified) Raw() []byte       { return bytes.Clone(v.raw) }
func (v Verified) Payload() []byte   { return bytes.Clone(v.envelope.Payload) }
func (v Verified) Signature() []byte { return bytes.Clone(v.envelope.Signature) }
func (v Verified) Digest() string    { return v.digest }
func (v Verified) Facts() Facts      { var f Facts; _ = json.Unmarshal(v.envelope.Payload, &f); return f }

func Verify(raw []byte, trust Trust) (Verified, error) {
	if err := ValidateTrust(trust); err != nil {
		return Verified{}, err
	}
	var e Envelope
	if err := strictJSON(raw, &e); err != nil {
		return Verified{}, err
	}
	if e.Version != Version || e.KeyID != trust.KeyID || len(e.Signature) != ed25519.SignatureSize {
		return Verified{}, fmt.Errorf("unsupported signature envelope")
	}
	message := append([]byte(SignatureDomain), e.Payload...)
	if !ed25519.Verify(ed25519.PublicKey(trust.PublicKey), message, e.Signature) {
		return Verified{}, fmt.Errorf("invalid signature")
	}
	var f Facts
	if err := strictJSON(e.Payload, &f); err != nil {
		return Verified{}, err
	}
	if f.Sequence <= 0 || f.ObservedAt.IsZero() {
		return Verified{}, fmt.Errorf("invalid sample clock/sequence")
	}
	return Verified{envelope: e, facts: f, raw: bytes.Clone(raw), digest: Digest(raw)}, nil
}

// CheckFacts separates signature authenticity from complete capacity coverage.
func CheckFacts(f Facts, t Trust) error {
	if f.Identity != t.Identity || f.ManifestDigest != t.ManifestDigest {
		return fmt.Errorf("identity drift")
	}
	a := f.Anchor
	if a.Path != t.AnchorPath || a.FSUUID != t.Identity.FSUUID || a.MountID != t.Identity.MountID || a.Device != t.Identity.Device || a.FSType != "xfs" || a.ProjectID != 0 || a.XFlags&0x200 != 0 || a.XFlags > math.MaxUint32 || !contains(a.MountOptions, "prjquota", "pquota") {
		return fmt.Errorf("pool statfs anchor unproven")
	}
	if !positive(f.EngineCPUMilli) || !positive(f.EngineMemoryBytes) || !bounded(a.AvailableBytes) {
		return fmt.Errorf("invalid Engine units")
	}
	if len(f.Core) != len(t.Core) || len(f.Storage) != len(t.Storage) {
		return fmt.Errorf("unknown coverage")
	}
	core := map[string]CoreDomain{}
	for _, c := range t.Core {
		core[c.ID] = c
	}
	for _, c := range f.Core {
		expected, ok := core[c.ID]
		if !ok || c != expected {
			return fmt.Errorf("core upper unproven")
		}
		delete(core, c.ID)
	}
	domains := map[string]StorageDomain{}
	projects := map[uint64]bool{}
	paths := []string{}
	for _, d := range t.Storage {
		domains[d.ID] = d
	}
	for _, d := range f.Storage {
		e, ok := domains[d.ID]
		e.UsedBytes = d.UsedBytes
		e.Quota.UsedBlocks512 = d.Quota.UsedBlocks512
		if !ok || e != d || !validStorage(d) || !storageOnPool(d, t.Identity) {
			return fmt.Errorf("storage coverage unproven")
		}
		delete(domains, d.ID)
		projects[d.ProjectID] = true
		paths = append(paths, d.Path)
	}
	ids := map[string]bool{}
	identities := map[string]bool{}
	cgroups := []string{}
	for _, c := range f.Core {
		cgroups = append(cgroups, c.CgroupPath)
	}
	for _, c := range f.Containers {
		if !label(c.ContainerID) || !label(c.RuntimeID) || !label(c.SessionID) || c.Generation == 0 || c.Generation > math.MaxInt64 || ids[c.ContainerID] || identities[c.SessionID+"\x00"+c.RuntimeID] || !label(c.ProfileID) || c.ProfileRevision == 0 || c.ProfileRevision > math.MaxInt64 || !digestPattern.MatchString(c.ProfileDigest) {
			return fmt.Errorf("invalid container identity")
		}
		ids[c.ContainerID] = true
		identities[c.SessionID+"\x00"+c.RuntimeID] = true
		if !absolute(c.CgroupPath) || !positive(c.CPUQuotaMicros) || c.CPUQuotaMicros > math.MaxInt64/1000 || !positive(c.CPUPeriodMicros) || !positive(c.NanoCPUs) || !positive(c.MemoryMax) || !positive(c.PIDsMax) || !validStorage(c.Layer) || !storageOnPool(c.Layer, t.Identity) || projects[c.Layer.ProjectID] {
			return fmt.Errorf("container enforcement unproven")
		}
		for _, p := range paths {
			if overlaps(p, c.Layer.Path) {
				return fmt.Errorf("overlapping storage domains")
			}
		}
		for _, p := range cgroups {
			if overlaps(p, c.CgroupPath) {
				return fmt.Errorf("overlapping worker cgroup")
			}
		}
		cgroups = append(cgroups, c.CgroupPath)
		paths = append(paths, c.Layer.Path)
		projects[c.Layer.ProjectID] = true
	}
	for _, r := range f.Removals {
		if r.EngineID != t.Identity.EngineID || !label(r.ContainerID) || !label(r.RuntimeID) || !label(r.SessionID) || !label(r.SecretVolumeID) || r.Generation == 0 || r.Generation > math.MaxInt64 || r.ChannelEpoch == 0 || r.ChannelEpoch > math.MaxInt64 || r.ContainerInspectHTTPStatus != 404 || r.SecretVolumeInspectHTTPStatus != 404 {
			return fmt.Errorf("invalid raw removal observation")
		}
	}
	return nil
}
func ValidateTrust(t Trust) error {
	if err := validateTrustAdmissionPolicy(t.AdmissionPolicy); err != nil {
		return err
	}
	i := t.Identity
	if !label(t.KeyID) || len(t.PublicKey) != ed25519.PublicKeySize || !digestPattern.MatchString(t.ManifestDigest) || !label(i.EngineID) || !absolute(i.DockerRoot) || !label(i.FSUUID) || !label(i.GuestBootID) || !label(i.MountNamespace) || i.MountID == 0 || i.MountID > math.MaxInt64 || !label(i.Device) || !absolute(t.AnchorPath) || len(t.Core) == 0 {
		return fmt.Errorf("incomplete deployment trust")
	}
	ids := map[string]bool{}
	paths := []string{}
	for _, c := range t.Core {
		if !label(c.ID) || ids[c.ID] || !absolute(c.CgroupPath) || !positive(c.CPUQuotaMicros) || !positive(c.CPUPeriodMicros) || c.CPUQuotaMicros > math.MaxInt64/1000 || !positive(c.MemoryMax) || !positive(c.PIDsMax) {
			return fmt.Errorf("invalid core hard upper")
		}
		for _, p := range paths {
			if overlaps(p, c.CgroupPath) {
				return fmt.Errorf("overlapping cgroup domains")
			}
		}
		paths = append(paths, c.CgroupPath)
		ids[c.ID] = true
	}
	required := map[string]bool{"core": false, "product": false, "images": false, "logs": false, "build": false, "working_copy": false, "cache": false}
	ids = map[string]bool{}
	projects := map[uint64]bool{}
	paths = nil
	for _, d := range t.Storage {
		if !validStorage(d) || !storageOnPool(d, t.Identity) || d.UsedBytes != 0 || ids[d.ID] || projects[d.ProjectID] {
			return fmt.Errorf("invalid deployment storage domain")
		}
		for _, p := range paths {
			if overlaps(p, d.Path) {
				return fmt.Errorf("overlapping storage domains")
			}
		}
		if overlaps(t.AnchorPath, d.Path) {
			return fmt.Errorf("anchor overlaps quota domain")
		}
		ids[d.ID] = true
		projects[d.ProjectID] = true
		paths = append(paths, d.Path)

	}
	coveredDomains := map[string]bool{}
	for _, coverage := range t.Coverage {
		seen, known := required[coverage.Role]
		if !known || seen || !ids[coverage.DomainID] {
			return fmt.Errorf("invalid deployment storage coverage")
		}
		required[coverage.Role] = true
		coveredDomains[coverage.DomainID] = true
	}
	for id := range ids {
		if !coveredDomains[id] {
			return fmt.Errorf("unmapped physical storage domain")
		}
	}

	for _, seen := range required {
		if !seen {
			return fmt.Errorf("missing deployment storage domain")
		}
	}
	return nil
}
func positive(v uint64) bool { return v > 0 && bounded(v) }
func bounded(v uint64) bool  { return v <= math.MaxInt64 }
func label(s string) bool {
	return len(s) > 0 && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\n\r\t")
}
func absolute(s string) bool {
	return label(s) && filepath.IsAbs(s) && filepath.Clean(s) == s && s != "/"
}
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Raw values use Linux UAPI constants (fs.h / dqblk_xfs.h):
// FS_XFLAG_PROJINHERIT=0x200, FS_QUOTA_PDQ_ACCT|ENFD=0x30,
// FS_PROJ_QUOTA=2; versions are 1. Realtime data/inheritance is outside
// this data-device budget and must not silently bypass its block quota.
func validStorage(d StorageDomain) bool {
	q := d.Quota
	return label(d.ID) && absolute(d.Path) && d.ProjectID > 0 && d.ProjectID <= math.MaxUint32 && positive(d.HardBytes) && bounded(d.UsedBytes) &&
		label(d.FSUUID) && label(d.Device) && d.MountID > 0 && d.MountID <= math.MaxInt64 && d.FSType == "xfs" && d.XFlags <= math.MaxUint32 && d.XFlags&0x200 != 0 && d.XFlags&0x101 == 0 &&
		q.Format == "linux_xfs_quotactl_v1" && q.StateVersion == 1 && q.StateFlags <= math.MaxUint16 && q.StateFlags&^uint64(0x3f) == 0 && q.StateFlags&0x30 == 0x30 && q.Version == 1 && q.Flags == 2 && q.ProjectID == d.ProjectID &&
		q.HardBlocks512 <= math.MaxInt64/512 && q.UsedBlocks512 <= math.MaxInt64/512 && q.HardBlocks512*512 == d.HardBytes && q.UsedBlocks512*512 == d.UsedBytes
}
func storageOnPool(d StorageDomain, i Identity) bool {
	return d.FSUUID == i.FSUUID && d.Device == i.Device && d.MountID == i.MountID
}

func contains(values []string, want ...string) bool {
	for _, v := range values {
		for _, w := range want {
			if v == w {
				return true
			}
		}
	}
	return false
}

// Reject duplicate keys before decoding (encoding/json otherwise uses last key).
// JSON numeric spellings are restricted to unsigned decimal integers, including
// within unknown fields, before the schema-specific decoder runs.
func strictJSON(raw []byte, v any) error {
	if len(raw) == 0 || len(raw) > MaxBytes {
		return fmt.Errorf("snapshot size invalid")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := scanValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	if err := requiredFields(raw, reflect.TypeOf(v).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if reflect.ValueOf(v).Elem().IsZero() {
		return fmt.Errorf("empty JSON object")
	}
	return nil
}
func scanValue(d *json.Decoder, depth int) error {
	if depth > 24 {
		return fmt.Errorf("JSON depth exceeded")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch x := token.(type) {
	case json.Delim:
		switch x {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := key.(string)
				if !ok || keys[s] {
					return fmt.Errorf("duplicate JSON key")
				}
				keys[s] = true
				if e = scanValue(d, depth+1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := scanValue(d, depth+1); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("invalid delimiter")
		}
		_, err = d.Token()
		return err
	case json.Number:
		for _, c := range x.String() {
			if c < '0' || c > '9' {
				return fmt.Errorf("unsigned integer required")
			}
		}
	case nil:
		return fmt.Errorf("null facts forbidden")
	}
	return nil
}

// Require every schema field, including meaningful zero observations such as
// project_id=0 and xflags=0. Absence must never masquerade as an observed zero.
func requiredFields(raw []byte, typ reflect.Type) error {
	if typ == reflect.TypeOf(time.Time{}) || typ == reflect.TypeOf([]byte{}) {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		expected := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			expected[name] = true
			b, ok := fields[name]
			if typ == reflect.TypeOf(Trust{}) && field.Name == "AdmissionPolicy" {
				if !ok {
					continue
				}
				if err := requiredFields(b, field.Type.Elem()); err != nil {
					return err
				}
				continue
			}
			if !ok {
				return fmt.Errorf("missing required field %s", name)
			}
			if err := requiredFields(b, field.Type); err != nil {
				return err
			}
		}
		for name := range fields {
			if !expected[name] {
				return fmt.Errorf("unknown field %s", name)
			}
		}
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for _, item := range items {
			if err := requiredFields(item, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
