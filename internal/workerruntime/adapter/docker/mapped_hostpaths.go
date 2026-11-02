package docker

import (
	"context"
	"fmt"
	"path/filepath"
)

// MappedHostPathVerifier verifies Docker Engine paths through the equivalent
// Backend bind mount. It is used when Backend itself runs in a container and
// therefore cannot stat the host's absolute EngineDataRoot directly.
type MappedHostPathVerifier struct {
	EngineRoot  string
	BackendRoot string
}

func (verifier MappedHostPathVerifier) VerifyHostPaths(
	ctx context.Context,
	root string,
	requirements []HostPathRequirement,
) error {
	if root != verifier.EngineRoot || !filepath.IsAbs(verifier.BackendRoot) ||
		filepath.Clean(verifier.BackendRoot) != verifier.BackendRoot {
		return fmt.Errorf("Docker HostPath mapping is invalid")
	}
	mapped := make([]HostPathRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		relative, err := filepath.Rel(verifier.EngineRoot, requirement.Path)
		if err != nil || relative == "." || relative == ".." ||
			filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
			return fmt.Errorf("Docker HostPath escapes its configured Engine root")
		}
		mapped = append(mapped, HostPathRequirement{
			Path: filepath.Join(verifier.BackendRoot, relative), Kind: requirement.Kind,
		})
	}
	return (LocalHostPathVerifier{}).VerifyHostPaths(ctx, verifier.BackendRoot, mapped)
}

var _ HostPathVerifier = MappedHostPathVerifier{}
