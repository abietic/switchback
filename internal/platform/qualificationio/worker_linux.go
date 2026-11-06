//go:build linux && (amd64 || arm64 || 386 || arm)

package qualificationio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func finiteContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := checkedContext(ctx); err != nil {
		return nil, nil, err
	}
	c, cancel := context.WithTimeout(ctx, 240*time.Second)
	return c, cancel, nil
}
func finiteChannels(ctx context.Context, out io.Writer) (io.Reader, io.Writer, error) {
	in, err := newFDChannel(ctx, os.Stdin)
	if err != nil {
		return nil, nil, err
	}
	if f, ok := out.(*os.File); ok {
		w, err := newFDChannel(ctx, f)
		return in, w, err
	}
	if out == nil {
		return nil, nil, fmt.Errorf("qualification output required")
	}
	return in, out, nil
}

// RunWorker is a finite UID10002 branch of the final worker-shim ELF. It never
// constructs shimruntime, ACP, LiteLLM, provider or Backend clients.
func RunWorker(ctx context.Context, output io.Writer) error {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("qualification worker architecture unsupported")
	}
	ctx, cancel, err := finiteContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c, err := readControl(ctx)
	if err != nil {
		return err
	}
	if c.Role != "worker" || c.Architecture != runtime.GOARCH {
		return fmt.Errorf("qualification worker role invalid")
	}
	p, err := observeLocal(ctx)
	if err != nil {
		return err
	}
	if err = verifyProcess(c, p, 0, c.BinaryDigest, true); err != nil {
		return err
	}
	if err = verifyWorkerResources(ctx, c, p); err != nil {
		return err
	}
	h, err := openWorkerObjects(ctx, c)
	if err != nil {
		return err
	}
	defer h.close()
	in, out, err := finiteChannels(ctx, output)
	if err != nil {
		return err
	}
	if err = emitWorkerFrame(ctx, out, c, h, 0, "worker-ready", "worker", nil); err != nil {
		return err
	}
	for seq, testCase := range []string{"quota-denied", "quota-success"} {
		n := uint64(seq + 1)
		if err = expectCommand(in, c, n, testCase, testCase); err != nil {
			return err
		}
		beforeDir, beforeChild, e := h.observe(ctx)
		if e != nil {
			return e
		}
		written, e := writeQuota(ctx, int(h.child.Fd()))
		if e != nil {
			return e
		}
		afterDir, afterChild, e := h.observe(ctx)
		if e != nil || !stableFD(beforeDir, afterDir) || !stableFD(beforeChild, afterChild) {
			return fmt.Errorf("qualification quota target changed")
		}
		if err = validQuotaResult(testCase, written); err != nil {
			return err
		}
		if err = emitWorkerFrame(ctx, out, c, h, n, testCase+"-after", testCase, &written); err != nil {
			return err
		}
	}
	if err = expectCommand(in, c, 3, "setters-begin", "worker"); err != nil {
		return err
	}
	seq := uint64(4)
	for _, abi := range []string{"native", "compat32"} {
		for _, command := range []string{"FS_IOC_FSSETXATTR", "FS_IOC_SETFLAGS"} {
			if err = runIOCTLChild(ctx, c, h, in, out, seq, abi, command); err != nil {
				return err
			}
			seq++
		}
	}
	if err = emitWorkerFrame(ctx, out, c, h, 8, "worker-after", "worker", nil); err != nil {
		return err
	}
	return expectCommand(in, c, 8, "worker-release", "worker")
}

func emitWorkerFrame(ctx context.Context, out io.Writer, c Control, h *heldObjects, seq uint64, phase, testCase string, w *WriteResult) error {
	p, err := observeLocal(ctx)
	if err != nil {
		return err
	}
	if err = verifyProcess(c, p, 0, c.BinaryDigest, true); err != nil {
		return err
	}
	d, ch, err := h.observe(ctx)
	if err != nil {
		return err
	}
	f := NewCommand(c, seq, phase, testCase)
	f.Process = &p
	f.Target = &d
	f.Child = &ch
	f.Write = w
	return WriteFrame(out, f)
}

