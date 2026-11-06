package localproduct

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestWorkspaceSourceBindingUsesOnlyPlannedFacts(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, filepath.Join(source, `a\b`), "abc", 0o601)
	if err := os.Chmod(filepath.Join(source, `a\b`), 0o601); err != nil {
		t.Fatal(err)
	}
	plan, err := planWorkspaceSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	// Independent Python literal for configured_workspace_source_root_v1 + NUL
	// + this configured resolved path. Binding must not open this pathname.
	plan.path = "/srv/configured/workspace"
	binding, err := plan.Binding()
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SourceOriginDigest string                             `json:"source_origin_digest"`
		Entries            []workerapp.RuntimeSourcePlanEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(binding.Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SourceOriginDigest != "sha256:e4f8b0cf8357b8f2aeccf38359426aeafc8169adebf80090f9e32a6b1bef71ab" {
		t.Fatalf("origin=%s", manifest.SourceOriginDigest)
	}
	want := []workerapp.RuntimeSourcePlanEntry{
		{Path: `a\b`, Kind: "file", Mode: 0o601, Size: 3, ContentDigest: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{Path: "empty", Kind: "directory", Mode: 0o700},
	}
	if len(manifest.Entries) != len(want) {
		t.Fatalf("entries=%+v", manifest.Entries)
	}
	for i := range want {
		if manifest.Entries[i] != want[i] {
			t.Fatalf("entry=%+v want=%+v", manifest.Entries[i], want[i])
		}
	}
	if stats, err := binding.Stats(); err != nil || stats != (workerapp.RuntimeSourcePlanStats{Files: 1, Directories: 1, LogicalBytes: 3}) {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if legacy, err := binding.LegacyFileDigest(); err != nil || legacy != plan.digest {
		t.Fatalf("legacy=%s planned=%s err=%v", legacy, plan.digest, err)
	}
	if err := plan.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	withoutSource, err := plan.Binding()
	if err != nil || withoutSource != binding {
		t.Fatalf("binding read source after planning: %+v err=%v", withoutSource, err)
	}
	plan.path += "-other"
	otherOrigin, err := plan.Binding()
	if err != nil || otherOrigin.Digest == binding.Digest {
		t.Fatalf("configured root not bound: %+v err=%v", otherOrigin, err)
	}
}

func TestWorkspaceSourceBindingRejectsInconsistentPlan(t *testing.T) {
	for _, field := range []string{"files", "directories", "bytes", "digest", "entry-digest", "entry-metadata"} {
		t.Run(field, func(t *testing.T) {
			source := t.TempDir()
			sourceWrite(t, filepath.Join(source, "file"), "abc", 0o644)
			plan, err := planWorkspaceSource(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			want := "statistics or legacy digest mismatch"
			switch field {
			case "files":
				plan.files++
			case "directories":
				plan.directories++
			case "bytes":
				plan.logicalBytes++
			case "digest":
				plan.digest = strings.Repeat("0", 64)
			case "entry-digest":
				plan.entries[0].digest = "not-a-digest"
				want = "lowercase content digest"
			case "entry-metadata":
				plan.entries[0].info = nil
				want = "metadata is required"
			}
			if _, err := plan.Binding(); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error=%v want containing %q", err, want)
			}
		})
	}
	var absent *workspaceSourcePlan
	if _, err := absent.Binding(); err == nil || !strings.Contains(err.Error(), "plan is required") {
		t.Fatalf("nil plan error=%v", err)
	}
}

func TestWorkspaceSourceBindingIncludesEmptyDirectories(t *testing.T) {
	source := t.TempDir()
	sourceWrite(t, filepath.Join(source, "file"), "abc", 0o644)
	first, err := planWorkspaceSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	firstBinding, err := first.Binding()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := planWorkspaceSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondBinding, err := second.Binding()
	if err != nil {
		t.Fatal(err)
	}
	if first.digest != second.digest || firstBinding.Digest == secondBinding.Digest {
		t.Fatalf("empty directory semantics: legacy %s/%s complete %s/%s", first.digest, second.digest, firstBinding.Digest, secondBinding.Digest)
	}
}
