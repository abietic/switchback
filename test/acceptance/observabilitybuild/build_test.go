package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type dockerExitFailure int

func (e dockerExitFailure) Error() string { return fmt.Sprintf("external Docker exit %d", e) }
func (e dockerExitFailure) ExitCode() int { return int(e) }

func TestBuildRetainsCommandReceiptForOversizeLogs(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, exit := range []int{7, 0} {
			t.Run(fmt.Sprintf("%s_exit%d", stream, exit), func(t *testing.T) {
				f := newFixture(t)
				f.prepareDocker()
				original := f.docker
				var actualArgv []string
				var actualStarted, actualCompleted time.Time
				f.docker = func(ctx context.Context, a []string, out, errout io.Writer) error {
					if len(a) <= 2 || a[2] != "build" {
						return original(ctx, a, out, errout)
					}
					actualArgv = append([]string(nil), a...)
					actualStarted = time.Now().UTC()
					large, small := out, errout
					if stream == "stderr" {
						large, small = errout, out
					}
					chunk := make([]byte, 64<<10)
					for i := 0; i < 4097; i++ {
						if n, e := large.Write(chunk); e != nil || n != len(chunk) {
							t.Fatalf("external log write %d %v", n, e)
						}
					}
					if _, e := io.WriteString(small, "other stream\n"); e != nil {
						t.Fatal(e)
					}
					actualCompleted = time.Now().UTC()
					if exit != 0 {
						return dockerExitFailure(exit)
					}
					return nil
				}
				code, out, message := f.invoke("build", f.commit, f.project, f.output)
				if code != 1 || out != "" || message == "" {
					t.Fatalf("oversize exit=%d stdout=%q stderr=%s", code, out, message)
				}
				if _, e := os.Stat(filepath.Join(f.output, "build", "provenance.json")); !os.IsNotExist(e) {
					t.Fatal("oversize generated success proof")
				}
				b, e := os.ReadFile(filepath.Join(f.output, "build", "backend.command.json"))
				if e != nil {
					t.Fatalf("missing completed command receipt: %v", e)
				}
				var receipt struct {
					Schema    string   `json:"schema"`
					Role      string   `json:"role"`
					Argv      []string `json:"argv"`
					Started   string   `json:"started_at"`
					Completed string   `json:"completed_at"`
					Exit      int      `json:"exit_code"`
					Stdout    struct {
						Name   string
						Bytes  int64
						SHA256 string
					}
					Stderr struct {
						Name   string
						Bytes  int64
						SHA256 string
					}
				}
				if e := json.Unmarshal(b, &receipt); e != nil {
					t.Fatal(e)
				}
				started, se := time.Parse(time.RFC3339Nano, receipt.Started)
				completed, ce := time.Parse(time.RFC3339Nano, receipt.Completed)
				if receipt.Schema != "observability.build_command.v1" || receipt.Role != "backend" || receipt.Exit != exit || receipt.Stdout.Name != "build/backend.stdout.log" || receipt.Stderr.Name != "build/backend.stderr.log" || !reflect.DeepEqual(receipt.Argv, actualArgv) || se != nil || ce != nil || started.After(actualStarted) || completed.Before(actualCompleted) || completed.Before(started) {
					t.Fatalf("actual execution receipt %s", b)
				}
				for _, log := range []struct {
					Name   string
					Bytes  int64
					SHA256 string
				}{receipt.Stdout, receipt.Stderr} {
					file, e := os.Open(filepath.Join(f.output, filepath.FromSlash(log.Name)))
					if e != nil {
						t.Fatal(e)
					}
					h := sha256.New()
					n, re := io.Copy(h, file)
					ce := file.Close()
					if re != nil || ce != nil || n != log.Bytes || fmt.Sprintf("%x", h.Sum(nil)) != log.SHA256 {
						t.Fatalf("raw log receipt %v actual bytes=%d errors=%v/%v", log, n, re, ce)
					}
					want := int64(len("other stream\n"))
					if log.Name == "build/backend."+stream+".log" {
						want = int64(4097 * 64 << 10)
					}
					if n != want {
						t.Fatalf("raw log truncated: %d want %d", n, want)
					}
				}
			})
		}
	}
}

