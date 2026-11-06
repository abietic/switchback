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

func TestParseCommandFailureV2(t *testing.T) {
	failure := &capacityprobe.DeploymentParseFailureObservation{Schema: capacityprobe.DeploymentParseFailureSchema, Kind: "diagnostic_only", Stage: "observe", ErrorClass: "invalid", LastPrimitive: &capacityprobe.DeploymentPrimitiveObservation{Operation: "read", Target: "pid1.cgroup", Outcome: "returned"}, ParseChecks: &capacityprobe.DeploymentProcessParseChecks{Owner: "pid1", StatRead: "bounded", CgroupRead: "bounded", CgroupCheck: "accepted", ProcessCheck: "rejected"}}
	var stdout, stderr bytes.Buffer
	exit := run(nil, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentParseFailureObservation) {
		return capacityprobe.DeploymentObservation{}, failure
	})
	var got capacityprobe.DeploymentParseFailureObservation
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if exit != 1 || stderr.Len() != 0 || got.Schema != capacityprobe.DeploymentParseFailureSchema || got.Validate() != nil || *got.ParseChecks != *failure.ParseChecks {
		t.Fatalf("exit=%d got=%#v stderr=%q", exit, got, stderr.String())
	}
}

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

func failureFixture(stage, class string) *capacityprobe.DeploymentParseFailureObservation {
	return &capacityprobe.DeploymentParseFailureObservation{Schema: capacityprobe.DeploymentParseFailureSchema, Kind: "diagnostic_only", Stage: stage, ErrorClass: class}
}

func wire(t *testing.T, value any) []byte {
	t.Helper()
	p, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(p, '\n')
}

func observeResult(value capacityprobe.DeploymentObservation, failure *capacityprobe.DeploymentParseFailureObservation) observer {
	return func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentParseFailureObservation) {
		return value, failure
	}
}

func assertRun(t *testing.T, value capacityprobe.DeploymentObservation, failure *capacityprobe.DeploymentParseFailureObservation, wantExit int, want any) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := run(nil, &stdout, &stderr, observeResult(value, failure))
	if exit != wantExit || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), wire(t, want)) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&parsed); err != io.EOF {
		t.Fatalf("trailing stdout: %v", err)
	}
}

func TestParseCommandSuccessAndNamespaceDenial(t *testing.T) {
	for _, errno := range []uint32{0, 1, 13} {
		t.Run(fmt.Sprint(errno), func(t *testing.T) {
			v := commandFixture()
			exit := 0
			if errno != 0 {
				class := "EPERM"
				if errno == 13 {
					class = "EACCES"
				}
				v.Namespaces[5].Before = capacityprobe.DeploymentNamespaceValue{Errno: errno, Class: class}
				v.Namespaces[5].After = v.Namespaces[5].Before
				exit = 2
			}
			assertRun(t, v, nil, exit, v)
		})
	}
}

func TestParseCommandEntry(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"pid=1"}, {"secret-argument-canary"}, {""}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			calls := 0
			var stdout, stderr bytes.Buffer
			exit := run(args, &stdout, &stderr, func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentParseFailureObservation) {
				calls++
				return commandFixture(), nil
			})
			if exit != 1 || calls != 0 || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), wire(t, failureFixture("entry", "invalid"))) {
				t.Fatal("arguments observed or leaked")
			}
		})
	}
	var stdout, stderr bytes.Buffer
	if exit := run(nil, &stdout, &stderr, nil); exit != 1 || stderr.Len() != 0 || !bytes.Equal(stdout.Bytes(), wire(t, failureFixture("entry", "invalid"))) {
		t.Fatal("nil observer")
	}
}

