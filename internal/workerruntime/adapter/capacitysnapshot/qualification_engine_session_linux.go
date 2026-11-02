//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"fmt"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/qualificationio"
	"github.com/moby/moby/client"
)

type qualificationHelperSession struct {
	attached   *qualificationAttachedHelper
	cid        string
	labels     map[string]string
	engineID   string
	engineRoot string
	boot       string
}

func qualificationControlFor(c qualificationCandidateRecord, w qualificationWindowOpen, testCase, cid, token, mountRoot, testRoot string,
	root, target, child qualificationio.Object) (qualificationio.Control, error) {
	profile, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if !ok || err != nil {
		return qualificationio.Control{}, errQualification
	}
	role, buildRole := "backend", "backend"
	if testCase == "worker" {
		role, buildRole = "worker", "worker"
	}
	var image, binary, native, compat string
	for _, b := range c.Source.Builds {
		switch b.Role {
		case buildRole:
			image, binary = b.ImageDigest, b.BinaryDigest
		case "worker-native-helper":
			native = b.BinaryDigest
		case "worker-compat-helper":
			compat = b.BinaryDigest
		}
	}
	control := qualificationio.Control{Schema: qualificationio.ControlSchema, Candidate: c.Fingerprint, WindowID: w.ID, Nonce: w.Nonce, Token: token,
		Architecture: c.Source.Inputs.Architecture, Role: role, ContainerID: cid, ImageDigest: image, BinaryDigest: binary,
		Profile: profile.Ref(), Network: network.Evidence(), HostTestRoot: testRoot, MountRoot: mountRoot,
		Root: root, Target: target, Child: child}
	if role == "worker" {
		control.NativeDigest, control.CompatDigest = native, compat
	} else {
		control.BackendCase = testCase
	}
	return control, control.Validate()
}

// Only a positive Create result with an exact CID is eligible for one attach
// and one Start. Errors after Create are unknown effects, so this function
// returns them without retrying, deleting, or inventing cleanup facts.
func qualificationStartHelper(ctx context.Context, engine qualificationEngine, root qualificationCandidateDirectory,
	spec qualificationHelperSpec, mountRoot string, rootObject, target, child qualificationio.Object) (*qualificationHelperSession, error) {
	controlRoot, err := qualificationCreateControlDirectory(ctx, root, spec.Case)
	if err != nil {
		return nil, err
	}
	spec.ControlRoot = controlRoot
	seccomp, err := qualificationSeccomp()
	if err != nil {
		return nil, err
	}
	options, err := qualificationHelperOptions(spec, seccomp)
	if err != nil {
		return nil, err
	}
	created, err := engine.ContainerCreate(ctx, options)
	if err != nil || !containerIDPattern.MatchString(created.ID) || len(created.Warnings) != 0 {
		return nil, fmt.Errorf("qualification helper create unknown: %w", errQualification)
	}
	engineID, engineRoot, err := qualificationVerifyCreatedHelper(ctx, engine, created.ID, options)
	if err != nil {
		return nil, err
	}
	if engineID != spec.Candidate.Trust.Identity.EngineID || engineRoot != spec.Candidate.Trust.Identity.DockerRoot {
		return nil, errQualification
	}
	token, err := qualificationRandomToken()
	if err != nil {
		return nil, err
	}
	control, err := qualificationControlFor(spec.Candidate, spec.Window, spec.Case, created.ID, token, mountRoot, spec.TestRoot, rootObject, target, child)
	if err != nil {
		return nil, err
	}
	if err := qualificationWriteControl(ctx, controlRoot, control); err != nil {
		return nil, err
	}
	attached, err := qualificationAttachHelper(ctx, engine, created.ID, control)
	if err != nil {
		return nil, err
	}
	if _, err := engine.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		attached.close()
		return nil, fmt.Errorf("qualification helper start unknown: %w", err)
	}
	return &qualificationHelperSession{attached: attached, cid: created.ID, labels: options.Config.Labels, engineID: engineID, engineRoot: engineRoot, boot: spec.Window.GuestBoot}, nil
}

func (s *qualificationHelperSession) finish(ctx context.Context, engine qualificationEngine) (qualificationHelperCleanup, error) {
	if s == nil || s.attached == nil {
		return qualificationHelperCleanup{}, errQualification
	}
	if err := qualificationWaitHelper(ctx, engine, s.attached); err != nil {
		return qualificationHelperCleanup{}, err
	}
	s.attached.close()
	return qualificationRemoveHelper(ctx, engine, s.cid, s.labels)
}
