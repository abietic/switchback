package firstreportcapacity

import (
	"math"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestCapacityUsesFloorEightyPercentForEachDimension(t *testing.T) {
	profile, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	if !ok {
		t.Fatal("missing fixed resource profile")
	}
	exact := application.FirstReportCapacityEvidence{
		EngineID: "isolated-engine-1", ObservedAt: time.Now().UTC(),
		AvailableNanoCPUs: 2_500_000_000, AvailableMemoryBytes: 5 << 30, AvailableStorageBytes: 25 << 29,
	}
	if !fits(&exact, profile) {
		t.Fatal("runtime profile must fit at each exact 80% boundary")
	}
	for _, dimension := range []struct {
		name   string
		reduce func(*application.FirstReportCapacityEvidence)
	}{
		{"cpu", func(sample *application.FirstReportCapacityEvidence) { sample.AvailableNanoCPUs-- }},
		{"memory", func(sample *application.FirstReportCapacityEvidence) { sample.AvailableMemoryBytes-- }},
		{"disk", func(sample *application.FirstReportCapacityEvidence) { sample.AvailableStorageBytes-- }},
	} {
		t.Run(dimension.name, func(t *testing.T) {
			sample := exact
			dimension.reduce(&sample)
			if fits(&sample, profile) {
				t.Fatal("insufficient capacity was accepted")
			}
		})
	}
	if fits(nil, profile) {
		t.Fatal("missing Engine evidence must fail closed")
	}
	if fourFifths(math.MaxInt64) != 7_378_697_629_483_820_645 {
		t.Fatal("capacity computation overflowed")
	}
}
