package capacitysnapshot

import (
	"fmt"
	"path/filepath"
)

// ReadTrust loads only deployment-owned bounded regular data. Every ancestor
// and the leaf are reopened with NOFOLLOW; errors never disclose keys or paths.
func ReadTrust(path string) (Trust, error) {
	var trust Trust
	invalid := fmt.Errorf("runtime capacity trust is invalid or unreadable")
	if !absolute(path) {
		return trust, invalid
	}
	raw, err := readBoundedFile(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return trust, invalid
	}
	if err = strictJSON(raw, &trust); err != nil {
		return Trust{}, invalid
	}
	if err = ValidateTrust(trust); err != nil {
		return Trust{}, invalid
	}
	return trust, nil
}
