package application

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"reflect"
	"strings"
	"testing"
)

const sourcePlanTestOrigin = "sha256:148eeff25d13a8ce2eab1130632371c91ae7f6becef594e020a596514e200732"
const sourcePlanTestContent = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

// These byte strings and digests were independently computed with Python, not
// derived from the implementation under test. Keep the fixtures self-contained.
func TestRuntimeSourcePlanGoldenVectors(t *testing.T) {
	for _, test := range []struct {
		name, canonical, digest, legacy string
		entries                         []RuntimeSourcePlanEntry
		stats                           RuntimeSourcePlanStats
	}{
		{
			name:      "empty",
			canonical: `{"version":"workspace_source_plan_v1","source_origin_digest":"sha256:148eeff25d13a8ce2eab1130632371c91ae7f6becef594e020a596514e200732","entries":[],"files":0,"directories":0,"logical_bytes":0,"legacy_file_digest":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`,
			digest:    "sha256:bb7e67c109a0a7683a9d716f55636b810adade204c960cf78e803a05d294d04d",
			legacy:    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:      "files-and-empty-directory",
			canonical: `{"version":"workspace_source_plan_v1","source_origin_digest":"sha256:148eeff25d13a8ce2eab1130632371c91ae7f6becef594e020a596514e200732","entries":[{"path":"empty","kind":"directory","mode":448,"size":0,"content_digest":""},{"path":"src","kind":"directory","mode":493,"size":0,"content_digest":""},{"path":"src/main.go","kind":"file","mode":420,"size":3,"content_digest":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}],"files":1,"directories":2,"logical_bytes":3,"legacy_file_digest":"6101faab3880092c2938e337fbf780c3a0ab35fa23ba253439c20ee38d945f84"}`,
			digest:    "sha256:0fdedc10552fb1eb87c3b1840af662d525eebadf2bfc7de33e5cbc71128c3b69",
			legacy:    "6101faab3880092c2938e337fbf780c3a0ab35fa23ba253439c20ee38d945f84",
			entries: []RuntimeSourcePlanEntry{
				{Path: "src/main.go", Kind: "file", Mode: 0o644, Size: 3, ContentDigest: sourcePlanTestContent},
				{Path: "src", Kind: "directory", Mode: 0o755},
				{Path: "empty", Kind: "directory", Mode: 0o700},
			},
			stats: RuntimeSourcePlanStats{Files: 1, Directories: 2, LogicalBytes: 3},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := append([]RuntimeSourcePlanEntry(nil), test.entries...)
			binding, err := NewRuntimeSourcePlanBinding(sourcePlanTestOrigin, test.entries)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(test.entries, before) {
				t.Fatalf("constructor mutated input: %+v", test.entries)
			}
			if binding.Manifest != test.canonical || binding.Digest != test.digest {
				t.Fatalf("binding=%+v, want canonical=%s digest=%s", binding, test.canonical, test.digest)
			}
			if err := binding.Validate(); err != nil {
				t.Fatal(err)
			}
			if stats, err := binding.Stats(); err != nil || stats != test.stats {
				t.Fatalf("stats=%+v err=%v", stats, err)
			}
			if legacy, err := binding.LegacyFileDigest(); err != nil || legacy != test.legacy {
				t.Fatalf("legacy=%s err=%v", legacy, err)
			}
			copyBinding := binding
			if len(test.entries) > 0 {
				test.entries[0].Path = "mutated-after-construction"
			}
			if copyBinding != binding || binding.Manifest != test.canonical {
				t.Fatal("binding retained mutable input")
			}
		})
	}
}

func TestRuntimeSourcePlanBindsEveryInputFact(t *testing.T) {
	file := RuntimeSourcePlanEntry{Path: "file", Kind: "file", Mode: 0o644, Size: 3, ContentDigest: sourcePlanTestContent}
	base := sourcePlanTestBinding(t, sourcePlanTestOrigin, []RuntimeSourcePlanEntry{file})
	for _, field := range []string{"origin", "path", "kind", "mode", "size", "content", "empty-directory"} {
		t.Run(field, func(t *testing.T) {
			origin, changed := sourcePlanTestOrigin, file
			entries := []RuntimeSourcePlanEntry{changed}
			switch field {
			case "origin":
				origin = "sha256:" + strings.Repeat("0", 64)
			case "path":
				entries[0].Path = "other"
			case "kind":
				entries[0].Kind, entries[0].Size, entries[0].ContentDigest = "directory", 0, ""
			case "mode":
				entries[0].Mode = 0o600
			case "size":
				entries[0].Size++
			case "content":
				entries[0].ContentDigest = strings.Repeat("0", 64)
			case "empty-directory":
				entries = append(entries, RuntimeSourcePlanEntry{Path: "empty", Kind: "directory", Mode: 0o755})
			}
			got := sourcePlanTestBinding(t, origin, entries)
			if got.Digest == base.Digest || got.Manifest == base.Manifest {
				t.Fatalf("%s did not change complete binding", field)
			}
			if field == "empty-directory" {
				baseLegacy, _ := base.LegacyFileDigest()
				gotLegacy, _ := got.LegacyFileDigest()
				if baseLegacy != gotLegacy {
					t.Fatal("empty directory changed legacy digest")
				}
			}
		})
	}
}

