//go:build !linux

package capacityprobe

import "errors"

func ReadLinux(*Collection) (*LinuxFacts, error) {
	return nil, errors.New("Linux collection unavailable on this OS")
}
