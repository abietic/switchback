package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

func commandFixture() capacityprobe.DeploymentObservation {
	p := capacityprobe.DeploymentProcessObservation{PID: 301, TGID: 301, TID: 307, StartTimeTicks: 99, UIDs: [4]uint32{1000, 1001, 1002, 1003}, GIDs: [4]uint32{2000, 2001, 2002, 2003}, CapInh: "0000000000000001", CapPrm: "00000000000000a2", CapEff: "0000000000000003", CapBnd: "00000000000000ff", CapAmb: "0000000000000004", Seccomp: 2, UnifiedCgroupPath: "/sys/fs/cgroup/fixture"}
	v := capacityprobe.DeploymentObservation{Schema: "switchback.capacity.deployment_diagnostic.v1", Kind: "diagnostic_only", Started: time.Date(2026, 10, 20, 1, 0, 1, 0, time.UTC), Ended: time.Date(2026, 10, 20, 1, 0, 2, 0, time.UTC), BootBefore: "00112233-4455-6677-8899-aabbccddeeff", BootAfter: "00112233-4455-6677-8899-aabbccddeeff", KernelRelease: "6.8.0-fixture", CapLastCap: 40, CallerBefore: p, CallerAfter: p}
	p.PID = 1
	p.TGID = 1
	p.TID = 1
	v.PID1Before = p
	v.PID1After = p
	for i := range v.Namespaces {
		v.Namespaces[i] = capacityprobe.DeploymentNamespaceObservation{Owner: [2]string{"caller_thread", "pid1"}[i/4], Kind: [4]string{"user", "pid", "mnt", "cgroup"}[i%4], Before: capacityprobe.DeploymentNamespaceValue{Device: "4", Inode: "1000", Class: "observed"}, After: capacityprobe.DeploymentNamespaceValue{Device: "4", Inode: "1000", Class: "observed"}}
	}
	return v
}
func TestDeploymentCommandComplete(t *testing.T) {
	var stdout, stderr bytes.Buffer
	want := commandFixture()
	calls := 0
	status := run(nil, &stdout, &stderr, func(ctx context.Context) (capacityprobe.DeploymentObservation, error) { calls++; return want, nil })
	if status != 0 {
		t.Fatalf("complete command status: got %d want 0; stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	var got capacityprobe.DeploymentObservation
	if e := json.Unmarshal(stdout.Bytes(), &got); e != nil || got != want || calls != 1 || stderr.Len() != 0 || !bytes.HasSuffix(stdout.Bytes(), []byte("\n")) {
		t.Fatalf("complete command: %#v %v calls=%d stderr=%q", got, e, calls, stderr.String())
	}
}
func TestDeploymentCommandDenied(t *testing.T) {
	for _, errno := range []uint32{13, 1} {
		want := commandFixture()
		class := "EACCES"
		if errno == 1 {
			class = "EPERM"
		}
		want.Namespaces[5].Before = capacityprobe.DeploymentNamespaceValue{Errno: errno, Class: class}
		want.Namespaces[5].After = want.Namespaces[5].Before
		var stdout, stderr bytes.Buffer
		status := run(nil, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, error) { return want, nil })
		if status != 2 || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("denied command status: got %d want 2 stdout=%q stderr=%q", status, stdout.String(), stderr.String())
		}
	}
}

