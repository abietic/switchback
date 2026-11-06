//go:build !linux || (!amd64 && !arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
	"strings"
)

func qualificationGenerateMaterialFixed(ctx context.Context, phase string) error {
	if phase == "host-preflight" {
		return qualificationGenerateHostMaterial(ctx)
	}
	if phase == "receive" {
		return qualificationTransferReceive(ctx)
	}
	if strings.HasPrefix(phase, "emit-") {
		return qualificationTransferEmit(ctx, strings.TrimPrefix(phase, "emit-"))
	}
	return fmt.Errorf("qualification guest material requires Linux native architecture")
}
