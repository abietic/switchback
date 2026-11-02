package capacityprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestDeploymentDiagnosticSuccessParity(t *testing.T) {
	for _, denied := range []uint32{0, 1, 13} {
		old, fixture := newDeploymentFixture(), newDeploymentFixture()
		old.denied, fixture.denied = denied, denied
		want, err := observeDeployment(context.Background(), old)
		if err != nil {
			t.Fatal(err)
		}
		got, failure := observeDeploymentDiagnostic(context.Background(), fixture)
		if failure != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("success mismatch: %#v", failure)
		}
		if !reflect.DeepEqual(old.operations, fixture.operations) || !reflect.DeepEqual(old.closed, fixture.closed) {
			t.Fatal("forwarding changed")
		}
	}
}

func TestDeploymentDiagnosticInitialFailureCases(t *testing.T) {
	for _, tc := range []struct {
		name, class, operation, target, outcome string
		setup                                   func(*deploymentFixture)
	}{
		{"late-errno", "unavailable", "open_global", "boot", "failed", func(f *deploymentFixture) {
			f.failAt = 2
			f.failAfter = true
			f.faultError = fmt.Errorf("secret: %w", syscall.EIO)
		}},
		{"parser", "invalid", "read", "boot", "returned", func(f *deploymentFixture) {
			f.mutate = func(op string, _ int, v any) any {
				if op == "read:global:0:0" {
					return []byte("secret-proc-canary")
				}
				return v
			}
		}},
		{"denial-then-other-failure", "unavailable", "namespace_stat", "caller.ns.pid", "failed", func(f *deploymentFixture) {
			f.namespaceErrors = map[string]map[int]error{"nsstat:0:0": {1: syscall.EACCES}, "nsstat:0:1": {1: syscall.EIO}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeploymentFixture()
			tc.setup(f)
			got, fail := observeDeploymentDiagnostic(context.Background(), f)
			if got != (DeploymentObservation{}) || fail == nil {
				t.Fatalf("expected zero and failure: %#v %#v", got, fail)
			}
			if fail.Validate() != nil || fail.Stage != "observe" || fail.ErrorClass != tc.class {
				t.Fatalf("bad failure: %#v", fail)
			}
			p := fail.LastPrimitive
			if p == nil || p.Operation != tc.operation || p.Target != tc.target || p.Outcome != tc.outcome {
				t.Fatalf("bad primitive: %#v", p)
			}
			if tc.outcome == "failed" && (p.Errno == nil || *p.Errno != 5) {
				t.Fatalf("errno: %#v", p)
			}
			deploymentAssertClosed(t, f)
		})
	}
}

func TestDeploymentDiagnosticNoIO(t *testing.T) {
	f := newDeploymentFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx          context.Context
		backend      deploymentBackend
		stage, class string
	}{
		{nil, f, "entry", "invalid"}, {context.Background(), nil, "entry", "invalid"}, {ctx, f, "observe", "cancelled"},
	} {
		got, fail := observeDeploymentDiagnostic(tc.ctx, tc.backend)
		if got != (DeploymentObservation{}) || fail == nil || fail.Stage != tc.stage || fail.ErrorClass != tc.class || fail.LastPrimitive != nil || fail.Validate() != nil {
			t.Fatalf("no IO result: %#v %#v", got, fail)
		}
	}
	if len(f.operations) != 0 {
		t.Fatal(f.operations)
	}
}