func TestRuntimeSourcePlanUnixPathsAndByteOrder(t *testing.T) {
	entries := []RuntimeSourcePlanEntry{
		{Path: "é", Kind: "directory"},
		{Path: `a\b`, Kind: "directory"},
		{Path: "a", Kind: "directory"},
		{Path: "a/z", Kind: "directory"},
		{Path: "Z", Kind: "directory"},
	}
	binding := sourcePlanTestBinding(t, sourcePlanTestOrigin, entries)
	wantPaths := []string{`"path":"Z"`, `"path":"a"`, `"path":"a/z"`, `"path":"a\\b"`, `"path":"é"`}
	previous := -1
	for _, value := range wantPaths {
		position := strings.Index(binding.Manifest, value)
		if position <= previous {
			t.Fatalf("path %s not ordered by slash path bytes: %s", value, binding.Manifest)
		}
		previous = position
	}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSourcePlanRejectsInvalidEntries(t *testing.T) {
	file := RuntimeSourcePlanEntry{Path: "file", Kind: "file", Mode: 0o644, Size: 3, ContentDigest: sourcePlanTestContent}
	for _, test := range []struct {
		name, want string
		mutate     func(*RuntimeSourcePlanEntry)
	}{
		{"empty-path", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "" }},
		{"root", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "." }},
		{"absolute", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "/file" }},
		{"parent", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "../file" }},
		{"internal-parent", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "a/../file" }},
		{"dot-component", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "a/./file" }},
		{"double-slash", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "a//file" }},
		{"trailing-slash", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "file/" }},
		{"nul", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "f\x00ile" }},
		{"utf8", "invalid source plan path", func(e *RuntimeSourcePlanEntry) { e.Path = "f\xffile" }},
		{"kind", "invalid source plan kind", func(e *RuntimeSourcePlanEntry) { e.Kind = "symlink" }},
		{"mode", "invalid source plan mode", func(e *RuntimeSourcePlanEntry) { e.Mode = 0o1000 }},
		{"negative-size", "nonnegative size", func(e *RuntimeSourcePlanEntry) { e.Size = -1 }},
		{"empty-hash", "lowercase content digest", func(e *RuntimeSourcePlanEntry) { e.ContentDigest = "" }},
		{"uppercase-hash", "lowercase content digest", func(e *RuntimeSourcePlanEntry) { e.ContentDigest = strings.ToUpper(sourcePlanTestContent) }},
		{"prefixed-hash", "lowercase content digest", func(e *RuntimeSourcePlanEntry) { e.ContentDigest = "sha256:" + sourcePlanTestContent }},
		{"invalid-hash", "lowercase content digest", func(e *RuntimeSourcePlanEntry) { e.ContentDigest = strings.Repeat("g", 64) }},
		{"directory-size", "directory must have zero size", func(e *RuntimeSourcePlanEntry) { e.Kind, e.ContentDigest = "directory", "" }},
		{"directory-hash", "empty content digest", func(e *RuntimeSourcePlanEntry) { e.Kind, e.Size = "directory", 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := file
			test.mutate(&entry)
			_, err := NewRuntimeSourcePlanBinding(sourcePlanTestOrigin, []RuntimeSourcePlanEntry{entry})
			sourcePlanTestError(t, err, test.want)
		})
	}
	for _, origin := range []string{"", sourcePlanTestContent, "sha256:" + strings.ToUpper(sourcePlanTestContent), "sha256:" + strings.Repeat("g", 64)} {
		_, err := NewRuntimeSourcePlanBinding(origin, nil)
		sourcePlanTestError(t, err, "source origin")
	}
}

func TestRuntimeSourcePlanRejectsInvalidTreeAndOverflow(t *testing.T) {
	file := RuntimeSourcePlanEntry{Path: "a", Kind: "file", Mode: 0o644, Size: 3, ContentDigest: sourcePlanTestContent}
	child := file
	child.Path = "a/b"
	for _, test := range []struct {
		name, want string
		entries    []RuntimeSourcePlanEntry
	}{
		{"duplicate", "duplicate source plan path", []RuntimeSourcePlanEntry{file, file}},
		{"missing-parent", "parent must be a directory", []RuntimeSourcePlanEntry{child}},
		{"file-parent", "parent must be a directory", []RuntimeSourcePlanEntry{file, child}},
		{"missing-ancestor", "parent must be a directory", []RuntimeSourcePlanEntry{{Path: "a/b", Kind: "directory"}, {Path: "a/b/c", Kind: "directory"}}},
		{"overflow", "logical bytes overflow", []RuntimeSourcePlanEntry{{Path: "a", Kind: "file", Size: math.MaxInt64, ContentDigest: sourcePlanTestContent}, {Path: "b", Kind: "file", Size: 1, ContentDigest: sourcePlanTestContent}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRuntimeSourcePlanBinding(sourcePlanTestOrigin, test.entries)
			sourcePlanTestError(t, err, test.want)
		})
	}
	max := sourcePlanTestBinding(t, sourcePlanTestOrigin, []RuntimeSourcePlanEntry{{Path: "a", Kind: "file", Size: math.MaxInt64, ContentDigest: sourcePlanTestContent}})
	if stats, err := max.Stats(); err != nil || stats.LogicalBytes != math.MaxInt64 {
		t.Fatalf("maximum size rejected: %+v %v", stats, err)
	}
}

