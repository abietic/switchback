package docker_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"reflect"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
)

type projectAttributeProfile struct {
	DefaultAction   string `json:"defaultAction"`
	DefaultErrnoRet int    `json:"defaultErrnoRet"`
	Syscalls        []struct {
		Names  []string `json:"names"`
		Action string   `json:"action"`
		Args   []struct {
			Index    uint   `json:"index"`
			Value    uint64 `json:"value"`
			ValueTwo uint64 `json:"valueTwo"`
			Op       string `json:"op"`
		} `json:"args"`
	} `json:"syscalls"`
}

func TestProjectAttributeWorkerCreatePolicy(t *testing.T) {
	recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	if _, err := newProvisioner(t, classicOverlay2Engine(recording), &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart); err != nil {
		t.Fatal(err)
	}
	if len(recording.creates) != 2 {
		t.Fatalf("creates=%d", len(recording.creates))
	}
	var policies []string
	for _, option := range recording.creates[1].HostConfig.SecurityOpt {
		if strings.HasPrefix(option, "seccomp=") {
			policies = append(policies, strings.TrimPrefix(option, "seccomp="))
		}
	}
	if len(policies) != 1 {
		t.Fatalf("Worker must receive one complete seccomp policy, got %d", len(policies))
	}
	t.Logf("captured Worker seccomp SHA256: %x", sha256.Sum256([]byte(policies[0])))
	var profile projectAttributeProfile
	if err := json.Unmarshal([]byte(policies[0]), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.DefaultAction != "SCMP_ACT_ERRNO" || profile.DefaultErrnoRet != 1 {
		t.Fatalf("unsafe default: %+v", profile)
	}
	for _, rule := range profile.Syscalls {
		for _, name := range rule.Names {
			if name == "ioctl" && rule.Action == "SCMP_ACT_ALLOW" && len(rule.Args) == 0 {
				t.Fatal("unconditional ioctl ALLOW")
			}
		}
	}
	for _, command := range []uint64{0x401c5820, 0x40086602, 0x40046602} {
		for _, high := range []uint64{0, 1 << 32, 0xffffffff00000000} {
			if ioctlAllowed(t, profile, high|command) {
				t.Fatalf("setter %#x allowed", high|command)
			}
		}
	}
	for _, command := range []uint64{0x801c581f, 0x80086601, 0x80046601, 0x5401, 0x5413} {
		for _, high := range []uint64{0, 1 << 32, 0xffffffff00000000} {
			if !ioctlAllowed(t, profile, high|command) {
				t.Fatalf("getter/TTY %#x blocked", high|command)
			}
		}
	}
	for _, option := range recording.creates[0].HostConfig.SecurityOpt {
		if strings.HasPrefix(option, "seccomp=") {
			t.Fatal("Worker policy applied to secret populator")
		}
	}
}

// Interpret the captured production JSON directly, independently of the builder.
func ioctlAllowed(t *testing.T, profile projectAttributeProfile, command uint64) bool {
	t.Helper()
	for _, rule := range profile.Syscalls {
		for _, name := range rule.Names {
			if name != "ioctl" {
				continue
			}
			if rule.Action != "SCMP_ACT_ALLOW" || len(rule.Args) != 1 {
				t.Fatal("unexpected ioctl rule")
			}
			arg := rule.Args[0]
			if arg.Index != 1 || arg.Op != "SCMP_CMP_MASKED_EQ" {
				t.Fatal("unexpected ioctl comparator")
			}
			if command&arg.Value == arg.ValueTwo {
				return true
			}
		}
	}
	return false
}

func TestProjectAttributePolicyMismatchRetainsCreatedIdentity(t *testing.T) {
	for _, boundary := range []int{1, 2} {
		for _, replacement := range [][]string{
			{"no-new-privileges"},
			{"no-new-privileges", "seccomp=unconfined"},
			{"no-new-privileges", `seccomp={"defaultAction":"SCMP_ACT_ALLOW"}`},
		} {
			t.Run(fmt.Sprintf("inspect-%d/%s", boundary, strings.Join(replacement, ",")), func(t *testing.T) {
				e := &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound})}
				e.mutate = func(n int, inspected *container.InspectResponse) {
					if n == boundary {
						host := *inspected.HostConfig
						host.SecurityOpt = replacement
						inspected.HostConfig = &host
					}
				}
				got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
				if err == nil || err.Error() != "runtime container immutable configuration conflict" || got.ContainerID != "container-2" || !got.PhysicalEffectUnknown || !got.StartedAt.IsZero() {
					t.Fatalf("policy mismatch lost owner: result=%+v error=%v", got, err)
				}
				if e.inspectCount != boundary || !reflect.DeepEqual(e.started, []string{"container-1"}) || !reflect.DeepEqual(e.removed, []string{"container-1"}) || len(e.volumeRemoved) != 0 {
					t.Fatalf("policy mismatch started/cleaned Worker: inspections=%d starts=%v removes=%v volumes=%v", e.inspectCount, e.started, e.removed, e.volumeRemoved)
				}
			})
		}
	}
}

