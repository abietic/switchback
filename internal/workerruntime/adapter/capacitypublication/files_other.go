//go:build !linux && !darwin

package capacitypublication

// No path IO fallback is available on unsupported operating systems.
func newNativeFiles() fileSystem { return nil }