func TestRuntimeSourcePlanRejectsNoncanonicalManifest(t *testing.T) {
	base := sourcePlanTestBinding(t, sourcePlanTestOrigin, []RuntimeSourcePlanEntry{{Path: "file", Kind: "file", Mode: 0o644, Size: 3, ContentDigest: sourcePlanTestContent}})
	for _, test := range []struct {
		name, manifest, want string
	}{
		{"unknown-field", strings.Replace(base.Manifest, `"files":1`, `"other":0,"files":1`, 1), "unknown field"},
		{"unknown-entry-field", strings.Replace(base.Manifest, `"kind":"file"`, `"extra":0,"kind":"file"`, 1), "unknown field"},
		{"unknown-version", strings.Replace(base.Manifest, RuntimeSourcePlanVersion, "v2", 1), "unknown source plan version"},
		{"duplicate-top-key", strings.Replace(base.Manifest, `"files":1`, `"files":1,"files":1`, 1), "not canonical"},
		{"duplicate-entry-key", strings.Replace(base.Manifest, `"mode":420`, `"mode":420,"mode":420`, 1), "not canonical"},
		{"duplicate-escaped-key", strings.Replace(base.Manifest, `"files":1`, `"files":1,"\u0066iles":1`, 1), "not canonical"},
		{"stats-files", strings.Replace(base.Manifest, `"files":1`, `"files":0`, 1), "inconsistent statistics"},
		{"stats-directories", strings.Replace(base.Manifest, `"directories":0`, `"directories":1`, 1), "inconsistent statistics"},
		{"stats-bytes", strings.Replace(base.Manifest, `"logical_bytes":3`, `"logical_bytes":4`, 1), "inconsistent statistics"},
		{"legacy", strings.Replace(base.Manifest, `"legacy_file_digest":"`, `"legacy_file_digest":"0`, 1), "legacy digest"},
		{"whitespace", " " + base.Manifest, "not canonical"},
		{"trailing-json", base.Manifest + `{}`, "not canonical"},
		{"trailing-garbage", base.Manifest + `x`, "not canonical"},
		{"escaped-path", strings.Replace(base.Manifest, `"path":"file"`, `"path":"\u0066ile"`, 1), "not canonical"},
		{"escaped-invalid-surrogate", strings.Replace(base.Manifest, `"path":"file"`, `"path":"\ud800"`, 1), "not canonical"},
		{"raw-invalid-utf8", strings.Replace(base.Manifest, `"path":"file"`, "\"path\":\"\xff\"", 1), "not canonical"},
		{"missing-mode", strings.Replace(base.Manifest, `"mode":420,`, "", 1), "not canonical"},
		{"null-mode", strings.Replace(base.Manifest, `"mode":420`, `"mode":null`, 1), "not canonical"},
		{"float-size", strings.Replace(base.Manifest, `"size":3`, `"size":3.0`, 1), "decode source plan"},
		{"overflow-size", strings.Replace(base.Manifest, `"size":3`, `"size":9223372036854775808`, 1), "decode source plan"},
		{"malformed", `{`, "decode source plan"},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := sourcePlanTestRehash(test.manifest)
			sourcePlanTestError(t, binding.Validate(), test.want)
			_, statsErr := binding.Stats()
			sourcePlanTestError(t, statsErr, test.want)
			_, legacyErr := binding.LegacyFileDigest()
			sourcePlanTestError(t, legacyErr, test.want)
		})
	}
	empty := sourcePlanTestBinding(t, sourcePlanTestOrigin, nil)
	for _, entries := range []string{"null", "{}"} {
		binding := sourcePlanTestRehash(strings.Replace(empty.Manifest, `"entries":[]`, `"entries":`+entries, 1))
		if err := binding.Validate(); err == nil {
			t.Fatalf("accepted entries=%s", entries)
		}
	}
	base.Digest = "sha256:" + strings.Repeat("0", 64)
	sourcePlanTestError(t, base.Validate(), "digest mismatch")
}

func sourcePlanTestBinding(t *testing.T, origin string, entries []RuntimeSourcePlanEntry) RuntimeSourcePlanBinding {
	t.Helper()
	binding, err := NewRuntimeSourcePlanBinding(origin, entries)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func sourcePlanTestRehash(manifest string) RuntimeSourcePlanBinding {
	sum := sha256.Sum256([]byte(RuntimeSourcePlanVersion + "\x00" + manifest))
	return RuntimeSourcePlanBinding{Manifest: manifest, Digest: "sha256:" + hex.EncodeToString(sum[:])}
}

func sourcePlanTestError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error=%v, want containing %q", err, want)
	}
}
