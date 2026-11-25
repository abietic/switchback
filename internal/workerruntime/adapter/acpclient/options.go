package acpclient

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	sdk "github.com/coder/acp-go-sdk"
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
	// ExtensionFailureSession returns the exact session named by a trusted
	// agent's terminal failure notification, or an empty string for other input.
	ExtensionFailureSession func(method string, params json.RawMessage) string
}

func New(options Options) (*Adapter, error) {
	adapter, err := newAdapter(options)
	if err != nil {
		return nil, err
	}
	auth := options.Authentication
	if auth.MethodID == "" || strings.TrimSpace(auth.MethodID) != auth.MethodID || strings.ContainsAny(auth.MethodID, "\r\n\x00") || auth.Request == nil {
		return nil, fmt.Errorf("ACP authentication policy is required")
	}
	return adapter, nil
}

// NewPreconfigured supports agents whose trusted launcher supplies the model
// proxy token and whose ACP handshake declares no authentication methods.
func NewPreconfigured(options Options, proxyToken string, command func(string) *exec.Cmd) (*Adapter, error) {
	auth := options.Authentication
	if options.Command != nil || command == nil || auth.MethodID != "" || auth.Request != nil || auth.Capabilities.Terminal || len(auth.Capabilities.Meta) != 0 {
		return nil, fmt.Errorf("ACP preconfigured authentication policy is invalid")
	}
	if err := validateProxyToken(proxyToken); err != nil {
		return nil, err
	}
	options.Command = func() *exec.Cmd { return command(proxyToken) }
	adapter, err := newAdapter(options)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(proxyToken))
	adapter.preconfiguredProxyDigest = &digest
	adapter.recorder.setProxyToken(proxyToken)
	return adapter, nil
}

func newAdapter(options Options) (*Adapter, error) {
	if options.Command == nil || options.Trace == nil || options.WorkspaceRoot == "" || options.ProcessName == "" {
		return nil, fmt.Errorf("ACP adapter process, workspace and Trace are required")
	}
	if _, err := workspaceRelativePath(options.WorkspaceRoot, options.WorkspaceRoot+"/.switchback-check"); err != nil {
		return nil, fmt.Errorf("ACP adapter workspace root is invalid")
	}
	return &Adapter{options: options, state: stateNew, recorder: newTraceRecorder(options.Trace)}, nil
}
