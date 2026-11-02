package acpclient

import (
	"fmt"
	sdk "github.com/coder/acp-go-sdk"
	"io"
	"os/exec"
	"strings"
)

// Authentication is selected by trusted adapter composition, not Task input.
type Authentication struct {
	MethodID     string
	Capabilities sdk.AuthCapabilities
	Request      func(proxyToken string) (sdk.AuthenticateRequest, error)
}

// Options is internal Go composition. Callers own immutable policy values.
type Options struct {
	Command        func() *exec.Cmd
	ProcessName    string
	WorkspaceRoot  string
	Trace          io.Writer
	Authentication Authentication
	IsSystemError  func(sdk.SessionNotification) bool
}

func New(options Options) (*Adapter, error) {
	if options.Command == nil || options.Trace == nil || options.WorkspaceRoot == "" || options.ProcessName == "" {
		return nil, fmt.Errorf("ACP adapter process, workspace and Trace are required")
	}
	if _, err := workspaceRelativePath(options.WorkspaceRoot, options.WorkspaceRoot+"/.switchback-check"); err != nil {
		return nil, fmt.Errorf("ACP adapter workspace root is invalid")
	}
	auth := options.Authentication
	if auth.MethodID == "" || strings.TrimSpace(auth.MethodID) != auth.MethodID || strings.ContainsAny(auth.MethodID, "\r\n\x00") || auth.Request == nil {
		return nil, fmt.Errorf("ACP authentication policy is required")
	}
	return &Adapter{options: options, state: stateNew, recorder: newTraceRecorder(options.Trace)}, nil
}
