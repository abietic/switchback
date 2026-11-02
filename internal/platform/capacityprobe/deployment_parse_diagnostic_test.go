package capacityprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestDeploymentParseDiagnosticDistinguishes(t *testing.T) {
	for _, tc := range []struct {
		name, op   string
		occurrence int
		value      func([]byte) []byte
		want       *DeploymentProcessParseChecks
	}{
		{"negative-start", "read:leaf:1:1", 1, func(v []byte) []byte { return bytes.Replace(v, []byte("18 99 20\n"), []byte("18 -1 20\n"), 1) }, &DeploymentProcessParseChecks{"pid1", "bounded", "bounded", "accepted", "rejected"}},
		{"multiline", "read:leaf:2:1", 1, func([]byte) []byte { return []byte("0::/\nextra\n") }, &DeploymentProcessParseChecks{"pid1", "bounded", "bounded", "rejected", "rejected"}},
		{"empty", "read:leaf:2:1", 1, func([]byte) []byte { return []byte{} }, &DeploymentProcessParseChecks{"pid1", "bounded", "empty", "not_checked", "not_checked"}},
		{"root", "read:leaf:2:1", 0, func([]byte) []byte { return []byte("0::/\n") }, nil},
		{"canonical-drift", "read:leaf:1:1", 3, func(v []byte) []byte { return bytes.Replace(v, []byte("18 99 20\n"), []byte("18 100 20\n"), 1) }, &DeploymentProcessParseChecks{"pid1", "bounded", "bounded", "accepted", "accepted"}},
		{"caller-tid", "read:leaf:1:0", 3, func(v []byte) []byte { return bytes.Replace(v, []byte("18 99 20\n"), []byte("18 100 20\n"), 1) }, &DeploymentProcessParseChecks{"caller", "bounded", "bounded", "accepted", "accepted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeploymentFixture()
			f.mutate = func(op string, n int, v any) any {
				if op == tc.op && (tc.occurrence == 0 || n == tc.occurrence) {
					return tc.value(v.([]byte))
				}
				return v
			}
			old := newDeploymentFixture()
			old.mutate = f.mutate
			deploymentParseAssertParity(t, old, f, context.Background(), context.Background())
			f = newDeploymentFixture()
			f.mutate = old.mutate
			got, failure := observeDeploymentParseDiagnostic(context.Background(), f)
			if tc.want == nil {
				if failure != nil || got.Schema == "" {
					t.Fatalf("want success: %#v", failure)
				}
			} else if failure == nil || failure.Validate() != nil || !reflect.DeepEqual(failure.ParseChecks, tc.want) {
				t.Fatalf("failure: %#v want %#v", failure, tc.want)
			}
			deploymentAssertClosed(t, f)
		})
	}
}

