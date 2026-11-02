package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const approvedTestSource = "package fixture\nimport \"testing\"\nfunc TestApprovedAddition(t *testing.T) { t.Log(\"registered\") }\n"
const approvedHelperSource = "package fixture\nfunc approvedHelper() string { return \"registered\" }\n"
const approvedEntries = `{ID:"test/acceptance/observability/TestApprovedAddition", SHA256:"12eca838f2dcab0e44eab265728cc12c566dcc32845aa0ea6b2bc637fc331a8a", Groups:[]string{"","integration"}},
{ID:"test/acceptance/observability/approvedHelper", SHA256:"a6afd46f37ca6e3394d652017d89f137fc247dfd6a28035cb3eb1c22fef1b45a"},`

// This CLI fixture substitutes only reviewed registration data in a copied tool.
func approvedGroupsFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module github.com/abietic/switchback\n\ngo 1.24\n")
	for _, pkg := range []string{"internal/architecture", "internal/localproduct", "test/acceptance/observability"} {
		write(t, root, pkg+"/base.go", "package fixture\n")
	}
	write(t, root, "internal/architecture/pure_test.go", testSource("", "TestPure"))
	write(t, root, "internal/architecture/fixture_test.go", testSource("", "TestMonitoringRegistryFixture"))
	for name, pkg := range native {
		tag := ""
		if pkg != "internal/architecture" {
			tag = "integration"
		}
		write(t, root, pkg+"/"+name+"_test.go", testSource(tag, name))
	}
	runOK(t, root, "git", "init", "-q")
	runOK(t, root, "git", "add", ".")
	runOK(t, root, "git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "baseline")
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(raw))
	write(t, root, "internal/architecture/fixture_test.go", testSource("observability_fixture", "TestMonitoringRegistryFixture"))
	for name, pkg := range native {
		tag := "observability_native"
		if pkg != "internal/architecture" {
			tag = "integration && " + tag
		}
		write(t, root, pkg+"/"+name+"_test.go", testSource(tag, name))
	}
	write(t, root, "test/acceptance/observability/approved_test.go", approvedTestSource)
	write(t, root, "test/acceptance/observability/approved_helper_test.go", approvedHelperSource)
	return root, base
}

func approvedGroupsBinary(t *testing.T, base, entries string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module groupsfixture\n\ngo 1.24\n")
	for _, name := range []string{"main.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, name, string(raw))
	}
	write(t, dir, "registered_additions.go", fmt.Sprintf("package main\ntype registeredAddition struct { ID string; SHA256 string; Groups []string }\nconst registeredBaseline = %q\nvar registeredAdditions = []registeredAddition{%s}\n", base, entries))
	binary := filepath.Join(dir, "groups")
	runOK(t, dir, "go", "build", "-p=1", "-o", binary, ".")
	return binary
}

func approvedGroupsRun(t *testing.T, binary, root, base, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, base)
	cmd.Dir = root
	raw, err := cmd.CombinedOutput()
	if want == "" {
		if err != nil || !strings.Contains(string(raw), "observability test identities, function bodies and build groups conserved; current groups compile") {
			t.Fatalf("CLI success: %v\n%s", err, raw)
		}
		return
	}
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 || !strings.Contains(string(raw), want) {
		t.Fatalf("wanted exit1 diagnostic %q; got %v\n%s", want, err, raw)
	}
	t.Logf("CLI exit=%d diagnostic=%s", exit.ExitCode(), strings.TrimSpace(string(raw)))
}

func TestGroupsRegisteredCLI(t *testing.T) {
	root, base := approvedGroupsFixture(t)
	// Digests are literal reviewed snapshots, never calculated by this test.
	binary := approvedGroupsBinary(t, base, approvedEntries)
	approvedGroupsRun(t, binary, root, base, "")
}

