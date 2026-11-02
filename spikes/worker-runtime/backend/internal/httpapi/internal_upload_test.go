package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"switchback.local/spikes/worker-runtime/backend/internal/objectstore"
	"switchback.local/spikes/worker-runtime/backend/internal/service"
	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
	"switchback.local/spikes/worker-runtime/internal/redact"
)

const (
	testBeforeTree = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testAfterTree  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestEvidenceUploadRejectsAuthenticationIdentityAndPayloadFailures(t *testing.T) {
	handler := newTestHandler(t)
	created := createRun(t, handler)
	path := "/internal/v1/sessions/" + created.WorkerSessionID + "/runs/" + created.WorkerRunID + "/turns/" + created.TurnID + "/evidence/trace"

	tests := []struct {
		name    string
		path    string
		headers map[string]string
		body    string
		want    int
	}{
		{name: "missing bearer", path: path, headers: map[string]string{"Content-Type": "text/plain", "X-Content-SHA256": digest("payload")}, body: "payload", want: http.StatusUnauthorized},
		{name: "wrong bearer", path: path, headers: uploadHeaders("wrong", "payload"), body: "payload", want: http.StatusUnauthorized},
		{name: "missing content type", path: path, headers: map[string]string{"Authorization": "Bearer test-worker-token", "X-Content-SHA256": digest("payload")}, body: "payload", want: http.StatusBadRequest},
		{name: "identity mismatch", path: strings.Replace(path, created.WorkerRunID, "other-run", 1), headers: validUploadHeaders("payload"), body: "payload", want: http.StatusForbidden},
		{name: "turn mismatch", path: strings.Replace(path, created.TurnID, "other-turn", 1), headers: validUploadHeaders("payload"), body: "payload", want: http.StatusForbidden},
		{name: "unsupported kind", path: strings.TrimSuffix(path, "trace") + "manifest", headers: validUploadHeaders("payload"), body: "payload", want: http.StatusBadRequest},
		{name: "checksum mismatch", path: path, headers: map[string]string{"Authorization": "Bearer test-worker-token", "Content-Type": "text/plain", "X-Content-SHA256": strings.Repeat("0", 64)}, body: "payload", want: http.StatusBadRequest},
		{name: "missing before tree", path: path, headers: uploadHeadersWithTrees("test-worker-token", "payload", "", testAfterTree), body: "payload", want: http.StatusBadRequest},
		{name: "missing after tree", path: path, headers: uploadHeadersWithTrees("test-worker-token", "payload", testBeforeTree, ""), body: "payload", want: http.StatusBadRequest},
		{name: "invalid tree", path: path, headers: uploadHeadersWithTrees("test-worker-token", "payload", "not-a-tree", testAfterTree), body: "payload", want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader(tt.body))
			for key, value := range tt.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.want {
				t.Fatalf("status = %d, body = %s, want %d", response.Code, response.Body.String(), tt.want)
			}
			if strings.Contains(response.Body.String(), "test-worker-token") {
				t.Fatalf("error response leaked token: %s", response.Body.String())
			}
		})
	}
}

func TestEvidenceUploadChecksRawPayloadBeforeRedactedStorage(t *testing.T) {
	root := t.TempDir()
	store := objectstore.LocalFileObjectStore{Root: root, Redactor: redact.New(redact.Secrets{})}
	handler := newTestHandlerWithStore(t, store)
	created := createRun(t, handler)
	payload := "report includes test-worker-token"
	path := "/internal/v1/sessions/" + created.WorkerSessionID + "/runs/" + created.WorkerRunID + "/turns/" + created.TurnID + "/evidence/report"
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(payload))
	for key, value := range validUploadHeaders(payload) {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	var ref sharedevidence.ObjectRef
	if err := json.Unmarshal(response.Body.Bytes(), &ref); err != nil {
		t.Fatalf("decode stored object ref: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(root, created.WorkerSessionID, created.WorkerRunID, "turns", created.TurnID, "report.md"))
	if err != nil {
		t.Fatalf("read redacted object: %v", err)
	}
	if got, want := string(stored), "report includes [REDACTED:worker_token]"; got != want {
		t.Fatalf("stored evidence = %q, want %q", got, want)
	}
	if strings.Contains(string(stored), "test-worker-token") {
		t.Fatal("stored evidence retained the dynamically generated worker token")
	}
	storedDigest := sha256.Sum256(stored)
	if ref.Kind != sharedevidence.KindReport ||
		ref.Path != filepath.Join(root, created.WorkerSessionID, created.WorkerRunID, "turns", created.TurnID, "report.md") ||
		ref.SHA256 != hex.EncodeToString(storedDigest[:]) ||
		ref.Bytes != int64(len(stored)) {
		t.Fatalf("Backend object ref = %+v, want actual redacted StoredObject", ref)
	}
}

func TestEvidenceUploadStoresOnlyVerifiedRawPayload(t *testing.T) {
	handler := newTestHandler(t)
	created := createRun(t, handler)
	path := "/internal/v1/sessions/" + created.WorkerSessionID + "/runs/" + created.WorkerRunID + "/turns/" + created.TurnID + "/evidence/report"

	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader("report payload"))
	for key, value := range validUploadHeaders("report payload") {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid upload status = %d, body = %s", response.Code, response.Body.String())
	}

}

func TestEvidenceUploadAuthenticatesBeforeReadingBody(t *testing.T) {
	handler := newTestHandler(t)
	created := createRun(t, handler)
	validPath := uploadPath(created, "trace")
	tests := []struct {
		name  string
		path  string
		token string
	}{
		{name: "unknown session", path: strings.Replace(validPath, created.WorkerSessionID, "unknown-session", 1), token: "test-worker-token"},
		{name: "wrong token", path: validPath, token: "wrong-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &readerSpy{}
			request := httptest.NewRequest(http.MethodPut, tt.path, nil)
			request.Body = io.NopCloser(spy)
			for key, value := range uploadHeaders(tt.token, "payload") {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s, want 401", response.Code, response.Body.String())
			}
			if spy.readCount() != 0 {
				t.Fatalf("body reads = %d, want zero before authentication", spy.readCount())
			}
		})
	}
}