func TestBuildRetainsReceiptWhenRawLogValidationFails(t *testing.T) {
	for _, kind := range []string{"nonregular", "rewritten"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.prepareDocker()
			original := f.docker
			f.docker = func(ctx context.Context, a []string, out, errout io.Writer) error {
				if len(a) <= 2 || a[2] != "build" {
					return original(ctx, a, out, errout)
				}
				if _, e := io.WriteString(out, "actual output\n"); e != nil {
					t.Fatal(e)
				}
				p := filepath.Join(f.output, "build", "backend.stdout.log")
				if kind == "nonregular" {
					if e := os.Rename(p, p+".retained"); e != nil {
						t.Fatal(e)
					}
					if e := os.Mkdir(p, 0700); e != nil {
						t.Fatal(e)
					}
				} else if e := os.WriteFile(p, []byte("changed bytes\n"), 0600); e != nil {
					t.Fatal(e)
				}
				return nil
			}
			code, out, message := f.invoke("build", f.commit, f.project, f.output)
			if code != 1 || out != "" || message == "" {
				t.Fatalf("log validation exit=%d stdout=%q stderr=%s", code, out, message)
			}
			if _, e := os.Stat(filepath.Join(f.output, "build", "provenance.json")); !os.IsNotExist(e) {
				t.Fatal("invalid raw log generated success proof")
			}
			b, e := os.ReadFile(filepath.Join(f.output, "build", "backend.command.json"))
			if e != nil {
				t.Fatal(e)
			}
			var receipt struct {
				Exit   int `json:"exit_code"`
				Stdout struct {
					Bytes  int64
					SHA256 string
				}
			}
			if e := json.Unmarshal(b, &receipt); e != nil {
				t.Fatal(e)
			}
			if receipt.Exit != 0 || receipt.Stdout.Bytes != int64(len("actual output\n")) || receipt.Stdout.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("actual output\n"))) {
				t.Fatalf("actual output receipt lost or rebound %s", b)
			}
			if kind == "nonregular" {
				retained, e := os.ReadFile(filepath.Join(f.output, "build", "backend.stdout.log.retained"))
				if e != nil || string(retained) != "actual output\n" {
					t.Fatalf("original log lost: %q %v", retained, e)
				}
			}
		})
	}
}

