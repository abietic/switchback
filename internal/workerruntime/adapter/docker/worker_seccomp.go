package docker

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/moby/moby/client"

	"github.com/moby/moby/api/types/system"
)

// The full pinned Moby baseline is retained, including daemon-side archMap and
// capability/kernel conditions. Never resolve it using the Backend architecture.
//
//go:embed seccomp/moby-default-v0.2.4.json
var embeddedWorkerSeccompBaseline []byte

const workerSeccompBaselineSHA256 = "785b2429264afba4d594320337cb17f144f3c7d51585f9805eef72e28f4f9334"

// QualificationWorkerSeccomp returns exactly the production Worker profile.
// It neither resolves a caller-provided baseline nor performs an Engine call.
func QualificationWorkerSeccomp() (string, error) {
	return buildWorkerSeccomp(embeddedWorkerSeccompBaseline)
}

func buildWorkerSeccomp(baseline []byte) (string, error) {
	if digest := fmt.Sprintf("%x", sha256.Sum256(baseline)); digest != workerSeccompBaselineSHA256 {
		return "", fmt.Errorf("Worker seccomp baseline digest mismatch")
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(baseline, &profile); err != nil {
		return "", fmt.Errorf("decode Worker seccomp baseline: %w", err)
	}
	var action string
	var errno int
	if err := json.Unmarshal(profile["defaultAction"], &action); err != nil || action != "SCMP_ACT_ERRNO" {
		return "", fmt.Errorf("Worker seccomp baseline must default to ERRNO")
	}
	if err := json.Unmarshal(profile["defaultErrnoRet"], &errno); err != nil || errno != 1 {
		return "", fmt.Errorf("Worker seccomp baseline must default to errno 1")
	}
	var rules []map[string]json.RawMessage
	if err := json.Unmarshal(profile["syscalls"], &rules); err != nil {
		return "", fmt.Errorf("decode Worker seccomp rules: %w", err)
	}
	ioctlRules := 0
	for _, rule := range rules {
		var legacyName string
		if raw, ok := rule["name"]; ok {
			if err := json.Unmarshal(raw, &legacyName); err != nil || legacyName == "ioctl" {
				return "", fmt.Errorf("unexpected legacy Worker seccomp syscall name")
			}
		}
		var names []string
		if err := json.Unmarshal(rule["names"], &names); err != nil {
			return "", fmt.Errorf("decode Worker seccomp syscall names: %w", err)
		}
		for i, name := range names {
			if name != "ioctl" {
				continue
			}
			ioctlRules++
			var ruleAction string
			if err := json.Unmarshal(rule["action"], &ruleAction); err != nil || ruleAction != "SCMP_ACT_ALLOW" {
				return "", fmt.Errorf("unexpected Worker seccomp ioctl action")
			}
			// Require the sole ioctl rule to be unconditional and argument-free.
			for _, key := range []string{"args", "includes", "excludes"} {
				if _, ok := rule[key]; ok {
					return "", fmt.Errorf("unexpected conditional Worker seccomp ioctl rule")
				}
			}
			peers := append([]string{}, names[:i]...)
			peers = append(peers, names[i+1:]...)
			encoded, err := json.Marshal(peers)
			if err != nil {
				return "", fmt.Errorf("encode Worker seccomp syscall names: %w", err)
			}
			rule["names"] = encoded
		}
	}
	if ioctlRules != 1 {
		return "", fmt.Errorf("Worker seccomp baseline must contain exactly one ioctl rule")
	}

	// Emit a disjoint cover of the low32 command space except these setters.
	// Kernel ioctl truncates the command to unsigned int, so upper32 aliases
	// must have identical classification. One comparator per rule also avoids
	// runtime loaders interpreting repeated argument comparisons as OR rules.
	forbidden := [...]uint32{0x401c5820, 0x40086602, 0x40046602}
	appendPrefixRule := func(mask, prefix uint32) {
		rules = append(rules, map[string]json.RawMessage{
			"names":  json.RawMessage(`["ioctl"]`),
			"action": json.RawMessage(`"SCMP_ACT_ALLOW"`),
			"args":   json.RawMessage(fmt.Sprintf(`[{"index":1,"value":%d,"valueTwo":%d,"op":"SCMP_CMP_MASKED_EQ"}]`, uint64(mask), uint64(prefix))),
		})
	}
	var walk func(uint32, uint)
	walk = func(prefix uint32, depth uint) {
		var mask uint32
		if depth != 0 {
			mask = ^uint32(0) << (32 - depth)
		}
		blocked := false
		for _, command := range forbidden {
			if command&mask == prefix {
				blocked = true
				break
			}
		}
		if !blocked {
			appendPrefixRule(mask, prefix)
			return
		}
		if depth == 32 {
			return
		}
		walk(prefix, depth+1)
		walk(prefix|uint32(1)<<(31-depth), depth+1)
	}
	walk(0, 0)
	encodedRules, err := json.Marshal(rules)
	if err != nil {
		return "", fmt.Errorf("encode Worker seccomp rules: %w", err)
	}
	profile["syscalls"] = encodedRules
	encoded, err := json.Marshal(profile)
	if err != nil {
		return "", fmt.Errorf("encode Worker seccomp profile: %w", err)
	}
	return string(encoded), nil
}

// Engine Info is prerequisite evidence, not proof that the runtime installed BPF.
func checkWorkerSeccompPrerequisites(info system.Info) error {
	switch info.Architecture {
	case "amd64", "x86_64", "arm64", "aarch64":
	default:
		return fmt.Errorf("Docker Worker seccomp unavailable: unreviewed Engine architecture %q", info.Architecture)
	}
	for _, option := range info.SecurityOptions {
		if option == "name=seccomp" || strings.HasPrefix(option, "name=seccomp,") {
			return nil
		}
	}
	return fmt.Errorf("Docker Worker seccomp unavailable: Engine Info must declare seccomp support")
}

// VerifyWorkerPolicy admits only the exact server-owned container identity with
// the same immutable policy used at creation. It is read-only and does not grant
// capacity or control authority. Inspect/Cleanup intentionally remain independent
// so an unfiltered legacy owner can still be observed and reconciled.
func (p *Provisioner) VerifyWorkerPolicy(ctx context.Context, target application.CreatedRuntimeIdentity) error {
	if p == nil || isNilInterface(p.engine) || p.workerSeccomp == "" {
		return fmt.Errorf("Worker policy verifier is unavailable")
	}
	for name, value := range map[string]string{"container id": target.ContainerID, "runtime instance id": target.RuntimeInstanceID, "worker session id": target.WorkerSessionID} {
		if err := validateIdentity(name, value); err != nil {
			return err
		}
	}
	if target.Generation == 0 {
		return fmt.Errorf("Worker policy runtime generation is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	engine, ok := p.engine.(InfoEngine)
	if !ok || isNilInterface(engine) {
		return fmt.Errorf("Worker policy requires Engine Info capability")
	}
	info, err := engine.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("Worker policy inspect Engine Info: %w", err)
	}
	if info.Info.OSType != "linux" {
		return fmt.Errorf("Worker policy requires Linux Engine (os=%q)", info.Info.OSType)
	}
	if err := checkWorkerSeccompPrerequisites(info.Info); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	inspected, err := p.engine.ContainerInspect(ctx, target.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		return dockerError("inspect Worker policy", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c := inspected.Container
	if c.ID != target.ContainerID || c.Name != "/"+containerName(target.RuntimeInstanceID) || c.Config == nil {
		return fmt.Errorf("Worker policy container identity conflict")
	}
	labels := c.Config.Labels
	if labels[labelManaged] != managedWorkerRuntime || labels[labelRuntimeInstanceID] != target.RuntimeInstanceID || labels[labelWorkerSessionID] != target.WorkerSessionID || labels[labelRuntimeGeneration] != strconv.FormatUint(target.Generation, 10) {
		return fmt.Errorf("Worker policy container owner conflict")
	}
	if c.HostConfig == nil || c.HostConfig.Privileged || !reflect.DeepEqual(c.HostConfig.SecurityOpt, []string{"no-new-privileges", "seccomp=" + p.workerSeccomp}) {
		return fmt.Errorf("Worker policy container configuration conflict")
	}
	return nil
}
