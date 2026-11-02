package upload

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
	workerevidence "switchback.local/spikes/worker-runtime/worker/internal/evidence"
)

func TestFinalizeCompletesOnlyWithFourBackendObjectRefs(t *testing.T) {
	t.Parallel()

	bundle := testBundle()
	var requestMu sync.Mutex
	var requestedKinds []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		kind := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		requestMu.Lock()
		requestedKinds = append(requestedKinds, kind)
		requestMu.Unlock()
		assertUploadRequest(t, request, bundle, kind)
		writeObjectRef(t, writer, sharedevidence.ObjectRef{
			Kind:   kind,
			Path:   "/objects/turn-1/" + kind,
			SHA256: strings.Repeat(kindDigestCharacter(kind), 64),
			Bytes:  int64(100 + len(kind)),
		})
	}))
	defer server.Close()

	sink := &recordingTerminalSink{}
	client, err := NewClient(server.URL, "worker-token", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Finalize(context.Background(), bundle, sink); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}

	if sink.completedCalls != 1 {
		t.Fatalf("TurnCompleted calls = %d, want 1", sink.completedCalls)
	}
	if sink.failedCalls != 0 {
		t.Fatalf("TurnFailed calls = %d, want 0", sink.failedCalls)
	}
	if len(sink.completedRefs) != 4 {
		t.Fatalf("completed refs = %d, want 4", len(sink.completedRefs))
	}
	for index, ref := range sink.completedRefs {
		if ref.Kind != bundle.Objects[index].Kind {
			t.Fatalf("completed ref %d kind = %q, want %q", index, ref.Kind, bundle.Objects[index].Kind)
		}
		if ref.Path != "/objects/turn-1/"+ref.Kind {
			t.Fatalf("completed ref %d path = %q", index, ref.Path)
		}
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if strings.Join(requestedKinds, ",") != "report,raw-acp,trace,patch" {
		t.Fatalf("upload order = %v", requestedKinds)
	}
}

func TestFinalizePartialUploadFailureEmitsFailedAndNeverCompleted(t *testing.T) {
	t.Parallel()

	bundle := testBundle()
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		kind := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		if kind == sharedevidence.KindRawACP {
			http.Error(writer, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
		writeObjectRef(t, writer, sharedevidence.ObjectRef{
			Kind:   kind,
			Path:   "/objects/turn-1/" + kind,
			SHA256: strings.Repeat("a", 64),
			Bytes:  100,
		})
	}))
	defer server.Close()

	sink := &recordingTerminalSink{}
	client, err := NewClient(server.URL, "worker-token", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	err = client.Finalize(context.Background(), bundle, sink)
	if err == nil || !strings.Contains(err.Error(), "upload raw-acp") {
		t.Fatalf("Finalize() error = %v, want raw-acp upload failure", err)
	}
	if requestCount != 2 {
		t.Fatalf("upload requests = %d, want stop after second request", requestCount)
	}
	if sink.completedCalls != 0 {
		t.Fatalf("TurnCompleted calls = %d, want 0", sink.completedCalls)
	}
	if sink.failedCalls != 1 {
		t.Fatalf("TurnFailed calls = %d, want 1", sink.failedCalls)
	}
	if sink.failureCode != FailureCodeEvidenceUpload {
		t.Fatalf("failure code = %q, want %q", sink.failureCode, FailureCodeEvidenceUpload)
	}
	if len(sink.partialRefs) != 1 || sink.partialRefs[0].Kind != sharedevidence.KindReport {
		t.Fatalf("partial refs = %#v, want only verified report", sink.partialRefs)
	}
}

func TestFinalizeRejectsMismatchedBackendRefAndFailsTurn(t *testing.T) {
	t.Parallel()

	bundle := testBundle()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		kind := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		returnedKind := kind
		if kind == sharedevidence.KindTrace {
			returnedKind = sharedevidence.KindPatch
		}
		writeObjectRef(t, writer, sharedevidence.ObjectRef{
			Kind:   returnedKind,
			Path:   "/objects/turn-1/" + returnedKind,
			SHA256: strings.Repeat("b", 64),
			Bytes:  101,
		})
	}))
	defer server.Close()

	sink := &recordingTerminalSink{}
	client, err := NewClient(server.URL, "worker-token", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	err = client.Finalize(context.Background(), bundle, sink)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("Finalize() error = %v, want kind mismatch", err)
	}
	if sink.completedCalls != 0 || sink.failedCalls != 1 {
		t.Fatalf("terminal calls = completed:%d failed:%d, want 0/1", sink.completedCalls, sink.failedCalls)
	}
	if len(sink.partialRefs) != 2 {
		t.Fatalf("partial refs = %d, want two refs before mismatched trace", len(sink.partialRefs))
	}
}

