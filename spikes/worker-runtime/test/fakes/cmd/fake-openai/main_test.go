package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamBarrierRequiresRegisteredIDAndExactRelease(t *testing.T) {
	server := httptest.NewServer(newFixtureHandler())
	defer server.Close()
	scope, _ := json.Marshal(fixtureScope{Nonce: "abcdefgh", Session: "session", Runtime: "runtime", Generation: 1, Turn: "turn-a", Ordinal: 1})
	key := base64.RawURLEncoding.EncodeToString(scope)
	body, _ := json.Marshal(map[string]any{"model": "fake", "stream": true, "input": "switchback-obsv-stream:" + key})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", bytes.NewReader(body))
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(res.Body)
	if !scanner.Scan() || scanner.Text() != "event: response.created" {
		t.Fatal("first event is not nonterminal")
	}
	if !scanner.Scan() || !strings.Contains(scanner.Text(), `"status":"in_progress"`) {
		t.Fatal("initial response state")
	}
	post := func(path string, input controlInput) int {
		b, _ := json.Marshal(input)
		r, e := server.Client().Post(server.URL+"/fixture/"+path, "application/json", bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		_, _ = io.Copy(io.Discard, r.Body)
		return r.StatusCode
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if got := post("release", controlInput{Key: key, Digest: digest}); got != 409 {
		t.Fatalf("release before header registration = %d", got)
	}
	if got := post("register", controlInput{Key: key, ExpectedID: "real-response-header-id"}); got != 200 {
		t.Fatal(got)
	}
	if got := post("register", controlInput{Key: key, ExpectedID: "replacement-id"}); got != 409 {
		t.Fatal("identity changed")
	}
	r, err := server.Client().Get(server.URL + "/fixture/status")
	if err != nil {
		t.Fatal(err)
	}
	var records []fixtureRecord
	err = json.NewDecoder(r.Body).Decode(&records)
	r.Body.Close()
	if err != nil || len(records) != 1 || !records[0].CompletedAt.IsZero() || records[0].ExpectedID != "real-response-header-id" {
		t.Fatalf("invalid pre-release ledger %v %v", records, err)
	}
	if got := post("release", controlInput{Key: key, Digest: digest}); got != 200 {
		t.Fatal(got)
	}
	completed := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"type":"response.completed"`) {
			completed = true
		}
	}
	if scanner.Err() != nil || !completed {
		t.Fatalf("completion missing: %v", scanner.Err())
	}
	if got := post("release", controlInput{Key: key, Digest: digest}); got != 200 {
		t.Fatal("identical release not idempotent")
	}
	if got := post("release", controlInput{Key: key, Digest: "sha256:" + strings.Repeat("b", 64)}); got != 409 {
		t.Fatal("conflicting release accepted")
	}
	if got := post("result", controlInput{Key: key, ExpectedID: "real-response-header-id", Completed: true}); got != 204 {
		t.Fatal(got)
	}
}

func TestFaultRecordsConnectedCancelledAndRecoveryWithoutHeaders(t *testing.T) {
	f := &faultRegistry{instance: "process-nonce", block: "ingestion"}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "http://test/api/public/ingestion?secret=do-not-record", strings.NewReader("private-body")).WithContext(ctx)
	r.Header.Set("Authorization", "private-credential")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		f.handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("blocked request forwarded") })).ServeHTTP(w, r)
		close(done)
	}()
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		connected := len(f.records) == 1
		f.mu.Unlock()
		if connected {
			break
		}
		select {
		case <-deadline:
			t.Fatal("request never connected")
		default:
		}
	}
	cancel()
	<-done
	if w.Body.Len() != 0 || w.Flushed {
		t.Fatal("fault wrote response")
	}
	control := httptest.NewRequest(http.MethodPost, "/fixture/fault", strings.NewReader(`{"block":"off"}`))
	control.RemoteAddr = "127.0.0.1:1234"
	out := httptest.NewRecorder()
	f.control(out, control)
	if strings.Contains(out.Body.String(), "private") || strings.Contains(out.Body.String(), "secret") {
		t.Fatal("fault ledger leaked request")
	}
	var records []faultRecord
	if err := json.Unmarshal(out.Body.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ConnectedAt.IsZero() || records[0].CancelledAt.Before(records[0].ConnectedAt) || records[0].RecoveredAt.Before(records[0].CancelledAt) {
		t.Fatal("fault lifecycle missing")
	}
}

func TestFaultIngestionClientDisconnectRecordsCancellation(t *testing.T) {
	root, stop := context.WithCancel(context.Background())
	server := httptest.NewUnstartedServer(newFixtureHandler())
	server.Config.BaseContext = func(net.Listener) context.Context { return root }
	server.Start()
	defer func() {
		stop()
		server.Close()
	}()
	client := server.Client()
	client.Timeout = time.Second
	control, err := client.Post(server.URL+"/fixture/fault", "application/json", strings.NewReader(`{"block":"ingestion"}`))
	if err != nil {
		t.Fatal("cannot enable ingestion fault")
	}
	control.Body.Close()
	if control.StatusCode != http.StatusOK {
		t.Fatalf("ingestion fault control status %d", control.StatusCode)
	}

	readRecords := func() []faultRecord {
		t.Helper()
		response, err := client.Get(server.URL + "/fixture/fault")
		if err != nil {
			t.Fatal("cannot read fault status")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || bytes.Contains(body, []byte("private")) || bytes.Contains(body, []byte("secret")) {
			t.Fatal("fault status exposed request data")
		}
		var records []faultRecord
		if response.StatusCode != http.StatusOK || json.Unmarshal(body, &records) != nil {
			t.Fatal("invalid fault status")
		}
		return records
	}
	requestContext, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, server.URL+"/api/public/ingestion?secret=do-not-record", strings.NewReader("private-body"))
	if err != nil {
		t.Fatal("cannot construct ingestion request")
	}
	request.Header.Set("Authorization", "private-credential")
	clientResult := make(chan error, 1)
	go func() {
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
		}
		clientResult <- err
	}()
	connectedDeadline := time.Now().Add(time.Second)
	for {
		if records := readRecords(); len(records) == 1 {
			if records[0].ConnectedAt.IsZero() || !records[0].CancelledAt.IsZero() {
				t.Fatal("invalid connected fault record")
			}
			break
		}
		if time.Now().After(connectedDeadline) {
			t.Fatal("ingestion request did not reach fault handler")
		}
		time.Sleep(time.Millisecond)
	}
	disconnect()
	select {
	case err := <-clientResult:
		if err == nil {
			t.Fatal("fault returned a response instead of client disconnecting")
		}
	case <-time.After(time.Second):
		t.Fatal("client did not disconnect")
	}
	cancelDeadline := time.Now().Add(200 * time.Millisecond)
	for {
		records := readRecords()
		if len(records) != 1 {
			t.Fatal("fault request identity changed")
		}
		if !records[0].CancelledAt.IsZero() {
			if records[0].CancelledAt.Before(records[0].ConnectedAt) || !records[0].RecoveredAt.IsZero() {
				t.Fatal("fault cancellation chronology invalid")
			}
			break
		}
		if time.Now().After(cancelDeadline) {
			t.Fatal("client disconnected but ingestion fault never recorded cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	off, err := client.Post(server.URL+"/fixture/fault", "application/json", strings.NewReader(`{"block":"off"}`))
	if err != nil {
		t.Fatal("cannot disable ingestion fault")
	}
	off.Body.Close()
	if off.StatusCode != http.StatusOK {
		t.Fatalf("fault recovery control status %d", off.StatusCode)
	}
	records := readRecords()
	if len(records) != 1 || records[0].RecoveredAt.IsZero() || records[0].RecoveredAt.Before(records[0].CancelledAt) {
		t.Fatal("fault recovery chronology invalid")
	}
}

func TestFaultIngestionBodyLimitRejectsWithoutClaimingCancellation(t *testing.T) {
	f := &faultRegistry{instance: "process-nonce", block: "ingestion"}
	r := httptest.NewRequest(http.MethodPost, "/api/public/ingestion", strings.NewReader(strings.Repeat("x", maxFaultIngestionBytes+1)))
	w := httptest.NewRecorder()
	f.handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("oversized fault request forwarded")
	})).ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized fault request status %d", w.Code)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.records) != 1 || f.records[0].ConnectedAt.IsZero() || !f.records[0].CancelledAt.IsZero() {
		t.Fatal("oversized request falsely recorded client cancellation")
	}
}

func TestReleaseRejectsNonLoopbackAndUnknownKey(t *testing.T) {
	h := newFixtureHandler()
	for _, remote := range []string{"192.0.2.1:1234", "127.0.0.1:1234"} {
		r := httptest.NewRequest(http.MethodPost, "/fixture/release", strings.NewReader(`{"key":"unknown","evidence_digest":"sha256:`+strings.Repeat("a", 64)+`"}`))
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 && w.Code != 409 {
			t.Fatal(w.Code)
		}
	}
}
