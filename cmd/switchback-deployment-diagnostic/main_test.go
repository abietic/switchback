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
	p := capacityprobe.DeploymentProcessObservation{PID: 301, TGID: 301, TID: 307, StartTimeTicks: 99, UIDs: [4]uint32{1000, 1001, 1002, 1003}, GIDs: [4]uint32{2000, 2001, 2002, 2003}, CapInh: "0000000000000001", CapPrm: "00000000000000a2", CapEff: "0000000000000003", CapBnd: "00000000000000ff", CapAmb: "0000000000000004", NoNewPrivs: 1, Seccomp: 2, UnifiedCgroupPath: "/fixture/session"}
	v := capacityprobe.DeploymentObservation{Schema: "switchback.capacity.deployment_diagnostic.v1", Kind: "diagnostic_only", Started: time.Date(2026, 10, 20, 1, 0, 1, 0, time.UTC), Ended: time.Date(2026, 10, 20, 1, 0, 2, 0, time.UTC), BootBefore: "00112233-4455-6677-8899-aabbccddeeff", BootAfter: "00112233-4455-6677-8899-aabbccddeeff", KernelRelease: "6.8.0-fixture", CapLastCap: 40, CallerBefore: p, CallerAfter: p}
	p.PID, p.TGID, p.TID = 1, 1, 1
	v.PID1Before, v.PID1After = p, p
	for i := range v.Namespaces {
		n := capacityprobe.DeploymentNamespaceValue{Device: "4", Inode: "1000", Class: "observed"}
		v.Namespaces[i] = capacityprobe.DeploymentNamespaceObservation{Owner: [2]string{"caller_thread", "pid1"}[i/4], Kind: [4]string{"user", "pid", "mnt", "cgroup"}[i%4], Before: n, After: n}
	}
	return v
}

func failureFixture(stage, class string) *capacityprobe.DeploymentFailureObservation {
	return &capacityprobe.DeploymentFailureObservation{Schema: capacityprobe.DeploymentFailureSchema, Kind: "diagnostic_only", Stage: stage, ErrorClass: class}
}

func packetFor(t *testing.T, v any) []byte {
	t.Helper()
	p, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return append(p, '\n')
}

func TestDiagnosticSuccessAndDenied(t *testing.T) {
	for _, errno := range []uint32{0, 1, 13} {
		v := commandFixture()
		wantExit := 0
		if errno != 0 {
			class := "EPERM"
			if errno == 13 {
				class = "EACCES"
			}
			v.Namespaces[5].Before = capacityprobe.DeploymentNamespaceValue{Errno: errno, Class: class}
			v.Namespaces[5].After = v.Namespaces[5].Before
			wantExit = 2
		}
		var stdout, stderr bytes.Buffer
		calls := 0
		gotExit := run(nil, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
			calls++
			return v, nil
		})
		if gotExit != wantExit || calls != 1 || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), packetFor(t, v)) {
			t.Fatalf("errno=%d exit=%d calls=%d stdout=%q stderr=%q", errno, gotExit, calls, stdout.String(), stderr.String())
		}
		var decoded capacityprobe.DeploymentObservation
		if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded != v {
			t.Fatalf("original success changed: err=%v got=%#v", err, decoded)
		}
	}
}

func TestDiagnosticObserveFailure(t *testing.T) {
	f := failureFixture("observe", "unavailable")
	errno := uint32(13)
	f.LastPrimitive = &capacityprobe.DeploymentPrimitiveObservation{Operation: "open_leaf", Target: "caller.status", Outcome: "failed", Errno: &errno}
	var stdout, stderr bytes.Buffer
	calls := 0
	exit := run(nil, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
		calls++
		return capacityprobe.DeploymentObservation{}, f
	})
	if exit != 1 || calls != 1 || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), packetFor(t, f)) {
		t.Fatalf("exit=%d calls=%d stdout=%q stderr=%q", exit, calls, stdout.String(), stderr.String())
	}
}

func TestDiagnosticArguments(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"pid=1"}, {"secret-argument-canary"}, {""}} {
		var stdout, stderr bytes.Buffer
		calls := 0
		exit := run(args, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
			calls++
			return commandFixture(), nil
		})
		if exit != 1 || calls != 0 || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), packetFor(t, failureFixture("entry", "invalid"))) {
			t.Fatalf("exit=%d calls=%d stdout=%q stderr=%q", exit, calls, stdout.String(), stderr.String())
		}
	}
}

