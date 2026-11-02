package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
)

var requiredScenarios = []string{"healthy_two_turn", "cold_start_outage", "paired_20", "connected_unanswered_ingestion", "connected_unanswered_query", "original_source_recovery", "fixture_unchanged", "owned_residue"}

type Report struct {
	Physical      *PhysicalEvidence `json:"physical_evidence,omitempty"`
	Mode          string            `json:"mode"`
	ControlledACP bool              `json:"controlled_acp_fixture"`
	SourceCommit  string            `json:"source_commit"`
	WorkerImage   string            `json:"worker_image_digest"`
	BackendImage  string            `json:"backend_image_digest"`
	Coverage      map[string]string `json:"coverage"`
	Pairs         []Pair            `json:"pairs"`
}
type Pair struct {
	Healthy Sample `json:"healthy"`
	Outage  Sample `json:"outage"`
}
type Sample struct {
	Measurement    Measurement `json:"measurement"`
	ID             string      `json:"scenario_id"`
	SettlementsMS  []float64   `json:"terminal_receipt_to_settlement_ms"`
	NextDispatchMS float64     `json:"terminal_receipt_to_next_dispatch_ms"`
	SourceVerified bool        `json:"original_source_verified"`
	IdentityStable bool        `json:"identity_stable"`
}

func (r Report) Validate() error {
	if r.Mode != "full" || !r.ControlledACP {
		return fmt.Errorf("full controlled fixture report required; smoke is not slice-ready")
	}
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(r.SourceCommit) {
		return fmt.Errorf("source commit missing")
	}
	for _, digest := range []string{r.WorkerImage, r.BackendImage} {
		if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(digest) {
			return fmt.Errorf("exact image digest required")
		}
	}
	for _, name := range requiredScenarios {
		if r.Coverage[name] != "passed" {
			return fmt.Errorf("scenario %s missing/failed/skipped", name)
		}
	}
	if len(r.Pairs) < 20 {
		return fmt.Errorf("at least20 pairs required")
	}
	if r.Physical == nil {
		return fmt.Errorf("physical evidence missing; recipe labels are not evidence")
	}
	if err := r.Physical.validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	seenTurns := map[string]bool{}
	var healthy, outage [3][]float64
	for _, pair := range r.Pairs {
		for i, s := range []Sample{pair.Healthy, pair.Outage} {
			if s.Measurement.SessionID != r.Physical.Cold.SessionID || s.Measurement.RuntimeID != r.Physical.Cold.RuntimeID || s.Measurement.Generation != r.Physical.Cold.Generation {
				return fmt.Errorf("measurement changed original resident Session/Runtime")
			}
			if s.ID != s.Measurement.ID {
				return fmt.Errorf("measurement ID mismatch")
			}
			if err := s.Measurement.validate(r.Physical.Project); err != nil {
				return err
			}
			state := "healthy"
			if i == 1 {
				state = "unavailable"
			}
			if err := r.Physical.Intervals.validateWindow(r.Physical.Project, state, s.Measurement.From, s.Measurement.To); err != nil {
				return err
			}
			for _, id := range s.Measurement.TurnIDs {
				if seenTurns[id] {
					return fmt.Errorf("measurement repeats existing Turn")
				}
				seenTurns[id] = true
			}
			if s.ID == "" || seen[s.ID] || len(s.SettlementsMS) != 2 || !s.SourceVerified || !s.IdentityStable {
				return fmt.Errorf("incomplete or duplicate scenario %q", s.ID)
			}
			seen[s.ID] = true
			values := append(append([]float64{}, s.SettlementsMS...), s.NextDispatchMS)
			timings := append(append([]TransitionTiming{}, s.Measurement.Settlements...), s.Measurement.Dispatch)
			for metric, v := range values {
				if v != timings[metric].Milliseconds {
					return fmt.Errorf("measurement sample latency differs from original timestamps")
				}
			}
			for metric, value := range values {
				if i == 0 {
					healthy[metric] = append(healthy[metric], value)
				} else {
					outage[metric] = append(outage[metric], value)
				}
			}
		}
	}
	for metric := range healthy {
		sort.Float64s(healthy[metric])
		sort.Float64s(outage[metric])
		idx := int(math.Ceil(.95*float64(len(healthy[metric])))) - 1
		if outage[metric][idx]-healthy[metric][idx] > 1000 {
			return fmt.Errorf("metric %d outage p95 increase exceeds1000ms", metric)
		}
	}
	return nil
}