func TestParseCommandClosedStageClassPairs(t *testing.T) {
	stages := []string{"entry", "observe", "encode", "size", "write", "secret-stage-canary"}
	classes := []string{"invalid", "unavailable", "cancelled", "unsupported", "internal_invalid_result", "encoding_failed", "size_exceeded", "write_failed", "secret-class-canary"}
	legal := map[string]bool{"entry/invalid": true, "observe/invalid": true, "observe/unavailable": true, "observe/cancelled": true, "observe/unsupported": true, "observe/internal_invalid_result": true, "encode/encoding_failed": true, "size/size_exceeded": true, "write/write_failed": true}
	for _, stage := range stages {
		for _, class := range classes {
			t.Run(stage+"/"+class, func(t *testing.T) {
				f := failureFixture(stage, class)
				want := f
				if !legal[stage+"/"+class] {
					want = failureFixture("observe", "internal_invalid_result")
				}
				assertRun(t, capacityprobe.DeploymentObservation{}, f, 1, want)
			})
		}
	}
}

func TestParseCommandChecksAndPointerConsistency(t *testing.T) {
	tuples := [][3]string{{"empty", "not_checked", "not_checked"}, {"oversize", "not_checked", "not_checked"}, {"bounded", "accepted", "rejected"}, {"bounded", "rejected", "rejected"}, {"bounded", "accepted", "accepted"}}
	for _, owner := range []string{"caller", "pid1"} {
		for _, tuple := range tuples {
			t.Run(owner+"/"+strings.Join(tuple[:], "/"), func(t *testing.T) {
				f := failureFixture("observe", "invalid")
				f.LastPrimitive = &capacityprobe.DeploymentPrimitiveObservation{Operation: "read", Target: owner + ".cgroup", Outcome: "returned"}
				f.ParseChecks = &capacityprobe.DeploymentProcessParseChecks{Owner: owner, StatRead: "bounded", CgroupRead: tuple[0], CgroupCheck: tuple[1], ProcessCheck: tuple[2]}
				before := wire(t, f)
				assertRun(t, capacityprobe.DeploymentObservation{}, f, 1, f)
				if !bytes.Equal(before, wire(t, f)) {
					t.Fatal("runner changed observer pointers")
				}
			})
		}
	}
	errno := uint32(4095)
	f := failureFixture("observe", "unavailable")
	f.LastPrimitive = &capacityprobe.DeploymentPrimitiveObservation{Operation: "open_namespace", Target: "caller.ns.cgroup", Outcome: "failed", Errno: &errno}
	before := wire(t, f)
	assertRun(t, capacityprobe.DeploymentObservation{}, f, 1, f)
	if errno != 4095 || !bytes.Equal(before, wire(t, f)) {
		t.Fatal("errno pointer changed")
	}
}