func TestGroupsRegisteredExistingProtection(t *testing.T) {
	for _, mode := range []string{"old_test_deleted", "old_assertion_changed", "unregistered_test", "unregistered_helper"} {
		t.Run(mode, func(t *testing.T) {
			root, base := approvedGroupsFixture(t)
			binary := approvedGroupsBinary(t, base, approvedEntries)
			want := ""
			switch mode {
			case "old_test_deleted":
				mustRemove(t, filepath.Join(root, "internal/architecture/pure_test.go"))
				want = "missing internal/architecture/TestPure"
			case "old_assertion_changed":
				write(t, root, "internal/architecture/pure_test.go", strings.ReplaceAll(testSource("", "TestPure"), "original", "weakened"))
				want = "changed internal/architecture/TestPure"
			case "unregistered_test":
				write(t, root, "test/acceptance/observability/foreign_test.go", testSource("", "TestForeign"))
				want = "unexpected test/acceptance/observability/TestForeign"
			case "unregistered_helper":
				write(t, root, "test/acceptance/observability/foreign_helper_test.go", "package fixture\nfunc foreignHelper() {}\n")
				want = "unexpected test/acceptance/observability/foreignHelper"
			}
			approvedGroupsRun(t, binary, root, base, want)
		})
	}
}

func TestGroupsRegisteredContentAndGroups(t *testing.T) {
	for _, mode := range []string{"test_deleted", "helper_deleted", "test_changed", "helper_changed", "integration_only", "native_only"} {
		t.Run(mode, func(t *testing.T) {
			root, base := approvedGroupsFixture(t)
			binary := approvedGroupsBinary(t, base, approvedEntries)
			want := ""
			switch mode {
			case "test_deleted":
				mustRemove(t, filepath.Join(root, "test/acceptance/observability/approved_test.go"))
				want = "registered addition missing: test/acceptance/observability/TestApprovedAddition"
			case "helper_deleted":
				mustRemove(t, filepath.Join(root, "test/acceptance/observability/approved_helper_test.go"))
				want = "registered addition missing: test/acceptance/observability/approvedHelper"
			case "test_changed":
				write(t, root, "test/acceptance/observability/approved_test.go", strings.ReplaceAll(approvedTestSource, "registered", "changed"))
				want = "registered addition changed: test/acceptance/observability/TestApprovedAddition"
			case "helper_changed":
				write(t, root, "test/acceptance/observability/approved_helper_test.go", strings.ReplaceAll(approvedHelperSource, "registered", "changed"))
				want = "registered addition changed: test/acceptance/observability/approvedHelper"
			case "integration_only":
				write(t, root, "test/acceptance/observability/approved_test.go", "//go:build integration\n\n"+approvedTestSource)
				want = "ordinary group \"\" changed: missing test/acceptance/observability/TestApprovedAddition"
			case "native_only":
				write(t, root, "test/acceptance/observability/approved_test.go", "//go:build observability_native\n\n"+approvedTestSource)
				want = "ordinary group \"\" changed: missing test/acceptance/observability/TestApprovedAddition"
			}
			approvedGroupsRun(t, binary, root, base, want)
		})
	}
}

func TestGroupsRegisteredConfiguration(t *testing.T) {
	for _, mode := range []string{"wrong_baseline", "duplicate_identity", "empty_identity", "invalid_digest", "uppercase_digest", "invalid_groups", "baseline_collision"} {
		t.Run(mode, func(t *testing.T) {
			root, base := approvedGroupsFixture(t)
			binding, entries, want := base, approvedEntries, ""
			switch mode {
			case "wrong_baseline":
				binding = strings.Repeat("a", 40)
				want = "unexpected test/acceptance/observability/TestApprovedAddition"
			case "duplicate_identity":
				entries += approvedEntries
				want = "registered addition empty or duplicate identity: test/acceptance/observability/TestApprovedAddition"
			case "empty_identity":
				entries = strings.Replace(entries, "test/acceptance/observability/TestApprovedAddition", "", 1)
				want = "registered addition empty or duplicate identity:"
			case "invalid_digest":
				entries = strings.Replace(entries, "12eca838f2dcab0e44eab265728cc12c566dcc32845aa0ea6b2bc637fc331a8a", "bad", 1)
				want = "registered addition invalid digest: test/acceptance/observability/TestApprovedAddition"
			case "uppercase_digest":
				entries = strings.Replace(entries, "12eca838f2dcab0e44eab265728cc12c566dcc32845aa0ea6b2bc637fc331a8a", "12ECA838F2DCAB0E44EAB265728CC12C566DCC32845AA0EA6B2BC637FC331A8A", 1)
				want = "registered addition invalid digest: test/acceptance/observability/TestApprovedAddition"
			case "invalid_groups":
				entries = strings.Replace(entries, `[]string{"","integration"}`, `[]string{"observability_native"}`, 1)
				want = "registered addition invalid groups: test/acceptance/observability/TestApprovedAddition"
			case "baseline_collision":
				entries = strings.Replace(entries, "test/acceptance/observability/TestApprovedAddition", "internal/architecture/TestPure", 1)
				want = "registered addition collides with baseline: internal/architecture/TestPure"
			}
			binary := approvedGroupsBinary(t, binding, entries)
			approvedGroupsRun(t, binary, root, base, want)
		})
	}
}

