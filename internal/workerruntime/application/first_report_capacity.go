package application

import (
	"errors"
	"time"
)

var ErrFirstReportCapacityUnavailable = errors.New("first-report runtime capacity unavailable")

// FirstReportCapacityEvidence is produced by the deployment's trusted Engine
// observer. It is internal to Backend and never part of a TaskSpec or HTTP input.
type FirstReportCapacityEvidence struct {
	EngineID              string
	ObservedAt            time.Time
	AvailableNanoCPUs     int64
	AvailableMemoryBytes  int64
	AvailableStorageBytes int64
}