func TestDiagnosticNilObserver(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := run(nil, &stdout, &stderr, nil); exit != 1 || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), packetFor(t, failureFixture("entry", "invalid"))) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestDiagnosticInvalidResult(t *testing.T) {
	cases := []struct {
		name    string
		value   capacityprobe.DeploymentObservation
		failure *capacityprobe.DeploymentFailureObservation
	}{
		{"both", commandFixture(), failureFixture("observe", "invalid")},
		{"neither", capacityprobe.DeploymentObservation{}, nil},
		{"partial_with_failure", capacityprobe.DeploymentObservation{KernelRelease: "secret-proc-canary"}, failureFixture("observe", "invalid")},
	}
	for _, field := range []string{"schema", "kind", "stage", "class", "operation", "target", "outcome", "errno_zero", "errno_overflow", "errno_returned", "primitive_stage", "stage_class"} {
		f := failureFixture("observe", "invalid")
		errno := uint32(1)
		f.LastPrimitive = &capacityprobe.DeploymentPrimitiveObservation{Operation: "read", Target: "caller.status", Outcome: "failed", Errno: &errno}
		switch field {
		case "schema":
			f.Schema = "secret-schema-canary"
		case "kind":
			f.Kind = "secret-kind-canary"
		case "stage":
			f.Stage = "secret-stage-canary"
		case "class":
			f.ErrorClass = "secret-error-canary"
		case "operation":
			f.LastPrimitive.Operation = "secret-operation-canary"
		case "target":
			f.LastPrimitive.Target = "/secret/path-canary"
		case "outcome":
			f.LastPrimitive.Outcome = "secret-outcome-canary"
		case "errno_zero":
			errno = 0
		case "errno_overflow":
			errno = 4096
		case "errno_returned":
			f.LastPrimitive.Outcome = "returned"
		case "primitive_stage":
			f.Stage, f.ErrorClass = "entry", "invalid"
		case "stage_class":
			f.Stage, f.ErrorClass, f.LastPrimitive = "size", "write_failed", nil
		}
		cases = append(cases, struct {
			name    string
			value   capacityprobe.DeploymentObservation
			failure *capacityprobe.DeploymentFailureObservation
		}{field, capacityprobe.DeploymentObservation{}, f})
	}
	for _, field := range []string{"schema", "kind"} {
		v := commandFixture()
		if field == "schema" {
			v.Schema = "secret-schema-canary"
		} else {
			v.Kind = "secret-kind-canary"
		}
		cases = append(cases, struct {
			name    string
			value   capacityprobe.DeploymentObservation
			failure *capacityprobe.DeploymentFailureObservation
		}{"success_" + field, v, nil})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := run(nil, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
				return tc.value, tc.failure
			})
			if exit != 1 || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), packetFor(t, failureFixture("observe", "internal_invalid_result"))) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestDiagnosticEncodingFailure(t *testing.T) {
	for _, field := range []string{"started", "ended"} {
		v := commandFixture()
		invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		if field == "started" {
			v.Started = invalid
		} else {
			v.Ended = invalid
		}
		var stdout, stderr bytes.Buffer
		exit := run(nil, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
			return v, nil
		})
		if exit != 1 || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), packetFor(t, failureFixture("encode", "encoding_failed"))) {
			t.Fatalf("field=%s exit=%d stdout=%q stderr=%q", field, exit, stdout.String(), stderr.String())
		}
	}
}

func TestDiagnosticSuccessSizeBoundary(t *testing.T) {
	base := commandFixture()
	for _, size := range []int{32768, 32769} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			v := base
			v.KernelRelease = strings.Repeat("x", size-len(packetFor(t, base))+len(base.KernelRelease))
			if len(packetFor(t, v)) != size {
				t.Fatal("fixture length")
			}
			var stdout, stderr bytes.Buffer
			exit := run(nil, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
				return v, nil
			})
			wantExit, wantPacket := 0, packetFor(t, v)
			if size > 32768 {
				wantExit, wantPacket = 1, packetFor(t, failureFixture("size", "size_exceeded"))
			}
			if exit != wantExit || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), wantPacket) {
				t.Fatalf("size=%d exit=%d stdout bytes=%d stderr=%q", size, exit, stdout.Len(), stderr.String())
			}
		})
	}
}

func TestDiagnosticDeadline(t *testing.T) {
	before := time.Now()
	var captured context.Context
	var stdout, stderr bytes.Buffer
	exit := run(nil, &stdout, &stderr, func(ctx context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
		captured = ctx
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Before(before.Add(5*time.Second)) || deadline.After(time.Now().Add(5*time.Second)) {
			t.Fatalf("not a five-second deadline: %v %v", deadline, ok)
		}
		return commandFixture(), nil
	})
	if exit != 0 || captured == nil || captured.Err() != context.Canceled {
		t.Fatalf("exit=%d context=%v", exit, captured)
	}
}

