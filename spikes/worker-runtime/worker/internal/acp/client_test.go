package acp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/coder/acp-go-sdk"
)

func TestProtocolClientReadsAndWritesInsideWorkspace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	client := &protocolClient{workspaceRoot: root}
	path := filepath.Join(root, "notes.txt")
	if _, err := client.WriteTextFile(context.Background(), sdk.WriteTextFileRequest{
		Path:    path,
		Content: "line one\nline two\nline three",
	}); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	line := 2
	limit := 1
	response, err := client.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{
		Path:  path,
		Line:  &line,
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if response.Content != "line two" {
		t.Fatalf("read content = %q, want line two", response.Content)
	}
	if _, err := client.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{
		Path: filepath.Join(filepath.Dir(root), "outside.txt"),
	}); err == nil {
		t.Fatal("outside read unexpectedly succeeded")
	}
}

func TestProtocolClientRejectsSymlinkEscapesAndNonRegularFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "ancestor-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "final-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	client := &protocolClient{workspaceRoot: root}

	readPaths := []string{
		filepath.Join(root, "ancestor-link", "secret.txt"),
		filepath.Join(root, "final-link"),
		filepath.Join(root, "directory"),
	}
	for _, path := range readPaths {
		if _, err := client.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{
			Path: path,
		}); err == nil {
			t.Fatalf("ReadTextFile(%q) unexpectedly succeeded", path)
		}
	}
	writePaths := []string{
		filepath.Join(root, "ancestor-link", "created.txt"),
		filepath.Join(root, "final-link"),
		filepath.Join(root, "directory"),
	}
	for _, path := range writePaths {
		if _, err := client.WriteTextFile(context.Background(), sdk.WriteTextFileRequest{
			Path:    path,
			Content: "overwrite-attempt",
		}); err == nil {
			t.Fatalf("WriteTextFile(%q) unexpectedly succeeded", path)
		}
	}
	content, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "outside-secret" {
		t.Fatalf("outside file = %q, want unchanged", content)
	}
	if _, err := os.Stat(filepath.Join(outside, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("ancestor symlink write created outside file: %v", err)
	}
}

func TestProtocolClientReadUsesOpenedDirectoryAfterPathReplacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	originalDirectory := filepath.Join(root, "safe")
	movedDirectory := filepath.Join(root, "safe-opened")
	outside := t.TempDir()
	if err := os.Mkdir(originalDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originalDirectory, "value.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "value.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	reached := make(chan struct{})
	release := make(chan struct{})
	client := &protocolClient{
		workspaceRoot: root,
		beforeFinalOpen: func() {
			close(reached)
			<-release
		},
	}
	result := make(chan struct {
		content string
		err     error
	}, 1)
	go func() {
		response, err := client.ReadTextFile(context.Background(), sdk.ReadTextFileRequest{
			Path: filepath.Join(originalDirectory, "value.txt"),
		})
		result <- struct {
			content string
			err     error
		}{content: response.Content, err: err}
	}()
	<-reached
	if err := os.Rename(originalDirectory, movedDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, originalDirectory); err != nil {
		t.Fatal(err)
	}
	close(release)

	got := <-result
	if got.err != nil {
		t.Fatalf("ReadTextFile() error = %v", got.err)
	}
	if got.content != "inside" {
		t.Fatalf("ReadTextFile() content = %q, want opened-directory content", got.content)
	}
}

func TestProtocolClientWriteUsesOpenedDirectoryAfterPathReplacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	originalDirectory := filepath.Join(root, "safe")
	movedDirectory := filepath.Join(root, "safe-opened")
	outside := t.TempDir()
	if err := os.Mkdir(originalDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originalDirectory, "value.txt"), []byte("inside-old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "value.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	reached := make(chan struct{})
	release := make(chan struct{})
	client := &protocolClient{
		workspaceRoot: root,
		beforeFinalOpen: func() {
			close(reached)
			<-release
		},
	}
	result := make(chan error, 1)
	go func() {
		_, err := client.WriteTextFile(context.Background(), sdk.WriteTextFileRequest{
			Path:    filepath.Join(originalDirectory, "value.txt"),
			Content: "inside-new",
		})
		result <- err
	}()
	<-reached
	if err := os.Rename(originalDirectory, movedDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, originalDirectory); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("WriteTextFile() error = %v", err)
	}

	insideContent, err := os.ReadFile(filepath.Join(movedDirectory, "value.txt"))
	if err != nil {
		t.Fatal(err)
	}
	outsideContent, err := os.ReadFile(filepath.Join(outside, "value.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(insideContent) != "inside-new" || string(outsideContent) != "outside" {
		t.Fatalf("contents = inside:%q outside:%q", insideContent, outsideContent)
	}
}

func TestProtocolClientFailsClosedForPermissionsAndTerminals(t *testing.T) {
	t.Parallel()

	client := &protocolClient{}
	permission, err := client.RequestPermission(context.Background(), sdk.RequestPermissionRequest{})
	if err != nil {
		t.Fatalf("request permission: %v", err)
	}
	if permission.Outcome.Cancelled == nil || permission.Outcome.Selected != nil {
		t.Fatalf("permission outcome = %+v, want cancelled", permission.Outcome)
	}

	terminalCalls := []struct {
		name string
		call func() error
	}{
		{
			name: "create",
			call: func() error {
				_, callErr := client.CreateTerminal(context.Background(), sdk.CreateTerminalRequest{})
				return callErr
			},
		},
		{
			name: "kill",
			call: func() error {
				_, callErr := client.KillTerminal(context.Background(), sdk.KillTerminalRequest{})
				return callErr
			},
		},
		{
			name: "output",
			call: func() error {
				_, callErr := client.TerminalOutput(context.Background(), sdk.TerminalOutputRequest{})
				return callErr
			},
		},
		{
			name: "release",
			call: func() error {
				_, callErr := client.ReleaseTerminal(context.Background(), sdk.ReleaseTerminalRequest{})
				return callErr
			},
		},
		{
			name: "wait",
			call: func() error {
				_, callErr := client.WaitForTerminalExit(context.Background(), sdk.WaitForTerminalExitRequest{})
				return callErr
			},
		},
	}
	for _, terminalCall := range terminalCalls {
		if err := terminalCall.call(); err == nil {
			t.Fatalf("%s terminal call unexpectedly succeeded", terminalCall.name)
		}
	}
}

func TestValidateWorkspacePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path  string
		valid bool
	}{
		{path: "/workspace/file.txt", valid: true},
		{path: "/workspace/subdir/.hidden", valid: true},
		{path: "/workspace", valid: false},
		{path: "/workspace-copy/file.txt", valid: false},
		{path: "/other/file.txt", valid: false},
		{path: "relative.txt", valid: false},
		{path: "/workspace/subdir/../file.txt", valid: false},
		{path: "/workspace/../outside.txt", valid: false},
		{path: "/workspace//file.txt", valid: false},
	}
	for _, test := range tests {
		err := validateWorkspacePath(test.path)
		if test.valid && err != nil {
			t.Errorf("validate %q: %v", test.path, err)
		}
		if !test.valid && err == nil {
			t.Errorf("validate %q unexpectedly succeeded", test.path)
		}
	}
}
