package application

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ArtifactReviewSchemaVersion = "artifact.review_manifest.v1"
	DefaultArtifactReviewBytes  = 8 << 20
)

type ArtifactReviewDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ArtifactReviewManifest struct {
	SchemaVersion    string                     `json:"schema_version"`
	Outcome          string                     `json:"outcome"`
	Renderable       bool                       `json:"renderable"`
	DownloadAllowed  bool                       `json:"download_allowed"`
	MediaType        string                     `json:"media_type"`
	ContentSizeBytes int64                      `json:"content_size_bytes"`
	Diagnostics      []ArtifactReviewDiagnostic `json:"diagnostics"`
}

type ArtifactReviewInput struct {
	Type        string
	SizeBytes   int64
	Content     []byte
	OutcomeHint string
}

type ArtifactReview struct {
	Manifest ArtifactReviewManifest
	Content  []byte
}

var modelMetadataWarning = regexp.MustCompile(
	`(?s)^\s*Warning:\s*Model metadata for .*? not found\.\s*Defaulting to fallback metadata; this can degrade performance and cause issues\.\s*`,
)

func BuildArtifactReview(input ArtifactReviewInput) ArtifactReview {
	mediaType := "text/markdown"
	if input.Type == "patch" {
		mediaType = "text/x-diff"
	}
	manifest := ArtifactReviewManifest{
		SchemaVersion: ArtifactReviewSchemaVersion, Outcome: input.OutcomeHint,
		MediaType: mediaType, ContentSizeBytes: input.SizeBytes,
		Diagnostics: []ArtifactReviewDiagnostic{},
	}
	switch input.OutcomeHint {
	case "missing":
		manifest.Diagnostics = append(manifest.Diagnostics, ArtifactReviewDiagnostic{
			Code: "artifact_missing", Message: "Artifact content is unavailable.",
		})
		return ArtifactReview{Manifest: manifest}
	case "checksum_failed":
		manifest.Diagnostics = append(manifest.Diagnostics, ArtifactReviewDiagnostic{
			Code: "artifact_integrity_failed", Message: "Artifact checksum verification failed.",
		})
		return ArtifactReview{Manifest: manifest}
	case "oversized":
		manifest.DownloadAllowed = true
		manifest.Diagnostics = append(manifest.Diagnostics, ArtifactReviewDiagnostic{
			Code: "artifact_render_limit_exceeded", Message: "Artifact exceeds the safe render limit.",
		})
		return ArtifactReview{Manifest: manifest}
	}

	manifest.DownloadAllowed = true
	if len(input.Content) == 0 && input.Type == "patch" {
		manifest.Outcome = "empty"
		return ArtifactReview{Manifest: manifest, Content: []byte{}}
	}
	if bytes.IndexByte(input.Content, 0) >= 0 || !utf8.Valid(input.Content) {
		manifest.Outcome = "binary"
		manifest.Diagnostics = append(manifest.Diagnostics, ArtifactReviewDiagnostic{
			Code: "artifact_binary", Message: "Binary Artifact content cannot be rendered safely.",
		})
		return ArtifactReview{Manifest: manifest}
	}
	content := append([]byte(nil), input.Content...)
	if input.Type == "report" {
		cleaned := modelMetadataWarning.ReplaceAll(content, nil)
		if len(cleaned) != len(content) {
			manifest.Diagnostics = append(manifest.Diagnostics, ArtifactReviewDiagnostic{
				Code:    "model_metadata_fallback",
				Message: "The model provider used fallback model metadata.",
			})
			content = cleaned
		}
	} else if !parseablePatch(content) {
		manifest.Outcome = "parse_failed"
		manifest.Diagnostics = append(manifest.Diagnostics, ArtifactReviewDiagnostic{
			Code: "artifact_patch_parse_failed", Message: "Patch content is not a supported unified diff.",
		})
		return ArtifactReview{Manifest: manifest}
	}
	manifest.Outcome = "online"
	manifest.Renderable = true
	manifest.ContentSizeBytes = int64(len(content))
	return ArtifactReview{Manifest: manifest, Content: content}
}

func parseablePatch(content []byte) bool {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if strings.HasPrefix(text, "diff --git ") {
		return strings.Contains(text, "\n--- ") && strings.Contains(text, "\n+++ ") ||
			strings.Contains(text, "\nnew file mode ") ||
			strings.Contains(text, "\ndeleted file mode ") ||
			strings.Contains(text, "\nrename from ") ||
			strings.Contains(text, "\nBinary files ")
	}
	return strings.HasPrefix(text, "--- ") && strings.Contains(text, "\n+++ ")
}
