//go:build !linux || (!amd64 && !arm64)

package capacitypublisher

// Production rejects unsupported platforms before fixed-path IO. Portable tests
// use only their own finite file source, never this native backend.
func newPublisherFiles() publisherFiles { return nil }
