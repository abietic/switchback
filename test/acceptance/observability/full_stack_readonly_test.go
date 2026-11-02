package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type readonlyFile struct {
	Mode     fs.FileMode
	Size     int64
	Modified time.Time
	Bytes    []byte
}

func readonlyDirectorySnapshot(t *testing.T, output string) map[string]readonlyFile {
	t.Helper()
	result := map[string]readonlyFile{}
	err := filepath.WalkDir(output, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := readonlyFile{Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime()}
		if info.Mode().IsRegular() {
			entry.Bytes, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		name, err := filepath.Rel(output, path)
		if err != nil {
			return err
		}
		result[name] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFullStackReadOnlyCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "observability")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-p=1", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOFLAGS=-p=1", "GOWORK=off")
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile CLI: %v\n%s", err, data)
	}
	trapDir := t.TempDir()
	marker := filepath.Join(trapDir, "docker-called")
	trap := []byte("#!/bin/sh\nprintf 'called\\n' >> \"$READONLY_DOCKER_MARKER\"\nexit 97\n")
	if err := os.WriteFile(filepath.Join(trapDir, "docker"), trap, 0700); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"cold", "matrix"} {
		for _, name := range []string{"success", "missing input", "backend", "report"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				project, output := readonlyFixture(t, kind)
				args := []string{"-mode", "check-full-stack-" + kind, "-project", project, "-output", output}
				switch name {
				case "missing input":
					file := "cold-before.json"
					if kind == "matrix" {
						file = "full-stack-matrix-recipe.json"
					}
					if err := os.Remove(filepath.Join(output, file)); err != nil {
						t.Fatal(err)
					}
				case "backend":
					args = append(args, "-backend", "http://127.0.0.1:1")
				case "report":
					args = append(args, "-report", filepath.Join(output, "unused.json"))
				}
				before := readonlyDirectorySnapshot(t, output)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Env = append(os.Environ(), "PATH="+trapDir+string(os.PathListSeparator)+os.Getenv("PATH"), "READONLY_DOCKER_MARKER="+marker)
				var stdout, stderr bytes.Buffer
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				err := cmd.Run()
				if name == "success" {
					if err != nil || stderr.Len() != 0 {
						t.Fatalf("CLI failed: %v stderr=%s", err, &stderr)
					}
					var result struct {
						Schema    string `json:"schema_version"`
						Project   string `json:"project"`
						Scope     string `json:"scope"`
						Status    string `json:"status"`
						PairCount int    `json:"pair_count"`
					}
					if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					scope := "cold_start_only"
					if kind == "matrix" {
						scope = "paired_outage_only"
					}
					if result.Schema != "observability.full_stack_"+kind+".v1" || result.Project != project || result.Scope != scope || result.Status != "passed" || (kind == "matrix" && result.PairCount != 20) {
						t.Fatalf("wrong CLI result: %+v", result)
					}
				} else {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
						t.Fatalf("CLI rejection err=%v stdout=%s stderr=%s", err, &stdout, &stderr)
					}
				}
				if !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
					t.Fatal("CLI changed evidence")
				}
				if _, err := os.Lstat(marker); !os.IsNotExist(err) {
					t.Fatalf("CLI invoked Docker: %v", err)
				}
			})
		}
	}
	// Help exposes the two new modes without executing an evidence route.
	help := exec.CommandContext(ctx, binary, "-h")
	data, err := help.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"cold", "matrix"} {
		if !strings.Contains(string(data), "check-full-stack-"+kind) {
			t.Fatalf("help omits %s", kind)
		}
	}
}

func TestFullStackReadOnlyClaimPrecheck(t *testing.T) {
	for _, kind := range []string{"cold", "matrix"} {
		for _, name := range []string{"canonical alias", "static symlink", "FIFO", "oversize", "truncated JSON", "wrong output", "wrong project", "invalid project"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				project, output := readonlyFixture(t, kind)
				claimPath := filepath.Join(output, "claim.json")
				checkOutput := output
				switch name {
				case "canonical alias":
					alias := filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(output, alias); err != nil {
						t.Fatal(err)
					}
					checkOutput = alias
				case "static symlink":
					if err := os.Remove(claimPath); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(output, "cold-before.json"), claimPath); err != nil {
						t.Fatal(err)
					}
				case "FIFO":
					if err := os.Remove(claimPath); err != nil {
						t.Fatal(err)
					}
					if err := syscall.Mkfifo(claimPath, 0600); err != nil {
						t.Fatal(err)
					}
				case "oversize":
					if err := os.WriteFile(claimPath, make([]byte, (16<<20)+1), 0600); err != nil {
						t.Fatal(err)
					}
				case "truncated JSON":
					if err := os.WriteFile(claimPath, []byte(`{"project":`), 0600); err != nil {
						t.Fatal(err)
					}
				case "wrong output":
					rewriteFullStackFile(t, output, "claim.json", func(c *map[string]string) { (*c)["output"] = filepath.Dir(output) })
				case "wrong project":
					rewriteFullStackFile(t, output, "claim.json", func(c *map[string]string) { (*c)["project"] = "switchback-obsv-other123" })
				case "invalid project":
					project = "invalid"
				}
				before := readonlyDirectorySnapshot(t, output)
				var stdout bytes.Buffer
				if err := checkFullStackEvidence(kind, project, checkOutput, &stdout); err == nil {
					t.Fatal("invalid claim accepted")
				}
				if stdout.Len() != 0 || !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
					t.Fatal("claim rejection emitted success or changed evidence")
				}
			})
		}
	}
}

