package artifacthttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type FixedViewer struct {
	TenantID string
	UserID   string
}

type ArtifactContent struct {
	ArtifactID string
	Type       string
	ObjectKey  string
	Checksum   string
	SizeBytes  int64
}

type ContentReader interface {
	ReadArtifactContent(context.Context, FixedViewer, string) (ArtifactContent, error)
}

type ContentHandler struct {
	viewer  FixedViewer
	reader  ContentReader
	objects workerapp.ObjectStore
}

func NewContentHandler(viewer FixedViewer, reader ContentReader, objects workerapp.ObjectStore) (*ContentHandler, error) {
	if !canonical(viewer.TenantID) || !canonical(viewer.UserID) || reader == nil || objects == nil {
		return nil, fmt.Errorf("Artifact content handler dependencies are required")
	}
	return &ContentHandler{viewer: viewer, reader: reader, objects: objects}, nil
}

func (handler *ContentHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	artifactID := request.PathValue("artifact_id")
	if artifactID == "" {
		path := strings.TrimPrefix(request.URL.Path, "/api/v1/artifacts/")
		artifactID = strings.TrimSuffix(path, "/content")
		if path == artifactID || strings.Contains(artifactID, "/") {
			artifactID = ""
		}
	}
	if request.Method != http.MethodGet || !canonical(artifactID) {
		http.NotFound(response, request)
		return
	}
	artifact, err := handler.reader.ReadArtifactContent(request.Context(), handler.viewer, artifactID)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if err := handler.objects.Verify(request.Context(), artifact.ObjectKey, artifact.Checksum); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(response, http.StatusNotFound, "artifact_unavailable")
			return
		}
		writeError(response, http.StatusConflict, "artifact_integrity_failed")
		return
	}
	content, err := handler.objects.Open(request.Context(), artifact.ObjectKey)
	if err != nil {
		writeError(response, http.StatusNotFound, "artifact_unavailable")
		return
	}
	defer content.Close()
	contentType := "application/octet-stream"
	extension := ".bin"
	if artifact.Type == "report" {
		contentType, extension = "text/markdown; charset=utf-8", ".md"
	} else if artifact.Type == "patch" {
		contentType, extension = "text/x-diff; charset=utf-8", ".patch"
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", artifact.ArtifactID+extension))
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	response.WriteHeader(http.StatusOK)
	_, _ = io.Copy(response, content)
}

func writeError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = fmt.Fprintf(response, "{\"code\":%q}\n", code)
}

func canonical(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, " /\\\r\n\x00")
}