// Exercise the CLI against real Git objects and Go build-constraint selection.
func TestGroupsCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "groups")
	runOK(t, "", "go", "build", "-p=1", "-o", binary, ".")
	for _, mode := range []string{"valid", "unisolated", "missing_pure", "missing_sql", "missing_native", "helper_default", "changed_assertion", "added_test", "wrong_default", "format_only", "changed_helper", "worker_missing_integration", "changed_crossbuild", "shared_helper_native_default", "shared_helper_native_integration", "changed_for_clause"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "go.mod", "module github.com/abietic/switchback\n\ngo 1.24\n")
			packages := map[string][]string{
				"internal/architecture":         {"TestPure", "TestLocalObservabilityExploreNative", "TestLocalObservabilityMonitoringNative", "TestLocalObservabilityAlloyFreshVolumeNative", "TestLocalObservabilityPromQL", "TestMonitoringRegistryFixture"},
				"internal/localproduct":         {"TestWorkerPure", "TestTelemetryWorkerDockerNativeCollectionAndReplacement"},
				"test/acceptance/observability": {"TestAcceptanceQueriesAgainstMigratedPostgres", "TestSupplementProofsReadPublishedClaimThroughProductionLoader"},
			}
			for pkg, names := range packages {
				write(t, root, pkg+"/base.go", "package fixture\n")
				for _, name := range names {
					tag := ""
					if pkg != "internal/architecture" {
						tag = "integration"
					}
					write(t, root, pkg+"/"+name+"_test.go", sharedTestSource(tag, name))
				}
			}
			// Both callers and the helper are real Go declarations in the Git
			// baseline. A verbatim helper move must not strand an ordinary caller.
			shared := "package fixture\nfunc sharedValue() string { return \"original\" }\n"
			write(t, root, "internal/architecture/shared_test.go", shared)
			write(t, root, "test/acceptance/observability/shared_test.go", "//go:build integration\n\n"+shared)
			crossbuild := "package fixture\nimport (\"context\"; \"os/exec\")\nfunc newMonitoringFixture() { ctx := context.Background(); binary := \"fixture\"; build := exec.CommandContext(ctx, \"go\", \"test\", \"-c\", \"-o\", binary, \".\"); _ = build }\n"
			write(t, root, "internal/architecture/crossbuild_test.go", crossbuild)
			decoderLoop := "package fixture\nimport (\"encoding/json\"; \"strings\"; \"testing\")\nfunc TestDecoderLoop(t *testing.T) { decoder := json.NewDecoder(strings.NewReader(\"\")); for decoder.More() { if err := decoder.Decode(new(any)); err != nil { t.Fatal(err) } } }\n"
			write(t, root, "internal/architecture/decoder_test.go", decoderLoop)
			if mode == "changed_for_clause" {
				runOK(t, root, "go", "test", "-p=1", "-run", "^$", "./internal/architecture")
			}
			runOK(t, root, "git", "init", "-q")
			runOK(t, root, "git", "add", ".")
			runOK(t, root, "git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "baseline")
			for pkg, names := range packages {
				for _, name := range names {
					tag := ""
					if pkg != "internal/architecture" {
						tag = "integration"
					}
					if strings.Contains(name, "Native") || name == "TestLocalObservabilityPromQL" || name == "TestSupplementProofsReadPublishedClaimThroughProductionLoader" {
						if tag != "" {
							tag += " && "
						}
						tag += "observability_native"
					}
					if name == "TestMonitoringRegistryFixture" {
						tag = "observability_fixture"
					}
					if mode == "unisolated" {
						tag = ""
						if pkg != "internal/architecture" {
							tag = "integration"
						}
					}
					if mode == "helper_default" && name == "TestMonitoringRegistryFixture" {
						tag = ""
					}
					if mode == "missing_native" && name == "TestLocalObservabilityExploreNative" {
						tag = "never"
					}
					if mode == "wrong_default" && name == "TestPure" {
						tag = "integration"
					}
					if mode == "worker_missing_integration" && name == "TestTelemetryWorkerDockerNativeCollectionAndReplacement" {
						tag = "observability_native"
					}
					source := sharedTestSource(tag, name)
					if mode == "changed_assertion" && name == "TestPure" {
						source = strings.ReplaceAll(source, "sharedValue()", "\"weakened\"")
					}
					if mode == "changed_helper" && name == "TestMonitoringRegistryFixture" {
						source = strings.ReplaceAll(source, "original", "changed")
					}
					if mode == "format_only" {
						source = strings.ReplaceAll(source, "t.Fatal", "\n t.Fatal")
					}
					write(t, root, pkg+"/"+name+"_test.go", source)
				}
			}
			if mode == "shared_helper_native_default" {
				write(t, root, "internal/architecture/shared_test.go", "//go:build observability_native\n\n"+shared)
			}
			if mode == "shared_helper_native_integration" {
				write(t, root, "test/acceptance/observability/shared_test.go", "//go:build integration && observability_native\n\n"+shared)
			}
			crossbuild = strings.ReplaceAll(crossbuild, "\"-c\", \"-o\"", "\"-c\", \"-tags=observability_fixture\", \"-o\"")
			if mode == "changed_crossbuild" {
				crossbuild = strings.ReplaceAll(crossbuild, "\"fixture\"", "\"different\"")
			}
			write(t, root, "internal/architecture/crossbuild_test.go", "//go:build observability_native\n\n"+crossbuild)
			if mode == "changed_for_clause" {
				// Both forms compile and have identical Test identity/build group.
				// Moving More into init changes an EOF-checked loop to an
				// unconditional loop. Never execute this counterexample.
				write(t, root, "internal/architecture/decoder_test.go", strings.Replace(decoderLoop, "for decoder.More() {", "for decoder.More(); ; {", 1))
				runOK(t, root, "go", "test", "-p=1", "-run", "^$", "./internal/architecture")
			}
			if mode == "format_only" {
				write(t, root, "internal/architecture/decoder_test.go", strings.ReplaceAll(decoderLoop, "; ", "\n"))
			}
			if mode == "missing_pure" {
				mustRemove(t, filepath.Join(root, "internal/architecture/TestPure_test.go"))
			}
			if mode == "missing_sql" {
				mustRemove(t, filepath.Join(root, "test/acceptance/observability/TestAcceptanceQueriesAgainstMigratedPostgres_test.go"))
			}
			if mode == "added_test" {
				write(t, root, "internal/architecture/added_test.go", testSource("", "TestUnregistered"))
			}
			cmd := exec.Command(binary, "HEAD")
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			wantOK := mode == "valid" || mode == "format_only"
			if (err == nil) != wantOK {
				t.Fatalf("success=%t want=%t: %v\n%s", err == nil, wantOK, err, out)
			}
			if strings.HasPrefix(mode, "shared_helper_native_") {
				group := ""
				if mode == "shared_helper_native_integration" {
					group = "integration"
				}
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "current build group \""+group+"\" does not compile") || !strings.Contains(string(out), "undefined: sharedValue") {
					t.Fatalf("expected exit1 with real %q shared-helper compilation failure, got: %v\n%s", group, err, out)
				}
			}
			if mode == "changed_for_clause" {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "changed internal/architecture/TestDecoderLoop") {
					t.Fatalf("expected exit1 for changed loop body, got: %v\n%s", err, out)
				}
				t.Logf("CLI exit=%d\n%s", exit.ExitCode(), out)
			}
		})
	}
	for _, args := range [][]string{nil, {"HEAD", "extra"}, {"--invalid"}} {
		cmd := exec.Command(binary, args...)
		if err := cmd.Run(); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		} else if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 2 {
			t.Fatalf("argument exit: %v", err)
		}
	}
}