func TestFullStackReadOnlyRejectsRouteBeforeReading(t *testing.T) {
	// These invalid routes must terminate even with a FIFO at the claim path.
	output := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(output, "claim.json"), 0600); err != nil {
		t.Fatal(err)
	}
	before := readonlyDirectorySnapshot(t, output)
	var stdout bytes.Buffer
	if err := checkFullStackEvidence("unknown", nativeCollectorProject, output, &stdout); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := checkFullStackEvidence("cold", nativeCollectorProject, output, nil); err == nil {
		t.Fatal("nil stdout accepted")
	}
	if stdout.Len() != 0 || !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
		t.Fatal("invalid route changed evidence or emitted JSON")
	}
}

func TestFullStackReadOnlyColdRejectsBrokenEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"missing input", func(t *testing.T, o string) {
			if err := os.Remove(filepath.Join(o, "cold-before.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong project", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-before.json", func(c *ProjectCapture) { c.Project = "switchback-obsv-other123" })
		}},
		{"bad JSON", func(t *testing.T, o string) {
			if err := os.WriteFile(filepath.Join(o, "cold-before.json"), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"ninth service", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-before.json", func(c *ProjectCapture) { c.AbsentServices = c.AbsentServices[:len(c.AbsentServices)-1] })
		}},
		{"interval gap", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "intervals-cold.json", func(e *IntervalEvidence) { e.Subscriptions[0].End = "unexpected_eof" })
		}},
		{"five seconds", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "smoke-pending.json", func(e *RawEvidence) {
				*e.Settlements[0].Milliseconds = 5100
				e.Settlements[0].To = e.Settlements[0].From.Add(5100 * time.Millisecond)
			})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, output := fullStackColdFixture(t)
			tc.change(t, output)
			readonlyReject(t, "cold", project, output)
		})
	}
}

func TestFullStackReadOnlyMatrixRejectsBrokenEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, string, fullStackMatrixRecipe)
	}{
		{"missing input", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			if err := os.Remove(filepath.Join(o, "full-stack-matrix-recipe.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong project", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Original, func(e *RawEvidence) { e.Project = "switchback-obsv-other123" })
		}},
		{"bad JSON", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			if err := os.WriteFile(filepath.Join(o, "full-stack-matrix-recipe.json"), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing pair", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, "full-stack-matrix-recipe.json", func(e *fullStackMatrixRecipe) { e.Pairs = e.Pairs[:19] })
		}},
		{"reused Turn", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			var first RawEvidence
			data, err := os.ReadFile(filepath.Join(o, r.Pairs[0].Healthy.Original))
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &first); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{r.Pairs[1].Healthy.Original, r.Pairs[1].Healthy.Recovered} {
				rewriteFullStackFile(t, o, name, func(e *RawEvidence) {
					old := []string{e.Settlements[0].TurnID, e.Settlements[1].TurnID}
					for i := 0; i < 2; i++ {
						e.Settlements[i].TurnID = first.Settlements[i].TurnID
						e.Expected[i].TurnID = first.Settlements[i].TurnID
					}
					e.Dispatch.TurnID = first.Settlements[1].TurnID
					for i := range e.FragmentProofs {
						for j := 0; j < 2; j++ {
							if e.FragmentProofs[i].TurnID == old[j] {
								e.FragmentProofs[i].TurnID = first.Settlements[j].TurnID
							}
						}
					}
					var rows []fullStackTurn
					if err := json.Unmarshal(e.Turns, &rows); err != nil {
						t.Fatal(err)
					}
					for i := range rows {
						rows[i].ID = first.Settlements[i].TurnID
					}
					e.Turns, _ = json.Marshal(rows)
				})
			}
		}},
		{"ACK before second Turn", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			for _, name := range []string{r.Pairs[0].Healthy.Original, r.Pairs[0].Healthy.Recovered} {
				rewriteFullStackFile(t, o, name, func(e *RawEvidence) {
					e.Dispatch.To = e.Dispatch.From.Add(30 * time.Millisecond)
					*e.Dispatch.Milliseconds = 30
				})
			}
		}},
		{"resident Worker label", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.WorkerFinal, func(e *fullStackWorkerCapture) { e.Labels["com.switchback.worker-session-id"] = "unrelated" })
		}},
		{"interval gap", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Intervals, func(e *IntervalEvidence) { e.Subscriptions[0].End = "unexpected_eof" })
		}},
		{"five seconds", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Original, func(e *RawEvidence) {
				*e.Settlements[0].Milliseconds = 5100
				e.Settlements[0].To = e.Settlements[0].From.Add(5100 * time.Millisecond)
			})
		}},
		{"p95 increase", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			for _, pair := range r.Pairs {
				for _, name := range []string{pair.Outage.Original, pair.Outage.Recovered} {
					rewriteFullStackFile(t, o, name, func(e *RawEvidence) {
						*e.Settlements[0].Milliseconds = 1200
						e.Settlements[0].To = e.Settlements[0].From.Add(1200 * time.Millisecond)
					})
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, output, recipe := fullStackMatrixFixture(t)
			tc.change(t, output, recipe)
			readonlyReject(t, "matrix", project, output)
		})
	}
}

