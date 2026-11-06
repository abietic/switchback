//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
	"github.com/moby/moby/client"
)

func qualificationRuntimeReadback(ctx context.Context) (qualificationRuntimePreflight, error) {
	kit, err := qualificationMaterialKit(ctx, qualificationMaterialRead)
	if err != nil {
		return qualificationRuntimePreflight{}, err
	}
	if err = qualificationVerifyEngineDeployment(ctx, kit); err != nil {
		return qualificationRuntimePreflight{}, err
	}
	engine, e := qualificationFixedEngineClient()
	if e != nil {
		return qualificationRuntimePreflight{}, e
	}
	defer engine.Close()
	info, e := engine.Info(ctx, client.InfoOptions{})
	if e != nil || info.Info.NCPU <= 0 || info.Info.MemTotal <= 0 {
		return qualificationRuntimePreflight{}, errQualification
	}
	core := CoreDomain{ID: "core", CgroupPath: qualificationCoreCgroup, CPUQuotaMicros: 120000, CPUPeriodMicros: 100000, MemoryMax: 3 << 30, PIDsMax: 1024}
	actual, e := qualificationReadCoreLimits(ctx, qualificationCandidateRecord{Trust: Trust{Core: []CoreDomain{core}}})
	if e != nil || len(actual.CPU) != 1 || actual.CPU[0] != "120000 100000\n" || actual.Memory[0] != "3221225472\n" || actual.PIDs[0] != "1024\n" || actual.Swap[0] != "0\n" {
		return qualificationRuntimePreflight{}, fmt.Errorf("configured core aggregate differs")
	}
	return qualificationRuntimeBudget(info.Info.ID, uint64(info.Info.NCPU), uint64(info.Info.MemTotal), []CoreDomain{core})
}
func qualificationRuntimePreflightFixed(ctx context.Context) error {
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	if e = d.claim(ctx, "runtime-preflight"); e != nil {
		return e
	}
	result, e := qualificationRuntimeReadback(ctx)
	if e != nil {
		return e
	}
	return d.put(ctx, "runtime-preflight.json", result)
}
func qualificationRuntimePreflightVerify(ctx context.Context) error {
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	var expected qualificationRuntimePreflight
	if e = d.decode(ctx, "runtime-preflight.json", &expected); e != nil {
		return e
	}
	actual, e := qualificationRuntimeReadback(ctx)
	if e != nil || actual != expected {
		return fmt.Errorf("prepared runtime prerequisite changed")
	}
	return nil
}

func qualificationCoreEngineLeader(ctx context.Context, role qualificationRole) (uint64, error) {
	if role.Role != "web" && role.Role != "postgres" {
		return 0, nil
	}
	want := uint64(101)
	if role.Role == "postgres" {
		want = 999
	}
	if role.Kind != "container" || role.UID != want {
		return 0, errQualification
	}
	engine, e := qualificationFixedEngineClient()
	if e != nil {
		return 0, e
	}
	defer engine.Close()
	i, e := engine.ContainerInspect(ctx, role.ContainerID, client.ContainerInspectOptions{})
	if e != nil {
		return 0, e
	}
	c := i.Container
	if c.ID != role.ContainerID || c.Image != role.ImageDigest || c.State == nil || !c.State.Running || c.State.Pid <= 0 || c.Config == nil || c.Config.User != fmt.Sprintf("%d:%d", want, want) {
		return 0, errQualification
	}
	return uint64(c.State.Pid), nil
}
