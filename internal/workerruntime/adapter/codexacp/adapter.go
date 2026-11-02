package codexacp

import (
	"context"
	"fmt"
	"github.com/abietic/switchback/internal/workerruntime/adapter/acpclient"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"io"
	"os/exec"
)

type processOptions struct {
	command       func() *exec.Cmd
	workspaceRoot string
	trace         io.Writer
}
type Adapter struct{ client *acpclient.Adapter }

var _ application.AgentAdapter = (*Adapter)(nil)

func New(trace io.Writer) (*Adapter, error) {
	return newAdapter(processOptions{command: fixedCommand, workspaceRoot: workspaceRoot, trace: trace})
}
func newAdapter(options processOptions) (*Adapter, error) {
	if options.command == nil || options.trace == nil || options.workspaceRoot == "" {
		return nil, fmt.Errorf("CodexAdapter process, workspace and Trace are required")
	}
	client, err := acpclient.New(acpclient.Options{Command: options.command, ProcessName: "codex-acp", WorkspaceRoot: options.workspaceRoot, Trace: options.trace, Authentication: gatewayAuthentication(), IsSystemError: isCodexSystemError})
	if err != nil {
		return nil, fmt.Errorf("CodexAdapter workspace root is invalid")
	}
	return &Adapter{client: client}, nil
}
func (a *Adapter) Initialize(ctx context.Context, r application.AgentInitializeRequest) (application.AgentCapabilities, error) {
	return a.client.Initialize(ctx, r)
}
func (a *Adapter) Authenticate(ctx context.Context, token string) error {
	return a.client.Authenticate(ctx, token)
}
func (a *Adapter) OpenSession(ctx context.Context, r application.AgentSessionRequest) (string, error) {
	return a.client.OpenSession(ctx, r)
}
func (a *Adapter) Prompt(ctx context.Context, r application.AgentPromptRequest) (<-chan application.AgentUpdate, error) {
	return a.client.Prompt(ctx, r)
}
func (a *Adapter) Cancel(ctx context.Context, sessionID, turnID string) error {
	return a.client.Cancel(ctx, sessionID, turnID)
}
func (a *Adapter) Close(ctx context.Context) error { return a.client.Close(ctx) }