func TestBuildRejectsDuplicateContextIdentity(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	original := f.docker
	f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
		if len(a) > 1 && a[0] == "context" && a[1] == "inspect" {
			fmt.Fprint(o, `{"Name":"remote","Name":"default","Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}`)
			return nil
		}
		return original(ctx, a, o, e)
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("duplicate context exit=%d %s", c, m)
	}
}
func TestBuildRejectsRemoteBuilder(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	original := f.docker
	f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
		if len(a) > 3 && a[2] == "buildx" {
			fmt.Fprint(o, "Name: default\nDriver: docker-container\nNodes:\nName: n\nEndpoint: tcp://remote:2375\nStatus: running\n")
			return nil
		}
		return original(ctx, a, o, e)
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("remote builder exit=%d %s", c, m)
	}
	if _, e := os.Stat(filepath.Join(f.output, "build", "backend.iid")); !os.IsNotExist(e) {
		t.Fatal("remote builder reached build")
	}
}
func (f *fixture) prepareDocker() {
	refs := []string{"golang", "debian", "node", "python"}
	var args strings.Builder
	for i, r := range refs {
		fmt.Fprintf(&args, "ARG %s_IMAGE=docker.io/library/%s@sha256:%s\n", []string{"GO", "DEBIAN", "NODE", "PYTHON"}[i], r, strings.Repeat(fmt.Sprint(i+1), 64))
	}
	f.write("images/backend/Dockerfile", args.String()+"FROM ${GO_IMAGE}\n")
	f.write("images/worker/Dockerfile", args.String()+"FROM ${NODE_IMAGE}\n")
	f.write("test/acceptance/observability/Dockerfile.worker", args.String()+"ARG WORKER_BASE\nFROM ${WORKER_BASE}\n")
	testGit(f.t, f.repo, "add", ".")
	testGit(f.t, f.repo, "commit", "-m", "fixed base refs")
	f.commit = testGit(f.t, f.repo, "rev-parse", "HEAD")
	images := map[string]string{}
	f.docker = func(ctx context.Context, a []string, out, errout io.Writer) error {
		f.dockerCalled = true
		if strings.Join(a, " ") == "context show" {
			fmt.Fprintln(out, "default")
			return nil
		}
		if len(a) >= 3 && a[0] == "context" && a[1] == "inspect" {
			fmt.Fprintln(out, `{"Name":"default","Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}`)
			return nil
		}
		if len(a) > 2 && a[2] == "info" {
			fmt.Fprintln(out, `{"ID":"fixture-engine","Name":"fixture-local","OSType":"linux","Architecture":"aarch64"}`)
			return nil
		}
		if len(a) > 3 && a[2] == "buildx" && a[3] == "inspect" {
			fmt.Fprint(out, "Name: default\nDriver: docker\nNodes:\nName: default\nEndpoint: default\nStatus: running\n")
			return nil
		}
		if len(a) > 3 && a[2] == "image" && a[3] == "inspect" {
			ref := a[4]
			id, ok := images[ref]
			if !ok && strings.Contains(ref, "@sha256:") {
				id = "sha256:" + strings.Repeat("e", 64)
				ok = true
			}
			if !ok {
				fmt.Fprintf(errout, "No such image: %s\n", ref)
				return fmt.Errorf("No such image: %s", ref)
			}
			fmt.Fprintf(out, "{\"Id\":%q,\"Os\":\"linux\",\"Architecture\":\"arm64\"}\n", id)
			return nil
		}
		if len(a) > 2 && a[2] == "build" {
			var iid, tag string
			for i := range a {
				if a[i] == "--iidfile" {
					iid = a[i+1]
				}
				if a[i] == "-t" {
					tag = a[i+1]
				}
			}
			digit := "a"
			if strings.Contains(tag, "worker-base") {
				digit = "b"
			} else if strings.Contains(tag, "-worker:") {
				digit = "c"
			}
			id := "sha256:" + strings.Repeat(digit, 64)
			if e := os.WriteFile(iid, []byte(id+"\n"), 0600); e != nil {
				return e
			}
			images[tag] = id
			fmt.Fprint(out, "fixture build output\n")
			return nil
		}
		return fmt.Errorf("unexpected Docker argv %q", a)
	}
}
func TestBuildRejectsMismatchedIID(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	original := f.docker
	f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
		err := original(ctx, a, o, e)
		if len(a) > 2 && a[2] == "build" {
			for i := range a {
				if a[i] == "--iidfile" {
					return os.WriteFile(a[i+1], []byte("sha256:"+strings.Repeat("f", 64)+"\n"), 0600)
				}
			}
		}
		return err
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("IID exit=%d %s", c, m)
	}
}
func TestBuildRejectsReplacedWorkerBase(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	original := f.docker
	built := false
	f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
		if len(a) > 2 && a[2] == "build" && strings.Contains(strings.Join(a, " "), "-worker-base:acceptance") {
			err := original(ctx, a, o, e)
			built = true
			return err
		}
		if built && len(a) > 4 && a[2] == "image" && a[4] == f.project+"-worker-base:acceptance" {
			fmt.Fprintf(o, "{\"Id\":%q,\"Os\":\"linux\",\"Architecture\":\"arm64\"}", "sha256:"+strings.Repeat("b", 64))
			built = false
			return nil
		}
		if !built && len(a) > 4 && a[2] == "image" && a[4] == f.project+"-worker-base:acceptance" {
			var buf strings.Builder
			err := original(ctx, a, &buf, e)
			if err != nil {
				return err
			}
			fmt.Fprint(o, strings.ReplaceAll(buf.String(), strings.Repeat("b", 64), strings.Repeat("f", 64)))
			return nil
		}
		return original(ctx, a, o, e)
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("replaced base exit=%d %s", c, m)
	}
}
func TestBuildRejectsSnapshotMutation(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	original := f.docker
	f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
		err := original(ctx, a, o, e)
		if len(a) > 2 && a[2] == "build" {
			p := filepath.Join(f.output, "build", "source", "source.txt")
			if e := os.Chmod(p, 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(p, []byte("mutated\n"), 0400); e != nil {
				t.Fatal(e)
			}
		}
		return err
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("snapshot exit=%d %s", c, m)
	}
}
func TestBuildRecordsThreeSuccessfulCommands(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 0 {
		t.Fatalf("build exit=%d %s", c, m)
	}
	for _, role := range []string{"backend", "worker_base", "controlled_worker"} {
		b, e := os.ReadFile(filepath.Join(f.output, "build", role+".command.json"))
		if e != nil {
			t.Fatal(e)
		}
		var r map[string]any
		if e = json.Unmarshal(b, &r); e != nil {
			t.Fatal(e)
		}
		if r["schema"] != "observability.build_command.v1" || r["exit_code"] != float64(0) {
			t.Fatalf("command %s", b)
		}
		log, e := os.ReadFile(filepath.Join(f.output, "build", role+".stdout.log"))
		if e != nil || string(log) != "fixture build output\n" {
			t.Fatalf("raw log %q %v", log, e)
		}
	}
}
func TestBuildMetadataAndFailureGuards(t *testing.T) {
	for _, kind := range []string{"missing_context", "tcp_context", "engine_arch", "wrong_image_arch", "duplicate_nodes", "unknown_builder", "conflicting_arg", "unpinned_arg", "build_nonzero", "wrong_inspect", "base_after"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.prepareDocker()
			if kind == "conflicting_arg" {
				f.writeTrackedFileAndCommit("images/worker/Dockerfile", "ARG GO_IMAGE=docker.io/library/golang@sha256:"+strings.Repeat("f", 64)+"\nARG NODE_IMAGE=docker.io/library/node@sha256:"+strings.Repeat("3", 64)+"\nARG PYTHON_IMAGE=docker.io/library/python@sha256:"+strings.Repeat("4", 64)+"\n")
			} else if kind == "unpinned_arg" {
				f.writeTrackedFileAndCommit("images/backend/Dockerfile", "ARG GO_IMAGE=golang:latest\n")
			}
			original := f.docker
			controlled := false
			f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
				switch kind {
				case "missing_context":
					if len(a) > 1 && a[0] == "context" && a[1] == "inspect" {
						fmt.Fprint(o, `{}`)
						return nil
					}
				case "tcp_context":
					if len(a) > 1 && a[0] == "context" && a[1] == "inspect" {
						fmt.Fprint(o, `{"Name":"default","Endpoints":{"docker":{"Host":"tcp://remote:2375"}}}`)
						return nil
					}
				case "engine_arch":
					if len(a) > 2 && a[2] == "info" {
						fmt.Fprint(o, `{"ID":"fixture-engine","Name":"fixture-local","OSType":"linux","Architecture":"riscv64"}`)
						return nil
					}
				case "duplicate_nodes":
					if len(a) > 2 && a[2] == "buildx" {
						fmt.Fprint(o, "Name: default\nDriver: docker\nNodes:\nName: default\nEndpoint: default\nStatus: running\nName: second\nEndpoint: default\nStatus: running\n")
						return nil
					}
				case "unknown_builder":
					if len(a) > 2 && a[2] == "buildx" {
						fmt.Fprint(o, "Name: default\nDriver: docker\nNodes:\nName: default\nEndpoint: default\nStatus: running\nUnknown: ignored\n")
						return nil
					}
				case "build_nonzero":
					if len(a) > 2 && a[2] == "build" {
						fmt.Fprint(o, "partial actual bytes\n")
						fmt.Fprint(e, "fake external failure\n")
						return fmt.Errorf("failed")
					}
				case "wrong_image_arch", "wrong_inspect", "base_after":
					if kind == "base_after" && len(a) > 2 && a[2] == "build" && strings.Contains(strings.Join(a, " "), "-worker:controlled") {
						controlled = true
					}
					if len(a) > 4 && a[2] == "image" {
						bad := kind == "wrong_image_arch" && strings.Contains(a[4], "@sha256:") || kind == "wrong_inspect" && a[4] == f.project+"-backend:acceptance" || kind == "base_after" && controlled && a[4] == f.project+"-worker-base:acceptance"
						if bad {
							var buf strings.Builder
							err := original(ctx, a, &buf, e)
							if err != nil {
								return err
							}
							s := buf.String()
							if kind == "wrong_image_arch" {
								s = strings.ReplaceAll(s, "arm64", "amd64")
							} else {
								s = strings.ReplaceAll(s, strings.Repeat("a", 64), strings.Repeat("f", 64))
								s = strings.ReplaceAll(s, strings.Repeat("b", 64), strings.Repeat("f", 64))
							}
							fmt.Fprint(o, s)
							return nil
						}
					}
				}
				return original(ctx, a, o, e)
			}
			c, _, m := f.invoke("build", f.commit, f.project, f.output)
			if c != 1 {
				t.Fatalf("guard exit=%d %s", c, m)
			}
			if _, e := os.Stat(filepath.Join(f.output, "build", "provenance.json")); !os.IsNotExist(e) {
				t.Fatal("failed build generated success proof")
			}
			if kind == "build_nonzero" {
				b, e := os.ReadFile(filepath.Join(f.output, "build", "backend.command.json"))
				if e != nil || !strings.Contains(string(b), `"exit_code":1`) {
					t.Fatalf("failure command receipt %s %v", b, e)
				}
				b, e = os.ReadFile(filepath.Join(f.output, "build", "backend.stdout.log"))
				if e != nil || string(b) != "partial actual bytes\n" {
					t.Fatalf("failure raw log %s %v", b, e)
				}
			}
		})
	}
}
func TestBuildRejectsMissingCachedBase(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	original := f.docker
	f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
		if len(a) > 4 && a[2] == "image" && strings.Contains(a[4], "@sha256:") {
			return fmt.Errorf("missing cache")
		}
		return original(ctx, a, o, e)
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("missing cache exit=%d %s", c, m)
	}
	if _, e := os.Stat(filepath.Join(f.output, "build", "backend.iid")); !os.IsNotExist(e) {
		t.Fatal("missing cache reached build")
	}
}
func TestBuildRejectsExistingTarget(t *testing.T) {
	f := newFixture(t)
	f.prepareDocker()
	original := f.docker
	f.docker = func(ctx context.Context, a []string, o, e io.Writer) error {
		if len(a) > 4 && a[2] == "image" && a[4] == f.project+"-worker:controlled" {
			fmt.Fprintf(o, "{\"Id\":%q,\"Os\":\"linux\",\"Architecture\":\"arm64\"}", "sha256:"+strings.Repeat("d", 64))
			return nil
		}
		return original(ctx, a, o, e)
	}
	c, _, m := f.invoke("build", f.commit, f.project, f.output)
	if c != 1 {
		t.Fatalf("existing target exit=%d %s", c, m)
	}
	if _, e := os.Stat(filepath.Join(f.output, "build", "backend.iid")); !os.IsNotExist(e) {
		t.Fatal("existing target reached build")
	}
}