type commandWriter struct {
	buf   bytes.Buffer
	calls int
	mode  string
}

func (w *commandWriter) Write(packet []byte) (int, error) {
	w.calls++
	n := len(packet)
	var err error
	switch w.mode {
	case "zero", "zero_error":
		n = 0
	case "short", "short_error":
		n /= 2
	case "negative":
		return -1, nil
	case "overcount":
		return n + 1, nil
	}
	if strings.HasSuffix(w.mode, "error") {
		err = errors.New("secret-writer-error-canary")
	}
	_, _ = w.buf.Write(packet[:n])
	return n, err
}

func TestDiagnosticWriterFailures(t *testing.T) {
	for _, initial := range []string{"success", "observe", "entry", "invalid", "encode", "size", "denied"} {
		for _, mode := range []string{"complete", "zero", "zero_error", "short", "short_error", "full_error", "negative", "overcount"} {
			t.Run(initial+"/"+mode, func(t *testing.T) {
				value, failure := commandFixture(), (*capacityprobe.DeploymentFailureObservation)(nil)
				var args []string
				wantExit := 0
				var wantPacket []byte
				switch initial {
				case "observe":
					value, failure, wantExit = capacityprobe.DeploymentObservation{}, failureFixture("observe", "cancelled"), 1
					wantPacket = packetFor(t, failure)
				case "entry":
					args, wantExit = []string{"secret-argument-canary"}, 1
					wantPacket = packetFor(t, failureFixture("entry", "invalid"))
				case "invalid":
					failure, wantExit = failureFixture("observe", "invalid"), 1
					wantPacket = packetFor(t, failureFixture("observe", "internal_invalid_result"))
				case "encode":
					value.Started, wantExit = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), 1
					wantPacket = packetFor(t, failureFixture("encode", "encoding_failed"))
				case "size":
					value.KernelRelease, wantExit = strings.Repeat("secret-proc-canary", 3000), 1
					wantPacket = packetFor(t, failureFixture("size", "size_exceeded"))
				case "denied":
					value.Namespaces[0].Before = capacityprobe.DeploymentNamespaceValue{Errno: 1, Class: "EPERM"}
					value.Namespaces[0].After = value.Namespaces[0].Before
					wantExit = 2
					wantPacket = packetFor(t, value)
				default:
					wantPacket = packetFor(t, value)
				}
				stdout, stderr := &commandWriter{mode: mode}, &commandWriter{}
				exit := run(args, stdout, stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
					return value, failure
				})
				wantErrWrites := 0
				if mode != "complete" {
					wantExit, wantErrWrites = 1, 1
				}
				if exit != wantExit || stdout.calls != 1 || stderr.calls != wantErrWrites {
					t.Fatalf("exit=%d stdout writes=%d stderr writes=%d", exit, stdout.calls, stderr.calls)
				}
				if mode == "complete" && !bytes.Equal(stdout.buf.Bytes(), wantPacket) {
					t.Fatal("wrong complete packet")
				}
				if mode == "full_error" && !bytes.Equal(stdout.buf.Bytes(), wantPacket) {
					t.Fatal("full bytes changed after error")
				}
				if strings.HasPrefix(mode, "short") && !bytes.Equal(stdout.buf.Bytes(), wantPacket[:len(wantPacket)/2]) {
					t.Fatal("partial stdout appended or changed")
				}
				if mode != "complete" && !bytes.Equal(stderr.buf.Bytes(), []byte("{\"schema\":\"switchback.capacity.deployment_failure.v1\",\"kind\":\"diagnostic_only\",\"stage\":\"write\",\"error_class\":\"write_failed\"}\n")) {
					t.Fatalf("stderr=%q", stderr.buf.String())
				}
			})
		}
	}
}

func TestDiagnosticNilWriters(t *testing.T) {
	for _, output := range []string{"success", "failure", "nil_observer"} {
		stderr := &commandWriter{}
		var observe func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation)
		if output != "nil_observer" {
			observe = func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
				if output == "failure" {
					return capacityprobe.DeploymentObservation{}, failureFixture("observe", "unsupported")
				}
				return commandFixture(), nil
			}
		}
		if exit := run(nil, nil, stderr, observe); exit != 1 || stderr.calls != 1 || !bytes.Equal(stderr.buf.Bytes(), packetFor(t, failureFixture("write", "write_failed"))) {
			t.Fatalf("nil stdout: exit=%d writes=%d bytes=%q", exit, stderr.calls, stderr.buf.String())
		}
		if exit := run(nil, nil, nil, observe); exit != 1 {
			t.Fatalf("nil streams exit=%d", exit)
		}
	}
	stdout := &commandWriter{}
	if exit := run(nil, stdout, nil, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
		return commandFixture(), nil
	}); exit != 0 || stdout.calls != 1 {
		t.Fatalf("unused nil stderr exit=%d writes=%d", exit, stdout.calls)
	}
}

