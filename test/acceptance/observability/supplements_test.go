package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func evidenceReport(t *testing.T) Report {
	t.Helper()
	project := "switchback-obsv-a1234567"
	base := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	p := &PhysicalEvidence{Project: project, CleanupSessionID: "session", CleanupRuntimeID: "runtime", CleanupGeneration: 1}
	r := Report{Mode: "full", ControlledACP: true, SourceCommit: strings.Repeat("a", 40), WorkerImage: digest, BackendImage: digest, Coverage: map[string]string{}, Physical: p}
	for _, s := range requiredScenarios {
		r.Coverage[s] = "passed"
	}
	measurement := func(id string, at time.Time) Measurement {
		m := Measurement{Project: project, ID: id, From: at, To: at.Add(time.Second), SessionID: "session", RuntimeID: "runtime", Generation: 1, TurnIDs: []string{id + "a", id + "b"}, CommandIDs: []string{id + "ca", id + "cb"}, LegacyBefore: digest, LegacyAfter: digest}
		m.Settlements = []TransitionTiming{{TurnID: m.TurnIDs[0], MessageID: m.CommandIDs[0], From: at, To: at.Add(10 * time.Millisecond), Milliseconds: 10}, {TurnID: m.TurnIDs[1], MessageID: m.CommandIDs[1], From: at.Add(980 * time.Millisecond), To: m.To, Milliseconds: 20}}
		m.Dispatch = TransitionTiming{TurnID: m.TurnIDs[1], MessageID: id + "start", From: at, To: at.Add(30 * time.Millisecond), Milliseconds: 30}
		for _, turn := range m.TurnIDs {
			m.Expected = append(m.Expected, ExpectedModel{FixtureContainerID: "fixture-container", FixtureInstance: "fixture-instance", ObservationID: turn + "model", TurnID: turn, FixtureKey: turn, RegisteredAt: at, FrozenAt: at.Add(2 * time.Second), TerminalAt: at.Add(time.Second), IdleOrNextAt: at.Add(time.Second), ReleasedAt: at.Add(3 * time.Second)})
			for _, kind := range []string{"run_basis", "turn_basis", "model"} {
				s := FragmentProof{Version: fragmentVersion, Kind: kind, SourceID: turn + kind, Digest: digest, RunID: turn + "run", TurnID: turn, AcceptedAt: at.Add(4 * time.Second), SealedAt: at.Add(4 * time.Second), PublishedAt: at.Add(5 * time.Second), VerifiedAt: at.Add(6 * time.Second), InputDigest: digest, OutputDigest: digest}
				s.SessionID = "session"
				s.RuntimeID = "runtime"
				s.Generation = 1
				if kind == "model" {
					s.ObservationID = turn + "model"
				}
				m.Sources = append(m.Sources, s)
			}
		}
		return m
	}
	addIntervals := func(m Measurement, state string) {
		s := EventSubscription{ID: m.ID, Project: project, From: m.From.Add(-time.Second), To: m.To.Add(time.Second), End: "requested_stop"}
		s.Handshake = &SubscriptionHandshake{ContainerID: "postgres", ExecID: m.ID + "handshake", From: s.From, To: s.From}
		for _, action := range []string{"exec_create", "exec_die"} {
			s.Events = append(s.Events, ServiceEvent{Project: project, Service: "postgres", ContainerID: "postgres", ExecID: s.Handshake.ExecID, At: s.From, Action: action})
		}
		for _, service := range observationServices {
			for _, at := range []time.Time{m.From.Add(-time.Millisecond), m.To.Add(time.Millisecond)} {
				kind := "independent_health_probe"
				if state == "unavailable" {
					kind = "exact_inspect"
				}
				s.Probes = append(s.Probes, ServiceProbe{Project: project, Service: service, ContainerID: service, StartedAt: base.Add(-time.Hour), From: at, To: at, State: state, Kind: kind})
			}
		}
		p.Intervals.Subscriptions = append(p.Intervals.Subscriptions, s)
	}
	p.Cold = measurement("cold", base.Add(300*time.Millisecond))
	healthy := "healthy"
	p.ColdBefore = ProjectCapture{Schema: "observability.project_capture.v1", Project: project, StartedAt: base.Add(-time.Second), CompletedAt: base, AbsentServices: append([]string{"switchback-backend"}, observationServices...)}
	p.ColdAfter = ProjectCapture{Schema: "observability.project_capture.v1", Project: project, StartedAt: base.Add(1500 * time.Millisecond), CompletedAt: base.Add(2 * time.Second), AbsentServices: observationServices, Containers: []CapturedContainer{{Project: project, Service: "switchback-backend", ID: "backend", Running: true, CreatedAt: base.Add(100 * time.Millisecond), StartedAt: base.Add(200 * time.Millisecond)}, {Project: project, Service: "postgres", ID: "postgres", Running: true, Health: &healthy}}, BackendHealth: &HTTPHealthProbe{StartedAt: base.Add(1600 * time.Millisecond), CompletedAt: base.Add(1700 * time.Millisecond), Status: 200}}
	coldWindow := p.Cold
	coldWindow.From = base
	addIntervals(coldWindow, "unavailable")
	for i := 0; i < 20; i++ {
		h := measurement(fmt.Sprint("h", i), base.Add(time.Duration(i*20+20)*time.Second))
		o := measurement(fmt.Sprint("o", i), h.From.Add(10*time.Second))
		addIntervals(h, "healthy")
		addIntervals(o, "unavailable")
		r.Pairs = append(r.Pairs, Pair{Healthy: Sample{ID: h.ID, Measurement: h, SettlementsMS: []float64{10, 20}, NextDispatchMS: 30, SourceVerified: true, IdentityStable: true}, Outage: Sample{ID: o.ID, Measurement: o, SettlementsMS: []float64{10, 20}, NextDispatchMS: 30, SourceVerified: true, IdentityStable: true}})
	}
	for i, kind := range []string{"ingestion", "query"} {
		m := measurement(kind, base.Add(time.Duration(500+i*10)*time.Second))
		addIntervals(m, "healthy")
		p.Faults = append(p.Faults, FaultProof{Project: project, Kind: kind, ProxyContainerID: "proxy", Instance: "nonce", RequestID: kind, ConnectedAt: m.From.Add(-time.Second), CancelledAt: m.To.Add(time.Second), RecoveredAt: m.To.Add(2 * time.Second), Measurement: m})
	}
	for i, id := range []string{"ingestion-warmup", "query-warmup", "cleanup"} {
		m := measurement(id, base.Add(time.Duration(520+i*10)*time.Second))
		addIntervals(m, "healthy")
		p.Additional = append(p.Additional, MeasuredWindow{State: "healthy", Measurement: m})
	}
	b := CapturedContainer{Project: project, Service: "switchback-backend", ID: "before", StartedAt: base, Running: true}
	a := b
	a.ID = "after"
	a.StartedAt = base.Add(time.Second)
	p.Restart = RestartProof{Project: project, Before: b, After: a, StoppedAt: base.Add(time.Millisecond), HealthyAt: base.Add(2 * time.Second), SourcesBefore: p.Cold.Sources, SourcesAfter: p.Cold.Sources}
	p.RegistryPresentAt = base
	p.RegistryAbsentAt = base.Add(5500 * time.Millisecond)
	p.CleanupConfirmedAt = p.RegistryAbsentAt
	p.CleanupFenceRevision = 1
	p.RecoveredSources = p.Cold.Sources
	p.Resources = []OwnedResource{{Project: project, Kind: "container", ID: "owned-container", Ownership: "compose_label", ObservedAt: base, AbsentAt: base.Add(time.Second)}, {Project: project, Kind: "network", ID: "owned-network", Ownership: "compose_label", ObservedAt: base, AbsentAt: base.Add(time.Second)}}
	var err error
	p.Intervals.Intervals, err = p.Intervals.derive(project)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEveryPhysicalScenarioRejectsInvalidLatency(t *testing.T) {
	for _, scenario := range []string{"cold", "ingestion", "query", "ingestion-warmup", "query-warmup", "cleanup"} {
		for _, value := range []float64{5100, math.NaN(), math.Inf(1), -1, 11} {
			t.Run(fmt.Sprintf("%s/%v", scenario, value), func(t *testing.T) {
				r := evidenceReport(t)
				m := &r.Physical.Cold
				for i := range r.Physical.Faults {
					if r.Physical.Faults[i].Kind == scenario {
						m = &r.Physical.Faults[i].Measurement
					}
				}
				for i := range r.Physical.Additional {
					if r.Physical.Additional[i].Measurement.ID == scenario {
						m = &r.Physical.Additional[i].Measurement
					}
				}
				m.Settlements[0].Milliseconds = value
				if value == 5100 {
					m.Settlements[0].To = m.Settlements[0].From.Add(5100 * time.Millisecond)
				}
				if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "latency") {
					t.Fatalf("got %v, want invalid latency rejection", err)
				}
			})
		}
	}
}

