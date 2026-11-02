package capacityprobe

import (
	"context"
	"errors"
	"syscall"
	"time"
)

const createdLayerWindow = 30 * time.Second

// CollectCreatedLayer performs bounded, fixed GETs and one physical observation.
// The context bounds cooperative callbacks; it cannot preempt synchronous syscalls
// or an injected callback which ignores cancellation. Late returns are incomplete.
func CollectCreatedLayer(ctx context.Context, target CreatedLayerTarget, sources CreatedLayerSources) (c CreatedLayerCollection) {
	c = CreatedLayerCollection{Schema: "switchback.capacity.untrusted_created_layer_collection.v1", Status: "preparation_blocked", Trust: "none", Blockers: []string{"unsigned_diagnostic_only", "quota_compliance_and_admission_unproven", "ABA_and_external_writers_unexcluded", "all_time_capacity_unknown"}}
	now := sources.Now
	if now == nil {
		now = time.Now
	}
	c.Started = now().UTC()
	last := c.Started
	clockOK := !c.Started.IsZero()
	complete := false
	stamp := func() time.Time {
		t := now().UTC()
		if t.Before(last) || t.Sub(c.Started) > createdLayerWindow {
			clockOK = false
		}
		last = t
		return t
	}
	defer func() {
		c.Ended = stamp()
		if !c.Ended.After(c.Started) {
			clockOK = false
		}
		if !clockOK {
			c.Blockers = append(c.Blockers, "observation_time_invalid")
		}
		c.ObservationComplete = complete && clockOK
	}()
	if ctx == nil {
		c.Blockers = append(c.Blockers, "context_missing")
		return
	}
	if !hexID.MatchString(target.ContainerID) {
		c.Blockers = append(c.Blockers, "container_id_invalid")
		return
	}
	if sources.Get == nil || sources.Physical == nil {
		c.Blockers = append(c.Blockers, "sources_unconfigured")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createdLayerWindow)
	defer cancel()
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !clockOK {
			return context.DeadlineExceeded
		}
		return nil
	}
	addSample := func(op, source string, start, end time.Time, err error) {
		outcome := "observed"
		if err != nil {
			outcome = "unavailable"
		}
		var number *uint64
		var errno syscall.Errno
		if errors.As(err, &errno) {
			n := uint64(errno)
			number = &n
		}
		c.Samples = append(c.Samples, CreatedLayerSample{Sequence: len(c.Samples) + 1, Operation: op, Source: source, Started: start, Ended: end, Outcome: outcome, Errno: number})
	}
	get := func(endpoint string) ([]byte, error) {
		if err := check(); err != nil {
			return nil, err
		}
		start := stamp()
		if err := check(); err != nil {
			return nil, err
		}
		raw, err := sources.Get(ctx, endpoint)
		end := stamp()
		if !end.After(start) {
			clockOK = false
		}
		if err == nil {
			err = check()
		}
		addSample(endpoint, "engine_get_v1.56", start, end, err)
		return raw, err
	}
	engine := func(expected *CreatedLayerEngine) (*CreatedLayerEngine, error) {
		raw, err := get("/v1.56/version")
		if err != nil {
			return nil, err
		}
		api, server, err := createdLayerEngineVersion(raw)
		if err != nil {
			return nil, err
		}
		if expected != nil && (api != expected.APIVersion || server != expected.Version) {
			return nil, errCreatedLayerJSON
		}
		raw, err = get("/v1.56/info")
		if err != nil {
			return nil, err
		}
		return createdLayerEngineInfo(raw, api, server)
	}
	before, err := engine(nil)
	if err != nil {
		c.Blockers = append(c.Blockers, "engine_before_unavailable_or_invalid")
		return
	}
	c.Engine = before
	endpoint := "/v1.56/containers/" + target.ContainerID + "/json"
	raw, err := get(endpoint)
	if err != nil {
		c.Blockers = append(c.Blockers, "inspect_before_unavailable")
		return
	}
	inspect, err := createdLayerDecodeInspect(raw, target.ContainerID)
	if err != nil {
		c.Blockers = append(c.Blockers, "inspect_before_invalid")
		return
	}
	c.Inspect = inspect
	if check() != nil {
		c.Blockers = append(c.Blockers, "context_or_time_invalid")
		return
	}
	start := stamp()
	if check() != nil {
		c.Blockers = append(c.Blockers, "context_or_time_invalid")
		return
	}
	physical, physicalErr := sources.Physical(ctx, inspect.UpperDir)
	end := stamp()
	if !end.After(start) {
		clockOK = false
	}
	if physicalErr == nil {
		physicalErr = check()
	}
	addSample("physical", "linux_syscall", start, end, physicalErr)
	if physical != nil {
		copied := *physical
		samples := physical.Samples
		if len(samples) > 64 {
			samples = samples[:64]
			copied.ObservationComplete = false
			copied.SamplesTruncated = true
		}
		copied.Samples = append([]CreatedLayerSample(nil), samples...)
		for i := range copied.Samples {
			if copied.Samples[i].Errno != nil {
				n := *copied.Samples[i].Errno
				copied.Samples[i].Errno = &n
			}
		}
		physical = &copied
		if physicalErr != nil {
			physical.ObservationComplete = false
		}
	}
	physicalOK := createdLayerPhysicalResult(physical, inspect, start, end)
	c.Physical = physical
	if physicalErr != nil || !physicalOK {
		c.Blockers = append(c.Blockers, "physical_unavailable_or_incomplete")
	}
	if check() != nil {
		c.Blockers = append(c.Blockers, "context_or_time_invalid")
		return
	}
	raw, err = get(endpoint)
	if err != nil {
		c.Blockers = append(c.Blockers, "inspect_after_unavailable")
		return
	}
	afterInspect, err := createdLayerDecodeInspect(raw, target.ContainerID)
	if err != nil || *afterInspect != *inspect {
		c.Blockers = append(c.Blockers, "inspect_after_invalid_or_drift")
		return
	}
	after, err := engine(before)
	if err != nil || *after != *before {
		c.Blockers = append(c.Blockers, "engine_after_invalid_or_drift")
		return
	}
	if check() != nil {
		c.Blockers = append(c.Blockers, "context_or_time_invalid")
		return
	}
	complete = physicalErr == nil && physicalOK
	return
}