func TestDeploymentCommandArguments(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"pid=1"}, {""}} {
		var out, err bytes.Buffer
		calls := 0
		status := run(args, &out, &err, func(context.Context) (capacityprobe.DeploymentObservation, error) {
			calls++
			return commandFixture(), nil
		})
		if status != 1 || calls != 0 || out.Len() != 0 || err.String() != "deployment diagnostic failed\n" {
			t.Fatal(status, calls, out.String(), err.String())
		}
	}
}
func TestDeploymentCommandDeadline(t *testing.T) {
	var out, err bytes.Buffer
	before := time.Now()
	var captured context.Context
	status := run(nil, &out, &err, func(ctx context.Context) (capacityprobe.DeploymentObservation, error) {
		captured = ctx
		deadline, ok := ctx.Deadline()
		remaining := deadline.Sub(before)
		if !ok || remaining < 4900*time.Millisecond || remaining > 5100*time.Millisecond {
			t.Fatalf("deadline %v %v", remaining, ok)
		}
		return commandFixture(), nil
	})
	if status != 0 || captured.Err() != context.Canceled {
		t.Fatal(status, captured.Err())
	}
}
func TestDeploymentCommandFailures(t *testing.T) {
	for _, failure := range []error{capacityprobe.ErrDeploymentUnsupported, capacityprobe.ErrDeploymentCancelled, capacityprobe.ErrDeploymentInvalid, capacityprobe.ErrDeploymentUnavailable, errors.New("secret-syscall-canary")} {
		var out, err bytes.Buffer
		status := run(nil, &out, &err, func(context.Context) (capacityprobe.DeploymentObservation, error) { return commandFixture(), failure })
		if status != 1 || out.Len() != 0 || err.String() != "deployment diagnostic failed\n" {
			t.Fatal(status, out.String(), err.String())
		}
	}
	for _, kind := range []string{"oversize", "encoding"} {
		t.Run(kind, func(t *testing.T) {
			v := commandFixture()
			if kind == "oversize" {
				v.KernelRelease = strings.Repeat("secret-status-canary", 2000)
			} else {
				v.Started = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			var out, err bytes.Buffer
			status := run(nil, &out, &err, func(context.Context) (capacityprobe.DeploymentObservation, error) { return v, nil })
			if status != 1 || out.Len() != 0 || err.String() != "deployment diagnostic failed\n" {
				t.Fatal(status, out.Len(), err.String())
			}
		})
	}
	var out, err bytes.Buffer
	if status := run(nil, &out, &err, nil); status != 1 || out.Len() != 0 || err.String() != "deployment diagnostic failed\n" {
		t.Fatal(status, out.String(), err.String())
	}
}

type commandWriter struct {
	buf   bytes.Buffer
	count int
	mode  string
}

func (w *commandWriter) Write(p []byte) (int, error) {
	w.count++
	switch w.mode {
	case "short":
		return w.buf.Write(p[:len(p)/2])
	case "error":
		_, _ = w.buf.Write(p[:5])
		return 5, errors.New("secret-writer-canary")
	case "empty":
		return 0, nil
	default:
		return w.buf.Write(p)
	}
}
func TestDeploymentCommandWrite(t *testing.T) {
	for _, mode := range []string{"short", "error", "empty", "complete"} {
		t.Run(mode, func(t *testing.T) {
			w := &commandWriter{mode: mode}
			var stderr bytes.Buffer
			status := run(nil, w, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, error) { return commandFixture(), nil })
			want := 1
			if mode == "complete" {
				want = 0
			}
			if status != want || w.count != 1 {
				t.Fatal(status, w.count)
			}
			if mode != "complete" && stderr.String() != "deployment diagnostic failed\n" {
				t.Fatal(stderr.String())
			}
			if (mode == "short" || mode == "error") && w.buf.Len() == 0 {
				t.Fatal("partial bytes should be retained")
			}
			if mode == "complete" {
				dec := json.NewDecoder(&w.buf)
				var v capacityprobe.DeploymentObservation
				if e := dec.Decode(&v); e != nil {
					t.Fatal(e)
				}
				if e := dec.Decode(&v); e != io.EOF {
					t.Fatal("not exactly one JSON", e)
				}
			}
		})
	}
}

func TestDeploymentCommandSizeBoundary(t *testing.T) {
	base := commandFixture()
	raw, e := json.Marshal(base)
	if e != nil {
		t.Fatal(e)
	}
	for _, size := range []int{32768, 32769} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			v := base
			v.KernelRelease = strings.Repeat("x", size-(len(raw)+1)+len(base.KernelRelease))
			var out, err bytes.Buffer
			status := run(nil, &out, &err, func(context.Context) (capacityprobe.DeploymentObservation, error) { return v, nil })
			if size == 32768 {
				if status != 0 || out.Len() != size || err.Len() != 0 {
					t.Fatal(status, out.Len(), err.String())
				}
			} else if status != 1 || out.Len() != 0 || err.String() != "deployment diagnostic failed\n" {
				t.Fatal(status, out.Len(), err.String())
			}
		})
	}
}
func TestDeploymentCommandProductionBinding(t *testing.T) {
	file, e := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	var mainDecl *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
			mainDecl = fn
		}
	}
	if mainDecl == nil || len(mainDecl.Body.List) != 1 {
		t.Fatal("main must bind only the fixed runner")
	}
	expression, ok := mainDecl.Body.List[0].(*ast.ExprStmt)
	if !ok {
		t.Fatal("main expression")
	}
	exit, ok := expression.X.(*ast.CallExpr)
	if !ok || len(exit.Args) != 1 {
		t.Fatal("main exit")
	}
	var buf bytes.Buffer
	if e := format.Node(&buf, token.NewFileSet(), exit); e != nil {
		t.Fatal(e)
	}
	if buf.String() != "os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, capacityprobe.ObserveDeployment))" {
		t.Fatal("production observer binding changed:", buf.String())
	}
}
