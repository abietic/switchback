package capacitysnapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

const staticIntentSchema = "switchback.qualification.static_intent.v1"

// The projection is the exact pre-Create material. A container CID, its
// cgroup suffix, current mount device/root/ID, and source-derived inventory
// digests cannot be known before Engine Create or role Start. All build,
// image, profile, capability, mount-source and ownership intent remains.
func qualificationStaticIntentProjection(in qualificationStaged) qualificationStaged {
	s := qualificationClone(in)
	for i := range s.Source.Inputs.Roles {
		role := &s.Source.Inputs.Roles[i]
		if role.Kind == "container" {
			if containerIDPattern.MatchString(role.ContainerID) && role.CgroupPath == qualificationContainerCgroupPath(filepath.Dir(role.CgroupPath), role.ContainerID) {
				role.CgroupPath = filepath.Dir(role.CgroupPath)
			}
			role.ContainerID = ""
		}
		for j := range role.Mounts {
			role.Mounts[j].Device = ""
			role.Mounts[j].Root = ""
			role.Mounts[j].MountID = 0
		}
	}
	for i := range s.Binding.Core {
		s.Binding.Core[i].ContainerID = ""
	}
	s.Inventory.DeploymentInputsDigest = ""
	s.Inventory.SourceManifestDigest = ""
	return s
}

func qualificationContainerCgroupPath(parent, cid string) string {
	return filepath.Join(parent, "docker-"+cid+".scope")
}

func qualificationStaticIntentDigest(s qualificationStaged) string {
	p, err := qualificationProfileRefs()
	if err != nil {
		return ""
	}
	return qualificationDigest(struct {
		Schema   string              `json:"schema"`
		Staged   qualificationStaged `json:"staged"`
		Resource any                 `json:"resource_profile"`
		Network  any                 `json:"network_profile"`
	}{staticIntentSchema, qualificationStaticIntentProjection(s), p.resource, p.network})
}

func qualificationValidateIntentInput(s qualificationStaged) error {
	if err := qualificationValidateStaged(s); err != nil || !qualificationSameJSON(s, qualificationStaticIntentProjection(s)) ||
		!digestPattern.MatchString(qualificationStaticIntentDigest(s)) {
		return errQualification
	}
	for _, role := range s.Source.Inputs.Roles {
		if role.Kind == "container" && (role.ContainerID != "" || !absolute(role.CgroupPath) ||
			!strings.HasPrefix(role.CgroupPath, "/sys/fs/cgroup/switchback.slice/")) {
			return errQualification
		}
	}
	return nil
}

type qualificationStaticIntent struct {
	Schema      string `json:"schema"`
	WindowID    string `json:"window_id"`
	Nonce       string `json:"nonce"`
	InputDigest string `json:"input_digest"`
	Digest      string `json:"digest"`
}

// PrepareNativeQualificationIntent is an exclusive pre-Create calculation
// against a complete typed static intent input. It never claims a completed
// qualification or accepts caller-provided paths/values.
func PrepareNativeQualificationIntent(ctx context.Context) error {
	dir, err := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if err != nil {
		return err
	}
	defer dir.close()
	if err := dir.claim(ctx, "intent"); err != nil {
		return err
	}
	var input qualificationStaged
	if err := dir.decode(ctx, "intent-input.json", &input); err != nil {
		return err
	}
	if err := qualificationValidateIntentInput(input); err != nil {
		return err
	}
	if err := qualificationCheckStagedArtifacts(ctx, input, qualificationHashFile); err != nil {
		return err
	}
	v := qualificationStaticIntent{Schema: staticIntentSchema, WindowID: input.WindowID, Nonce: input.Nonce,
		InputDigest: qualificationDigest(input), Digest: qualificationStaticIntentDigest(input)}
	return dir.put(ctx, "static-intent.json", v)
}

func qualificationValidateStaticIntent(ctx context.Context, read func(context.Context, string, any) error, staged qualificationStaged) (string, error) {
	var input qualificationStaged
	var frozen qualificationStaticIntent
	if err := read(ctx, "intent-input.json", &input); err != nil {
		return "", err
	}
	if err := read(ctx, "static-intent.json", &frozen); err != nil {
		return "", err
	}
	if qualificationValidateIntentInput(input) != nil || !qualificationSameJSON(input, qualificationStaticIntentProjection(staged)) ||
		frozen.Schema != staticIntentSchema || frozen.WindowID != staged.WindowID || frozen.Nonce != staged.Nonce ||
		frozen.InputDigest != qualificationDigest(input) || frozen.Digest != qualificationStaticIntentDigest(staged) || !digestPattern.MatchString(frozen.Digest) {
		return "", fmt.Errorf("qualification pre-Create static intent drift")
	}
	return frozen.Digest, qualificationContext(ctx)
}
