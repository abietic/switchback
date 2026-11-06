package capacitysnapshot

import (
	"reflect"
	"testing"
	"time"
)

func TestQualificationLoadedIdentityEndFollowsLastRead(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	order := []string{}
	loaded, end, err := qualificationLoadedAtEnd(func() (qualificationLoaded, error) {
		order = append(order, "actual PID1 namespace read")
		return qualificationLoaded{Filesystem: "xfs"}, nil
	}, func() time.Time { order = append(order, "ended timestamp"); return t0.Add(time.Second) })
	if err != nil || loaded.Filesystem != "xfs" || !end.Equal(t0.Add(time.Second)) || !reflect.DeepEqual(order, []string{"actual PID1 namespace read", "ended timestamp"}) {
		t.Fatalf("loaded check closed before read: order=%v end=%s err=%v", order, end, err)
	}
}