func TestEvidenceUploadTokenCannotAuthorizeAnotherSessionOrRun(t *testing.T) {
	identifiers := []string{
		"session-a", "run-a", "turn-a", "runtime-a", "trace-a",
		"session-b", "run-b", "turn-b", "runtime-b", "trace-b",
	}
	tokens := []string{"token-a", "token-b"}
	coordinator := service.NewCoordinator(service.Options{
		Materializer: materializerFunc(func(context.Context, string) error { return nil }),
		ObjectStore:  memoryObjectStore{},
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken: func() (string, error) {
			value := tokens[0]
			tokens = tokens[1:]
			return value, nil
		},
	})
	handler := NewHandler(coordinator)
	first := createRun(t, handler)
	second := createRun(t, handler)
	tests := []struct {
		name  string
		path  string
		token string
		want  int
	}{
		{name: "token B against session A", path: uploadPath(first, "trace"), token: "token-b", want: http.StatusUnauthorized},
		{name: "token A against session B", path: uploadPath(second, "trace"), token: "token-a", want: http.StatusUnauthorized},
		{name: "valid token A with run B path", path: strings.Replace(uploadPath(first, "trace"), first.WorkerRunID, second.WorkerRunID, 1), token: "token-a", want: http.StatusForbidden},
		{name: "valid token A with turn B path", path: strings.Replace(uploadPath(first, "trace"), first.TurnID, second.TurnID, 1), token: "token-a", want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader("payload"))
			for key, value := range uploadHeaders(tt.token, "payload") {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.want {
				t.Fatalf("status = %d, body = %s, want %d", response.Code, response.Body.String(), tt.want)
			}
		})
	}
}

func TestEvidenceUploadAcceptsExact32MiBBoundary(t *testing.T) {
	handler := newTestHandler(t)
	created := createRun(t, handler)
	payload := strings.Repeat("x", internalUploadLimit)
	request := httptest.NewRequest(http.MethodPut, uploadPath(created, "patch"), strings.NewReader(payload))
	for key, value := range validUploadHeaders(payload) {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("exact-limit status = %d, body = %s, want 201", response.Code, response.Body.String())
	}
}

func TestEvidenceUploadRejects32MiBPlusOne(t *testing.T) {
	handler := newTestHandler(t)
	created := createRun(t, handler)
	payload := strings.Repeat("x", internalUploadLimit+1)
	request := httptest.NewRequest(http.MethodPut, uploadPath(created, "patch"), strings.NewReader(payload))
	for key, value := range validUploadHeaders(payload) {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit status = %d, body = %s, want 413", response.Code, response.Body.String())
	}
}

func TestEvidenceUploadAcceptsUppercaseChecksumHex(t *testing.T) {
	handler := newTestHandler(t)
	created := createRun(t, handler)
	payload := "checksum payload"
	request := httptest.NewRequest(http.MethodPut, uploadPath(created, "report"), strings.NewReader(payload))
	for key, value := range validUploadHeaders(payload) {
		request.Header.Set(key, value)
	}
	request.Header.Set("X-Content-SHA256", strings.ToUpper(digest(payload)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("uppercase checksum status = %d, body = %s, want 201", response.Code, response.Body.String())
	}
}

func uploadPath(created createdRunResponse, kind string) string {
	return "/internal/v1/sessions/" + created.WorkerSessionID + "/runs/" + created.WorkerRunID + "/turns/" + created.TurnID + "/evidence/" + kind
}

type readerSpy struct {
	mu    sync.Mutex
	reads int
}

func (s *readerSpy) Read([]byte) (int, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return 0, io.EOF
}

func (s *readerSpy) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func validUploadHeaders(payload string) map[string]string {
	return uploadHeaders("test-worker-token", payload)
}

func uploadHeaders(token, payload string) map[string]string {
	return uploadHeadersWithTrees(token, payload, testBeforeTree, testAfterTree)
}

func uploadHeadersWithTrees(token, payload, beforeTree, afterTree string) map[string]string {
	return map[string]string{
		"Authorization":       "Bearer " + token,
		"Content-Type":        "text/plain",
		"X-Content-SHA256":    digest(payload),
		"X-Switchback-Before-Tree": beforeTree,
		"X-Switchback-After-Tree":  afterTree,
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
