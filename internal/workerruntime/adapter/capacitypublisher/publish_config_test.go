package capacitypublisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublisherConfigFixtureDetached(t *testing.T) {
	c, _, _ := publisherFixture(t, false)
	before := c.trust.Core[0].ID
	c.binding.Core[0].CoreID = "changed"
	if c.trust.Core[0].ID != before || c.core[0].CoreID != before {
		t.Fatal("alias")
	}
}

func TestPublisherConfigStrictBinding(t *testing.T) {
	for _, kind := range []string{"unknown", "duplicate", "escaped_duplicate", "missing", "null", "array", "trailing", "oversize", "worker_image", "manifest", "core", "evidence", "trust_path", "core_path", "inventory_unknown", "negative", "float"} {
		t.Run(kind, func(t *testing.T) {
			c, _, _ := publisherFixture(t, false)
			data := map[string][]byte{}
			for n, b := range c.files.data {
				data[n] = bytes.Clone(b)
			}
			var b map[string]any
			json.Unmarshal(data["publisher-bindings.json"], &b)
			switch kind {
			case "unknown":
				b["enabled"] = true
			case "missing":
				delete(b, "schema")
			case "null":
				b["core"] = nil
			case "array":
				b["core"] = map[string]any{}
			case "worker_image":
				b["worker_image_digest"] = capacitysnapshot.Digest([]byte("other"))
			case "manifest":
				b["manifest_digest"] = capacitysnapshot.Digest([]byte("other"))
			case "core":
				b["core"].([]any)[0].(map[string]any)["core_id"] = "other"
			case "evidence":
				data["host-boundary-receipt.json"] = []byte(`{"changed":true}`)
			case "trust_path":
				data["trust.json"] = bytes.ReplaceAll(data["trust.json"], []byte(capacityprobe.Root+"/anchor"), []byte("/unapproved/anchor"))
			case "core_path":
				data["trust.json"] = bytes.ReplaceAll(data["trust.json"], []byte(syntheticCore), []byte("/sys/fs/cgroup/unapproved"))
			case "inventory_unknown":
				data["deployment-inventory.json"] = append([]byte(`{"enabled":true,`), data["deployment-inventory.json"][1:]...)
			case "negative":
				data["trust.json"] = bytes.Replace(data["trust.json"], []byte(`"mount_id":73`), []byte(`"mount_id":-1`), 1)
			case "float":
				data["trust.json"] = bytes.Replace(data["trust.json"], []byte(`"mount_id":73`), []byte(`"mount_id":7.3e1`), 1)
			}
			data["publisher-bindings.json"], _ = json.Marshal(b)
			switch kind {
			case "duplicate":
				data["publisher-bindings.json"] = append([]byte(`{"schema":"bad",`), data["publisher-bindings.json"][1:]...)
			case "escaped_duplicate":
				data["publisher-bindings.json"] = append([]byte(`{"sche\u006da":"bad",`), data["publisher-bindings.json"][1:]...)
			case "trailing":
				data["publisher-bindings.json"] = append(data["publisher-bindings.json"], []byte(`{}`)...)
			case "oversize":
				data["publisher-bindings.json"] = bytes.Repeat([]byte{' '}, capacitysnapshot.MaxBytes+1)
			}
			if _, e := parsePublisherConfig(data); e == nil {
				t.Fatal("unbound config accepted")
			}
		})
	}
}
func TestPublisherConfigRecheckFixedLeaves(t *testing.T) {
	for _, kind := range []string{"extra", "missing", "replace", "bytes", "symlink", "mode", "owner", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			c, _, _ := publisherFixture(t, false)
			dir := c.files.path
			p := filepath.Join(dir, "trust.json")
			switch kind {
			case "extra":
				os.WriteFile(filepath.Join(dir, "enabled"), []byte("true"), 0400)
			case "missing":
				os.Remove(p)
			case "bytes":
				os.Remove(p)
				os.WriteFile(p, append(bytes.Clone(c.files.data["trust.json"]), ' '), 0400)
			case "symlink":
				os.Remove(p)
				os.Symlink("admission-manifest.json", p)
			case "mode":
				os.Chmod(p, 0600)
			default:
				fs := c.files.fs.(*publisherTestFiles)
				fs.mutate = func(p string, m *publisherMeta) {
					switch kind {
					case "replace":
						m.inode++
					case "owner":
						m.uid++
					case "hardlink":
						if p != dir {
							m.links++
						}
					case "directory":
						if p == dir {
							m.inode++
						}
					}
				}
			}
			if e := c.files.recheck(context.Background()); e == nil {
				t.Fatal("changed config accepted")
			}
		})
	}
}

func TestPublisherConfigRetainsOriginalInventoryMetadataRange(t *testing.T) {
	c, _, _ := publisherFixture(t, false)
	data := map[string][]byte{}
	for n, b := range c.files.data {
		data[n] = bytes.Clone(b)
	}
	var inventory capacitysnapshot.DeploymentInventory
	json.Unmarshal(data["deployment-inventory.json"], &inventory)
	inventory.Binaries = nil
	for n := 0; n < 33; n++ {
		inventory.Binaries = append(inventory.Binaries, capacitysnapshot.AdmissionBinary{Name: fmt.Sprintf("components/%s/%d", strings.Repeat("a", 200), n), Digest: capacitysnapshot.Digest([]byte("synthetic-binary"))})
	}
	data["deployment-inventory.json"], _ = json.Marshal(inventory)
	var manifest capacitysnapshot.AdmissionManifest
	json.Unmarshal(data["admission-manifest.json"], &manifest)
	manifest.DeploymentInventoryDigest = capacitysnapshot.Digest(data["deployment-inventory.json"])
	data["admission-manifest.json"], _ = json.Marshal(manifest)
	trust := c.trust
	trust.ManifestDigest = capacitysnapshot.Digest(data["admission-manifest.json"])
	data["trust.json"], _ = json.Marshal(trust)
	binding := c.binding
	binding.ManifestDigest = trust.ManifestDigest
	data["publisher-bindings.json"], _ = json.Marshal(binding)
	if _, e := capacitysnapshot.ParseAdmissionManifest(data["admission-manifest.json"], trust); e != nil {
		t.Fatal(e)
	}
	if _, e := parsePublisherConfig(data); e != nil {
		t.Fatal("original inventory metadata range rejected", e)
	}
}
