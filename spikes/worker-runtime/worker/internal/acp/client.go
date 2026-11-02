package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/coder/acp-go-sdk"
	"golang.org/x/sys/unix"
)

const workspaceRoot = "/workspace"

type protocolClient struct {
	workspaceRoot   string
	beforeFinalOpen func()
	onSessionUpdate func(sdk.SessionNotification)
}

var _ sdk.Client = (*protocolClient)(nil)

func (*protocolClient) RequestPermission(
	context.Context,
	sdk.RequestPermissionRequest,
) (sdk.RequestPermissionResponse, error) {
	return sdk.RequestPermissionResponse{
		Outcome: sdk.RequestPermissionOutcome{
			Cancelled: &sdk.RequestPermissionOutcomeCancelled{},
		},
	}, nil
}

func (c *protocolClient) SessionUpdate(
	_ context.Context,
	notification sdk.SessionNotification,
) error {
	if c.onSessionUpdate != nil {
		c.onSessionUpdate(notification)
	}
	return nil
}

func (c *protocolClient) ReadTextFile(
	_ context.Context,
	request sdk.ReadTextFileRequest,
) (sdk.ReadTextFileResponse, error) {
	file, err := c.openWorkspaceFile(request.Path, false)
	if err != nil {
		return sdk.ReadTextFileResponse{}, err
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		return sdk.ReadTextFileResponse{}, fmt.Errorf("read workspace file")
	}
	text := string(content)
	if request.Line != nil || request.Limit != nil {
		lines := strings.Split(text, "\n")
		start := 0
		if request.Line != nil && *request.Line > 0 {
			start = min(*request.Line-1, len(lines))
		}
		end := len(lines)
		if request.Limit != nil && *request.Limit > 0 {
			end = min(start+*request.Limit, end)
		}
		text = strings.Join(lines[start:end], "\n")
	}
	return sdk.ReadTextFileResponse{Content: text}, nil
}

func (c *protocolClient) WriteTextFile(
	_ context.Context,
	request sdk.WriteTextFileRequest,
) (sdk.WriteTextFileResponse, error) {
	file, err := c.openWorkspaceFile(request.Path, true)
	if err != nil {
		return sdk.WriteTextFileResponse{}, err
	}
	defer file.Close()
	written, err := file.Write([]byte(request.Content))
	if err != nil || written != len(request.Content) {
		return sdk.WriteTextFileResponse{}, fmt.Errorf("write workspace file")
	}
	return sdk.WriteTextFileResponse{}, nil
}

func (c *protocolClient) openWorkspaceFile(path string, write bool) (*os.File, error) {
	relative, err := workspaceRelativePath(c.root(), path)
	if err != nil {
		return nil, err
	}
	components := strings.Split(relative, string(filepath.Separator))
	if len(components) == 0 {
		return nil, fmt.Errorf("workspace file path is invalid")
	}

	directoryFD, err := unix.Open(
		c.root(),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open workspace root")
	}
	defer func() {
		if directoryFD >= 0 {
			_ = unix.Close(directoryFD)
		}
	}()
	for _, component := range components[:len(components)-1] {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("workspace file path is invalid")
		}
		nextFD, openErr := unix.Openat(
			directoryFD,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
		if openErr != nil {
			return nil, fmt.Errorf("open workspace directory")
		}
		_ = unix.Close(directoryFD)
		directoryFD = nextFD
	}
	finalName := components[len(components)-1]
	if finalName == "" || finalName == "." || finalName == ".." {
		return nil, fmt.Errorf("workspace file path is invalid")
	}
	if c.beforeFinalOpen != nil {
		c.beforeFinalOpen()
	}

	fileFD, err := openWorkspaceFinal(directoryFD, finalName, write)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileFD), "workspace-file")
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, fmt.Errorf("open workspace file")
	}
	return file, nil
}

func openWorkspaceFinal(directoryFD int, name string, write bool) (int, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	fileFD := -1
	var err error
	if write {
		flags = unix.O_WRONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		fileFD, err = unix.Openat(directoryFD, name, flags, 0)
		if errors.Is(err, unix.ENOENT) {
			fileFD, err = unix.Openat(
				directoryFD,
				name,
				flags|unix.O_CREAT|unix.O_EXCL,
				0o644,
			)
		}
	} else {
		fileFD, err = unix.Openat(directoryFD, name, flags, 0)
	}
	if err != nil {
		return -1, fmt.Errorf("open workspace file")
	}

	var status unix.Stat_t
	if err := unix.Fstat(fileFD, &status); err != nil ||
		status.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fileFD)
		return -1, fmt.Errorf("workspace final path must be a regular file")
	}
	if write {
		if err := unix.Ftruncate(fileFD, 0); err != nil {
			_ = unix.Close(fileFD)
			return -1, fmt.Errorf("truncate workspace file")
		}
	}
	return fileFD, nil
}

func (*protocolClient) CreateTerminal(
	context.Context,
	sdk.CreateTerminalRequest,
) (sdk.CreateTerminalResponse, error) {
	return sdk.CreateTerminalResponse{}, unsupportedTerminalError()
}

func (*protocolClient) KillTerminal(
	context.Context,
	sdk.KillTerminalRequest,
) (sdk.KillTerminalResponse, error) {
	return sdk.KillTerminalResponse{}, unsupportedTerminalError()
}

func (*protocolClient) TerminalOutput(
	context.Context,
	sdk.TerminalOutputRequest,
) (sdk.TerminalOutputResponse, error) {
	return sdk.TerminalOutputResponse{}, unsupportedTerminalError()
}

func (*protocolClient) ReleaseTerminal(
	context.Context,
	sdk.ReleaseTerminalRequest,
) (sdk.ReleaseTerminalResponse, error) {
	return sdk.ReleaseTerminalResponse{}, unsupportedTerminalError()
}

func (*protocolClient) WaitForTerminalExit(
	context.Context,
	sdk.WaitForTerminalExitRequest,
) (sdk.WaitForTerminalExitResponse, error) {
	return sdk.WaitForTerminalExitResponse{}, unsupportedTerminalError()
}

func validateWorkspacePath(path string) error {
	return validatePathBelow(workspaceRoot, path)
}

func (c *protocolClient) root() string {
	if c.workspaceRoot == "" {
		return workspaceRoot
	}
	return c.workspaceRoot
}

func validatePathBelow(root, path string) error {
	_, err := workspaceRelativePath(root, path)
	return err
}

func workspaceRelativePath(root, path string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", fmt.Errorf("workspace root must be a cleaned absolute path")
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("workspace file path must be a cleaned absolute path")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil ||
		relative == "." ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return "", fmt.Errorf("workspace file path must be below %s", root)
	}
	return relative, nil
}

func unsupportedTerminalError() error {
	return fmt.Errorf("ACP terminal operations are unsupported")
}
