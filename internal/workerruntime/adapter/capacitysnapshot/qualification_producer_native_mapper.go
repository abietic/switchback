package capacitysnapshot

import (
	"time"
)

// These are raw observed facts, not seven preconstructed qualification checks.
// The production collector fills them from live Engine/proc/syscall operations;
// a single mapper fixes check IDs, order, source, window and byte digests.
type qualificationNativeFacts struct {
	Loaded                                  qualificationLoaded
	Namespaces                              qualificationNamespaces
	Core                                    qualificationCore
	Quota                                   qualificationQuota
	SettersRaw                              []byte
	Handoff                                 qualificationHandoff
	Reachability                            qualificationReachability
	SampleStart, ReadonlyStart, ReadonlyEnd time.Time
	BackendStart, BackendEnd                time.Time
	QuotaStart, QuotaEnd                    time.Time
}

func qualificationLoadedAtEnd(read func() (qualificationLoaded, error), now func() time.Time) (qualificationLoaded, time.Time, error) {
	loaded, err := read()
	if err != nil {
		return qualificationLoaded{}, time.Time{}, err
	}
	return loaded, now(), nil
}

func qualificationMapNativeChecks(window string, facts qualificationNativeFacts) ([]qualificationCheck, error) {
	if facts.Loaded.Worker.PID == 0 || len(facts.Namespaces.Namespaces) != 8 || len(facts.Core.CPU) == 0 ||
		facts.Quota.Denied.ProjectID == 0 || len(facts.SettersRaw) == 0 || len(facts.Handoff.Cases) != 7 ||
		len(facts.Reachability.Observed) == 0 {
		return nil, errQualification
	}
	checks := make([]qualificationCheck, 0, len(qualificationNativeChecks))
	for _, item := range []struct {
		id       string
		from, to time.Time
		value    any
		raw      []byte
	}{
		{"loaded-identity", facts.SampleStart, facts.ReadonlyEnd, facts.Loaded, nil},
		{"namespaces-and-roles", facts.SampleStart, facts.ReadonlyEnd, facts.Namespaces, nil},
		{"core-limits", facts.ReadonlyStart, facts.ReadonlyEnd, facts.Core, nil},
		{"byte-quota", facts.QuotaStart, facts.QuotaEnd, facts.Quota, nil},
		{"worker-setters", facts.QuotaStart, facts.QuotaEnd, nil, facts.SettersRaw},
		{"backend-handoff", facts.BackendStart, facts.BackendEnd, facts.Handoff, nil},
		{"mounts-and-writers", facts.SampleStart, facts.ReadonlyEnd, facts.Reachability, nil},
	} {
		raw := item.raw
		if raw == nil {
			var err error
			raw, err = qualificationEncode(item.value)
			if err != nil {
				return nil, err
			}
		}
		check, err := qualificationCheckRaw(item.id, window, item.from, item.to, raw)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}
