package main

import (
	"bytes"
	"context"
	"errors"
	"runtime"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	ipc "github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"strings"
	"testing"
)

func TestInstalledObserverWaitBeforeKeyAndListener(t *testing.T) {
	for _, failure := range []string{"pending", "invalid", "cancel", "timeout", "role", "success"} {
		t.Run(failure, func(t *testing.T) {
			var events []string
			ops := operations{true, func() ([]byte, error) { events = append(events, "key"); return make([]byte, 32), nil }, func(context.Context, []byte, string) error { events = append(events, "listener"); return nil }, nil}
			ops.serveInstalled = ops.serve
			installed := installedOperations{func(context.Context, string, string) (*capacitysnapshot.DeploymentQualification, error) {
				events = append(events, "wait")
				if failure != "role" && failure != "success" {
					return nil, errors.New(failure)
				}
				return &capacitysnapshot.DeploymentQualification{}, nil
			}, func(context.Context, *capacitysnapshot.DeploymentQualification) (string, error) {
				events = append(events, "role-env")
				if failure == "role" {
					return "", errors.New("private-canary")
				}
				return "sha256:" + strings.Repeat("a", 64), nil
			}}
			var out bytes.Buffer
			code := runInstalled(context.Background(), []string{"serve-installed"}, &out, ops, installed)
			want := 1
			if failure == "role" {
				want = 2
			}
			if failure == "success" {
				want = 4
				if code != 0 {
					t.Fatal(code)
				}
			} else if code != 1 {
				t.Fatal(code)
			}
			if len(events) != want || strings.Contains(out.String(), "private-canary") {
				t.Fatalf("events %v output %s", events, &out)
			}
		})
	}
}
func TestInstalledObserverRejectExtraArgumentsBeforeWait(t *testing.T) {
	var out bytes.Buffer
	if runInstalled(context.Background(), []string{"serve-installed", "extra"}, &out, operations{supported: true}, installedOperations{}) != 2 {
		t.Fatal("accepted args")
	}
}

type observerServiceFunc func(context.Context) error

func (f observerServiceFunc) Serve(ctx context.Context) error { return f(ctx) }

func TestInstalledObserverSelectsQualificationServerAfterBarrier(t *testing.T) {
	for _, installedMode := range []bool{false, true} {
		events := []string{}
		factory := func(name string) observerServerFactory {
			return func([]byte, string) (observerService, error) {
				events = append(events, name)
				return observerServiceFunc(func(context.Context) error { events = append(events, "serve"); return nil }), nil
			}
		}
		ops := observerServiceOperations(true, func() ([]byte, error) { events = append(events, "key"); return []byte("private-test-key"), nil }, factory("legacy"), factory("qualification"))
		installed := installedOperations{await: func(context.Context, string, string) (*capacitysnapshot.DeploymentQualification, error) {
			events = append(events, "wait")
			return &capacitysnapshot.DeploymentQualification{}, nil
		}, verify: func(context.Context, *capacitysnapshot.DeploymentQualification) (string, error) {
			events = append(events, "verify")
			return "sha256:" + strings.Repeat("a", 64), nil
		}}
		var out bytes.Buffer
		var code int
		if installedMode {
			code = runInstalled(context.Background(), []string{"serve-installed"}, &out, ops, installed)
		} else {
			code = run(context.Background(), []string{"serve", "sha256:" + strings.Repeat("a", 64)}, &out, ops)
		}
		want := "key,legacy,serve"
		if installedMode {
			want = "wait,verify,key,qualification,serve"
		}
		if code != 0 || strings.Join(events, ",") != want {
			t.Fatal(code, events, out.String())
		}
	}
}

func TestInstalledObserverRoleRejectsLegacyCapsAndIdentityDrift(t *testing.T) {
	expected := capacitysnapshot.DeploymentRoleBinding{CgroupPath: "/sys/fs/cgroup" + ipc.QualificationObserverCgroupPath, Architecture: runtime.GOARCH, Capabilities: 0x200000, BinaryDigest: "binary", GuestBootID: "boot"}
	current := capacityprobe.RoleObservation{BinaryDigest: "binary", Boot: "boot", Process: capacityprobe.DeploymentProcessObservation{UnifiedCgroupPath: ipc.QualificationObserverCgroupPath, CapPrm: "0000000000200000", CapEff: "0000000000200000", CapBnd: "0000000000200000", CapInh: "0000000000000000", CapAmb: "0000000000000000", NoNewPrivs: 1, Seccomp: 2}}
	for _, failure := range []string{"good", "legacy-binding", "wrong-binding", "current-cgroup", "expected-caps", "effective-caps", "bounding-caps", "uid", "nnp", "seccomp", "binary", "boot"} {
		t.Run(failure, func(t *testing.T) {
			e, c := expected, current
			switch failure {
			case "legacy-binding":
				e.CgroupPath = "/sys/fs/cgroup" + ipc.ObserverCgroupPath
			case "wrong-binding":
				e.CgroupPath += "/child"
			case "current-cgroup":
				c.Process.UnifiedCgroupPath += "/child"
			case "expected-caps":
				e.Capabilities = 0
			case "effective-caps":
				c.Process.CapEff = "0000000000000000"
			case "bounding-caps":
				c.Process.CapBnd = "0000000000000000"
			case "uid":
				c.Process.UIDs[1] = 1
			case "nnp":
				c.Process.NoNewPrivs = 0
			case "seccomp":
				c.Process.Seccomp = 0
			case "binary":
				c.BinaryDigest = "other"
			case "boot":
				c.Boot = "old"
			}
			if installedObserverRoleMatches(e, c) != (failure == "good") {
				t.Fatal("role drift accepted", failure)
			}
		})
	}
}