type boundedCapture struct {
	raw      []byte
	overflow bool
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	if len(b.raw)+len(p) > 4096 {
		b.overflow = true
		return 0, fmt.Errorf("qualification child output oversized")
	}
	b.raw = append(b.raw, p...)
	return len(p), nil
}

func runIOCTLChild(ctx context.Context, c Control, h *heldObjects, producer io.Reader, out io.Writer, seq uint64, abi, command string) (err error) {
	p, artifact := NativeHelperPath, "worker-native-helper"
	if abi == "compat32" {
		p = CompatHelperPath
		artifact = "worker-compat-helper"
	}
	f, elf, err := openExecutable(ctx, p, artifact, false)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = expectedELF(c, abi, elf); err != nil {
		return err
	}
	// fd 5 pins the verified executable through the actual execve. fd 3/4 pin
	// the exact two objects; neither case reopens an arbitrary path.
	cmd := exec.CommandContext(ctx, "/proc/self/fd/5")
	cmd.ExtraFiles = []*os.File{h.target, h.child, f}
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer stdout.Close()
	capture := &boundedCapture{}
	cmd.Stderr = capture
	if err = cmd.Start(); err != nil {
		errno, cause, e := compatExecOutcome(abi, err)
		if e != nil {
			return e
		}
		if checkedContext(ctx) != nil {
			return ctx.Err()
		}
		f := NewCommand(c, seq, "setter-unsupported", abi+":"+command)
		f.ELF = &elf
		f.ExecErrno = errno
		f.UnsupportedCause = cause
		p, e := observeLocal(ctx)
		if e != nil {
			return e
		}
		if e = verifyProcess(c, p, 0, c.BinaryDigest, true); e != nil {
			return e
		}
		f.Parent = &p
		if err = WriteFrame(out, f); err != nil {
			return err
		}
		return expectCommand(producer, c, seq, "setter-release", f.Case)
	}
	waited := false
	defer func() {
		if !waited {
			cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	inputFile, ok := stdin.(*os.File)
	if !ok {
		return fmt.Errorf("qualification child stdin unavailable")
	}
	outputFile, ok := stdout.(*os.File)
	if !ok {
		return fmt.Errorf("qualification child stdout unavailable")
	}
	childIn, err := newFDChannel(ctx, inputFile)
	if err != nil {
		return err
	}
	childOut, err := newFDChannel(ctx, outputFile)
	if err != nil {
		return err
	}
	testCase := abi + ":" + command
	if err = WriteFrame(childIn, NewCommand(c, seq, "ioctl-start", testCase)); err != nil {
		return err
	}
	var beforeLocal LocalProcess
	for _, phase := range []struct{ observation, action string }{{"setter-before", "setter-operate"}, {"setter-after", "setter-release"}} {
		frame, e := ReadFrame(childOut)
		if e != nil {
			return e
		}
		if e = frame.Match(c, seq, phase.observation, testCase); e != nil {
			return e
		}
		if frame.Process == nil || frame.Parent != nil || frame.Target == nil || frame.Child == nil || frame.ELF == nil || frame.ExecErrno != 0 || frame.UnsupportedCause != "" || frame.Write != nil || frame.Backend != nil || expectedELF(c, abi, *frame.ELF) != nil || !bytes.Equal(frame.ELF.Header, elf.Header) || !matchesObject(*frame.Target, c.Target) || !matchesObject(*frame.Child, c.Child) || frame.Process.PID != uint64(cmd.Process.Pid) || frame.Process.Snapshot.ExeDigest != elf.Digest {
			return fmt.Errorf("qualification helper observation invalid")
		}
		if e = verifyProcess(c, *frame.Process, 0, elf.Digest, true); e != nil {
			return e
		}
		if beforeLocal.PID != 0 && !sameLocalProcess(beforeLocal, *frame.Process) {
			return fmt.Errorf("qualification executor identity drift")
		}
		beforeLocal = *frame.Process
		if (phase.observation == "setter-after") != (frame.Setter != nil) {
			return fmt.Errorf("qualification helper result cardinality invalid")
		}
		p, e := observeLocal(ctx)
		if e != nil {
			return e
		}
		if e = verifyProcess(c, p, 0, c.BinaryDigest, true); e != nil {
			return e
		}
		frame.Parent = &p
		if err = WriteFrame(out, frame); err != nil {
			return err
		}
		if err = expectCommand(producer, c, seq, phase.action, testCase); err != nil {
			return err
		}
		if err = WriteFrame(childIn, NewCommand(c, seq, phase.action, testCase)); err != nil {
			return err
		}
	}
	stdin.Close()
	err = cmd.Wait()
	waited = true
	if err != nil || capture.overflow || len(capture.raw) != 0 || checkedContext(ctx) != nil {
		return fmt.Errorf("qualification helper result unavailable")
	}
	return nil
}

// RunCompat is shared syscall source for two explicitly built ELF artifacts.
// The command takes no arguments; its first fixed frame selects one finite
// setter matching this actual executable ABI, and it has no exec path of its own.
func RunCompat(ctx context.Context, output io.Writer) error {
	ctx, cancel, err := finiteContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c, err := readControl(ctx)
	if err != nil {
		return err
	}
	if c.Role != "worker" {
		return fmt.Errorf("qualification ioctl role invalid")
	}
	abi, requestABI, artifact, class, machine, err := helperABI(c)
	if err != nil {
		return err
	}
	p, err := observeLocal(ctx)
	if err != nil {
		return err
	}
	digest := c.NativeDigest
	if abi == "compat32" {
		digest = c.CompatDigest
	}
	if err = verifyProcess(c, p, 0, digest, true); err != nil {
		return err
	}
	if err = verifyWorkerResources(ctx, c, p); err != nil {
		return err
	}
	exe, elf, err := openExecutable(ctx, "/proc/self/exe", artifact, true)
	if err != nil {
		return err
	}
	exe.Close()
	if elf.Class != class || elf.Machine != machine || expectedELF(c, abi, elf) != nil {
		return fmt.Errorf("qualification ioctl ELF invalid")
	}
	h, err := inheritedObjects(ctx, c)
	if err != nil {
		return err
	}
	defer h.close()
	in, out, err := finiteChannels(ctx, output)
	if err != nil {
		return err
	}
	start, err := ReadFrame(in)
	if err != nil {
		return err
	}
	command, err := validateHelperStart(c, start, abi)
	if err != nil {
		return err
	}
	beforeDir, beforeChild, err := h.observe(ctx)
	if err != nil {
		return err
	}
	before := NewCommand(c, start.Sequence, "setter-before", start.Case)
	before.Process = &p
	before.Target = &beforeDir
	before.Child = &beforeChild
	before.ELF = &elf
	if err = WriteFrame(out, before); err != nil {
		return err
	}
	if err = expectCommand(in, c, start.Sequence, "setter-operate", start.Case); err != nil {
		return err
	}
	result, err := executeSetter(ctx, h, command, requestABI)
	if err != nil {
		return err
	}
	afterDir, afterChild, err := h.observe(ctx)
	if err != nil || !stableFD(beforeDir, afterDir) || !stableFD(beforeChild, afterChild) {
		return fmt.Errorf("qualification ioctl object changed")
	}
	p, err = observeLocal(ctx)
	if err != nil {
		return err
	}
	if err = verifyProcess(c, p, 0, digest, true); err != nil {
		return err
	}
	after := NewCommand(c, start.Sequence, "setter-after", start.Case)
	after.Process = &p
	after.Target = &afterDir
	after.Child = &afterChild
	after.Setter = &result
	after.ELF = &elf
	if err = WriteFrame(out, after); err != nil {
		return err
	}
	return expectCommand(in, c, start.Sequence, "setter-release", start.Case)
}