func TestParseCommandInvalidResultsAreFinite(t *testing.T) {
	type result struct {
		value   capacityprobe.DeploymentObservation
		failure *capacityprobe.DeploymentParseFailureObservation
	}
	cases := map[string]result{"both": {commandFixture(), failureFixture("observe", "invalid")}, "neither": {}, "partial_with_failure": {capacityprobe.DeploymentObservation{KernelRelease: "secret-proc-canary"}, failureFixture("observe", "invalid")}}
	for _, field := range []string{"schema", "kind"} {
		v := commandFixture()
		if field == "schema" {
			v.Schema = "secret-schema-canary"
		} else {
			v.Kind = "secret-kind-canary"
		}
		cases["success_"+field] = result{value: v}
	}
	for _, field := range []string{"schema", "kind", "operation", "target", "outcome", "errno_zero", "errno_overflow", "errno_returned", "primitive_stage", "checks_owner", "checks_stat", "checks_cgroup", "checks_tuple", "checks_qualification", "checks_missing_primitive"} {
		f := failureFixture("observe", "invalid")
		errno := uint32(1)
		f.LastPrimitive = &capacityprobe.DeploymentPrimitiveObservation{Operation: "read", Target: "pid1.cgroup", Outcome: "returned"}
		f.ParseChecks = &capacityprobe.DeploymentProcessParseChecks{Owner: "pid1", StatRead: "bounded", CgroupRead: "bounded", CgroupCheck: "accepted", ProcessCheck: "rejected"}
		switch field {
		case "schema":
			f.Schema = "secret-schema-canary"
		case "kind":
			f.Kind = "secret-kind-canary"
		case "operation":
			f.LastPrimitive.Operation = "secret-operation-canary"
		case "target":
			f.LastPrimitive.Target = "/secret/path-canary"
		case "outcome":
			f.LastPrimitive.Outcome = "secret-outcome-canary"
		case "errno_zero":
			errno = 0
			f.ParseChecks = nil
			f.LastPrimitive.Outcome = "failed"
			f.LastPrimitive.Errno = &errno
		case "errno_overflow":
			errno = 4096
			f.ParseChecks = nil
			f.LastPrimitive.Outcome = "failed"
			f.LastPrimitive.Errno = &errno
		case "errno_returned":
			f.ParseChecks = nil
			f.LastPrimitive.Errno = &errno
		case "primitive_stage":
			f.Stage, f.ErrorClass = "entry", "invalid"
			f.ParseChecks = nil
		case "checks_owner":
			f.ParseChecks.Owner = "caller"
		case "checks_stat":
			f.ParseChecks.StatRead = "empty"
		case "checks_cgroup":
			f.ParseChecks.CgroupRead = "secret-check-canary"
		case "checks_tuple":
			f.ParseChecks.CgroupCheck = "rejected"
			f.ParseChecks.ProcessCheck = "accepted"
		case "checks_qualification":
			f.ErrorClass = "cancelled"
		case "checks_missing_primitive":
			f.LastPrimitive = nil
		}
		cases[field] = result{failure: f}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assertRun(t, tc.value, tc.failure, 1, failureFixture("observe", "internal_invalid_result"))
		})
	}
}

func TestParseCommandEncodingAndSize(t *testing.T) {
	for _, field := range []string{"started", "ended"} {
		t.Run(field, func(t *testing.T) {
			v := commandFixture()
			invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			if field == "started" {
				v.Started = invalid
			} else {
				v.Ended = invalid
			}
			assertRun(t, v, nil, 1, failureFixture("encode", "encoding_failed"))
		})
	}
	for _, size := range []int{32768, 32769} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			v := commandFixture()
			v.KernelRelease = strings.Repeat("x", size-len(wire(t, v))+len(v.KernelRelease))
			if len(wire(t, v)) != size {
				t.Fatal("boundary fixture")
			}
			if size == 32768 {
				assertRun(t, v, nil, 0, v)
			} else {
				assertRun(t, v, nil, 1, failureFixture("size", "size_exceeded"))
			}
		})
	}
}

func TestParseCommandFailureUpperBound(t *testing.T) {
	longest := 0
	// Compare the longest no-check vocabulary against both owners' largest checks.
	f := failureFixture("observe", "internal_invalid_result")
	errno := uint32(4095)
	f.LastPrimitive = &capacityprobe.DeploymentPrimitiveObservation{Operation: "open_namespace", Target: "caller.ns.cgroup", Outcome: "failed", Errno: &errno}
	candidates := []*capacityprobe.DeploymentParseFailureObservation{f}
	for _, owner := range []string{"caller", "pid1"} {
		f := failureFixture("observe", "invalid")
		f.LastPrimitive = &capacityprobe.DeploymentPrimitiveObservation{Operation: "read", Target: owner + ".cgroup", Outcome: "returned"}
		f.ParseChecks = &capacityprobe.DeploymentProcessParseChecks{Owner: owner, StatRead: "bounded", CgroupRead: "oversize", CgroupCheck: "not_checked", ProcessCheck: "not_checked"}
		candidates = append(candidates, f)
	}
	for _, f := range candidates {
		if err := f.Validate(); err != nil {
			t.Fatal(err)
		}
		n := len(wire(t, f))
		if n > longest {
			longest = n
		}
		if n >= 4096 {
			t.Fatal("legal failure exceeds bound")
		}
		assertRun(t, capacityprobe.DeploymentObservation{}, f, 1, f)
	}
	t.Logf("longest valid failure including LF: %d bytes", longest)
}

