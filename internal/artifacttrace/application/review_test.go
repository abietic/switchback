package application_test

import (
	"strings"
	"testing"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
)

func TestArtifactReviewManifestSeparatesRenderingFromVerifiedDownload(t *testing.T) {
	tests := []struct {
		name       string
		input      artifactapp.ArtifactReviewInput
		outcome    string
		renderable bool
		download   bool
		diagnostic string
	}{
		{
			name: "empty patch", input: artifactapp.ArtifactReviewInput{Type: "patch"},
			outcome: "empty", download: true,
		},
		{
			name: "binary", input: artifactapp.ArtifactReviewInput{
				Type: "patch", SizeBytes: 3, Content: []byte{'a', 0, 'b'},
			},
			outcome: "binary", download: true, diagnostic: "artifact_binary",
		},
		{
			name: "oversized", input: artifactapp.ArtifactReviewInput{
				Type: "report", SizeBytes: artifactapp.DefaultArtifactReviewBytes + 1,
				OutcomeHint: "oversized",
			},
			outcome: "oversized", download: true, diagnostic: "artifact_render_limit_exceeded",
		},
		{
			name: "parse failed", input: artifactapp.ArtifactReviewInput{
				Type: "patch", SizeBytes: 10, Content: []byte("not a diff"),
			},
			outcome: "parse_failed", download: true, diagnostic: "artifact_patch_parse_failed",
		},
		{
			name: "missing", input: artifactapp.ArtifactReviewInput{
				Type: "report", OutcomeHint: "missing",
			},
			outcome: "missing", diagnostic: "artifact_missing",
		},
		{
			name: "checksum failed", input: artifactapp.ArtifactReviewInput{
				Type: "report", OutcomeHint: "checksum_failed",
			},
			outcome: "checksum_failed", diagnostic: "artifact_integrity_failed",
		},
		{
			name: "online patch", input: artifactapp.ArtifactReviewInput{
				Type: "patch", SizeBytes: 80,
				Content: []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"),
			},
			outcome: "online", renderable: true, download: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			review := artifactapp.BuildArtifactReview(test.input)
			if review.Manifest.Outcome != test.outcome ||
				review.Manifest.Renderable != test.renderable ||
				review.Manifest.DownloadAllowed != test.download {
				t.Fatalf("review=%+v", review)
			}
			if test.diagnostic != "" &&
				(len(review.Manifest.Diagnostics) != 1 ||
					review.Manifest.Diagnostics[0].Code != test.diagnostic) {
				t.Fatalf("diagnostics=%+v", review.Manifest.Diagnostics)
			}
		})
	}
}

func TestArtifactReviewRemovesKnownProviderDiagnosticFromCanonicalReport(t *testing.T) {
	input := "Warning: Model metadata for `switchback-codex` not found. " +
		"Defaulting to fallback metadata; this can degrade performance and cause issues. " +
		"## Result\nDone."
	review := artifactapp.BuildArtifactReview(artifactapp.ArtifactReviewInput{
		Type: "report", SizeBytes: int64(len(input)), Content: []byte(input),
	})
	if review.Manifest.Outcome != "online" || !review.Manifest.Renderable ||
		len(review.Manifest.Diagnostics) != 1 ||
		review.Manifest.Diagnostics[0].Code != "model_metadata_fallback" ||
		strings.Contains(string(review.Content), "Model metadata") ||
		string(review.Content) != "## Result\nDone." {
		t.Fatalf("review=%+v content=%q", review.Manifest, review.Content)
	}
}