func TestDeploymentDiagnosticFaultAndCancellationParity(t *testing.T) {
	baseline := newDeploymentFixture()
	if _, err := observeDeployment(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	for i, op := range baseline.operations {
		for _, after := range []bool{false, true} {
			for _, cancelled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%03d-%s-after%t-cancel%t", i+1, op, after, cancelled), func(t *testing.T) {
					original, fixture := newDeploymentFixture(), newDeploymentFixture()
					oldctx, oldcancel := context.WithCancel(context.Background())
					defer oldcancel()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					for _, f := range []*deploymentFixture{original, fixture} {
						f.failAfter = after
						if cancelled {
							f.cancelAt = i + 1
						} else {
							f.failAt = i + 1
							f.faultError = fmt.Errorf("secret-syscall-canary: %w", syscall.EIO)
						}
					}
					original.cancel, fixture.cancel = oldcancel, cancel
					want, err := observeDeployment(oldctx, original)
					got, failure := observeDeploymentDiagnostic(ctx, fixture)
					class := "unavailable"
					if cancelled {
						class = "cancelled"
					}
					if err == nil || want != (DeploymentObservation{}) || got != want || failure == nil || failure.ErrorClass != class || failure.Validate() != nil {
						t.Fatalf("parity: %#v %#v %v", got, failure, err)
					}
					if !reflect.DeepEqual(original.operations, fixture.operations) || !reflect.DeepEqual(original.closed, fixture.closed) {
						t.Fatal("forwarding/cleanup differs")
					}
					p := failure.LastPrimitive
					if p == nil || p.Operation != deploymentDiagnosticExpectedOperation(op) {
						t.Fatalf("last call %s: %#v", op, p)
					}
					if cancelled {
						outcome := "failed"
						if after {
							outcome = "returned"
						}
						if p.Outcome != outcome || p.Errno != nil {
							t.Fatalf("cancellation context: %#v", p)
						}
					} else if p.Outcome != "failed" || p.Errno == nil || *p.Errno != 5 {
						t.Fatalf("current errno: %#v", p)
					}
					deploymentAssertClosed(t, fixture)
				})
			}
		}
	}
	t.Logf("replayed %d primitive positions x fail/cancel x before/after", len(baseline.operations))
}

func deploymentDiagnosticExpectedOperation(op string) string {
	switch strings.Split(op, ":")[0] {
	case "global":
		return "open_global"
	case "proc":
		return "open_proc"
	case "leaf":
		return "open_leaf"
	case "nsopen":
		return "open_namespace"
	case "nsstat":
		return "namespace_stat"
	case "fs":
		return "statfs"
	default:
		return strings.Split(op, ":")[0]
	}
}

func TestDeploymentDiagnosticReturnedRejection(t *testing.T) {
	for _, tc := range []struct {
		name, op, target string
		occurrence       int
		change           func(any) any
	}{
		{"invalid-kernel", "kernel", "kernel", 1, func(any) any { return "secret kernel" }},
		{"unsupported-cap", "read:global:1:0", "cap_last_cap", 1, func(any) any { return []byte("64\n") }},
		{"canonical-identity-drift", "stat:proc:0", "caller", 2, func(v any) any { m := v.(deploymentMetadata); m.inode++; return m }},
		{"boot-drift", "read:global:0:0", "clock", 2, func(any) any { return []byte("10112233-4455-6677-8899-aabbccddeeff\n") }},
		{"denial-drift", "nsstat:0:0", "clock", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeploymentFixture()
			f.mutate = func(op string, n int, v any) any {
				if op == tc.op && n == tc.occurrence {
					return tc.change(v)
				}
				return v
			}
			if tc.name == "denial-drift" {
				f.namespaceErrors = map[string]map[int]error{"nsstat:0:0": {1: syscall.EACCES}}
			}
			_, failure := observeDeploymentDiagnostic(context.Background(), f)
			class := "invalid"
			if tc.name == "unsupported-cap" {
				class = "unsupported"
			}
			if failure == nil || failure.ErrorClass != class || failure.Validate() != nil {
				t.Fatalf("failure %#v", failure)
			}
			p := failure.LastPrimitive
			if p == nil || p.Target != tc.target || p.Outcome != "returned" || p.Errno != nil {
				t.Fatalf("returned context %#v", p)
			}
			deploymentAssertClosed(t, f)
		})
	}
}

func TestDeploymentDiagnosticCurrentErrorOnly(t *testing.T) {
	for _, tc := range []struct {
		err   error
		class string
	}{
		{errors.New("secret-error-canary"), "unavailable"},
		{syscall.Errno(0), "unavailable"}, {syscall.Errno(4096), "unavailable"},
		{ErrDeploymentInvalid, "invalid"}, {ErrDeploymentCancelled, "cancelled"}, {ErrDeploymentUnsupported, "unsupported"},
		{fmt.Errorf("secret: %w", ErrDeploymentInvalid), "unavailable"},
	} {
		f := newDeploymentFixture()
		f.failAt = 1
		f.faultError = tc.err
		_, failure := observeDeploymentDiagnostic(context.Background(), f)
		if failure == nil || failure.ErrorClass != tc.class || failure.LastPrimitive.Errno != nil || failure.Validate() != nil {
			t.Fatalf("error mapping %#v", failure)
		}
		raw, err := json.Marshal(failure)
		if err != nil || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), tc.err.Error()) {
			t.Fatalf("raw data escaped: %s", raw)
		}
	}
	for _, errno := range []uint32{1, 4095} {
		f := newDeploymentFixture()
		f.failAt = 1
		f.faultError = fmt.Errorf("secret: %w", syscall.Errno(errno))
		_, failure := observeDeploymentDiagnostic(context.Background(), f)
		if failure == nil || failure.LastPrimitive.Errno == nil || *failure.LastPrimitive.Errno != errno {
			t.Fatal(failure)
		}
	}
}