func TestParseCommandContextDeadlineAndCancel(t *testing.T) {
	for _, variant := range []string{"success", "cancelled", "invalid", "encoding", "size", "write"} {
		t.Run(variant, func(t *testing.T) {
			before := time.Now()
			var captured context.Context
			out := &testWriter{mode: "complete"}
			errout := &testWriter{mode: "complete"}
			run(nil, out, errout, func(ctx context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentParseFailureObservation) {
				captured = ctx
				deadline, ok := ctx.Deadline()
				if !ok || deadline.Before(before.Add(5*time.Second)) || deadline.After(time.Now().Add(5*time.Second)) || ctx.Err() != nil {
					t.Fatal("not fresh five-second context")
				}
				v := commandFixture()
				switch variant {
				case "cancelled":
					return capacityprobe.DeploymentObservation{}, failureFixture("observe", "cancelled")
				case "invalid":
					return v, failureFixture("observe", "invalid")
				case "encoding":
					v.Started = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
				case "size":
					v.KernelRelease = strings.Repeat("x", 32768)
				case "write":
					out.mode = "short"
				}
				return v, nil
			})
			if captured == nil || captured.Err() != context.Canceled {
				t.Fatal("context not cancelled on return")
			}
		})
	}
}

type testWriter struct {
	mode  string
	calls int
	buf   bytes.Buffer
}

func (w *testWriter) Write(packet []byte) (int, error) {
	w.calls++
	n := len(packet)
	switch w.mode {
	case "negative":
		return -1, nil
	case "overlong":
		return n + 1, nil
	case "zero", "zero_error":
		n = 0
	case "short", "short_error":
		n /= 2
	}
	_, _ = w.buf.Write(packet[:n])
	if strings.HasSuffix(w.mode, "error") {
		return n, errors.New("secret-writer-error-canary")
	}
	return n, nil
}

func TestParseCommandOneWriteCrossProduct(t *testing.T) {
	modes := []string{"complete", "zero", "zero_error", "short", "short_error", "full_error", "negative", "overlong"}
	for _, variant := range []string{"success", "denied", "failure", "entry", "nil_observer", "invalid", "encoding", "size"} {
		for _, outmode := range modes {
			for _, errmode := range modes {
				t.Run(variant+"/"+outmode+"/"+errmode, func(t *testing.T) {
					v := commandFixture()
					var f *capacityprobe.DeploymentParseFailureObservation
					var args []string
					wantExit := 0
					var want any = v
					switch variant {
					case "denied":
						v.Namespaces[0].Before = capacityprobe.DeploymentNamespaceValue{Errno: 13, Class: "EACCES"}
						v.Namespaces[0].After = v.Namespaces[0].Before
						want, wantExit = v, 2
					case "failure":
						v = capacityprobe.DeploymentObservation{}
						f = failureFixture("observe", "cancelled")
						want, wantExit = f, 1
					case "entry":
						args = []string{"secret-argument-canary"}
						want, wantExit = failureFixture("entry", "invalid"), 1
					case "nil_observer":
						want, wantExit = failureFixture("entry", "invalid"), 1
					case "invalid":
						f = failureFixture("observe", "invalid")
						want, wantExit = failureFixture("observe", "internal_invalid_result"), 1
					case "encoding":
						v.Started = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
						want, wantExit = failureFixture("encode", "encoding_failed"), 1
					case "size":
						v.KernelRelease = strings.Repeat("secret-proc-canary", 3000)
						want, wantExit = failureFixture("size", "size_exceeded"), 1
					}
					observe := observeResult(v, f)
					if variant == "nil_observer" {
						observe = nil
					}
					out, errout := &testWriter{mode: outmode}, &testWriter{mode: errmode}
					exit := run(args, out, errout, observe)
					wantErrCalls := 0
					if outmode != "complete" {
						wantExit, wantErrCalls = 1, 1
					}
					if exit != wantExit || out.calls != 1 || errout.calls != wantErrCalls {
						t.Fatalf("exit=%d writes=%d/%d", exit, out.calls, errout.calls)
					}
					wantOut := wire(t, want)
					switch outmode {
					case "zero", "zero_error", "negative", "overlong":
						wantOut = nil
					case "short", "short_error":
						wantOut = wantOut[:len(wantOut)/2]
					}
					if !bytes.Equal(out.buf.Bytes(), wantOut) {
						t.Fatal("stdout retry/append or wrong bytes")
					}
					var wantErr []byte
					if wantErrCalls == 1 {
						wantErr = []byte("{\"schema\":\"switchback.capacity.deployment_failure.v2\",\"kind\":\"diagnostic_only\",\"stage\":\"write\",\"error_class\":\"write_failed\"}\n")
						switch errmode {
						case "zero", "zero_error", "negative", "overlong":
							wantErr = nil
						case "short", "short_error":
							wantErr = wantErr[:len(wantErr)/2]
						}
					}
					if !bytes.Equal(errout.buf.Bytes(), wantErr) {
						t.Fatal("stderr retry/append, canary leak, or wrong fixed packet")
					}
				})
			}
		}
	}
}

