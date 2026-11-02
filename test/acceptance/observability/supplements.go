package main

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"time"
)

const fragmentVersion = "langfuse.projection.v3"

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Measurement struct {
	Project      string             `json:"project"`
	ID           string             `json:"id"`
	From         time.Time          `json:"from"`
	To           time.Time          `json:"to"`
	SessionID    string             `json:"worker_session_id"`
	RuntimeID    string             `json:"runtime_id"`
	Generation   uint64             `json:"runtime_generation"`
	TurnIDs      []string           `json:"agent_turn_ids"`
	CommandIDs   []string           `json:"command_ids"`
	Settlements  []TransitionTiming `json:"settlements"`
	Dispatch     TransitionTiming   `json:"next_dispatch"`
	Sources      []FragmentProof    `json:"sources"`
	Expected     []ExpectedModel    `json:"expected_models"`
	LegacyBefore string             `json:"v2_bytes_digest_before"`
	LegacyAfter  string             `json:"v2_bytes_digest_after"`
}

// TransitionTiming retains the actual journal receipt and settlement/ACK facts.
// MessageID is the terminal event for settlements, and the exact start command
// for dispatch; dispatch From must equal the first terminal receipt.
type TransitionTiming struct {
	TurnID       string    `json:"agent_turn_id"`
	MessageID    string    `json:"message_id"`
	From         time.Time `json:"terminal_receipt_at"`
	To           time.Time `json:"completed_or_acknowledged_at"`
	Milliseconds float64   `json:"milliseconds"`
}

func (m Measurement) validateTiming() error {
	if len(m.TurnIDs) != 2 || len(m.CommandIDs) != 2 || len(m.Settlements) != 2 {
		return fmt.Errorf("measurement timing identities missing")
	}
	var from, to time.Time
	for i, timing := range append(append([]TransitionTiming{}, m.Settlements...), m.Dispatch) {
		v := timing.Milliseconds
		if timing.MessageID == "" || timing.TurnID == "" || timing.From.IsZero() || timing.To.IsZero() || timing.To.Before(timing.From) || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 5000 || math.Abs(float64(timing.To.Sub(timing.From))/float64(time.Millisecond)-v) > .001 {
			return fmt.Errorf("measurement latency outside0..5000ms or timestamp mismatch")
		}
		if i < 2 {
			if timing.TurnID != m.TurnIDs[i] || timing.MessageID != m.CommandIDs[i] {
				return fmt.Errorf("measurement terminal identity mismatch")
			}
		} else if timing.TurnID != m.TurnIDs[1] || !timing.From.Equal(m.Settlements[0].From) || timing.MessageID == m.CommandIDs[0] || timing.MessageID == m.CommandIDs[1] {
			return fmt.Errorf("measurement exact next command ACK mismatch")
		}
		if from.IsZero() || timing.From.Before(from) {
			from = timing.From
		}
		if timing.To.After(to) {
			to = timing.To
		}
	}
	if !m.From.Equal(from) || !m.To.Equal(to) {
		return fmt.Errorf("measurement timing window mismatch")
	}
	return nil
}

type ExpectedModel struct {
	FixtureContainerID string    `json:"fixture_container_id"`
	FixtureInstance    string    `json:"fixture_instance"`
	ObservationID      string    `json:"observation_id"`
	TurnID             string    `json:"agent_turn_id"`
	FixtureKey         string    `json:"fixture_key"`
	RegisteredAt       time.Time `json:"registered_at"`
	FrozenAt           time.Time `json:"frozen_at"`
	TerminalAt         time.Time `json:"terminal_at"`
	IdleOrNextAt       time.Time `json:"idle_or_next_at"`
	ReleasedAt         time.Time `json:"released_at"`
}

type FragmentProof struct {
	SessionID     string    `json:"worker_session_id"`
	RuntimeID     string    `json:"runtime_id"`
	Generation    uint64    `json:"runtime_generation"`
	Version       string    `json:"version"`
	Kind          string    `json:"kind"`
	SourceID      string    `json:"source_id"`
	Digest        string    `json:"frame_digest"`
	RunID         string    `json:"worker_run_id"`
	TurnID        string    `json:"agent_turn_id"`
	ObservationID string    `json:"observation_id,omitempty"`
	AcceptedAt    time.Time `json:"accepted_at"`
	SealedAt      time.Time `json:"sealed_at"`
	PublishedAt   time.Time `json:"published_at"`
	VerifiedAt    time.Time `json:"verified_at"`
	InputDigest   string    `json:"input_digest"`
	OutputDigest  string    `json:"output_digest"`
}

