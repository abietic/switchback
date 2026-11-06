package capacityprobe

import (
	"context"
	"reflect"
	"testing"
)

func TestValidateCreatedLayerObservationRequiresNativeCoverageWithoutSanitizing(t *testing.T) {
	for _, name := range []string{"complete_zero_off", "sequence", "source", "missing", "errno", "geometry", "raw_realtime", "attrs", "metadata"} {
		t.Run(name, func(t *testing.T) {
			c := nativeCollection(t, newRecordingLayer(t), context.Background())
			switch name {
			case "sequence":
				c.Physical.Samples[0].Sequence = 0
			case "source":
				c.Physical.Samples[0].Source = "invented"
			case "missing":
				c.Physical.Samples = nil
			case "errno":
				n := uint64(2)
				c.Physical.Samples[17].Errno = &n
			case "geometry":
				c.Physical.Parent.Geometry[88] = 4
				c.Physical.Parent.GeometryAfter = c.Physical.Parent.Geometry
			case "raw_realtime":
				c.Physical.TargetQuota[72] = 1
			case "attrs":
				c.Physical.Diff.FSXAttr[0] = 1
				c.Physical.Diff.FSXAttrAfter = c.Physical.Diff.FSXAttr
			case "metadata":
				c.Physical.Root.Before.UID = 7
				c.Physical.Root.After.UID = 7
			}
			before := append([]CreatedLayerSample(nil), c.Physical.Samples...)
			err := ValidateCreatedLayerObservation(nativeCID, c)
			if (err == nil) != (name == "complete_zero_off") {
				t.Fatalf("result %v", err)
			}
			if !reflect.DeepEqual(before, c.Physical.Samples) {
				t.Fatal("validator sanitized evidence")
			}
		})
	}
}