func readonlyFixture(t *testing.T, kind string) (string, string) {
	t.Helper()
	if kind == "cold" {
		return fullStackColdFixture(t)
	}
	project, output, _ := fullStackMatrixFixture(t)
	return project, output
}

func TestFullStackReadOnlyExistingPassAndRepeat(t *testing.T) {
	for _, kind := range []string{"cold", "matrix"} {
		t.Run(kind, func(t *testing.T) {
			project, output := readonlyFixture(t, kind)
			path := filepath.Join(output, "full-stack-"+kind+"-passed.json")
			if err := os.WriteFile(path, []byte("not acceptance proof"), 0640); err != nil {
				t.Fatal(err)
			}
			before := readonlyDirectorySnapshot(t, output)
			var first, second bytes.Buffer
			if err := checkFullStackEvidence(kind, project, output, &first); err != nil {
				t.Fatal(err)
			}
			if err := checkFullStackEvidence(kind, project, output, &second); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Bytes(), second.Bytes()) || !json.Valid(first.Bytes()) {
				t.Fatal("repeat check is not deterministic JSON")
			}
			var err error
			if kind == "cold" {
				err = verifyFullStackCold(project, output)
			} else {
				err = verifyFullStackMatrix(project, output)
			}
			if err == nil {
				t.Fatal("old writer overwrote existing PASS")
			}
			if !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
				t.Fatal("check or old writer changed sentinel/evidence")
			}
		})
	}
}

type readonlyOutputWriter struct {
	err   error
	short bool
}

func (w readonlyOutputWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.short {
		return len(p) - 1, nil
	}
	return len(p), nil
}

func TestFullStackReadOnlyOutputFailure(t *testing.T) {
	sentinel := errors.New("output unavailable")
	for _, kind := range []string{"cold", "matrix"} {
		for _, tc := range []struct {
			name   string
			writer io.Writer
			want   error
		}{{"error", readonlyOutputWriter{err: sentinel}, sentinel}, {"short", readonlyOutputWriter{short: true}, io.ErrShortWrite}} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				project, output := readonlyFixture(t, kind)
				before := readonlyDirectorySnapshot(t, output)
				if err := checkFullStackEvidence(kind, project, output, tc.writer); !errors.Is(err, tc.want) {
					t.Fatalf("output error=%v want %v", err, tc.want)
				}
				if !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
					t.Fatal("output failure changed evidence")
				}
			})
		}
	}
}

func readonlyReject(t *testing.T, kind, project, output string) {
	t.Helper()
	before := readonlyDirectorySnapshot(t, output)
	var stdout bytes.Buffer
	if err := checkFullStackEvidence(kind, project, output, &stdout); err == nil {
		t.Fatal("invalid evidence accepted")
	}
	if stdout.Len() != 0 {
		t.Fatal("failed verification emitted success JSON")
	}
	if !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
		t.Fatal("failed verification changed evidence")
	}
}

func TestFullStackReadOnlyColdPreservesEvidence(t *testing.T) {
	project, output := fullStackColdFixture(t)
	before := readonlyDirectorySnapshot(t, output)
	var stdout bytes.Buffer
	if err := checkFullStackEvidence("cold", project, output, &stdout); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Schema  string `json:"schema_version"`
		Project string `json:"project"`
		Scope   string `json:"scope"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != "observability.full_stack_cold.v1" || result.Project != project || result.Scope != "cold_start_only" || result.Status != "passed" {
		t.Fatalf("wrong scoped output: %+v", result)
	}
	if !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
		t.Fatal("readonly verification changed evidence")
	}
}

func TestFullStackReadOnlyMatrixPreservesEvidence(t *testing.T) {
	project, output, _ := fullStackMatrixFixture(t)
	before := readonlyDirectorySnapshot(t, output)
	var stdout bytes.Buffer
	if err := checkFullStackEvidence("matrix", project, output, &stdout); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Schema    string `json:"schema_version"`
		Project   string `json:"project"`
		Scope     string `json:"scope"`
		Status    string `json:"status"`
		PairCount int    `json:"pair_count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != "observability.full_stack_matrix.v1" || result.Project != project || result.Scope != "paired_outage_only" || result.Status != "passed" || result.PairCount != 20 {
		t.Fatalf("wrong scoped output: %+v", result)
	}
	if !reflect.DeepEqual(before, readonlyDirectorySnapshot(t, output)) {
		t.Fatal("readonly verification changed evidence")
	}
}