func deploymentParseProjection(v *DeploymentParseFailureObservation) *DeploymentFailureObservation {
	if v == nil {
		return nil
	}
	return &DeploymentFailureObservation{Schema: DeploymentFailureSchema, Kind: v.Kind, Stage: v.Stage, ErrorClass: v.ErrorClass, LastPrimitive: v.LastPrimitive}
}
func deploymentParseAssertParity(t *testing.T, old, f *deploymentFixture, ctx, oldctx context.Context) {
	t.Helper()
	want, wf := observeDeploymentDiagnostic(oldctx, old)
	got, gf := observeDeploymentParseDiagnostic(ctx, f)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(deploymentParseProjection(gf), wf) {
		t.Fatalf("projection changed: %#v %#v", gf, wf)
	}
	if !reflect.DeepEqual(old.operations, f.operations) || !reflect.DeepEqual(old.closed, f.closed) || old.maximum != f.maximum {
		t.Fatal("operation/FD/close drift")
	}
	if gf != nil {
		if gf.Validate() != nil {
			t.Fatal(gf)
		}
		raw, e := json.Marshal(gf)
		if e != nil || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "fixture") {
			t.Fatalf("canary escaped: %s", raw)
		}
	}
	deploymentAssertClosed(t, old)
	deploymentAssertClosed(t, f)
}
func TestDeploymentParseSuccessParity(t *testing.T) {
	for _, errno := range []uint32{0, 1, 13} {
		t.Run(fmt.Sprint(errno), func(t *testing.T) {
			old, f := newDeploymentFixture(), newDeploymentFixture()
			old.denied = errno
			f.denied = errno
			deploymentParseAssertParity(t, old, f, context.Background(), context.Background())
		})
	}
}
func TestDeploymentParseNoIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx          context.Context
		nilBackend   bool
		stage, class string
	}{{nil, false, "entry", "invalid"}, {context.Background(), true, "entry", "invalid"}, {ctx, false, "observe", "cancelled"}} {
		f := newDeploymentFixture()
		var b deploymentBackend = f
		if tc.nilBackend {
			b = nil
		}
		v, e := observeDeploymentParseDiagnostic(tc.ctx, b)
		if v != (DeploymentObservation{}) || e == nil || e.Stage != tc.stage || e.ErrorClass != tc.class || e.Validate() != nil || e.ParseChecks != nil || len(f.operations) != 0 {
			t.Fatal(v, e, f.operations)
		}
	}
}
func TestDeploymentParseFaultCancellationParity(t *testing.T) {
	base := newDeploymentFixture()
	if _, e := observeDeploymentDiagnostic(context.Background(), base); e != nil {
		t.Fatal(e)
	}
	for i, op := range base.operations {
		for _, after := range []bool{false, true} {
			for _, cancelled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%03d-%s-after%t-cancel%t", i+1, op, after, cancelled), func(t *testing.T) {
					old, f := newDeploymentFixture(), newDeploymentFixture()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					oldctx, oldcancel := context.WithCancel(context.Background())
					defer oldcancel()
					old.cancel = oldcancel
					f.cancel = cancel
					for _, x := range []*deploymentFixture{old, f} {
						x.failAfter = after
						if cancelled {
							x.cancelAt = i + 1
						} else {
							x.failAt = i + 1
							x.faultError = fmt.Errorf("secret-error-canary: %w", syscall.EIO)
						}
					}
					deploymentParseAssertParity(t, old, f, ctx, oldctx)
				})
			}
		}
	}
	t.Logf("%d positions x 4 = %d subtests", len(base.operations), len(base.operations)*4)
}
func TestDeploymentParseReadQualification(t *testing.T) {
	for _, tc := range []struct {
		name, op string
		value    []byte
		checks   bool
	}{
		{"stat-empty", "read:leaf:1:1", nil, false}, {"stat-oversize", "read:leaf:1:1", bytes.Repeat([]byte("x"), 4097), false},
		{"group-oversize", "read:leaf:2:1", bytes.Repeat([]byte("x"), 4097), true},
		{"both-invalid", "read:leaf:2:1", []byte("secret-proc-canary"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, f := newDeploymentFixture(), newDeploymentFixture()
			for _, x := range []*deploymentFixture{old, f} {
				x.mutate = func(op string, n int, v any) any {
					if op == tc.op {
						return tc.value
					}
					if tc.name == "both-invalid" && op == "read:leaf:1:1" {
						return []byte("secret-stat-canary")
					}
					return v
				}
			}
			deploymentParseAssertParity(t, old, f, context.Background(), context.Background())
			fresh := newDeploymentFixture()
			fresh.mutate = f.mutate
			_, e := observeDeploymentParseDiagnostic(context.Background(), fresh)
			if (e.ParseChecks != nil) != tc.checks {
				t.Fatalf("checks: %#v", e)
			}
			if tc.name == "group-oversize" && e.ParseChecks.CgroupRead != "oversize" {
				t.Fatal(e)
			}
			if tc.name == "both-invalid" && (e.ParseChecks.CgroupCheck != "rejected" || e.ParseChecks.ProcessCheck != "rejected") {
				t.Fatal(e)
			}
		})
	}
}
func deploymentParseDirect(t *testing.T) (*deploymentParseBackend, *deploymentFixture, int) {
	t.Helper()
	f := newDeploymentFixture()
	b := &deploymentParseBackend{deploymentBackend: f, mappings: map[int]deploymentParseFD{}}
	ctx := context.Background()
	b.ids(ctx)
	p, e := b.openProc(ctx, deploymentPID1)
	if e != nil {
		t.Fatal(e)
	}
	return b, f, p
}
func deploymentParseDirectRead(t *testing.T, b *deploymentParseBackend, parent int, l deploymentLeaf) []byte {
	t.Helper()
	ctx := context.Background()
	fd, e := b.openLeaf(ctx, parent, l)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := b.read(ctx, fd, 4096)
	b.close(fd)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestDeploymentParseCaptureLineage(t *testing.T) {
	for _, mode := range []string{"closed-leaves", "buffer-alias", "new-status", "failed-status", "wrong-proc", "group-before-stat", "stale-leaf", "fd-reuse", "failed-read", "other-owner", "overwrite-round"} {
		t.Run(mode, func(t *testing.T) {
			b, f, p := deploymentParseDirect(t)
			defer b.close(p)
			ctx := context.Background()
			deploymentParseDirectRead(t, b, p, deploymentStatus)
			stat := deploymentParseDirectRead(t, b, p, deploymentStat)
			group := deploymentParseDirectRead(t, b, p, deploymentCgroup)
			want := true
			switch mode {
			case "buffer-alias":
				for i := range stat {
					stat[i] = 'x'
				}
				for i := range group {
					group[i] = 'x'
				}
			case "new-status":
				deploymentParseDirectRead(t, b, p, deploymentStatus)
				want = false
			case "failed-status":
				f.failAt = len(f.operations) + 1
				b.openLeaf(ctx, p, deploymentStatus)
				want = false
			case "wrong-proc":
				q, _ := b.openProc(ctx, deploymentPID1)
				defer b.close(q)
				deploymentParseDirectRead(t, b, q, deploymentCgroup)
				want = false
			case "group-before-stat":
				deploymentParseDirectRead(t, b, p, deploymentStatus)
				deploymentParseDirectRead(t, b, p, deploymentCgroup)
				want = false
			case "stale-leaf":
				fd, _ := b.openLeaf(ctx, p, deploymentCgroup)
				deploymentParseDirectRead(t, b, p, deploymentStatus)
				b.read(ctx, fd, 4096)
				b.close(fd)
				want = false
			case "fd-reuse":
				f.next = p
				deploymentParseDirectRead(t, b, p, deploymentStatus)
				deploymentParseDirectRead(t, b, p, deploymentStat)
				f.next = p
				deploymentParseDirectRead(t, b, p, deploymentCgroup)
			case "failed-read":
				fd, _ := b.openLeaf(ctx, p, deploymentCgroup)
				f.failAt = len(f.operations) + 1
				f.failAfter = true
				b.read(ctx, fd, 4096)
				b.close(fd)
				want = false
			case "other-owner":
				q, _ := b.openProc(ctx, deploymentCaller)
				defer b.close(q)
				deploymentParseDirectRead(t, b, q, deploymentStatus)
				deploymentParseDirectRead(t, b, q, deploymentStat)
				deploymentParseDirectRead(t, b, q, deploymentCgroup)
				want = false
			case "overwrite-round":
				deploymentParseDirectRead(t, b, p, deploymentStatus)
				deploymentParseDirectRead(t, b, p, deploymentStat)
				deploymentParseDirectRead(t, b, p, deploymentCgroup)
			}
			v := deploymentParseTestFailure("pid1")
			got := b.checks(&v)
			if (got != nil) != want {
				t.Fatalf("checks: %#v", got)
			}
			if got != nil && (got.CgroupCheck != "accepted" || got.ProcessCheck != "accepted") {
				t.Fatalf("snapshot changed: %#v", got)
			}
		})
	}
}
func TestDeploymentParseCaptureBounds(t *testing.T) {
	b, f, p := deploymentParseDirect(t)
	ctx := context.Background()
	defer b.close(p)
	for round := 0; round < 20; round++ {
		for _, o := range []deploymentOwner{deploymentCaller, deploymentPID1} {
			q, _ := b.openProc(ctx, o)
			f.mutate = func(op string, n int, v any) any {
				if strings.HasPrefix(op, "read:leaf:1:") || strings.HasPrefix(op, "read:leaf:2:") {
					return bytes.Repeat([]byte("x"), 4096)
				}
				return v
			}
			deploymentParseDirectRead(t, b, q, deploymentStatus)
			deploymentParseDirectRead(t, b, q, deploymentStat)
			deploymentParseDirectRead(t, b, q, deploymentCgroup)
			b.close(q)
		}
		retained := 0
		for _, s := range b.slots {
			retained += cap(s.stat) + cap(s.group)
		}
		if retained > 16384 || len(b.mappings) > 16 {
			t.Fatal(retained, len(b.mappings))
		}
	}
	f.mutate = func(op string, n int, v any) any {
		if strings.HasPrefix(op, "read:leaf:2:") {
			return bytes.Repeat([]byte("x"), 1<<20)
		}
		return v
	}
	deploymentParseDirectRead(t, b, p, deploymentStatus)
	deploymentParseDirectRead(t, b, p, deploymentStat)
	deploymentParseDirectRead(t, b, p, deploymentCgroup)
	if b.slots[deploymentPID1].group != nil {
		t.Fatal("retained oversized buffer")
	}
}
func TestDeploymentParseMapOverflow(t *testing.T) {
	b, f, p := deploymentParseDirect(t)
	ctx := context.Background()
	fds := []int{p}
	defer func() {
		for _, fd := range fds {
			b.close(fd)
		}
		if len(f.live) != 0 {
			t.Fatal(f.live)
		}
		for _, n := range f.closed {
			if n != 1 {
				t.Fatal("close count", n)
			}
		}
	}()
	for i := 0; i < 16; i++ {
		fd, e := b.openProc(ctx, deploymentCaller)
		if e != nil {
			t.Fatal(e)
		}
		fds = append(fds, fd)
	}
	if !b.disabled || len(b.mappings) > 16 {
		t.Fatal("unbounded map")
	}
	deploymentParseDirectRead(t, b, p, deploymentStatus)
	deploymentParseDirectRead(t, b, p, deploymentStat)
	deploymentParseDirectRead(t, b, p, deploymentCgroup)
	v := deploymentParseTestFailure("pid1")
	if b.checks(&v) != nil {
		t.Fatal("overflow facts")
	}
	for _, s := range b.slots {
		if len(s.stat)+len(s.group) != 0 {
			t.Fatal("retained overflow bytes")
		}
	}
}
func TestDeploymentParseIndependentValues(t *testing.T) {
	f := newDeploymentFixture()
	f.mutate = func(op string, n int, v any) any {
		if op == "read:leaf:2:1" {
			return []byte("secret-proc-canary")
		}
		return v
	}
	_, a := observeDeploymentParseDiagnostic(context.Background(), f)
	g := newDeploymentFixture()
	g.mutate = f.mutate
	_, b := observeDeploymentParseDiagnostic(context.Background(), g)
	a.ParseChecks.Owner = "changed"
	a.LastPrimitive.Target = "changed"
	if b.ParseChecks.Owner != "pid1" || b.LastPrimitive.Target != "pid1.cgroup" {
		t.Fatal("shared result")
	}
}
func TestDeploymentParseNativeBindingSource(t *testing.T) {
	raw, e := os.ReadFile("deployment_parse_diagnostic_linux.go")
	if e != nil {
		t.Fatal(e)
	}
	f, e := parser.ParseFile(token.NewFileSet(), "binding.go", raw, 0)
	if e != nil {
		t.Fatal(e)
	}
	calls := []string{}
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			switch x := c.Fun.(type) {
			case *ast.SelectorExpr:
				calls = append(calls, x.Sel.Name)
			case *ast.Ident:
				calls = append(calls, x.Name)
			}
		}
		return true
	})
	if !reflect.DeepEqual(calls, []string{"LockOSThread", "UnlockOSThread", "observeDeploymentParseDiagnostic"}) || !bytes.Contains(raw, []byte("deploymentLinux{}")) || !bytes.Contains(raw, []byte("defer runtime.UnlockOSThread()")) {
		t.Fatal(calls)
	}
	raw, e = os.ReadFile("deployment_parse_diagnostic_other.go")
	if e != nil || bytes.Contains(raw, []byte("deploymentLinux")) {
		t.Fatal("unsupported constructs native backend")
	}
}