func TestParseCommandNilWriters(t *testing.T) {
	for _, observe := range []observer{nil, observeResult(commandFixture(), nil), observeResult(capacityprobe.DeploymentObservation{}, failureFixture("observe", "unsupported"))} {
		errout := &testWriter{mode: "complete"}
		if exit := run(nil, nil, errout, observe); exit != 1 || errout.calls != 1 || !bytes.Equal(errout.buf.Bytes(), wire(t, failureFixture("write", "write_failed"))) {
			t.Fatal("nil stdout")
		}
		if exit := run(nil, nil, nil, observe); exit != 1 {
			t.Fatal("nil streams")
		}
	}
	out := &testWriter{mode: "complete"}
	if exit := run(nil, out, nil, observeResult(commandFixture(), nil)); exit != 0 || out.calls != 1 {
		t.Fatal("unused nil stderr")
	}
}

func TestParseCommandFixedProductionBinding(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]bool{`"context"`: true, `"encoding/json"`: true, `"io"`: true, `"os"`: true, `"time"`: true, `"github.com/abietic/switchback/internal/platform/capacityprobe"`: true}
	for _, item := range file.Imports {
		if item.Name != nil || !imports[item.Path.Value] {
			t.Fatal("unexpected runtime import")
		}
	}
	var production *ast.FuncDecl
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.VAR {
				t.Fatal("mutable global hook")
			}
		case *ast.FuncDecl:
			if d.Name.IsExported() || d.Name.Name == "init" {
				t.Fatal("public/init hook")
			}
			if d.Name.Name == "main" {
				production = d
				continue
			}
			ast.Inspect(d, func(n ast.Node) bool {
				if s, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := s.X.(*ast.Ident); ok && (id.Name == "os" || strings.HasPrefix(s.Sel.Name, "ObserveDeployment")) {
						t.Fatalf("native selector outside fixed main: %s", s.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if production == nil || len(production.Body.List) != 1 {
		t.Fatal("main must only bind fixed API")
	}
	var text bytes.Buffer
	if err := format.Node(&text, fset, production.Body); err != nil {
		t.Fatal(err)
	}
	if text.String() != "{\n\tos.Exit(run(os.Args[1:], os.Stdout, os.Stderr, capacityprobe.ObserveDeploymentParseDiagnostic))\n}" {
		t.Fatalf("fixed binding changed: %s", text.String())
	}
}