func TestDeploymentDiagnosticDecoratorFDTargets(t *testing.T) {
	ctx := context.Background()
	for _, owner := range []struct {
		value  deploymentOwner
		target string
	}{{deploymentCaller, "caller"}, {deploymentPID1, "pid1"}} {
		f := newDeploymentFixture()
		b := &deploymentDiagnosticBackend{backend: f}
		parent, err := b.openProc(ctx, owner.value)
		if err != nil {
			t.Fatal(err)
		}
		if b.last.Target != owner.target {
			t.Fatal(b.last)
		}
		for _, leaf := range []struct {
			value  deploymentLeaf
			suffix string
		}{{deploymentStatus, ".status"}, {deploymentStat, ".stat"}, {deploymentCgroup, ".cgroup"}} {
			fd, err := b.openLeaf(ctx, parent, leaf.value)
			if err != nil {
				t.Fatal(err)
			}
			target := owner.target + leaf.suffix
			if b.last.Target != target {
				t.Fatal(b.last)
			}
			if _, err = b.read(ctx, fd, 4096); err != nil || b.last.Target != target {
				t.Fatal(b.last, err)
			}
			before := *b.last
			b.close(fd)
			if !reflect.DeepEqual(*b.last, before) || b.target(fd) != "unknown" {
				t.Fatal("close changed context or retained fd")
			}
		}
		for _, ns := range []struct {
			value  deploymentNS
			suffix string
		}{{deploymentUserNS, ".ns.user"}, {deploymentPIDNS, ".ns.pid"}, {deploymentMountNS, ".ns.mnt"}, {deploymentCgroupNS, ".ns.cgroup"}} {
			target := owner.target + ns.suffix
			if _, err = b.namespaceStat(ctx, parent, ns.value); err != nil || b.last.Target != target {
				t.Fatal(b.last, err)
			}
			fd, err := b.openNamespace(ctx, parent, ns.value)
			if err != nil || b.last.Target != target {
				t.Fatal(b.last, err)
			}
			if _, err = b.stat(ctx, fd); err != nil || b.last.Target != target {
				t.Fatal(b.last, err)
			}
			if _, err = b.statfs(ctx, fd); err != nil || b.last.Target != target {
				t.Fatal(b.last, err)
			}
			b.close(fd)
		}
		b.close(parent)
		deploymentAssertClosed(t, f)
	}
	// A late error still transfers descriptor ownership and target context.
	f := newDeploymentFixture()
	f.failAt = 1
	f.failAfter = true
	f.faultError = syscall.EIO
	b := &deploymentDiagnosticBackend{backend: f}
	fd, err := b.openGlobal(ctx, deploymentBoot)
	if fd < 0 || err != syscall.EIO || b.target(fd) != "boot" {
		t.Fatal(fd, err, b.target(fd))
	}
	last := *b.last
	b.close(fd)
	if !reflect.DeepEqual(*b.last, last) || b.target(fd) != "unknown" || f.closed[fd] != 1 {
		t.Fatal("late fd cleanup")
	}
	// The fixture can reuse the same number for a different logical owner.
	f.failAt = 0
	f.next = fd - 1
	reused, err := b.openProc(ctx, deploymentPID1)
	if err != nil || reused != fd || b.target(fd) != "pid1" {
		t.Fatal("fd reuse")
	}
	b.close(fd)
	if f.closed[fd] != 2 || len(f.live) != 0 {
		t.Fatal("reuse cleanup")
	}
}

