package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestCheckRejectsTruncatedLogReadOnly(t *testing.T) {
	f := checkedFixture(t)
	if e := os.WriteFile(filepath.Join(f.output, "build", "backend.stdout.log"), []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	before := claimState(t, f.output)
	c, _, m := f.invoke("check", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("truncated exit=%d %s", c, m)
	}
	if !reflect.DeepEqual(before, claimState(t, f.output)) {
		t.Fatal("failed check changed evidence")
	}
}
func TestCheckRejectsDuplicateJSONIdentity(t *testing.T) {
	f := checkedFixture(t)
	p := filepath.Join(f.output, "build", "provenance.json")
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	b = append([]byte(`{"project":"wrong",`), b[1:]...)
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	c, _, m := f.invoke("check", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("duplicate identity exit=%d %s", c, m)
	}
}
func changeJSON(t *testing.T, root, name string, change func(map[string]any)) {
	t.Helper()
	p := filepath.Join(root, name)
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	change(v)
	b, e = json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func rebindMaterial(t *testing.T, f *fixture, name string) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(f.output, name))
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(b)
	changeJSON(t, f.output, "build/provenance.json", func(v map[string]any) {
		for _, m := range v["materials"].([]any) {
			ref := m.(map[string]any)
			if ref["name"] == name {
				ref["bytes"] = len(b)
				ref["sha256"] = hex.EncodeToString(hash[:])
			}
		}
	})
}
func TestCheckRecomputesSourceFromGit(t *testing.T) {
	f := checkedFixture(t)
	changeJSON(t, f.output, "build/source.json", func(v map[string]any) { v["source_manifest_sha256"] = strings.Repeat("0", 64) })
	rebindMaterial(t, f, "build/source.json")
	changeJSON(t, f.output, "build/provenance.json", func(v map[string]any) { v["source_manifest_sha256"] = strings.Repeat("0", 64) })
	c, _, m := f.invoke("check", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("forged source exit=%d %s", c, m)
	}
}
func TestCheckRejectsReboundNonzeroBuild(t *testing.T) {
	f := checkedFixture(t)
	changeJSON(t, f.output, "build/backend.command.json", func(v map[string]any) { v["exit_code"] = 1 })
	rebindMaterial(t, f, "build/backend.command.json")
	c, _, m := f.invoke("check", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("nonzero build exit=%d %s", c, m)
	}
}
func TestCheckAdversarialEvidenceGuards(t *testing.T) {
	for _, kind := range []string{"missing_material", "duplicate_material", "escape_material", "missing_roles", "duplicate_roles", "unknown_schema", "self_filled_passed", "wrong_commit", "wrong_tree", "wrong_argv", "bad_time", "snapshot_bytes", "snapshot_extra", "snapshot_symlink", "log_symlink", "log_fifo", "metadata_oversize", "old_iid", "old_base", "wrong_engine", "claim_mismatch"} {
		t.Run(kind, func(t *testing.T) {
			f := checkedFixture(t)
			raw := ""
			switch kind {
			case "missing_material", "duplicate_material", "escape_material", "missing_roles", "duplicate_roles", "unknown_schema", "self_filled_passed", "wrong_commit", "wrong_tree":
				changeJSON(t, f.output, "build/provenance.json", func(v map[string]any) {
					switch kind {
					case "missing_material":
						v["materials"] = v["materials"].([]any)[1:]
					case "duplicate_material":
						m := v["materials"].([]any)
						m[1] = m[0]
					case "escape_material":
						v["materials"].([]any)[0].(map[string]any)["name"] = "../outside"
					case "missing_roles":
						v["images"] = v["images"].([]any)[:2]
					case "duplicate_roles":
						im := v["images"].([]any)
						im[1] = im[0]
					case "unknown_schema":
						v["schema"] = "unknown"
					case "self_filled_passed":
						v["passed"] = true
					case "wrong_commit":
						v["source_commit"] = integrationBase
					case "wrong_tree":
						v["source_tree"] = strings.Repeat("a", 40)
					}
				})
			case "wrong_argv", "bad_time":
				raw = "build/backend.command.json"
				changeJSON(t, f.output, raw, func(v map[string]any) {
					if kind == "wrong_argv" {
						a := v["argv"].([]any)
						for i, x := range a {
							if x == "--pull=false" {
								a[i] = "--pull=true"
							}
						}
					} else {
						v["started_at"] = "2000-01-01T00:00:00Z"
					}
				})
			case "snapshot_bytes":
				p := filepath.Join(f.output, "build/source/source.txt")
				if e := os.Chmod(p, 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(p, []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.Chmod(p, 0444); e != nil {
					t.Fatal(e)
				}
			case "snapshot_extra":
				if e := os.WriteFile(filepath.Join(f.output, "build/source/new"), []byte("extra"), 0444); e != nil {
					t.Fatal(e)
				}
			case "snapshot_symlink", "log_symlink", "log_fifo":
				p := filepath.Join(f.output, "build/backend.stdout.log")
				if kind == "snapshot_symlink" {
					p = filepath.Join(f.output, "build/source/source.txt")
				}
				if e := os.Remove(p); e != nil {
					t.Fatal(e)
				}
				if kind == "log_fifo" {
					if e := syscall.Mkfifo(p, 0600); e != nil {
						t.Fatal(e)
					}
				} else {
					outside := filepath.Join(t.TempDir(), "outside")
					if e := os.WriteFile(outside, []byte("fixture\n"), 0600); e != nil {
						t.Fatal(e)
					}
					if e := os.Symlink(outside, p); e != nil {
						t.Fatal(e)
					}
				}
			case "metadata_oversize":
				if e := os.WriteFile(filepath.Join(f.output, "build/engine.json"), make([]byte, (1<<20)+1), 0600); e != nil {
					t.Fatal(e)
				}
			case "old_iid":
				raw = "build/backend.iid"
				if e := os.WriteFile(filepath.Join(f.output, raw), []byte("sha256:"+strings.Repeat("f", 64)+"\n"), 0600); e != nil {
					t.Fatal(e)
				}
			case "old_base":
				raw = "build/worker-base-after.json"
				changeJSON(t, f.output, raw, func(v map[string]any) { v["Id"] = "sha256:" + strings.Repeat("f", 64) })
			case "wrong_engine":
				raw = "build/engine.json"
				changeJSON(t, f.output, raw, func(v map[string]any) { v["OSType"] = "windows" })
			case "claim_mismatch":
				raw = "claim.json"
				changeJSON(t, f.output, raw, func(v map[string]any) { v["project"] = "switchback-obsv-other001" })
			}
			if raw != "" {
				rebindMaterial(t, f, raw)
			}
			before := claimState(t, f.output)
			c, out, m := f.invoke("check", f.commit, f.project, f.output)
			if c != 1 || out != "" || m == "" {
				t.Fatalf("guard exit=%d stdout=%s stderr=%s", c, out, m)
			}
			if !reflect.DeepEqual(before, claimState(t, f.output)) {
				t.Fatal("failed check changed evidence")
			}
		})
	}
}
func TestCheckAcceptsHistoricalCandidateOnly(t *testing.T) {
	f := checkedFixture(t)
	old := f.commit
	testGit(t, f.repo, "commit", "--allow-empty", "-m", "later same source")
	newCommit := testGit(t, f.repo, "rev-parse", "HEAD")
	c, _, m := f.invoke("check", old, f.project, f.output)
	if c != 0 {
		t.Fatalf("historical candidate exit=%d %s", c, m)
	}
	c, _, m = f.invoke("check", newCommit, f.project, f.output)
	if c != 1 {
		t.Fatalf("same source wrong commit exit=%d %s", c, m)
	}
}
func TestCheckAcceptsEmptyBuildStdout(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	original := f.docker
	f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
		if len(a) > 2 && a[2] == "build" {
			o = io.Discard
		}
		return original(ctx, a, o, e)
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("empty log build exit=%d %s", c, m)
	}
	f.docker = func(context.Context, []string, io.Writer, io.Writer) error { t.Fatal("check Docker"); return nil }
	c, _, m = f.invoke("check", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("empty logs check exit=%d %s", c, m)
	}
}
func claimState(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	if e := filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, p)
		state[rel] = i.Mode().String()
		if i.Mode().IsRegular() {
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			state[rel] += ":" + string(b)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return state
}
func checkedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.prepareDocker()
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("build exit=%d %s", c, m)
	}
	f.docker = func(context.Context, []string, io.Writer, io.Writer) error {
		t.Fatal("check called Docker")
		return nil
	}
	return f
}
func TestBuildCheckReadOnlyRoundTrip(t *testing.T) {
	f := checkedFixture(t)
	before := claimState(t, f.output)
	c, out, m := f.invoke("check", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("check exit=%d %s", c, m)
	}
	var v map[string]any
	if e := json.Unmarshal([]byte(out), &v); e != nil {
		t.Fatal(e)
	}
	if v["schema_version"] != "observability.build_provenance_verified.v1" || v["source_commit"] != f.commit {
		t.Fatalf("verified identity %s", out)
	}
	if !reflect.DeepEqual(before, claimState(t, f.output)) {
		t.Fatal("check modified evidence")
	}
}
func TestCheckRejectsNonPrivateOuterRootsReadOnly(t *testing.T) {
	for _, rel := range []string{".", "build", "build/source"} {
		t.Run(rel, func(t *testing.T) {
			f := checkedFixture(t)
			if e := os.Chmod(filepath.Join(f.output, rel), 0777); e != nil {
				t.Fatal(e)
			}
			before := claimState(t, f.output)
			c, out, m := f.invoke("check", f.commit, f.project, f.output)
			if c != 1 || out != "" || m == "" {
				t.Fatalf("nonprivate root exit=%d stdout=%s stderr=%s", c, out, m)
			}
			if !reflect.DeepEqual(before, claimState(t, f.output)) {
				t.Fatal("check changed private root or evidence")
			}
		})
	}
}
func assertSuccessJSON(t *testing.T, f *fixture, out string, check bool) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(out))
	var v map[string]any
	if e := decoder.Decode(&v); e != nil {
		t.Fatalf("success JSON: %v output=%q", e, out)
	}
	var extra any
	if e := decoder.Decode(&extra); e != io.EOF {
		t.Fatalf("success must be one complete JSON value: %v", e)
	}
	if check {
		if v["schema_version"] != "observability.build_provenance_verified.v1" {
			t.Fatalf("public check schema %v", v)
		}
	} else {
		b, e := os.ReadFile(filepath.Join(f.output, "build/provenance.json"))
		if e != nil {
			t.Fatal(e)
		}
		if strings.TrimSpace(out) != string(b) || v["schema"] != "observability.build_provenance.v1" {
			t.Fatalf("build result differs from generated provenance")
		}
	}
	if v["source_commit"] != f.commit || v["project"] != f.project || v["output"] != f.output || v["source_tree"] != testGit(t, f.repo, "rev-parse", f.commit+"^{tree}") {
		t.Fatalf("result identity %v", v)
	}
	b, e := os.ReadFile(filepath.Join(f.output, "build/source.json"))
	if e != nil {
		t.Fatal(e)
	}
	var source map[string]any
	if e = json.Unmarshal(b, &source); e != nil {
		t.Fatal(e)
	}
	if v["source_manifest_sha256"] != source["source_manifest_sha256"] {
		t.Fatalf("source digest mismatch")
	}
	images, ok := v["images"].([]any)
	if !ok || len(images) != 3 {
		t.Fatalf("result image roles %v", v["images"])
	}
	for i, role := range []string{"backend", "worker_base", "controlled_worker"} {
		im, ok := images[i].(map[string]any)
		if !ok || im["role"] != role || im["id"] != "sha256:"+strings.Repeat([]string{"a", "b", "c"}[i], 64) {
			t.Fatalf("result image %v", images[i])
		}
	}
	if _, ok := v["gates_passed"]; ok {
		t.Fatal("unexpected gates status")
	}
	if _, ok := v["Ready"]; ok {
		t.Fatal("unexpected Ready")
	}
}
func TestRunPublicSuccessResults(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	c, out, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("build %d %s", c, m)
	}
	assertSuccessJSON(t, f, out, false)
	f.docker = func(context.Context, []string, io.Writer, io.Writer) error {
		t.Fatal("check called Docker")
		return nil
	}
	c, out, m = f.invoke("check", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("check %d %s", c, m)
	}
	assertSuccessJSON(t, f, out, true)
}

type failingSuccessOutput struct{}

func (failingSuccessOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestRunRejectsSuccessOutputWriteFailure(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	for _, mode := range []string{"build", "check"} {
		var errout strings.Builder
		c := runWithBaseline(context.Background(), f.repo, []string{mode, f.commit, f.project, f.output}, failingSuccessOutput{}, &errout, f.docker, f.baseline)
		if c != 1 || !strings.Contains(errout.String(), "output") {
			t.Fatalf("%s output failure exit=%d stderr=%s", mode, c, errout.String())
		}
	}
}
