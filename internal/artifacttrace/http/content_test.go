package artifacthttp_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	artifacthttp "github.com/abietic/switchback/internal/artifacttrace/http"
	workerfake "github.com/abietic/switchback/internal/workerruntime/adapter/fake"
)

func TestContentHandlerVerifiesAndServesCanonicalArtifact(t *testing.T) {
	objects := &workerfake.ObjectStore{Objects: map[string][]byte{"objects/report": []byte("# Report\n")}}
	handler, err := artifacthttp.NewContentHandler(
		artifacthttp.FixedViewer{TenantID: "tenant-local", UserID: "user-local"},
		fixedContentReader{content: artifacthttp.ArtifactContent{
			ArtifactID: "artifact-1", Type: "report", ObjectKey: "objects/report",
			Checksum: "sha256:verified", SizeBytes: 9,
		}}, objects)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/artifact-1/content", nil))
	if response.Code != http.StatusOK || response.Body.String() != "# Report\n" ||
		response.Header().Get("Content-Type") != "text/markdown; charset=utf-8" ||
		!strings.Contains(response.Header().Get("Content-Disposition"), "artifact-1.md") {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestContentHandlerReturnsSafeIntegrityAndVisibilityErrors(t *testing.T) {
	viewer := artifacthttp.FixedViewer{TenantID: "tenant-local", UserID: "user-local"}
	content := artifacthttp.ArtifactContent{
		ArtifactID: "artifact-secret", Type: "patch", ObjectKey: "private/object-secret",
		Checksum: "sha256:expected", SizeBytes: 1,
	}
	objects := &workerfake.ObjectStore{Err: errors.New("checksum mismatch at private/object-secret")}
	handler, _ := artifacthttp.NewContentHandler(viewer, fixedContentReader{content: content}, objects)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/artifact-secret/content", nil))
	if response.Code != http.StatusConflict || response.Body.String() != "{\"code\":\"artifact_integrity_failed\"}\n" ||
		strings.Contains(response.Body.String(), content.ObjectKey) {
		t.Fatalf("integrity response status=%d body=%q", response.Code, response.Body.String())
	}

	hidden, _ := artifacthttp.NewContentHandler(viewer, fixedContentReader{err: errors.New("not visible")}, objects)
	notFound := httptest.NewRecorder()
	hidden.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/artifact-secret/content", nil))
	if notFound.Code != http.StatusNotFound || strings.Contains(notFound.Body.String(), "not visible") {
		t.Fatalf("visibility response status=%d body=%q", notFound.Code, notFound.Body.String())
	}

	missingObjects := &workerfake.ObjectStore{Err: fmt.Errorf("verify object: %w", os.ErrNotExist)}
	missing, _ := artifacthttp.NewContentHandler(viewer, fixedContentReader{content: content}, missingObjects)
	missingResponse := httptest.NewRecorder()
	missing.ServeHTTP(
		missingResponse,
		httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/artifact-secret/content", nil),
	)
	if missingResponse.Code != http.StatusNotFound ||
		missingResponse.Body.String() != "{\"code\":\"artifact_unavailable\"}\n" {
		t.Fatalf("missing response status=%d body=%q", missingResponse.Code, missingResponse.Body.String())
	}
}

type fixedContentReader struct {
	content artifacthttp.ArtifactContent
	err     error
}

func (reader fixedContentReader) ReadArtifactContent(
	context.Context,
	artifacthttp.FixedViewer,
	string,
) (artifacthttp.ArtifactContent, error) {
	return reader.content, reader.err
}