func TestDeploymentDiagnosticDecoratorUnknownTargets(t *testing.T) {
	ctx := context.Background()
	f := newDeploymentFixture()
	b := &deploymentDiagnosticBackend{backend: f}
	calls := []func(){
		func() { fd, _ := b.openGlobal(ctx, deploymentGlobal(255)); b.close(fd) },
		func() { fd, _ := b.openProc(ctx, deploymentOwner(255)); b.close(fd) },
		func() { fd, _ := b.openLeaf(ctx, -123, deploymentStatus); b.close(fd) },
		func() { fd, _ := b.openNamespace(ctx, -123, deploymentUserNS); b.close(fd) },
		func() { b.namespaceStat(ctx, -123, deploymentUserNS) },
		func() { b.stat(ctx, -123) }, func() { b.statfs(ctx, -123) }, func() { b.read(ctx, -123, 123) },
	}
	for _, call := range calls {
		call()
		if b.last.Target != "unknown" {
			t.Fatal(b.last)
		}
	}
	parent, _ := b.openProc(ctx, deploymentCaller)
	for _, call := range []func(){
		func() { fd, _ := b.openLeaf(ctx, parent, deploymentLeaf(255)); b.close(fd) },
		func() { fd, _ := b.openNamespace(ctx, parent, deploymentNS(255)); b.close(fd) },
		func() { b.namespaceStat(ctx, parent, deploymentNS(255)) },
	} {
		call()
		if b.last.Target != "unknown" {
			t.Fatal(b.last)
		}
	}
	b.close(parent)
}

func TestDeploymentDiagnosticDecoratorReturnParity(t *testing.T) {
	run := func(b deploymentBackend) []any {
		ctx := context.Background()
		var values []any
		now, e := b.now(ctx)
		values = append(values, now, e)
		p, tid, e := b.ids(ctx)
		values = append(values, p, tid, e)
		kernel, e := b.kernel(ctx)
		values = append(values, kernel, e)
		global, e := b.openGlobal(ctx, deploymentLastCap)
		values = append(values, global, e)
		raw, e := b.read(ctx, global, 32)
		values = append(values, raw, e)
		b.close(global)
		parent, e := b.openProc(ctx, deploymentCaller)
		values = append(values, parent, e)
		leaf, e := b.openLeaf(ctx, parent, deploymentStatus)
		values = append(values, leaf, e)
		raw, e = b.read(ctx, leaf, 65536)
		values = append(values, raw, e)
		b.close(leaf)
		meta, e := b.namespaceStat(ctx, parent, deploymentUserNS)
		values = append(values, meta, e)
		ns, e := b.openNamespace(ctx, parent, deploymentUserNS)
		values = append(values, ns, e)
		meta, e = b.stat(ctx, ns)
		values = append(values, meta, e)
		fs, e := b.statfs(ctx, ns)
		values = append(values, fs, e)
		b.close(ns)
		b.close(parent)
		return values
	}
	// Each primitive's result, including values produced before a late error, must survive.
	baseline := newDeploymentFixture()
	run(baseline)
	for at := 0; at <= len(baseline.operations); at++ {
		old, f := newDeploymentFixture(), newDeploymentFixture()
		for _, fixture := range []*deploymentFixture{old, f} {
			fixture.failAt = at
			fixture.failAfter = true
			fixture.faultError = syscall.EIO
		}
		want := run(old)
		got := run(&deploymentDiagnosticBackend{backend: f})
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(old.operations, f.operations) || !reflect.DeepEqual(old.closed, f.closed) {
			t.Fatalf("return/call parity at %d", at)
		}
	}
}