// The callback supplies observations, never arbitrary diagnostic text. Restrict
// any path/sample strings before output while retaining partial raw/numeric facts.
func createdLayerPhysicalResult(p *CreatedLayerPhysical, inspect *CreatedLayerInspect, start, end time.Time) bool {
	if p == nil {
		return false
	}
	ok := p.ObservationComplete && !p.SamplesTruncated && p.MountInfoRechecked && p.MountInfoSHA256Before == p.MountInfoSHA256After && p.RootXFSVerified && p.SameMountVerified && p.BootIdentityRechecked && p.MountNamespaceRechecked && p.BootIDBefore == p.BootIDAfter && p.MountNamespaceBefore == p.MountNamespaceAfter
	for _, entry := range []struct {
		d    *CreatedLayerDirectory
		path string
	}{{&p.Root, "/mnt/lima-sbquota"}, {&p.Parent, inspect.ParentDir}, {&p.Diff, inspect.UpperDir}} {
		d := entry.d
		for _, identity := range []*CreatedLayerIdentity{&d.Before, &d.After} {
			if identity.CanonicalPath != entry.path {
				identity.CanonicalPath = ""
				ok = false
			}
		}
		if !d.NoFollowWalk || !d.ReopenedNoFollow || !d.IdentityRechecked || !d.FilesystemUUIDSameFD || d.Before != d.After || d.Geometry != d.GeometryAfter || d.FSXAttr != d.FSXAttrAfter {
			ok = false
		}
	}
	previous := start
	for i := range p.Samples {
		s := &p.Samples[i]
		s.Sequence = i + 1
		s.Source = "linux_syscall"
		if !createdLayerPhysicalOperation(s.Operation) {
			s.Operation = "physical_unknown_operation"
			s.Outcome = "unavailable"
			ok = false
		}
		if s.Outcome != "observed" && s.Outcome != "unavailable" {
			s.Outcome = "unavailable"
			ok = false
		}
		if s.Outcome != "observed" || s.Errno != nil || s.Started.Before(previous) || !s.Ended.After(s.Started) || s.Ended.After(end) {
			ok = false
		}
		previous = s.Ended
	}
	// A partial adapter object remains explicitly partial after sanitization.
	p.ObservationComplete = ok
	return ok
}

func createdLayerPhysicalOperation(op string) bool {
	switch op {
	case "mountinfo_before", "mountinfo_after", "boot_id_before", "boot_id_after", "mount_namespace_before", "mount_namespace_after", "root_open", "parent_open", "diff_open", "root_stat", "parent_stat", "diff_stat", "root_geometry", "parent_geometry", "diff_geometry", "root_fsxattr", "parent_fsxattr", "diff_fsxattr", "root_fsuuid", "parent_fsuuid", "diff_fsuuid", "qstatv", "target_quota", "default0_quota", "root_reopen", "parent_reopen", "diff_reopen":
		return true
	default:
		return false
	}
}