func TestFinalEvidenceCoverageValidatedBeforeInfrastructureCleanup(t *testing.T) {
	r := evidenceReport(t)
	r.Physical.Resources = nil // Infrastructure has deliberately not been deleted.
	if err := r.Physical.validateBeforeInfrastructureCleanup(); err != nil {
		t.Fatal(err)
	}
	r.Physical.Additional[0].Measurement.Dispatch.MessageID = ""
	if err := r.Physical.validateBeforeInfrastructureCleanup(); err == nil {
		t.Fatal("invalid additional measurement survived infrastructure cleanup gate")
	}
}

func TestFaultRequiresIndependentHealthyCoverage(t *testing.T) {
	for _, kind := range []string{"ingestion", "query"} {
		for _, defect := range []string{"missing", "eof", "gap", "container", "process", "not healthy"} {
			t.Run(kind+"/"+defect, func(t *testing.T) {
				r := evidenceReport(t)
				if err := r.Validate(); err != nil {
					t.Fatal(err)
				}
				for i := range r.Physical.Intervals.Subscriptions {
					s := &r.Physical.Intervals.Subscriptions[i]
					if s.ID != kind {
						continue
					}
					switch defect {
					case "missing":
						s.Probes = nil
					case "eof":
						s.End = "unexpected_eof"
					case "gap":
						s.Probes[0].From = s.Probes[0].From.Add(100 * time.Millisecond)
						s.Probes[0].To = s.Probes[0].From
					case "container":
						s.Probes[1].ContainerID = "replacement"
					case "process":
						s.Probes[1].StartedAt = s.Probes[1].StartedAt.Add(time.Second)
					case "not healthy":
						s.Probes[0].State = "unknown"
					}
				}
				r.Physical.Intervals.Intervals, _ = r.Physical.Intervals.derive(r.Physical.Project)
				if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "interval") {
					t.Fatalf("got %v, want fault interval failure", err)
				}
			})
		}
	}
}