func TestDeploymentDiagnosticFailureValidation(t *testing.T) {
	stages := []string{"entry", "observe", "encode", "size", "write"}
	classes := []string{"invalid", "unavailable", "cancelled", "unsupported", "internal_invalid_result", "encoding_failed", "size_exceeded", "write_failed", "", "secret"}
	valid := map[string]map[string]bool{
		"entry": {"invalid": true}, "observe": {"invalid": true, "unavailable": true, "cancelled": true, "unsupported": true, "internal_invalid_result": true},
		"encode": {"encoding_failed": true}, "size": {"size_exceeded": true}, "write": {"write_failed": true},
	}
	for _, stage := range append(stages, "", "secret") {
		for _, class := range classes {
			v := DeploymentFailureObservation{Schema: DeploymentFailureSchema, Kind: "diagnostic_only", Stage: stage, ErrorClass: class}
			err := v.Validate()
			if (err == nil) != valid[stage][class] || err != nil && err != ErrDeploymentInvalid {
				t.Fatalf("stage/class %s/%s: %v", stage, class, err)
			}
			v.LastPrimitive = &DeploymentPrimitiveObservation{Operation: "now", Target: "clock", Outcome: "returned"}
			if (v.Validate() == nil) != (valid[stage][class] && stage == "observe") {
				t.Fatalf("last stage %s/%s", stage, class)
			}
		}
	}
	operations := []string{"now", "ids", "kernel", "open_global", "open_proc", "open_leaf", "open_namespace", "namespace_stat", "stat", "statfs", "read"}
	targets := []string{"clock", "identity", "kernel", "boot", "cap_last_cap", "caller", "pid1", "unknown", "caller.status", "caller.stat", "caller.cgroup", "caller.ns.user", "caller.ns.pid", "caller.ns.mnt", "caller.ns.cgroup", "pid1.status", "pid1.stat", "pid1.cgroup", "pid1.ns.user", "pid1.ns.pid", "pid1.ns.mnt", "pid1.ns.cgroup"}
	for _, op := range operations {
		for _, target := range targets {
			for _, outcome := range []string{"returned", "failed"} {
				v := deploymentDiagnosticFailure("observe", "unavailable")
				v.LastPrimitive = &DeploymentPrimitiveObservation{Operation: op, Target: target, Outcome: outcome}
				if err := v.Validate(); err != nil {
					t.Fatal(v, err)
				}
				raw, err := json.Marshal(v)
				if err != nil || len(raw)+1 > 4096 {
					t.Fatalf("bounded failure %d %v", len(raw)+1, err)
				}
			}
		}
	}
	for _, field := range []string{"schema", "kind", "operation", "target", "outcome"} {
		for _, bad := range []string{"", "secret-canary", "/proc/self/status", "caller.ns.invalid"} {
			v := deploymentDiagnosticFailure("observe", "invalid")
			v.LastPrimitive = &DeploymentPrimitiveObservation{Operation: "read", Target: "caller.status", Outcome: "returned"}
			switch field {
			case "schema":
				v.Schema = bad
			case "kind":
				v.Kind = bad
			case "operation":
				v.LastPrimitive.Operation = bad
			case "target":
				v.LastPrimitive.Target = bad
			case "outcome":
				v.LastPrimitive.Outcome = bad
			}
			if v.Validate() != ErrDeploymentInvalid {
				t.Fatalf("accepted invalid %s %q", field, bad)
			}
		}
	}
	for _, n := range []uint32{0, 1, 4095, 4096, ^uint32(0)} {
		for _, outcome := range []string{"returned", "failed"} {
			v := deploymentDiagnosticFailure("observe", "unavailable")
			v.LastPrimitive = &DeploymentPrimitiveObservation{Operation: "read", Target: "unknown", Outcome: outcome, Errno: &n}
			if (v.Validate() == nil) != (outcome == "failed" && n >= 1 && n <= 4095) {
				t.Fatalf("errno %s %d", outcome, n)
			}
		}
	}
	v := deploymentDiagnosticFailure("entry", "invalid")
	raw, err := json.Marshal(v)
	if err != nil || string(raw) != `{"schema":"switchback.capacity.deployment_failure.v1","kind":"diagnostic_only","stage":"entry","error_class":"invalid"}` {
		t.Fatalf("JSON contract: %s %v", raw, err)
	}
}

func TestDeploymentDiagnosticUnsupportedWrapper(t *testing.T) {
	if runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		t.Skip("native entry is not invoked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx          context.Context
		stage, class string
	}{{nil, "entry", "invalid"}, {ctx, "observe", "cancelled"}, {context.Background(), "observe", "unsupported"}} {
		got, failure := ObserveDeploymentDiagnostic(tc.ctx)
		if got != (DeploymentObservation{}) || failure == nil || failure.Stage != tc.stage || failure.ErrorClass != tc.class || failure.LastPrimitive != nil || failure.Validate() != nil {
			t.Fatal(got, failure)
		}
	}
}

