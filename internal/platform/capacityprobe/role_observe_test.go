package capacityprobe

import (
	"context"
	"strings"
	"testing"
)

func TestRoleObservationSelfOnlyAndFailureCleanup(t *testing.T) {
	f := newDeploymentFixture()
	if _, e := observeRoleProcess(context.Background(), f); e != nil {
		t.Fatal(e)
	}
	deploymentAssertClosed(t, f)
	for _, op := range f.operations {
		if strings.Contains(op, "namespace") || strings.Contains(op, "pid1") {
			t.Fatal("Backend touched PID1/namespace", op)
		}
	}
	n := len(f.operations)
	for step := 1; step <= n; step++ {
		for _, late := range []bool{false, true} {
			g := newDeploymentFixture()
			g.failAt = step
			g.failAfter = late
			if _, e := observeRoleProcess(context.Background(), g); e == nil {
				t.Fatalf("fault %d late=%v accepted", step, late)
			}
			deploymentAssertClosed(t, g)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := newDeploymentFixture()
	if _, e := observeRoleProcess(ctx, g); e == nil || len(g.operations) != 0 {
		t.Fatal("cancel issued I/O")
	}
}

func TestRoleObservationUsesCallingThreadIdentity(t *testing.T) {
	f := newDeploymentFixture()
	got, e := observeRoleProcess(context.Background(), f)
	if e != nil {
		t.Fatal(e)
	}
	// The fixture deliberately has distinct process and thread identities.
	if got.Process.TGID != 301 || got.Process.PID != 301 || got.Process.TID != 307 {
		t.Fatalf("wrong caller thread: %+v", got.Process)
	}
	deploymentAssertClosed(t, f)
}
