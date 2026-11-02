package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const RuntimeSourcePlanVersion = "workspace_source_plan_v1"

type RuntimeSourcePlanEntry struct {
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Mode          uint32 `json:"mode"`
	Size          int64  `json:"size"`
	ContentDigest string `json:"content_digest"`
}

// RuntimeSourcePlanBinding contains immutable input facts, not execution authority
// or proof of a filesystem snapshot or physical storage reservation.
type RuntimeSourcePlanBinding struct{ Manifest, Digest string }

type RuntimeSourcePlanStats struct{ Files, Directories, LogicalBytes int64 }

type runtimeSourcePlanManifest struct {
	Version            string                   `json:"version"`
	SourceOriginDigest string                   `json:"source_origin_digest"`
	Entries            []RuntimeSourcePlanEntry `json:"entries"`
	Files              int64                    `json:"files"`
	Directories        int64                    `json:"directories"`
	LogicalBytes       int64                    `json:"logical_bytes"`
	LegacyFileDigest   string                   `json:"legacy_file_digest"`
}

func NewRuntimeSourcePlanBinding(sourceOrigin string, entries []RuntimeSourcePlanEntry) (RuntimeSourcePlanBinding, error) {
	if !strings.HasPrefix(sourceOrigin, "sha256:") || !runtimeSourcePlanHash(strings.TrimPrefix(sourceOrigin, "sha256:")) {
		return RuntimeSourcePlanBinding{}, fmt.Errorf("source origin must be a lowercase sha256 digest")
	}
	// Always allocate: nil input encodes as [] and sorting never mutates the caller.
	ordered := make([]RuntimeSourcePlanEntry, len(entries))
	copy(ordered, entries)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	manifest := runtimeSourcePlanManifest{Version: RuntimeSourcePlanVersion, SourceOriginDigest: sourceOrigin, Entries: ordered}
	byPath := make(map[string]string, len(ordered))
	legacy := sha256.New()
	for _, entry := range ordered {
		if entry.Path == "." || !utf8.ValidString(entry.Path) || strings.ContainsRune(entry.Path, 0) || !fs.ValidPath(entry.Path) {
			return RuntimeSourcePlanBinding{}, fmt.Errorf("invalid source plan path %q", entry.Path)
		}
		if _, exists := byPath[entry.Path]; exists {
			return RuntimeSourcePlanBinding{}, fmt.Errorf("duplicate source plan path %q", entry.Path)
		}
		if entry.Mode > 0o777 {
			return RuntimeSourcePlanBinding{}, fmt.Errorf("invalid source plan mode for %q", entry.Path)
		}
		switch entry.Kind {
		case "directory":
			if entry.Size != 0 || entry.ContentDigest != "" {
				return RuntimeSourcePlanBinding{}, fmt.Errorf("directory must have zero size and empty content digest: %q", entry.Path)
			}
			manifest.Directories++
		case "file":
			if entry.Size < 0 || !runtimeSourcePlanHash(entry.ContentDigest) {
				return RuntimeSourcePlanBinding{}, fmt.Errorf("file must have nonnegative size and lowercase content digest: %q", entry.Path)
			}
			if manifest.LogicalBytes > math.MaxInt64-entry.Size {
				return RuntimeSourcePlanBinding{}, fmt.Errorf("source plan logical bytes overflow")
			}
			manifest.Files++
			manifest.LogicalBytes += entry.Size
			_, _ = fmt.Fprintf(legacy, "%s\x00%o\x00%s\n", entry.Path, entry.Mode, entry.ContentDigest)
		default:
			return RuntimeSourcePlanBinding{}, fmt.Errorf("invalid source plan kind for %q", entry.Path)
		}
		byPath[entry.Path] = entry.Kind
	}
	for _, entry := range ordered {
		if parent := path.Dir(entry.Path); parent != "." && byPath[parent] != "directory" {
			return RuntimeSourcePlanBinding{}, fmt.Errorf("source plan parent must be a directory: %q", entry.Path)
		}
	}
	manifest.LegacyFileDigest = hex.EncodeToString(legacy.Sum(nil))
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return RuntimeSourcePlanBinding{}, fmt.Errorf("encode source plan: %w", err)
	}
	digest := sha256.Sum256(append([]byte(RuntimeSourcePlanVersion+"\x00"), encoded...))
	return RuntimeSourcePlanBinding{Manifest: string(encoded), Digest: "sha256:" + hex.EncodeToString(digest[:])}, nil
}

func runtimeSourcePlanHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func (binding RuntimeSourcePlanBinding) validatedManifest() (runtimeSourcePlanManifest, error) {
	var manifest runtimeSourcePlanManifest
	decoder := json.NewDecoder(strings.NewReader(binding.Manifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("decode source plan: %w", err)
	}
	if manifest.Version != RuntimeSourcePlanVersion {
		return manifest, fmt.Errorf("unknown source plan version %q", manifest.Version)
	}
	rebuilt, err := NewRuntimeSourcePlanBinding(manifest.SourceOriginDigest, manifest.Entries)
	if err != nil {
		return manifest, err
	}
	// Exact re-encoding also rejects duplicate keys, omitted fields, null entries,
	// reordered fields, alternate escapes/numbers, invalid UTF-8 and trailing data.
	if binding.Manifest != rebuilt.Manifest {
		return manifest, fmt.Errorf("source plan manifest is not canonical or has inconsistent statistics or legacy digest")
	}
	if binding.Digest != rebuilt.Digest {
		return manifest, fmt.Errorf("source plan digest mismatch")
	}
	return manifest, nil
}

func (binding RuntimeSourcePlanBinding) Validate() error {
	_, err := binding.validatedManifest()
	return err
}

func (binding RuntimeSourcePlanBinding) Stats() (RuntimeSourcePlanStats, error) {
	manifest, err := binding.validatedManifest()
	if err != nil {
		return RuntimeSourcePlanStats{}, err
	}
	return RuntimeSourcePlanStats{Files: manifest.Files, Directories: manifest.Directories, LogicalBytes: manifest.LogicalBytes}, nil
}

func (binding RuntimeSourcePlanBinding) LegacyFileDigest() (string, error) {
	manifest, err := binding.validatedManifest()
	if err != nil {
		return "", err
	}
	return manifest.LegacyFileDigest, nil
}
