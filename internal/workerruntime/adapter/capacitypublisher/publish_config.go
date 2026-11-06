package capacitypublisher

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"io"
	"path"
	"reflect"
	"strings"
)

const publisherBindingsVersion = "switchback.capacity.publisher_bindings.v1"

var publisherConfigLeaves = []string{"trust.json", "admission-manifest.json", "publisher-bindings.json", "source-manifest.json", "deployment-inventory.json", "host-boundary-receipt.json", "native-enforcement-receipt.json"}

type publisherCoreBinding struct {
	ContainerID string `json:"container_id"`
	ImageDigest string `json:"image_digest"`
	CoreID      string `json:"core_id"`
}
type publisherBinding struct {
	Schema            string                 `json:"schema"`
	ManifestDigest    string                 `json:"manifest_digest"`
	Core              []publisherCoreBinding `json:"core"`
	WorkerImageDigest string                 `json:"worker_image_digest"`
}
type publisherConfig struct {
	qualification *capacitysnapshot.DeploymentQualification
	files         *publisherFileSet
	trust         capacitysnapshot.Trust
	binding       publisherBinding
	core          []CoreContainerBinding
	plan          capacityprobe.PoolPlan
}

func readPublisherConfig(ctx context.Context, fs publisherFiles, dir string) (*publisherConfig, error) {
	set, e := readPublisherSet(ctx, fs, dir, publisherConfigLeaves, capacitysnapshot.MaxBytes)
	if e != nil {
		return nil, e
	}
	c, e := parsePublisherConfig(set.data)
	if e != nil {
		set.clear()
		return nil, e
	}
	c.files = set
	return c, nil
}
func parsePublisherConfig(data map[string][]byte) (*publisherConfig, error) {
	if len(data) != len(publisherConfigLeaves) {
		return nil, errPublisherInvalid
	}
	for _, n := range publisherConfigLeaves {
		if _, e := publisherJSONValue(data[n]); e != nil {
			return nil, errPublisherInvalid
		}
	}
	var trust capacitysnapshot.Trust
	var b publisherBinding
	var inventory capacitysnapshot.DeploymentInventory
	if publisherStrictJSON(data["trust.json"], &trust) != nil || capacitysnapshot.ValidateTrust(trust) != nil || trust.AdmissionPolicy == nil || publisherStrictJSON(data["publisher-bindings.json"], &b) != nil {
		return nil, errPublisherInvalid
	}
	m, e := capacitysnapshot.ParseAdmissionManifest(data["admission-manifest.json"], trust)
	if e != nil {
		return nil, errPublisherInvalid
	}
	for _, v := range []struct{ name, digest string }{{"source-manifest.json", m.SourceManifestDigest}, {"deployment-inventory.json", m.DeploymentInventoryDigest}, {"host-boundary-receipt.json", m.HostBoundaryReceiptDigest}, {"native-enforcement-receipt.json", m.NativeEnforcementReceiptDigest}} {
		if capacitysnapshot.Digest(data[v.name]) != v.digest {
			return nil, errPublisherInvalid
		}
	}
	if publisherStrictJSON(data["deployment-inventory.json"], &inventory) != nil || inventory.Schema != capacitysnapshot.DeploymentInventoryVersion || inventory.SourceManifestDigest != m.SourceManifestDigest || !digestPattern.MatchString(inventory.DeploymentInputsDigest) || !digestPattern.MatchString(inventory.WorkerImageDigest) || inventory.WorkerImage == "" || strings.TrimSpace(inventory.WorkerImage) != inventory.WorkerImage || len(inventory.WorkerImage) > 512 || strings.ContainsAny(inventory.WorkerImage, "\x00\n\r\t") || len(inventory.Binaries) == 0 {
		return nil, errPublisherInvalid
	}
	seen := map[string]bool{}
	for _, v := range inventory.Binaries {
		if v.Name == "" || len(v.Name) > 512 || strings.TrimSpace(v.Name) != v.Name || strings.ContainsAny(v.Name, "\x00\n\r\t") || seen[v.Name] || !digestPattern.MatchString(v.Digest) {
			return nil, errPublisherInvalid
		}
		seen[v.Name] = true
	}
	if b.Schema != publisherBindingsVersion || b.ManifestDigest != trust.ManifestDigest || b.WorkerImageDigest != inventory.WorkerImageDigest || len(b.Core) == 0 || len(b.Core) > 32 {
		return nil, errPublisherInvalid
	}
	c := &publisherConfig{trust: trust, binding: b, plan: capacityprobe.PoolPlan{AnchorPath: trust.AnchorPath}}
	if !publisherPoolPath(trust.AnchorPath) {
		return nil, errPublisherInvalid
	}
	for _, v := range b.Core {
		c.core = append(c.core, CoreContainerBinding{v.ContainerID, v.ImageDigest, v.CoreID})
	}
	if _, e := NewMapper(trust, c.core, b.WorkerImageDigest); e != nil {
		return nil, errPublisherInvalid
	}
	for _, v := range trust.Core {
		root := "/sys/fs/cgroup/switchback.slice/switchback-core.slice"
		if !publisherCanonical(v.CgroupPath) || (v.CgroupPath != root && !strings.HasPrefix(v.CgroupPath, root+"/")) {
			return nil, errPublisherInvalid
		}
		core := capacityprobe.PoolCorePlan{ID: v.ID, CgroupPath: v.CgroupPath}
		for _, b := range c.core {
			if b.CoreID == v.ID {
				core.ContainerIDs = append(core.ContainerIDs, b.ContainerID)
			}
		}
		c.plan.Core = append(c.plan.Core, core)
	}
	for _, v := range trust.Storage {
		if !publisherPoolPath(v.Path) {
			return nil, errPublisherInvalid
		}
		c.plan.Storage = append(c.plan.Storage, capacityprobe.PoolStoragePlan{ID: v.ID, Path: v.Path})
	}
	return c, nil
}
func publisherCanonical(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n\t\\ ") && len(p) <= 4096
}
func publisherPoolPath(p string) bool {
	return publisherCanonical(p) && strings.HasPrefix(p, capacityprobe.Root+"/")
}

