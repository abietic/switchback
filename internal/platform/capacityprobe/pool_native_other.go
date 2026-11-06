//go:build !linux || (!amd64 && !arm64)

package capacityprobe

func newPoolNative() poolKernel { return nil }
