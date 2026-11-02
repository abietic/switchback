package main

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	ipc "github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

const observerQualificationDirectory = "/mnt/lima-sbquota/core/capacity-publisher-config"
const observerInstalledEnvironment = "/etc/switchback/created-layer-observer/deployment.env"

type installedOperations struct {
	await  func(context.Context, string, string) (*capacitysnapshot.DeploymentQualification, error)
	verify func(context.Context, *capacitysnapshot.DeploymentQualification) (string, error)
}

func runInstalled(ctx context.Context, args []string, out io.Writer, ops operations, installed installedOperations) int {
	if ctx == nil || ctx.Err() != nil || len(args) != 1 || args[0] != "serve-installed" || !ops.supported {
		fmt.Fprintln(out, "observer: invalid installed invocation")
		return 2
	}
	q, err := installed.await(ctx, observerQualificationDirectory+"/admission-manifest.json", observerQualificationDirectory+"/trust.json")
	if err != nil || q == nil || ctx.Err() != nil {
		fmt.Fprintln(out, "observer: qualification unavailable")
		return 1
	}
	digest, err := installed.verify(ctx, q)
	if err != nil || ctx.Err() != nil {
		fmt.Fprintln(out, "observer: installed identity unavailable")
		return 1
	}
	if ops.serveInstalled == nil {
		return 1
	}
	ops.serve = ops.serveInstalled
	return run(ctx, []string{"serve", digest}, out, ops)
}

func verifyInstalledObserver(ctx context.Context, q *capacitysnapshot.DeploymentQualification) (string, error) {
	if err := q.Recheck(ctx); err != nil {
		return "", err
	}
	expected, err := q.RoleBinding("observer")
	if err != nil {
		return "", err
	}
	current, err := capacityprobe.ObserveRole(ctx)
	if err != nil {
		return "", err
	}
	if !installedObserverRoleMatches(expected, current) {
		return "", fmt.Errorf("observer role refused")
	}
	raw, err := readInstalledObserverEnvironment(ctx)
	if err != nil {
		return "", err
	}
	const prefix = "SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST="
	digest := strings.TrimSuffix(strings.TrimPrefix(string(raw), prefix), "\n")
	if string(raw) != prefix+digest+"\n" || !observerDigestPattern.MatchString(digest) {
		return "", fmt.Errorf("observer environment invalid")
	}
	// The full frozen recheck independently reads this same installed env and
	// requires its exact digest to match Trust. Environment never grants authority.
	if err := q.Recheck(ctx); err != nil {
		return "", err
	}
	return digest, nil
}

func installedObserverRoleMatches(expected capacitysnapshot.DeploymentRoleBinding, current capacityprobe.RoleObservation) bool {
	p := current.Process
	return expected.CgroupPath == "/sys/fs/cgroup"+ipc.QualificationObserverCgroupPath && !(expected.Architecture != runtime.GOARCH || expected.Capabilities != 0x200000 || current.BinaryDigest != expected.BinaryDigest || current.Boot != expected.GuestBootID || p.UIDs != [4]uint32{} || p.CapPrm != "0000000000200000" || p.CapEff != p.CapPrm || p.CapBnd != p.CapPrm || p.CapInh != "0000000000000000" || p.CapAmb != p.CapInh || p.NoNewPrivs != 1 || p.Seccomp != 2 || p.UnifiedCgroupPath != strings.TrimPrefix(expected.CgroupPath, "/sys/fs/cgroup"))
}