// Token traversal rejects decoded duplicate keys at every level before binding.
func publisherJSONValue(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > capacitysnapshot.MaxBytes {
		return nil, errPublisherInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errPublisherInvalid
		}
		v, e := d.Token()
		if e != nil {
			return nil, errPublisherInvalid
		}
		if token, ok := v.(json.Delim); ok {
			switch token {
			case '{':
				out := map[string]any{}
				for d.More() {
					key, e := d.Token()
					k, ok := key.(string)
					if e != nil || !ok {
						return nil, errPublisherInvalid
					}
					if _, ok := out[k]; ok {
						return nil, errPublisherInvalid
					}
					value, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					out[k] = value
				}
				end, e := d.Token()
				if e != nil || end != json.Delim('}') {
					return nil, errPublisherInvalid
				}
				return out, nil
			case '[':
				out := []any{}
				for d.More() {
					v, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					out = append(out, v)
				}
				end, e := d.Token()
				if e != nil || end != json.Delim(']') {
					return nil, errPublisherInvalid
				}
				return out, nil
			default:
				return nil, errPublisherInvalid
			}
		}
		return v, nil
	}
	v, e := read(0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errPublisherInvalid
	}
	if v == nil {
		return nil, errPublisherInvalid
	}
	return v, nil
}
func publisherStrictJSON(raw []byte, out any) error {
	v, e := publisherJSONValue(raw)
	if e != nil {
		return e
	}
	var shape func(any, reflect.Type) bool
	shape = func(v any, t reflect.Type) bool {
		if t.Kind() == reflect.Pointer {
			return v != nil && shape(v, t.Elem())
		}
		if v == nil {
			return false
		}
		switch t.Kind() {
		case reflect.Struct:
			obj, ok := v.(map[string]any)
			if !ok {
				return false
			}
			fields := map[string]reflect.StructField{}
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				tag := strings.Split(f.Tag.Get("json"), ",")[0]
				if tag == "-" {
					continue
				}
				if tag == "" {
					tag = f.Name
				}
				fields[tag] = f
			}
			for k, value := range obj {
				f, ok := fields[k]
				if !ok || !shape(value, f.Type) {
					return false
				}
			}
			for k, f := range fields {
				if _, ok := obj[k]; !ok && !strings.Contains(f.Tag.Get("json"), ",omitempty") {
					return false
				}
			}
			return true
		case reflect.Slice:
			if t.Elem().Kind() == reflect.Uint8 {
				_, ok := v.(string)
				return ok
			}
			a, ok := v.([]any)
			if !ok {
				return false
			}
			for _, v := range a {
				if !shape(v, t.Elem()) {
					return false
				}
			}
			return true
		case reflect.String:
			_, ok := v.(string)
			return ok
		case reflect.Uint, reflect.Uint32, reflect.Uint64, reflect.Int, reflect.Int64:
			_, ok := v.(json.Number)
			return ok
		case reflect.Bool:
			_, ok := v.(bool)
			return ok
		}
		return false
	}
	t := reflect.TypeOf(out)
	if t == nil || t.Kind() != reflect.Pointer || !shape(v, t.Elem()) {
		return errPublisherInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errPublisherInvalid
	}
	return nil
}
