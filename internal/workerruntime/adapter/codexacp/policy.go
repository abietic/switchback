package codexacp

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/abietic/switchback/internal/workerruntime/adapter/acpclient"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	sdk "github.com/coder/acp-go-sdk"
)

const (
	codexACPBinary = "/opt/switchback/node_modules/.bin/codex-acp"
	workspaceRoot  = "/workspace"
	proxyBaseURL   = "http://127.0.0.1:4000/v1"
	providerName   = "Switchback LiteLLM"
)

var fixedAgentEnvironment = []string{
	"APP_SERVER_LOGS=/run/switchback-runtime/app-server-logs",
	`CODEX_CONFIG={"model":"switchback-codex","approval_policy":"never","sandbox_mode":"danger-full-access","web_search":"disabled"}`,
	"CODEX_HOME=/var/lib/switchback/agent-state/codex",
	"CODEX_PATH=/opt/switchback/node_modules/.bin/codex",
	"CODEX_SQLITE_HOME=/run/switchback-runtime/codex-sqlite",
	"HOME=/var/lib/switchback/agent-state",
	"INITIAL_AGENT_MODE=agent-full-access",
	"NO_BROWSER=1",
	"PATH=/usr/local/bin:/usr/bin:/bin",
}

func fixedCommand() *exec.Cmd {
	command := exec.Command(codexACPBinary)
	command.Args = []string{codexACPBinary}
	command.Env = append([]string(nil), fixedAgentEnvironment...)
	if os.Geteuid() == 0 {
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: runtimesecret.AgentUID, Gid: runtimesecret.AgentUID, Groups: []uint32{},
		}}
	}
	return command
}

func isCodexSystemError(notification sdk.SessionNotification) bool {
	info := notification.Update.SessionInfoUpdate
	if info == nil {
		return false
	}
	codex, ok := info.Meta["codex"].(map[string]any)
	if !ok {
		return false
	}
	status, ok := codex["threadStatus"].(map[string]any)
	if !ok {
		return false
	}
	value, ok := status["type"].(string)
	return ok && value == "systemError"
}

func gatewayAuthentication() acpclient.Authentication {
	return acpclient.Authentication{MethodID: "gateway", Capabilities: sdk.AuthCapabilities{Meta: map[string]any{"gateway": true}}, Request: func(token string) (sdk.AuthenticateRequest, error) {
		return sdk.AuthenticateRequest{MethodId: "gateway", Meta: map[string]any{"gateway": map[string]any{"baseUrl": proxyBaseURL, "providerName": providerName, "headers": map[string]string{"Authorization": "Bearer " + token}}}}, nil
	}}
}
