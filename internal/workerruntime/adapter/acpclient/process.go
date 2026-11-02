package acpclient

import (
	"context"
	"fmt"
	sdk "github.com/coder/acp-go-sdk"
	"golang.org/x/sys/unix"
	"io"
	"syscall"
	"time"
)

func (a *Adapter) startProcess() error {
	command := a.options.Command()
	if command == nil {
		return fmt.Errorf("create %s child", a.options.ProcessName)
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	command.Stderr = io.Discard
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open %s stdin", a.options.ProcessName)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open %s stdout", a.options.ProcessName)
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("start %s child", a.options.ProcessName)
	}
	processGroup, err := unix.Getpgid(command.Process.Pid)
	if err != nil || processGroup != command.Process.Pid {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		return fmt.Errorf("%s process group is unverified", a.options.ProcessName)
	}
	outbound := newRecordingWriter(stdin, a.recorder, directionClientToAgent)
	sdkReader, sdkWriter := io.Pipe()
	client := &protocolClient{workspaceRoot: a.options.WorkspaceRoot, onUpdate: a.observeSessionUpdate}
	connection := sdk.NewClientSideConnection(client, outbound, sdkReader)
	done := make(chan error, 1)
	go func() {
		_ = pumpRecordedOutput(stdout, sdkWriter, newFrameWriter(a.recorder, directionAgentToClient))
		outbound.close()
		_ = stdin.Close()
		done <- command.Wait()
		close(done)
	}()
	a.mu.Lock()
	a.command = command
	a.stdin = stdin
	a.conn = connection
	a.done = done
	a.mu.Unlock()
	return nil
}

// Close fences the adapter and reaps the complete codex-acp process group.
func (a *Adapter) Close(ctx context.Context) error {
	a.mu.Lock()
	if a.state == stateClosed {
		a.mu.Unlock()
		return nil
	}
	a.state = stateClosed
	stdin := a.stdin
	command := a.command
	done := a.done
	a.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if command == nil || command.Process == nil {
		return nil
	}
	pid := command.Process.Pid
	_ = unix.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
	}
	_ = unix.Kill(-pid, syscall.SIGKILL)
	select {
	case <-done:
		return nil
	case <-time.After(time.Second):
		return fmt.Errorf("%s termination could not be confirmed", a.options.ProcessName)
	}
}
