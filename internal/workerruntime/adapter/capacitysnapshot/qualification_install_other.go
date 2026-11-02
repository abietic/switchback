//go:build !linux || (!amd64 && !arm64)

package capacitysnapshot

import (
	"context"
	"fmt"
)

func installQualificationFixed(context.Context) error {
	return fmt.Errorf("qualification installation requires Linux")
}
func bootstrapQualificationPublicationFixed(context.Context) error {
	return fmt.Errorf("qualification bootstrap requires Linux")
}

func RecordQualificationPublicationEmpty(context.Context) error {
	return fmt.Errorf("qualification preflight requires Linux")
}