func sharedTestSource(tag, name string) string {
	source := testSource(tag, name)
	if name == "TestPure" || name == "TestLocalObservabilityExploreNative" || name == "TestAcceptanceQueriesAgainstMigratedPostgres" || name == "TestSupplementProofsReadPublishedClaimThroughProductionLoader" {
		return strings.ReplaceAll(source, "t.Fatal(\"original\")", "t.Fatal(sharedValue())")
	}
	return source
}

func testSource(tag, name string) string {
	prefix := ""
	if tag != "" {
		prefix = "//go:build " + tag + "\n\n"
	}
	return prefix + "package fixture\nimport \"testing\"\nfunc " + name + "(t *testing.T) { t.Fatal(\"original\") }\n"
}
func write(t *testing.T, root, path, data string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
func runOK(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// Export comes from the strict commit gate. Every subsequent invocation has only
// a fresh parentless root, so no ancestor object can hide a manifest regression.
func TestGroupsManifestCLI(t *testing.T) {
	for _, mode := range []string{"valid", "missing_test", "changed_assertion", "changed_addition", "unregistered_helper", "narrow_tags", "native_missing", "fixture_default", "compile_failure", "duplicate_function", "bad_version", "empty_group", "duplicate_top_key", "duplicate_function_key", "duplicate_group_key", "unknown_field", "trailing_data"} {
		t.Run(mode, func(t *testing.T) {
			root, base := approvedGroupsFixture(t)
			binary := approvedGroupsBinary(t, base, approvedEntries)
			manifest := filepath.Join(root, "baseline.json")
			runOK(t, root, binary, "--export-manifest", base, manifest)
			if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
				t.Fatal(err)
			}
			runOK(t, root, "git", "init", "-q")
			runOK(t, root, "git", "add", ".")
			runOK(t, root, "git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "single root")
			switch mode {
			case "missing_test":
				mustRemove(t, filepath.Join(root, "internal/architecture/pure_test.go"))
			case "changed_assertion":
				write(t, root, "internal/architecture/pure_test.go", strings.ReplaceAll(testSource("", "TestPure"), "original", "weakened"))
			case "changed_addition":
				write(t, root, "test/acceptance/observability/approved_test.go", strings.ReplaceAll(approvedTestSource, "registered", "weakened"))
			case "unregistered_helper":
				write(t, root, "internal/architecture/foreign_test.go", "package fixture\nfunc foreignHelper(){}\n")
			case "narrow_tags":
				write(t, root, "test/acceptance/observability/approved_test.go", "//go:build integration\n\n"+approvedTestSource)
			case "native_missing":
				write(t, root, "internal/architecture/TestLocalObservabilityPromQL_test.go", testSource("unselected", "TestLocalObservabilityPromQL"))
			case "fixture_default":
				write(t, root, "internal/architecture/fixture_test.go", testSource("", "TestMonitoringRegistryFixture"))
			case "compile_failure":
				write(t, root, "internal/architecture/base.go", "package fixture\nvar _ = undefinedSharedHelper()\n")
			case "duplicate_top_key", "duplicate_function_key", "duplicate_group_key", "unknown_field", "trailing_data":
				raw, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "duplicate_top_key":
					raw = append([]byte(`{"version":99,`), raw[1:]...)
				case "duplicate_function_key":
					raw = []byte(strings.Replace(string(raw), `"id":`, `"id":"invalid-first-value","id":`, 1))
				case "duplicate_group_key":
					raw = []byte(strings.Replace(string(raw), `"tags":`, `"tags":"invalid-first-value","tags":`, 1))
				case "unknown_field":
					raw = append([]byte(`{"unknown":true,`), raw[1:]...)
				case "trailing_data":
					raw = append(raw, []byte(` {}`)...)
				}
				if err := os.WriteFile(manifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "duplicate_function", "bad_version", "empty_group":
				raw, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				var m portableManifest
				if err = json.Unmarshal(raw, &m); err != nil {
					t.Fatal(err)
				}
				if mode == "duplicate_function" {
					m.Functions = append(m.Functions, m.Functions[0])
				}
				if mode == "bad_version" {
					m.Version = 99
				}
				if mode == "empty_group" {
					m.Groups[0].Tests = nil
				}
				raw, err = json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(manifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(binary, "--manifest", manifest)
			cmd.Dir = root
			raw, err := cmd.CombinedOutput()
			if mode == "valid" {
				if err != nil {
					t.Fatalf("parentless manifest failed: %v\n%s", err, raw)
				}
				return
			}
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				t.Fatalf("mutation %s accepted: %v\n%s", mode, err, raw)
			}
			if strings.HasPrefix(mode, "duplicate_") && mode != "duplicate_function" && !strings.Contains(string(raw), "duplicate JSON key") {
				t.Fatalf("duplicate key parser not reached: %s", raw)
			}
			if mode == "compile_failure" && !strings.Contains(string(raw), "current build group") {
				t.Fatalf("compilation gate not reached: %s", raw)
			}
			t.Logf("%s rejected: %s", mode, raw)
		})
	}
}
