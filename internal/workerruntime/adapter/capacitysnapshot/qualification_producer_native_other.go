//go:build !linux || (!amd64 && !arm64)

package capacitysnapshot

import "context"

func CollectNativeQualification(context.Context) error { return errQualification }