func TestDeploymentParseCaptureEligibility(t *testing.T) {
	for _, mode := range []string{"stage", "class", "operation", "outcome", "errno", "target", "missing-primitive", "ids", "independent-checks"} {
		t.Run(mode, func(t *testing.T) {
			b, _, p := deploymentParseDirect(t)
			defer b.close(p)
			deploymentParseDirectRead(t, b, p, deploymentStatus)
			deploymentParseDirectRead(t, b, p, deploymentStat)
			deploymentParseDirectRead(t, b, p, deploymentCgroup)
			v := deploymentParseTestFailure("pid1")
			switch mode {
			case "stage":
				v.Stage = "entry"
			case "class":
				v.ErrorClass = "cancelled"
			case "operation":
				v.LastPrimitive.Operation = "stat"
			case "outcome":
				v.LastPrimitive.Outcome = "failed"
			case "errno":
				n := uint32(5)
				v.LastPrimitive.Errno = &n
			case "target":
				v.LastPrimitive.Target = "caller.cgroup"
			case "missing-primitive":
				v.LastPrimitive = nil
			case "ids":
				b.idsOK = false
			case "independent-checks":
				a := b.checks(&v)
				a.Owner = "changed"
				a.CgroupCheck = "changed"
			}
			got := b.checks(&v)
			if mode == "independent-checks" {
				if got == nil || got.Owner != "pid1" || got.CgroupCheck != "accepted" {
					t.Fatal(got)
				}
			} else if got != nil {
				t.Fatalf("ineligible checks: %#v", got)
			}
		})
	}
}