func TestProjectAttributeHistoricalPolicyMismatchIsNeverAdopted(t *testing.T) {
	recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	request := validRequest(t)
	if _, err := newProvisioner(t, classicOverlay2Engine(recording), &recordingVerifier{}).Provision(context.Background(), request, allowSyntheticPreStart); err != nil {
		t.Fatal(err)
	}
	created := recording.creates[1]
	for i, replacement := range [][]string{
		{"no-new-privileges"},
		{"no-new-privileges", "seccomp=unconfined"},
		{"no-new-privileges", `seccomp={"defaultAction":"SCMP_ACT_ALLOW"}`},
		// Semantically equivalent but different bytes must also fail exact equality.
		{"no-new-privileges", created.HostConfig.SecurityOpt[1] + " "},
	} {
		t.Run([]string{"missing", "unconfined", "tampered", "nonexact bytes"}[i], func(t *testing.T) {
			config := *created.Config
			config.Image = created.Image
			host := *created.HostConfig
			host.SecurityOpt = replacement
			e := &recordingEngine{inspect: client.ContainerInspectResult{Container: container.InspectResponse{
				ID: "historical-worker", Config: &config, HostConfig: &host, State: &container.State{Running: true},
			}}}
			verifier := &recordingVerifier{}
			got, err := newProvisioner(t, classicOverlay2Engine(e), verifier).Provision(context.Background(), request, allowSyntheticPreStart)
			if err == nil || err.Error() != "runtime container immutable configuration conflict" || got.ContainerID != "historical-worker" || !got.PhysicalEffectUnknown || !got.StartedAt.IsZero() {
				t.Fatalf("historical mismatch lost owner: result=%+v error=%v", got, err)
			}
			if e.inspections != 1 || len(e.creates) != 0 || len(e.started) != 0 || len(e.removed) != 0 || len(e.volumeCreates) != 0 || len(e.volumeRemoved) != 0 || e.volumeInspections != 0 || len(verifier.paths) != 0 {
				t.Fatal("historical mismatch mutated existing container or secrets")
			}
		})
	}
}

func TestProjectAttributeUnsupportedEngineFailsBeforeSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name, arch string
		options    []string
	}{
		{name: "missing architecture", options: []string{"name=seccomp"}},
		{name: "unknown architecture", arch: "riscv64", options: []string{"name=seccomp"}},
		{name: "wrong architecture case", arch: "AMD64", options: []string{"name=seccomp"}},
		{name: "missing support", arch: "amd64"},
		{name: "substring support", arch: "amd64", options: []string{"seccomp"}},
		{name: "prefix collision", arch: "amd64", options: []string{"name=seccomp-unconfined"}},
		{name: "embedded token", arch: "amd64", options: []string{"name=apparmor,seccomp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
			e := classicOverlay2Engine(recording)
			e.info.Info.Architecture = tc.arch
			e.info.Info.SecurityOptions = tc.options
			verifier := &recordingVerifier{}
			request := validRequest(t)
			if _, err := newProvisioner(t, e, verifier).Provision(context.Background(), request, allowSyntheticPreStart); err == nil || !strings.Contains(err.Error(), "seccomp unavailable") {
				t.Fatalf("unsupported Engine accepted: %v", err)
			}
			if e.infoCalls != 1 {
				t.Fatalf("Info calls=%d", e.infoCalls)
			}
			assertNoProvisionSideEffects(t, recording, verifier, request.Secrets.Bootstrap.BackendPath)
		})
	}
}

