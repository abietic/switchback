//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"bytes"
	"context"
	"reflect"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func qualificationReadExecutionProfile(ctx context.Context) (qualificationExecutionProfile, error) {
	dir, identities, err := openQualificationDirectory(ctx, qualificationGuestCandidateRoot)
	if err != nil {
		return qualificationExecutionProfile{}, err
	}
	defer dir.Close()
	root, err := qualificationStat(ctx, int(dir.Fd()))
	if err != nil || root.Mode&07777 != 0700 {
		return qualificationExecutionProfile{}, errQualification
	}
	profile, err := qualificationExecutionProfileAt(ctx, dir, qualificationStat)
	if err != nil {
		return qualificationExecutionProfile{}, err
	}
	fresh, after, err := openQualificationDirectory(ctx, qualificationGuestCandidateRoot)
	if err != nil {
		return qualificationExecutionProfile{}, err
	}
	defer fresh.Close()
	if !reflect.DeepEqual(identities, after) || qualificationContext(ctx) != nil {
		return qualificationExecutionProfile{}, errQualification
	}
	return profile, nil
}

func qualificationVerifyExecutionCoreNetworks(ctx context.Context, engine *client.Client, staged qualificationStaged) error {
	raw, err := qualificationEngineReadFile(ctx, qualificationEngineReceiptPath, 1<<20, 0600)
	if err != nil {
		return err
	}
	receipt, err := qualificationEngineReceiptV2Decode(raw)
	if err != nil || receipt.Engine.ID != staged.Trust.Identity.EngineID {
		return errQualification
	}
	kit, err := qualificationMaterialKit(ctx, qualificationMaterialRead)
	if err != nil {
		return err
	}
	if err = qualificationEngineBind(ctx, kit, receipt, qualificationEngineReadFile); err != nil {
		return err
	}
	networks := make(map[string]network.Inspect, len(receipt.Networks))
	for index, identity := range receipt.Networks {
		// Reuse the deployed Engine's native gate and exact fixed-network
		// identity contract; membership is an additional read-only SDK view.
		actual, err := qualificationEngineReadNetwork(ctx, receipt, index, identity.ID)
		if err != nil || !reflect.DeepEqual(actual, identity) {
			return errQualification
		}
		current, err := engine.NetworkInspect(ctx, identity.ID, client.NetworkInspectOptions{})
		if err != nil {
			return err
		}
		networks[identity.Name] = current.Network
	}
	for _, role := range staged.Source.Inputs.Roles {
		if role.Kind != "container" {
			continue
		}
		actual, err := engine.ContainerInspect(ctx, role.ContainerID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if err = qualificationCoreNetworkReadback(role, actual.Container, receipt.Networks, networks); err != nil {
			return err
		}
	}
	after, err := qualificationEngineReadFile(ctx, qualificationEngineReceiptPath, 1<<20, 0600)
	if err != nil || !bytes.Equal(raw, after) {
		return errQualification
	}
	return qualificationContext(ctx)
}
