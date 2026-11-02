//go:build !linux || (!amd64 && !arm64)

package capacityprobe

import "context"

func ReadCreatedLayerPhysical(ctx context.Context, upper string) (*CreatedLayerPhysical, error) {
	return nil, createdLayerFailure("unsupported_platform", nil)
}