func TestProjectAttributeSupportedEngineAliases(t *testing.T) {
	for _, arch := range []string{"amd64", "x86_64", "arm64", "aarch64"} {
		for _, option := range []string{"name=seccomp", "name=seccomp,profile=builtin"} {
			t.Run(arch+"/"+option, func(t *testing.T) {
				recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
				e := classicOverlay2Engine(recording)
				e.info.Info.Architecture = arch
				e.info.Info.SecurityOptions = []string{option}
				got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
				if err != nil || got.StartedAt.IsZero() || !reflect.DeepEqual(recording.started, []string{"container-1", "container-2"}) || e.infoCalls != 1 {
					t.Fatalf("supported prerequisite: result=%+v error=%v starts=%v Info=%d", got, err, recording.started, e.infoCalls)
				}
			})
		}
	}
}

func TestProjectAttributeCreateRejectionNeverRetriesWithoutFilter(t *testing.T) {
	failure := errors.New("custom seccomp profile rejected")
	e := &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound}), createErr: failure}
	got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
	if !errors.Is(err, failure) || got.ContainerID != "container-2" || !got.PhysicalEffectUnknown || !got.StartedAt.IsZero() || len(e.creates) != 2 || !reflect.DeepEqual(e.started, []string{"container-1"}) || !reflect.DeepEqual(e.removed, []string{"container-1"}) || len(e.volumeRemoved) != 0 {
		t.Fatalf("Create rejection lost owner/retried: result=%+v error=%v creates=%d starts=%v removes=%v volumes=%v", got, err, len(e.creates), e.started, e.removed, e.volumeRemoved)
	}
	options := e.creates[1].HostConfig.SecurityOpt
	if len(options) != 2 || !strings.HasPrefix(options[1], "seccomp={") {
		t.Fatalf("rejected Create lacked complete policy: %v", options)
	}
}

func TestProjectAttributeExistingWorkerPolicyAdmission(t *testing.T) {
	seed := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	request := validRequest(t)
	if _, err := newProvisioner(t, classicOverlay2Engine(seed), &recordingVerifier{}).Provision(context.Background(), request, allowSyntheticPreStart); err != nil {
		t.Fatal(err)
	}
	created := seed.creates[1]
	cases := []struct {
		name    string
		mutate  func(*container.InspectResponse)
		allowed bool
	}{
		{"exact", func(*container.InspectResponse) {}, true},
		{"missing", func(c *container.InspectResponse) { c.HostConfig.SecurityOpt = []string{"no-new-privileges"} }, false},
		{"unconfined", func(c *container.InspectResponse) {
			c.HostConfig.SecurityOpt = []string{"no-new-privileges", "seccomp=unconfined"}
		}, false},
		{"tampered", func(c *container.InspectResponse) { c.HostConfig.SecurityOpt[1] += " " }, false},
		{"privileged", func(c *container.InspectResponse) { c.HostConfig.Privileged = true }, false},
		{"nil host", func(c *container.InspectResponse) { c.HostConfig = nil }, false},
		{"nil config", func(c *container.InspectResponse) { c.Config = nil }, false},
		{"wrong CID", func(c *container.InspectResponse) { c.ID = "another" }, false},
		{"wrong name", func(c *container.InspectResponse) { c.Name = "/another" }, false},
		{"wrong managed", func(c *container.InspectResponse) { c.Config.Labels["com.switchback.managed"] = "other" }, false},
		{"wrong session", func(c *container.InspectResponse) { c.Config.Labels["com.switchback.worker-session-id"] = "other" }, false},
		{"wrong runtime", func(c *container.InspectResponse) { c.Config.Labels["com.switchback.runtime-instance-id"] = "other" }, false},
		{"wrong generation", func(c *container.InspectResponse) { c.Config.Labels["com.switchback.runtime-generation"] = "2" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := *created.Config
			cfg.Labels = map[string]string{}
			for k, v := range created.Config.Labels {
				cfg.Labels[k] = v
			}
			host := *created.HostConfig
			host.SecurityOpt = append([]string{}, host.SecurityOpt...)
			c := container.InspectResponse{ID: "historical-worker", Name: "/" + created.Name, Config: &cfg, HostConfig: &host, State: &container.State{Running: true}}
			tc.mutate(&c)
			e := &policyInspectEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspect: client.ContainerInspectResult{Container: c}})}
			p := newProvisioner(t, e, &recordingVerifier{})
			target := application.CreatedRuntimeIdentity{ContainerID: c.ID, RuntimeInstanceID: request.RuntimeInstanceID, WorkerSessionID: request.WorkerSessionID, Generation: request.Generation}
			target.ContainerID = "historical-worker"
			err := p.VerifyWorkerPolicy(context.Background(), target)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
			if e.inspectedID != target.ContainerID || e.inspections != 1 || len(e.creates)+len(e.started)+len(e.removed)+len(e.volumeCreates)+len(e.volumeRemoved) != 0 {
				t.Fatalf("admission mutated owner or did not inspect exact CID: %+v", e)
			}
			// A mismatched policy must not disable generic observation used by cleanup.
			if tc.name == "unconfined" {
				if facts, err := p.Inspect(context.Background(), request.RuntimeInstanceID); err != nil || !facts.Exists || facts.ContainerID != target.ContainerID {
					t.Fatalf("legacy observation lost: %+v %v", facts, err)
				}
			}
		})
	}
}

