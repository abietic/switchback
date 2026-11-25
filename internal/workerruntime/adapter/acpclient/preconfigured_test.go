package acpclient_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/acpclient"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestCorePreconfiguredProxyBinding(t *testing.T) {
	const token = `fixture-proxy-<token>`
	for _, tc := range []struct {
		name, scenario, provided string
		initialize, open         bool
	}{
		{name: "matching", scenario: "no-auth", provided: token, initialize: true, open: true},
		{name: "mismatch", scenario: "no-auth", provided: "other-token", initialize: true},
		{name: "declared-auth", scenario: "normal", provided: token},
		{name: "leaked-at-initialize", scenario: "preconfigured-leak", provided: token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			root := t.TempDir()
			var trace traceBuffer
			adapter, err := acpclient.NewPreconfigured(acpclient.Options{
				ProcessName: "fixture-acp", WorkspaceRoot: root, Trace: &trace,
			}, token, func(boundToken string) *exec.Cmd {
				if token != boundToken {
					t.Fatal("launcher did not receive the bound token")
				}
				command := exec.Command(os.Args[0], "-test.run=^TestCorePeerProcess$")
				command.Env = []string{"SWITCHBACK_CORE_PEER=1", "SWITCHBACK_CORE_ROOT=" + root,
					"SWITCHBACK_CORE_SCENARIO=" + tc.scenario, "SWITCHBACK_CORE_PROXY=" + boundToken}
				return command
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := adapter.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			_, err = adapter.Initialize(ctx, application.AgentInitializeRequest{
				WorkerSessionID: "fixture-worker", RuntimeID: "fixture-runtime", Generation: 1, ProtocolVersion: "1",
			})
			if (err == nil) != tc.initialize {
				t.Fatalf("initialize: %v", err)
			}
			request := application.AgentSessionRequest{WorkerSessionID: "fixture-worker", WorkingCopyPath: root}
			if _, err := adapter.OpenSession(ctx, request); err == nil {
				t.Fatal("opened session before validating the token")
			}
			if err := adapter.Authenticate(ctx, tc.provided); (err == nil) != tc.open {
				t.Fatalf("authenticate: %v", err)
			}
			if !tc.open && adapter.Authenticate(ctx, token) == nil {
				t.Fatal("invalid handshake or token mismatch did not fence the process")
			}
			if _, err := adapter.OpenSession(ctx, request); (err == nil) != tc.open {
				t.Fatalf("open: %v", err)
			}
			assertNoAuthTraffic(t, root)
			content := string(trace.Bytes())
			if strings.Contains(content, "fixture-proxy-") || (tc.scenario == "preconfigured-leak" && !strings.Contains(content, "[REDACTED:litellm_proxy_token]")) {
				t.Fatal("launcher token was not redacted in ACP Trace")
			}
		})
	}
}