func TestDeploymentDiagnosticDecoratorLateAcquisitions(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		acquire      func(*deploymentDiagnosticBackend, int) (int, error)
	}{
		{"global", "boot", func(b *deploymentDiagnosticBackend, _ int) (int, error) {
			return b.openGlobal(context.Background(), deploymentBoot)
		}},
		{"proc", "pid1", func(b *deploymentDiagnosticBackend, _ int) (int, error) {
			return b.openProc(context.Background(), deploymentPID1)
		}},
		{"leaf", "caller.status", func(b *deploymentDiagnosticBackend, p int) (int, error) {
			return b.openLeaf(context.Background(), p, deploymentStatus)
		}},
		{"namespace", "caller.ns.user", func(b *deploymentDiagnosticBackend, p int) (int, error) {
			return b.openNamespace(context.Background(), p, deploymentUserNS)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeploymentFixture()
			b := &deploymentDiagnosticBackend{backend: f}
			parent, err := b.openProc(context.Background(), deploymentCaller)
			if err != nil {
				t.Fatal(err)
			}
			f.failAt = 2
			f.failAfter = true
			f.faultError = syscall.EIO
			fd, err := tc.acquire(b, parent)
			if fd < 0 || err != syscall.EIO || b.target(fd) != tc.target || b.last.Target != tc.target || b.last.Errno == nil || *b.last.Errno != 5 {
				t.Fatal(fd, err, b.last)
			}
			last := *b.last
			b.close(fd)
			b.close(parent)
			if !reflect.DeepEqual(*b.last, last) || len(b.targets) != 0 {
				t.Fatal("cleanup overwrote last context or retained target")
			}
			deploymentAssertClosed(t, f)
		})
	}
}

func TestDeploymentDiagnosticFailureJSONPrivacy(t *testing.T) {
	f := newDeploymentFixture()
	f.mutate = func(op string, _ int, v any) any {
		if op == "read:leaf:0:0" {
			return []byte("secret-proc-canary /proc/1234/status")
		}
		return v
	}
	got, failure := observeDeploymentDiagnostic(context.Background(), f)
	if got != (DeploymentObservation{}) || failure == nil {
		t.Fatal(got, failure)
	}
	raw, err := json.Marshal(failure)
	if err != nil || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "/proc/") || strings.Contains(string(raw), "1234") {
		t.Fatalf("raw proc escaped: %s %v", raw, err)
	}
	want := `{"schema":"switchback.capacity.deployment_failure.v1","kind":"diagnostic_only","stage":"observe","error_class":"invalid","last_primitive":{"operation":"read","target":"caller.status","outcome":"returned"}}`
	if string(raw) != want {
		t.Fatalf("JSON contract: %s", raw)
	}
	f = newDeploymentFixture()
	f.failAt = 1
	f.faultError = fmt.Errorf("secret-error-canary: %w", syscall.EIO)
	_, failure = observeDeploymentDiagnostic(context.Background(), f)
	raw, err = json.Marshal(failure)
	want = `{"schema":"switchback.capacity.deployment_failure.v1","kind":"diagnostic_only","stage":"observe","error_class":"unavailable","last_primitive":{"operation":"now","target":"clock","outcome":"failed","errno":5}}`
	if err != nil || string(raw) != want {
		t.Fatalf("errno JSON contract: %s %v", raw, err)
	}
}

func TestDeploymentDiagnosticLongestFailurePacket(t *testing.T) {
	errno := uint32(4095)
	v := DeploymentFailureObservation{
		Schema: DeploymentFailureSchema, Kind: "diagnostic_only", Stage: "observe", ErrorClass: "internal_invalid_result",
		LastPrimitive: &DeploymentPrimitiveObservation{Operation: "open_namespace", Target: "caller.ns.cgroup", Outcome: "returned"},
	}
	raw, err := json.Marshal(v)
	if v.Validate() != nil || err != nil || len(raw)+1 > 4096 {
		t.Fatal(v, err, len(raw)+1)
	}
	v.LastPrimitive.Outcome = "failed"
	v.LastPrimitive.Errno = &errno
	raw, err = json.Marshal(v)
	if v.Validate() != nil || err != nil || len(raw)+1 > 4096 {
		t.Fatal(v, err, len(raw)+1)
	}
	t.Logf("longest fixed failure packet including LF: %d bytes", len(raw)+1)
}