type policyInspectEngine struct {
	*infoRecordingEngine
	inspectedID string
}

func (e *policyInspectEngine) ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	e.inspectedID = id
	return e.recordingEngine.ContainerInspect(ctx, id, opts)
}

func TestProjectAttributeExistingWorkerPolicyPrerequisites(t *testing.T) {
	target := application.CreatedRuntimeIdentity{ContainerID: "cid", RuntimeInstanceID: "runtime-1", WorkerSessionID: "session-1", Generation: 1}
	for _, kind := range []string{"unknown architecture", "missing seccomp", "Info error", "inspect error", "no Info"} {
		t.Run(kind, func(t *testing.T) {
			base := &recordingEngine{inspectErr: errors.New("inspect unavailable")}
			e := classicOverlay2Engine(base)
			switch kind {
			case "unknown architecture":
				e.info.Info.Architecture = "riscv64"
			case "missing seccomp":
				e.info.Info.SecurityOptions = nil
			case "Info error":
				e.infoErr = errors.New("Info unavailable")
			}
			var engine dockeradapter.Engine = e
			if kind == "no Info" {
				engine = base
			}
			p := newProvisioner(t, engine, &recordingVerifier{})
			if err := p.VerifyWorkerPolicy(context.Background(), target); err == nil {
				t.Fatal("missing evidence allowed")
			}
			wantInspect := 0
			if kind == "inspect error" {
				wantInspect = 1
			}
			if base.inspections != wantInspect || len(base.creates)+len(base.started)+len(base.removed)+len(base.volumeCreates)+len(base.volumeRemoved) != 0 {
				t.Fatal("prerequisite failure had effects")
			}
		})
	}
	var p *dockeradapter.Provisioner
	if p.VerifyWorkerPolicy(context.Background(), target) == nil {
		t.Fatal("nil verifier allowed")
	}
}

func TestProjectAttributeExistingWorkerPolicyRequiresLinux(t *testing.T) {
	target := application.CreatedRuntimeIdentity{ContainerID: "cid", RuntimeInstanceID: "runtime-1", WorkerSessionID: "session-1", Generation: 1}
	for _, osType := range []string{"windows", ""} {
		t.Run("os="+osType, func(t *testing.T) {
			base := &recordingEngine{inspectErr: errors.New("ContainerInspect must not be reached")}
			engine := classicOverlay2Engine(base)
			// Reviewed architecture and a seccomp token alone cannot establish Linux.
			engine.info.Info.OSType = osType
			err := newProvisioner(t, engine, &recordingVerifier{}).VerifyWorkerPolicy(context.Background(), target)
			if err == nil || !strings.Contains(err.Error(), "Linux") {
				t.Fatalf("missing Linux rejection: %v", err)
			}
			if engine.infoCalls != 1 || base.inspections != 0 || len(base.creates)+len(base.started)+len(base.removed)+len(base.volumeCreates)+len(base.volumeRemoved)+base.volumeInspections != 0 {
				t.Fatal("OS prerequisite denial inspected or mutated the container")
			}
		})
	}
}