func TestStrictPhysicalEvidenceRejectsBrokenRelationships(t *testing.T) {
	good := evidenceReport(t)
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, want string
		breakProof func(*Report)
	}{
		{"short coverage", "measurement", func(r *Report) {
			r.Pairs[0].Healthy.Measurement.From = r.Pairs[0].Healthy.Measurement.From.Add(-time.Hour)
		}},
		{"cold collector begins after Backend start", "interval", func(r *Report) {
			s := &r.Physical.Intervals.Subscriptions[0]
			for i := range s.Probes {
				if s.Probes[i].From.Before(r.Physical.Cold.From) {
					s.Probes[i].From = r.Physical.Cold.From
					s.Probes[i].To = s.Probes[i].From
				}
			}
			r.Physical.Intervals.Intervals, _ = r.Physical.Intervals.derive(r.Physical.Project)
		}},
		{"zero measurement window", "measurement", func(r *Report) { r.Pairs[0].Healthy.Measurement.To = r.Pairs[0].Healthy.Measurement.From }},
		{"zero terminal proof", "source", func(r *Report) {
			r.Pairs[0].Healthy.Measurement.Expected[0].TerminalAt = time.Time{}
			r.Pairs[0].Healthy.Measurement.Expected[0].IdleOrNextAt = time.Time{}
		}},
		{"duplicate fault request", "fault", func(r *Report) { r.Physical.Faults[1].RequestID = r.Physical.Faults[0].RequestID }},
		{"empty restart source", "source", func(r *Report) {
			r.Physical.Restart.SourcesBefore = []FragmentProof{{}}
			r.Physical.Restart.SourcesAfter = []FragmentProof{{VerifiedAt: r.Physical.Restart.HealthyAt.Add(time.Second)}}
		}},
		{"contradictory retained residue", "residue", func(r *Report) {
			r.Physical.Resources = append(r.Physical.Resources, OwnedResource{Project: r.Physical.Project, Kind: "data_volume", ID: "data", Ownership: "compose_label", ObservedAt: r.Physical.RegistryPresentAt, AbsentAt: r.Physical.RegistryAbsentAt, Retained: true})
		}},
		{"unrelated cleanup source", "cleanup", func(r *Report) { r.Physical.RecoveredSources[0].SourceID = "unrelated-valid-source" }},
		{"event gap", "interval", func(r *Report) { r.Physical.Intervals.Subscriptions[1].End = "unexpected_eof" }},
		{"identity changed", "interval", func(r *Report) { r.Physical.Intervals.Subscriptions[1].Probes[1].ContainerID = "new-container" }},
		{"running not healthy", "interval", func(r *Report) { r.Physical.Intervals.Subscriptions[1].Probes[0].Kind = "running" }},
		{"fault not connected", "fault", func(r *Report) { r.Physical.Faults[0].ConnectedAt = time.Time{} }},
		{"fault answered", "fault", func(r *Report) { r.Physical.Faults[0].ResponseWritten = true }},
		{"restart same process", "restart", func(r *Report) { r.Physical.Restart.After.StartedAt = r.Physical.Restart.Before.StartedAt }},
		{"foreign residue", "residue", func(r *Report) { r.Physical.Resources[0].Project = "switchback-obsv-foreign1" }},
		{"mixed source version", "source", func(r *Report) { r.Pairs[0].Healthy.Measurement.Sources[0].Version = "langfuse.projection.v2" }},
		{"missing root", "source", func(r *Report) { r.Pairs[0].Healthy.Measurement.Sources = r.Pairs[0].Healthy.Measurement.Sources[1:] }},
		{"expected inferred after release", "source", func(r *Report) {
			r.Pairs[0].Healthy.Measurement.Expected[0].FrozenAt = r.Pairs[0].Healthy.Measurement.Expected[0].ReleasedAt.Add(time.Second)
		}},
		{"repeated turns", "measurement", func(r *Report) {
			r.Pairs[1].Healthy = r.Pairs[0].Healthy
			r.Pairs[1].Healthy.ID = "new-label"
			r.Pairs[1].Healthy.Measurement.ID = "new-label"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(good)
			var r Report
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			tc.breakProof(&r)
			err := r.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s failure", err, tc.want)
			}
		})
	}
}