type FaultProof struct {
	Project          string      `json:"project"`
	Kind             string      `json:"kind"`
	ProxyContainerID string      `json:"proxy_container_id"`
	Instance         string      `json:"proxy_instance"`
	RequestID        string      `json:"request_id"`
	ConnectedAt      time.Time   `json:"connected_at"`
	CancelledAt      time.Time   `json:"cancelled_at"`
	RecoveredAt      time.Time   `json:"recovered_at"`
	ResponseWritten  bool        `json:"response_written"`
	Measurement      Measurement `json:"measurement"`
}

type RestartProof struct {
	Project       string            `json:"project"`
	Before        CapturedContainer `json:"before"`
	After         CapturedContainer `json:"after"`
	StoppedAt     time.Time         `json:"stopped_at"`
	HealthyAt     time.Time         `json:"healthy_at"`
	SourcesBefore []FragmentProof   `json:"sources_before"`
	SourcesAfter  []FragmentProof   `json:"sources_after"`
}

type OwnedResource struct {
	Project    string    `json:"project"`
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	Ownership  string    `json:"ownership"` // compose_label or runtime_db_and_labels
	SessionID  string    `json:"worker_session_id,omitempty"`
	RuntimeID  string    `json:"runtime_id,omitempty"`
	Generation uint64    `json:"runtime_generation,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	AbsentAt   time.Time `json:"absent_at,omitempty"`
	Retained   bool      `json:"retained"`
}

type PhysicalEvidence struct {
	Additional           []MeasuredWindow `json:"additional_measurements"`
	ColdBefore           ProjectCapture   `json:"cold_before"`
	ColdAfter            ProjectCapture   `json:"cold_after"`
	CleanupSessionID     string           `json:"cleanup_worker_session_id"`
	CleanupRuntimeID     string           `json:"cleanup_runtime_id"`
	CleanupGeneration    uint64           `json:"cleanup_generation"`
	Project              string           `json:"project"`
	Intervals            IntervalEvidence `json:"service_evidence"`
	Cold                 Measurement      `json:"cold_start"`
	Faults               []FaultProof     `json:"faults"`
	Restart              RestartProof     `json:"restart"`
	Resources            []OwnedResource  `json:"resources"`
	RegistryPresentAt    time.Time        `json:"registry_present_at"`
	RegistryAbsentAt     time.Time        `json:"registry_absent_at"`
	CleanupConfirmedAt   time.Time        `json:"cleanup_confirmed_at"`
	CleanupFenceRevision uint64           `json:"cleanup_fence_revision"`
	RecoveredSources     []FragmentProof  `json:"recovered_sources"`
}

type MeasuredWindow struct {
	State       string      `json:"state"`
	Measurement Measurement `json:"measurement"`
}

func (m Measurement) validate(project string) error {
	if m.Project != project || m.ID == "" || m.From.IsZero() || !m.To.After(m.From) || m.SessionID == "" || m.RuntimeID == "" || m.Generation == 0 || len(m.TurnIDs) != 2 || len(m.CommandIDs) != 2 || m.TurnIDs[0] == m.TurnIDs[1] || m.CommandIDs[0] == m.CommandIDs[1] {
		return fmt.Errorf("measurement exact identity/window incomplete")
	}
	if err := m.validateTiming(); err != nil {
		return err
	}
	if !digestPattern.MatchString(m.LegacyBefore) || m.LegacyBefore != m.LegacyAfter {
		return fmt.Errorf("source frozen v2 bytes changed/missing")
	}
	if len(m.Expected) != 2 {
		return fmt.Errorf("source independently expected model set missing")
	}
	models := map[string]ExpectedModel{}
	for _, e := range m.Expected {
		if e.FixtureContainerID == "" || e.FixtureInstance == "" {
			return fmt.Errorf("source fixture container/process identity missing")
		}
		if e.TerminalAt.IsZero() || e.IdleOrNextAt.IsZero() || e.TerminalAt.Before(e.RegisteredAt) {
			return fmt.Errorf("source terminal/idle proof missing")
		}
		if e.ObservationID == "" || e.FixtureKey == "" || e.RegisteredAt.IsZero() || e.FrozenAt.Before(e.RegisteredAt) || !e.ReleasedAt.After(e.FrozenAt) || !e.ReleasedAt.After(e.TerminalAt) || e.IdleOrNextAt.Before(e.TerminalAt) || !e.ReleasedAt.After(e.IdleOrNextAt) || (e.TurnID != m.TurnIDs[0] && e.TurnID != m.TurnIDs[1]) {
			return fmt.Errorf("source expected identity was not frozen before late release")
		}
		if _, ok := models[e.ObservationID]; ok {
			return fmt.Errorf("source duplicate expected model")
		}
		models[e.ObservationID] = e
	}
	seen := map[string]bool{}
	runs := map[string]bool{}
	turns := map[string]bool{}
	gotModels := map[string]bool{}
	for _, s := range m.Sources {
		if s.SessionID != m.SessionID || s.RuntimeID != m.RuntimeID || s.Generation != m.Generation {
			return fmt.Errorf("source measurement scope mismatch")
		}
		if err := s.validate(); err != nil {
			return err
		}
		if seen[s.SourceID] {
			return fmt.Errorf("source duplicate exact ref")
		}
		seen[s.SourceID] = true
		switch s.Kind {
		case "run_basis":
			runs[s.RunID] = true
		case "turn_basis":
			turns[s.TurnID] = true
		case "model":
			e, ok := models[s.ObservationID]
			if !ok || e.TurnID != s.TurnID || s.AcceptedAt.Before(e.ReleasedAt) {
				return fmt.Errorf("source model not in pre-release expected set")
			}
			gotModels[s.ObservationID] = true
		}
	}
	for _, s := range m.Sources {
		if !runs[s.RunID] || (s.Kind != "run_basis" && !turns[s.TurnID]) {
			return fmt.Errorf("source root/turn basis missing")
		}
	}
	if len(gotModels) != len(models) || !turns[m.TurnIDs[0]] || !turns[m.TurnIDs[1]] {
		return fmt.Errorf("source expected root/turn/model missing")
	}
	return nil
}

func (s FragmentProof) validate() error {
	if s.SessionID == "" || s.RuntimeID == "" || s.Generation == 0 {
		return fmt.Errorf("source runtime scope missing")
	}
	if s.Version != fragmentVersion || (s.Kind != "run_basis" && s.Kind != "turn_basis" && s.Kind != "model") || s.SourceID == "" || s.RunID == "" || !digestPattern.MatchString(s.Digest) || !digestPattern.MatchString(s.InputDigest) || (s.Kind != "run_basis" && !digestPattern.MatchString(s.OutputDigest)) || s.AcceptedAt.IsZero() || s.SealedAt.Before(s.AcceptedAt) || s.PublishedAt.Before(s.SealedAt) || s.VerifiedAt.Before(s.PublishedAt) {
		return fmt.Errorf("source version/content/publication proof invalid")
	}
	return nil
}

func (p PhysicalEvidence) validate() error {
	if err := p.validateBeforeInfrastructureCleanup(); err != nil {
		return err
	}
	return p.validateResources()
}

func (p PhysicalEvidence) validateBeforeInfrastructureCleanup() error {
	if err := p.validateCold(); err != nil {
		return err
	}
	return p.validateRecovery()
}

func (p PhysicalEvidence) validateCold() error {
	if !projectPattern.MatchString(p.Project) {
		return fmt.Errorf("physical evidence claim missing")
	}
	if p.ColdBefore.Project != p.Project || p.ColdAfter.Project != p.Project {
		return fmt.Errorf("cold claim capture missing")
	}
	if err := verifyColdStart(p.ColdBefore, p.ColdAfter); err != nil {
		return err
	}
	for _, c := range p.ColdAfter.Containers {
		if c.Service == "switchback-backend" && p.Cold.From.Before(c.StartedAt) {
			return fmt.Errorf("cold latency window precedes actual Backend start")
		}
	}
	if p.Cold.From.Before(p.ColdBefore.CompletedAt) || p.Cold.To.After(p.ColdAfter.StartedAt) {
		return fmt.Errorf("cold startup window does not bind captures")
	}
	if err := p.Cold.validate(p.Project); err != nil {
		return err
	}
	if err := p.Intervals.validateWindow(p.Project, "unavailable", p.ColdBefore.CompletedAt, p.Cold.To); err != nil {
		return err
	}
	return nil
}

func (p PhysicalEvidence) validateRecovery() error {
	faults := map[string]bool{}
	requests := map[string]bool{}
	for _, f := range p.Faults {
		key := f.ProxyContainerID + "/" + f.Instance + "/" + f.RequestID
		if requests[key] {
			return fmt.Errorf("fault request reused under different class")
		}
		requests[key] = true
		if f.Project != p.Project || (f.Kind != "ingestion" && f.Kind != "query") || faults[f.Kind] || f.ProxyContainerID == "" || f.Instance == "" || f.RequestID == "" || f.ConnectedAt.IsZero() || f.Measurement.From.Before(f.ConnectedAt) || f.CancelledAt.Before(f.Measurement.To) || !f.RecoveredAt.After(f.CancelledAt) || f.ResponseWritten {
			return fmt.Errorf("fault connected-unanswered lifecycle missing")
		}
		if err := f.Measurement.validate(p.Project); err != nil {
			return err
		}
		if err := p.Intervals.validateWindow(p.Project, "healthy", f.Measurement.From, f.Measurement.To); err != nil {
			return err
		}
		faults[f.Kind] = true
	}
	if len(faults) != 2 {
		return fmt.Errorf("fault ingestion/query evidence missing")
	}
	for _, w := range p.Additional {
		if w.State != "healthy" && w.State != "unavailable" {
			return fmt.Errorf("additional measurement state missing")
		}
		if err := w.Measurement.validate(p.Project); err != nil {
			return err
		}
		if err := p.Intervals.validateWindow(p.Project, w.State, w.Measurement.From, w.Measurement.To); err != nil {
			return err
		}
	}
	r := p.Restart
	if r.Project != p.Project || r.Before.Project != p.Project || r.After.Project != p.Project || r.Before.Service != "switchback-backend" || r.After.Service != r.Before.Service || r.Before.ID == "" || r.After.ID == "" || !r.Before.Running || !r.After.Running || !r.After.StartedAt.After(r.Before.StartedAt) || r.StoppedAt.Before(r.Before.StartedAt) || r.After.StartedAt.Before(r.StoppedAt) || !r.HealthyAt.After(r.After.StartedAt) || len(r.SourcesBefore) == 0 || len(r.SourcesBefore) != len(r.SourcesAfter) {
		return fmt.Errorf("restart process identity/lifecycle proof missing")
	}
	for i, b := range r.SourcesBefore {
		a := r.SourcesAfter[i]
		b.VerifiedAt = a.VerifiedAt
		if err := a.validate(); err != nil {
			return err
		}
		if err := b.validate(); err != nil {
			return err
		}
		if !reflect.DeepEqual(b, a) || a.VerifiedAt.Before(r.HealthyAt) {
			return fmt.Errorf("restart durable source identity/readback changed")
		}
	}
	if p.CleanupSessionID == "" || p.CleanupRuntimeID == "" || p.CleanupGeneration == 0 || len(p.RecoveredSources) != len(r.SourcesBefore) {
		return fmt.Errorf("cleanup original source scope missing")
	}
	if p.RegistryPresentAt.IsZero() || !p.RegistryAbsentAt.After(p.RegistryPresentAt) || p.CleanupConfirmedAt.Before(p.RegistryAbsentAt) || p.CleanupFenceRevision == 0 || len(p.RecoveredSources) == 0 {
		return fmt.Errorf("cleanup original registry/fence proof missing")
	}
	for i, s := range p.RecoveredSources {
		original := r.SourcesBefore[i]
		original.VerifiedAt = s.VerifiedAt
		if !reflect.DeepEqual(original, s) || s.SessionID != p.CleanupSessionID || s.RuntimeID != p.CleanupRuntimeID || s.Generation != p.CleanupGeneration {
			return fmt.Errorf("cleanup original source identity/scope changed")
		}
		if err := s.validate(); err != nil {
			return err
		}
		if s.PublishedAt.After(p.RegistryAbsentAt) || !s.VerifiedAt.After(p.CleanupConfirmedAt) {
			return fmt.Errorf("cleanup source not sealed before and verified after cleanup")
		}
	}
	return nil
}

func (p PhysicalEvidence) validateResources() error {
	if len(p.Resources) == 0 {
		return fmt.Errorf("residue owned inventory missing")
	}
	seen := map[string]bool{}
	containers, networks := 0, 0
	for _, r := range p.Resources {
		if r.Project != p.Project || r.ID == "" || r.ObservedAt.IsZero() || seen[r.Kind+":"+r.ID] || (r.Ownership != "compose_label" && r.Ownership != "runtime_db_and_labels") {
			return fmt.Errorf("residue claim ownership invalid")
		}
		seen[r.Kind+":"+r.ID] = true
		if r.Ownership == "runtime_db_and_labels" && (r.SessionID == "" || r.RuntimeID == "" || r.Generation == 0) {
			return fmt.Errorf("residue sibling runtime ownership missing")
		}
		if r.Retained {
			if !r.AbsentAt.IsZero() {
				return fmt.Errorf("residue resource both retained and absent")
			}
			if r.Kind != "data_volume" && r.Kind != "evidence_directory" {
				return fmt.Errorf("residue unsafe retained resource")
			}
		} else if !r.AbsentAt.After(r.ObservedAt) {
			return fmt.Errorf("residue owned resource still present")
		}
		if r.Kind == "container" {
			containers++
		}
		if r.Kind == "network" {
			networks++
		}
	}
	if containers == 0 || networks == 0 {
		return fmt.Errorf("residue container/network inventory missing")
	}
	return nil
}
