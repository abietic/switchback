package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSnapshotPreservesDockerCopyReadPermissions(t *testing.T) {
	f := newFixture(t)
	f.writeTrackedFileAndCommit("images/worker/litellm/config.yaml", "model_list: []\n")
	f.invoke("build", f.commit, f.project, f.output)
	for _, p := range []string{"images/worker/litellm/config.yaml", "source.txt"} {
		i, e := os.Stat(filepath.Join(f.output, "build", "source", p))
		if e != nil {
			t.Fatal(e)
		}
		if i.Mode().Perm() != 0444 {
			t.Fatalf("COPY input %s mode=%o", p, i.Mode().Perm())
		}
	}
	i, e := os.Stat(filepath.Join(f.output, "build", "source", "images", "worker", "litellm"))
	if e != nil || i.Mode().Perm() != 0755 {
		t.Fatalf("COPY directory %v %v", i, e)
	}
}
func TestBuildSourceSafetyGuards(t *testing.T) {
	for _, kind := range []string{"tracked_symlink", "backslash_path", "submodule", "export_ignore", "export_subst", "oversize", "duplicate_output", "claim_symlink", "claim_fifo"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "tracked_symlink":
				if e := os.Symlink("source.txt", filepath.Join(f.repo, "link")); e != nil {
					t.Fatal(e)
				}
			case "backslash_path":
				f.write("escape\\name", "bad")
			case "submodule":
				if e := os.Mkdir(filepath.Join(f.repo, "sub"), 0700); e != nil {
					t.Fatal(e)
				}
				testGit(t, f.repo, "update-index", "--add", "--cacheinfo", "160000,"+f.commit+",sub")
			case "export_ignore":
				f.write(".gitattributes", "source.txt export-ignore\n")
			case "export_subst":
				f.write(".gitattributes", "source.txt export-subst\n")
				f.write("source.txt", "$Format:%H$\n")
			case "oversize":
				f.write("huge", string(make([]byte, (16<<20)+1)))
			case "duplicate_output":
				if e := os.Mkdir(filepath.Join(f.output, "build"), 0700); e != nil {
					t.Fatal(e)
				}
			case "claim_symlink":
				if e := os.Rename(filepath.Join(f.output, "claim.json"), filepath.Join(f.output, "original.json")); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink("original.json", filepath.Join(f.output, "claim.json")); e != nil {
					t.Fatal(e)
				}
			case "claim_fifo":
				if e := os.Remove(filepath.Join(f.output, "claim.json")); e != nil {
					t.Fatal(e)
				}
				if e := syscall.Mkfifo(filepath.Join(f.output, "claim.json"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "submodule" {
				testGit(t, f.repo, "commit", "-m", "unsafe source")
			} else if kind == "tracked_symlink" || kind == "backslash_path" || kind == "export_ignore" || kind == "export_subst" || kind == "oversize" {
				testGit(t, f.repo, "add", ".")
				testGit(t, f.repo, "commit", "-m", "unsafe source")
			}
			f.commit = testGit(t, f.repo, "rev-parse", "HEAD")
			c, _, m := f.invoke("build", f.commit, f.project, f.output)
			if c != 1 || f.dockerCalled {
				t.Fatalf("source safety exit=%d docker=%v %s", c, f.dockerCalled, m)
			}
		})
	}
}
func TestBuildSanitizesPoisonedGitEnvironment(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	for k, v := range map[string]string{"GIT_DIR": "/nonexistent/git", "GIT_WORK_TREE": "/wrong", "GIT_INDEX_FILE": "/wrong/index", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.bare", "GIT_CONFIG_VALUE_0": "true", "GIT_REPLACE_REF_BASE": "refs/replace/poison"} {
		t.Setenv(k, v)
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("poisoned Git exit=%d %s", c, m)
	}
}
func TestBuildUnderPrivateUmask(t *testing.T) {
	old := syscall.Umask(077)
	defer syscall.Umask(old)
	f := newFixture(t)
	f.prepareDocker()
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("private umask exit=%d %s", c, m)
	}
}

func TestBuildRejectsNonCanonicalRoots(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(filepath.Dir(f.output), "claim-link")
	if e := os.Symlink(f.output, link); e != nil {
		t.Fatal(e)
	}
	f.writeClaim([]byte(`{"project":"` + f.project + `","output":"` + link + `","token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	c, _, m := f.invoke("build", f.commit, f.project, link)
	if c != 1 {
		t.Fatalf("symlink root exit=%d %s", c, m)
	}
}
func TestBuildRejectsAliasOutput(t *testing.T) {
	f := newFixture(t)
	alias := f.output + "/../claim"
	f.writeClaim([]byte(`{"project":"` + f.project + `","output":"` + alias + `","token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	c, _, m := f.invoke("build", f.commit, f.project, alias)
	if c != 1 {
		t.Fatalf("alias exit=%d %s", c, m)
	}
}

func TestBuildRejectsUnrelatedBaseline(t *testing.T) {
	f := newFixture(t)
	testGit(t, f.repo, "checkout", "--orphan", "unrelated")
	testGit(t, f.repo, "add", ".")
	testGit(t, f.repo, "commit", "-m", "unrelated")
	f.commit = testGit(t, f.repo, "rev-parse", "HEAD")
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("unrelated exit=%d stderr=%s", c, m)
	}
}

func TestBuildRejectsDirtyCandidate(t *testing.T) {
	f := newFixture(t)
	f.write("untracked", "dirty")
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("dirty exit=%d stderr=%s", c, m)
	}
	if _, e := os.Stat(filepath.Join(f.output, "build")); !os.IsNotExist(e) {
		t.Fatal("dirty candidate created evidence")
	}
}
func TestBuildRejectsClaimMismatch(t *testing.T) {
	f := newFixture(t)
	f.writeClaim([]byte(`{"project":"switchback-obsv-other001","output":"/wrong","token":"bad"}`))
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("claim exit=%d stderr=%s", c, m)
	}
	if _, e := os.Stat(filepath.Join(f.output, "build")); !os.IsNotExist(e) {
		t.Fatal("claim mismatch created evidence")
	}
}
func TestBuildRejectsNonPrivateClaimBeforeDocker(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	if e := os.Chmod(f.output, 0777); e != nil {
		t.Fatal(e)
	}
	c, out, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 || out != "" || f.dockerCalled {
		t.Fatalf("nonprivate claim exit=%d docker=%v stdout=%s stderr=%s", c, f.dockerCalled, out, m)
	}
	if _, e := os.Stat(filepath.Join(f.output, "build")); !os.IsNotExist(e) {
		t.Fatal("nonprivate claim created build output")
	}
}
func TestBuildCapturesCommittedSource(t *testing.T) {
	f := newFixture(t)
	_, _, message := f.invoke("build", f.commit, f.project, f.output)
	b, e := os.ReadFile(filepath.Join(f.output, "build", "source", "source.txt"))
	if e != nil || string(b) != "fixture\n" {
		t.Fatalf("snapshot %q %v stderr=%s", b, e, message)
	}
	b, e = os.ReadFile(filepath.Join(f.output, "build", "source.json"))
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	if v["schema"] != "observability.build_source.v1" || v["source_commit"] != f.commit {
		t.Fatalf("source identity %s", b)
	}
}