func TestDiagnosticStderrFailures(t *testing.T) {
	for _, mode := range []string{"zero", "zero_error", "short", "short_error", "full_error", "negative", "overcount"} {
		stdout, stderr := &commandWriter{mode: "short"}, &commandWriter{mode: mode}
		exit := run(nil, stdout, stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
			return commandFixture(), nil
		})
		if exit != 1 || stdout.calls != 1 || stderr.calls != 1 {
			t.Fatalf("mode=%s exit=%d stdout writes=%d stderr writes=%d", mode, exit, stdout.calls, stderr.calls)
		}
	}
}

func TestDiagnosticFailurePacketBound(t *testing.T) {
	errno := uint32(4095)
	f := failureFixture("observe", "internal_invalid_result")
	f.LastPrimitive = &capacityprobe.DeploymentPrimitiveObservation{Operation: "open_namespace", Target: "caller.ns.cgroup", Outcome: "failed", Errno: &errno}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	wantPacket := packetFor(t, f)
	if len(wantPacket) >= 4096 {
		t.Fatalf("longest legal packet exceeds bound: %d", len(wantPacket))
	}
	stdout, stderr := &commandWriter{}, &commandWriter{}
	exit := run(nil, stdout, stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
		return capacityprobe.DeploymentObservation{}, f
	})
	if exit != 1 || stdout.calls != 1 || stderr.calls != 0 || !bytes.Equal(stdout.buf.Bytes(), wantPacket) {
		t.Fatalf("exit=%d writes=%d/%d", exit, stdout.calls, stderr.calls)
	}
	t.Logf("longest legal failure packet including LF: %d bytes", len(wantPacket))
	f.LastPrimitive.Target = strings.Repeat("secret-target-canary", 500)
	stdout = &commandWriter{}
	exit = run(nil, stdout, stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
		return capacityprobe.DeploymentObservation{}, f
	})
	if exit != 1 || stdout.calls != 1 || stderr.calls != 0 || !bytes.Equal(stdout.buf.Bytes(), packetFor(t, failureFixture("observe", "internal_invalid_result"))) {
		t.Fatal("invalid oversized failure escaped")
	}
}

func TestDiagnosticProductionBinding(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowedImports := map[string]bool{`"context"`: true, `"encoding/json"`: true, `"io"`: true, `"os"`: true, `"time"`: true, `"github.com/abietic/switchback/internal/platform/capacityprobe"`: true}
	for _, imported := range file.Imports {
		if !allowedImports[imported.Path.Value] || imported.Name != nil {
			t.Fatalf("unexpected runtime dependency: %v", imported)
		}
	}
	var mainDecl *ast.FuncDecl
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.VAR {
				t.Fatal("mutable global hook")
			}
		case *ast.FuncDecl:
			if d.Name.IsExported() || d.Name.Name == "init" {
				t.Fatalf("public/init hook: %s", d.Name.Name)
			}
			if d.Name.Name == "main" {
				mainDecl = d
				continue
			}
			ast.Inspect(d, func(n ast.Node) bool {
				if selector, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := selector.X.(*ast.Ident); ok && (id.Name == "os" || selector.Sel.Name == "ObserveDeploymentDiagnostic" || selector.Sel.Name == "ObserveDeployment") {
						t.Fatalf("runtime selector outside fixed main: %s", selector.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if mainDecl == nil || len(mainDecl.Body.List) != 1 {
		t.Fatal("main must contain only fixed observer binding")
	}
	expr, ok := mainDecl.Body.List[0].(*ast.ExprStmt)
	if !ok {
		t.Fatal("main expression")
	}
	var formatted bytes.Buffer
	if err := format.Node(&formatted, fset, expr.X); err != nil {
		t.Fatal(err)
	}
	if formatted.String() != "os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, capacityprobe.ObserveDeploymentDiagnostic))" {
		t.Fatalf("production binding changed: %s", formatted.String())
	}
}

func TestDiagnosticExactlyOneJSON(t *testing.T) {
	for _, failure := range []*capacityprobe.DeploymentFailureObservation{nil, failureFixture("observe", "unsupported")} {
		var stdout, stderr bytes.Buffer
		run(nil, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation) {
			if failure != nil {
				return capacityprobe.DeploymentObservation{}, failure
			}
			return commandFixture(), nil
		})
		decoder := json.NewDecoder(&stdout)
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&value); err != io.EOF {
			t.Fatalf("second stdout JSON: %v", err)
		}
	}
}
