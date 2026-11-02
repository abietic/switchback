package acpclient_test

import (
	"context"
	"fmt"
	"github.com/abietic/switchback/internal/workerruntime/adapter/acpclient"
	"github.com/abietic/switchback/internal/workerruntime/application"
	sdk "github.com/coder/acp-go-sdk"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newPeerAdapter(t *testing.T, root, scenario string, trace io.Writer) (*acpclient.Adapter, context.Context) {
	t.Helper()
	return newPeerWithAuth(t, root, scenario, trace, func(token string) (sdk.AuthenticateRequest, error) {
		return sdk.AuthenticateRequest{MethodId: "fixture-auth", Meta: map[string]any{"token": token}}, nil
	})
}
func newPeerWithAuth(t *testing.T, root, scenario string, trace io.Writer, builder func(string) (sdk.AuthenticateRequest, error)) (*acpclient.Adapter, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	a, err := acpclient.New(acpclient.Options{Command: func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCorePeerProcess$")
		cmd.Env = append(os.Environ(), "SWITCHBACK_CORE_PEER=1", "SWITCHBACK_CORE_SCENARIO="+scenario, "SWITCHBACK_CORE_ROOT="+root)
		return cmd
	}, ProcessName: "fixture-acp", WorkspaceRoot: root, Trace: trace, Authentication: acpclient.Authentication{MethodID: "fixture-auth", Request: builder}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if err := a.Close(closeCtx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return a, ctx
}
func initializePeer(t *testing.T, a *acpclient.Adapter, ctx context.Context) {
	t.Helper()
	caps, err := a.Initialize(ctx, application.AgentInitializeRequest{WorkerSessionID: "fixture-worker", RuntimeID: "fixture-runtime", Generation: 1, ProtocolVersion: "1"})
	if err != nil || caps.ProtocolVersion != "1" || !caps.LoadSession {
		t.Fatalf("initialize: %+v %v", caps, err)
	}
}
func openPeer(t *testing.T, a *acpclient.Adapter, ctx context.Context, root string) {
	t.Helper()
	initializePeer(t, a, ctx)
	if err := a.Authenticate(ctx, "fixture-proxy-token"); err != nil {
		t.Fatal(err)
	}
	id, err := a.OpenSession(ctx, application.AgentSessionRequest{WorkerSessionID: "fixture-worker", WorkingCopyPath: root})
	if err != nil || id != "fixture-session" {
		t.Fatalf("open: %q %v", id, err)
	}
}
func promptPeer(t *testing.T, a *acpclient.Adapter, ctx context.Context) []application.AgentUpdate {
	t.Helper()
	ch, err := a.Prompt(ctx, application.AgentPromptRequest{AgentSessionID: "fixture-session", AgentTurnID: "fixture-turn", Content: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	var updates []application.AgentUpdate
	for u := range ch {
		updates = append(updates, u)
	}
	return updates
}
func promptRequest() application.AgentPromptRequest {
	return application.AgentPromptRequest{AgentSessionID: "fixture-session", AgentTurnID: "fixture-turn", Content: "normal"}
}
func TestCorePeerCompletesTypedPrompt(t *testing.T) {
	root := t.TempDir()
	a, ctx := newPeerAdapter(t, root, "normal", io.Discard)
	openPeer(t, a, ctx, root)
	updates := promptPeer(t, a, ctx)
	var kinds []string
	for _, u := range updates {
		kinds = append(kinds, u.Kind)
	}
	if strings.Join(kinds, ",") != "agent_message_chunk,terminal" || string(updates[1].Payload) != `{"stop_reason":"end_turn"}` {
		t.Fatalf("updates: %+v", updates)
	}
}
func TestCoreAuthenticationPersistsOnlyRedactedToken(t *testing.T) {
	root := t.TempDir()
	var trace traceBuffer
	a, ctx := newPeerAdapter(t, root, "normal", &trace)
	openPeer(t, a, ctx, root)
	u := promptPeer(t, a, ctx)
	if !strings.Contains(string(u[0].Payload), "token-ok") {
		t.Fatalf("peer did not confirm original token: %+v", u)
	}
	s := string(trace.Bytes())
	if strings.Contains(s, "fixture-proxy-token") || !strings.Contains(s, "[REDACTED:litellm_proxy_token]") {
		t.Fatalf("Trace redaction: %s", s)
	}
}
func TestCoreInvalidAuthenticationBuilderNeverReachesPeer(t *testing.T) {
	for _, name := range []string{"mismatch", "error"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			var trace traceBuffer
			a, ctx := newPeerWithAuth(t, root, "normal", &trace, func(token string) (sdk.AuthenticateRequest, error) {
				if name == "error" {
					return sdk.AuthenticateRequest{}, fmt.Errorf("builder-secret-%s", token)
				}
				return sdk.AuthenticateRequest{MethodId: "other"}, nil
			})
			initializePeer(t, a, ctx)
			err := a.Authenticate(ctx, "fixture-proxy-token")
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "fixture-proxy-token") {
				t.Fatalf("auth error: %v", err)
			}
			if err := a.Close(ctx); err != nil {
				t.Fatal(err)
			}
			assertNoAuthTraffic(t, root)
			if strings.Contains(string(trace.Bytes()), "fixture-proxy-token") {
				t.Fatal("token leaked")
			}
		})
	}
}
func assertNoAuthTraffic(t *testing.T, root string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "peer-evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "authenticate") {
		t.Fatalf("unexpected authenticate traffic: %s", b)
	}
}
func TestCoreTraceFailurePreventsAuthenticationDelivery(t *testing.T) {
	root := t.TempDir()
	trace := &traceBuffer{failAuth: true}
	a, ctx := newPeerAdapter(t, root, "normal", trace)
	initializePeer(t, a, ctx)
	if err := a.Authenticate(ctx, "fixture-proxy-token"); err == nil {
		t.Fatal("authentication survived Trace failure")
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoAuthTraffic(t, root)
	if strings.Contains(string(trace.Bytes()), "fixture-proxy-token") {
		t.Fatal("token persisted")
	}
}
func TestCoreCapabilityMismatchFencesLaterCalls(t *testing.T) {
	for _, scenario := range []string{"no-auth", "protocol-two"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			a, ctx := newPeerAdapter(t, root, scenario, io.Discard)
			_, err := a.Initialize(ctx, application.AgentInitializeRequest{WorkerSessionID: "fixture-worker", RuntimeID: "fixture-runtime", Generation: 1, ProtocolVersion: "1"})
			if err == nil {
				t.Fatal("mismatch initialized")
			}
			if a.Authenticate(ctx, "fixture-proxy-token") == nil {
				t.Fatal("authenticated after mismatch")
			}
			if _, err := a.OpenSession(ctx, application.AgentSessionRequest{WorkerSessionID: "fixture-worker", WorkingCopyPath: root}); err == nil {
				t.Fatal("opened after mismatch")
			}
		})
	}
}
func TestCoreCloseIsIdempotentAndFencesPublicCalls(t *testing.T) {
	root := t.TempDir()
	a, ctx := newPeerAdapter(t, root, "normal", io.Discard)
	openPeer(t, a, ctx, root)
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Initialize(ctx, application.AgentInitializeRequest{WorkerSessionID: "fixture-worker", RuntimeID: "fixture-runtime", Generation: 1, ProtocolVersion: "1"}); err == nil {
		t.Fatal("closed initialized")
	}
	if _, err := a.Prompt(ctx, promptRequest()); err == nil {
		t.Fatal("closed prompted")
	}
}
func TestCoreNilClassifierDoesNotImposeCodexPolicy(t *testing.T) {
	root := t.TempDir()
	a, ctx := newPeerAdapter(t, root, "system-error", io.Discard)
	openPeer(t, a, ctx, root)
	u := promptPeer(t, a, ctx)
	if len(u) != 2 || u[0].Kind != "session_info_update" || !strings.Contains(string(u[0].Payload), `"threadStatus":{"type":"systemError"}`) || u[1].Kind != "terminal" || string(u[1].Payload) != `{"stop_reason":"end_turn"}` {
		t.Fatalf("updates: %+v", u)
	}
}
