//go:build !linux

package capacitysnapshot

import "context"

func qualificationGuestBoot(context.Context) (string, error) { return "", errQualification }
func qualificationMonotonicNS() (int64, error)               { return 0, errQualification }