func testBundle() workerevidence.Bundle {
	identity := sharedevidence.TurnIdentity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 3,
		AgentSessionID:    "agent-1",
		AgentTurnID:       "turn-1",
		TraceID:           "trace-1",
	}
	kinds := []string{
		sharedevidence.KindReport,
		sharedevidence.KindRawACP,
		sharedevidence.KindTrace,
		sharedevidence.KindPatch,
	}
	objects := make([]workerevidence.Object, 0, len(kinds))
	for _, kind := range kinds {
		payload := []byte("payload-" + kind)
		objects = append(objects, workerevidence.Object{
			Kind:        kind,
			ContentType: "application/octet-stream",
			Payload:     payload,
			SHA256:      contentSHA256(payload),
			Bytes:       int64(len(payload)),
		})
	}
	return workerevidence.Bundle{
		Identity:   identity,
		BeforeTree: strings.Repeat("a", 40),
		AfterTree:  strings.Repeat("b", 40),
		Objects:    objects,
	}
}

func assertUploadRequest(t *testing.T, request *http.Request, bundle workerevidence.Bundle, kind string) {
	t.Helper()
	wantPath := fmt.Sprintf(
		"/internal/v1/sessions/%s/runs/%s/turns/%s/evidence/%s",
		bundle.Identity.WorkerSessionID,
		bundle.Identity.WorkerRunID,
		bundle.Identity.AgentTurnID,
		kind,
	)
	if request.Method != http.MethodPut || request.URL.Path != wantPath {
		t.Errorf("request = %s %s, want PUT %s", request.Method, request.URL.Path, wantPath)
	}
	if request.Header.Get("Authorization") != "Bearer worker-token" {
		t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
	}
	if request.Header.Get(HeaderBeforeTree) != bundle.BeforeTree ||
		request.Header.Get(HeaderAfterTree) != bundle.AfterTree {
		t.Errorf("tree headers = %q/%q", request.Header.Get(HeaderBeforeTree), request.Header.Get(HeaderAfterTree))
	}
	for _, object := range bundle.Objects {
		if object.Kind != kind {
			continue
		}
		if request.Header.Get("X-Content-SHA256") != object.SHA256 {
			t.Errorf("checksum header = %q, want %q", request.Header.Get("X-Content-SHA256"), object.SHA256)
		}
		if request.ContentLength != object.Bytes {
			t.Errorf("Content-Length = %d, want %d", request.ContentLength, object.Bytes)
		}
		return
	}
	t.Errorf("unknown requested kind %q", kind)
}

func writeObjectRef(t *testing.T, writer http.ResponseWriter, ref sharedevidence.ObjectRef) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(writer).Encode(ref); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func kindDigestCharacter(kind string) string {
	switch kind {
	case sharedevidence.KindReport:
		return "a"
	case sharedevidence.KindRawACP:
		return "b"
	case sharedevidence.KindTrace:
		return "c"
	default:
		return "d"
	}
}

type recordingTerminalSink struct {
	completedCalls int
	failedCalls    int
	completedRefs  []sharedevidence.ObjectRef
	partialRefs    []sharedevidence.ObjectRef
	failureCode    string
}

func (s *recordingTerminalSink) TurnCompleted(
	_ context.Context,
	_ sharedevidence.TurnIdentity,
	refs []sharedevidence.ObjectRef,
) error {
	s.completedCalls++
	s.completedRefs = append([]sharedevidence.ObjectRef(nil), refs...)
	return nil
}

func (s *recordingTerminalSink) TurnFailed(
	_ context.Context,
	_ sharedevidence.TurnIdentity,
	code string,
	_ string,
	refs []sharedevidence.ObjectRef,
) error {
	s.failedCalls++
	s.failureCode = code
	s.partialRefs = append([]sharedevidence.ObjectRef(nil), refs...)
	return nil
}
