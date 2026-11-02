package telemetry

import (
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Family selects one fixed observation family.
type Family string

const (
	FamilyWorkerRunTerminal   Family = "worker_run_terminal"
	FamilyAgentTurnTerminal   Family = "agent_turn_terminal"
	FamilySessionState        Family = "session_state"
	FamilyRuntimeState        Family = "runtime_state"
	FamilyQueueState          Family = "queue_state"
	FamilyCandidateProjection Family = "candidate_projection"
	FamilyFragmentProjection  Family = "fragment_projection"
)

// Status is a bounded result or state, never a caller-provided label.
type Status string

const (
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusCancelled    Status = "cancelled"
	StatusTimedOut     Status = "timed_out"
	StatusQueued       Status = "queued"
	StatusRunning      Status = "running"
	StatusAllocating   Status = "allocating"
	StatusInteractive  Status = "interactive"
	StatusRecovering   Status = "recovering"
	StatusLost         Status = "lost"
	StatusClosed       Status = "closed"
	StatusExpired      Status = "expired"
	StatusCapacityWait Status = "capacity_wait"
	StatusWaitingUser  Status = "waiting_user"
	StatusIdle         Status = "idle"
	StatusPending      Status = "pending"
	StatusProjected    Status = "projected"
	StatusHealthy      Status = "healthy"
	StatusSuspect      Status = "suspect"
	StatusUnhealthy    Status = "unhealthy"
	// Runtime lifecycle states mirror the finite authoritative PostgreSQL enum.
	StatusStarting   Status = "starting"
	StatusActive     Status = "active"
	StatusDraining   Status = "draining"
	StatusTerminated Status = "terminated"
	StatusUnknown    Status = "unknown"
)

// Harness is a verified finite coding-agent kind.
type Harness string

const (
	HarnessCodex   Harness = "codex"
	HarnessKimi    Harness = "kimi"
	HarnessYHC     Harness = "yhc"
	HarnessUnknown Harness = "unknown"
)

// CountMeasurement is one absolute snapshot value, not an increment.
type CountMeasurement struct {
	Family  Family
	Status  Status
	Harness Harness
	Value   float64
}

// DurationMeasurement is one cumulative histogram in seconds; Buckets correspond to DurationBuckets.
type DurationMeasurement struct {
	Family     Family
	Status     Status
	Harness    Harness
	Count      uint64
	SumSeconds float64
	Buckets    [11]uint64
}

// AgeMeasurement is one oldest-pending age in seconds for a projection family.
type AgeMeasurement struct {
	Family  Family
	Seconds float64
}

// Snapshot is a complete replacement of sampled domain observations. Missing measurements remain absent.
type Snapshot struct {
	SampledAt     time.Time
	Counts        []CountMeasurement
	Durations     []DurationMeasurement
	OldestPending []AgeMeasurement
}

var durationBuckets = [11]float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 300, 1800, 86400}

// DurationBucketBounds returns the immutable histogram bounds in seconds.
func DurationBucketBounds() [11]float64 { return durationBuckets }

var commonLabels = prometheus.Labels{"service": Service, "environment": Environment}

// Metrics owns a private registry and keeps the last successful domain snapshot.
type Metrics struct {
	mu              sync.RWMutex
	snapshot        Snapshot
	available       bool
	current         bool
	lastSuccess     float64
	failures        uint64
	queueDepth      float64
	queueCapacity   float64
	drops           map[LossReason]uint64
	firstDrop       map[LossReason]float64
	lastDrop        map[LossReason]float64
	accepted        uint64
	registry        *prometheus.Registry
	workerReaders   int
	workerAvailable bool
	workerLoss      [8]uint64
}

// WorkerLoss identifies a bounded collection gap, not discarded payload bytes.
type WorkerLoss uint8

const (
	WorkerAuthorityLoss WorkerLoss = iota
	WorkerCapacityLoss
	WorkerStreamLoss
	WorkerFrameLoss
	WorkerShutdownLoss
	WorkerLifecycleOverflow
	WorkerLifecycleLoss
	WorkerUnavailable
)

var workerLossNames = [8]string{"authority", "capacity", "stream", "frame", "shutdown", "lifecycle_overflow", "lifecycle", "unavailable"}

// WorkerCollectorState publishes current reader admission and authority health.
func (m *Metrics) WorkerCollectorState(readers int, available bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workerReaders = max(0, min(10, readers))
	m.workerAvailable = available
}

// WorkerCollectorLost accounts for a finite gap without copying error text.
func (m *Metrics) WorkerCollectorLost(reason WorkerLoss) {
	if int(reason) >= len(workerLossNames) {
		reason = WorkerUnavailable
	}
	m.mu.Lock()
	m.workerLoss[reason]++
	if reason == WorkerAuthorityLoss || reason == WorkerUnavailable {
		m.workerAvailable = false
	}
	m.mu.Unlock()
}

// NewMetrics creates an isolated metrics registry.
func NewMetrics() *Metrics {
	m := &Metrics{drops: make(map[LossReason]uint64), firstDrop: make(map[LossReason]float64), lastDrop: make(map[LossReason]float64), queueCapacity: 1024, registry: prometheus.NewRegistry()}
	m.registry.MustRegister(m)
	return m
}

// Handler exposes this instance's private Prometheus registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Replace atomically installs a validated absolute snapshot.
func (m *Metrics) Replace(s Snapshot) error {
	if err := validateSnapshot(s); err != nil {
		return err
	}
	m.mu.Lock()
	m.snapshot = cloneSnapshot(s)
	m.available = true
	m.current = true
	m.lastSuccess = float64(s.SampledAt.Unix())
	m.mu.Unlock()
	return nil
}

// SampleFailed records a failed domain sample while preserving prior measurements.
func (m *Metrics) SampleFailed() { m.mu.Lock(); m.failures++; m.current = false; m.mu.Unlock() }

func cloneSnapshot(s Snapshot) Snapshot {
	s.Counts = append([]CountMeasurement(nil), s.Counts...)
	s.Durations = append([]DurationMeasurement(nil), s.Durations...)
	s.OldestPending = append([]AgeMeasurement(nil), s.OldestPending...)
	return s
}
func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func validHarness(h Harness) bool {
	switch h {
	case HarnessCodex, HarnessKimi, HarnessYHC, HarnessUnknown:
		return true
	}
	return false
}
func validStatus(f Family, s Status) bool {
	if s == StatusUnknown {
		switch f {
		case FamilyWorkerRunTerminal, FamilyAgentTurnTerminal, FamilySessionState, FamilyRuntimeState, FamilyCandidateProjection, FamilyFragmentProjection:
			return true
		}
	}
	switch f {
	case FamilyWorkerRunTerminal, FamilyAgentTurnTerminal:
		switch s {
		case StatusCompleted, StatusFailed, StatusCancelled, StatusTimedOut:
			return true
		}
	case FamilySessionState:
		switch s {
		case StatusAllocating, StatusInteractive, StatusRunning, StatusRecovering, StatusWaitingUser, StatusIdle, StatusLost, StatusClosed, StatusExpired:
			return true
		}
	case FamilyRuntimeState:
		switch s {
		case StatusHealthy, StatusSuspect, StatusUnhealthy, StatusStarting, StatusActive, StatusDraining, StatusTerminated, StatusLost:
			return true
		}
	case FamilyQueueState:
		switch s {
		case StatusQueued, StatusRunning, StatusCapacityWait:
			return true
		}
	case FamilyCandidateProjection, FamilyFragmentProjection:
		switch s {
		case StatusPending, StatusProjected, StatusFailed:
			return true
		}
	}
	return false
}
func validateSnapshot(s Snapshot) error {
	if s.SampledAt.IsZero() || s.SampledAt.Before(time.Unix(0, 0)) || s.SampledAt.After(time.Now().Add(24*time.Hour)) {
		return errors.New("invalid sample timestamp")
	}
	if len(s.Counts) > 64 || len(s.Durations) > 32 || len(s.OldestPending) > 2 {
		return errors.New("too many measurements")
	}
	seen := make(map[string]bool)
	for _, c := range s.Counts {
		if !validStatus(c.Family, c.Status) || !finiteNonnegative(c.Value) || math.Trunc(c.Value) != c.Value || !validHarness(c.Harness) {
			return errors.New("invalid count measurement")
		}
		if c.Family != FamilyWorkerRunTerminal && c.Family != FamilyAgentTurnTerminal && c.Harness != HarnessUnknown {
			return errors.New("invalid harness dimension")
		}
		key := string(c.Family) + "/" + string(c.Status) + "/" + string(c.Harness)
		if seen[key] {
			return errors.New("duplicate count measurement")
		}
		seen[key] = true
	}
	for _, d := range s.Durations {
		if (d.Family != FamilyWorkerRunTerminal && d.Family != FamilyAgentTurnTerminal) || !validStatus(d.Family, d.Status) || !validHarness(d.Harness) || !finiteNonnegative(d.SumSeconds) {
			return errors.New("invalid duration measurement")
		}
		prev := uint64(0)
		minimum, maximum := float64(0), float64(0)
		for i, n := range d.Buckets {
			if n < prev || n > d.Count {
				return errors.New("invalid histogram buckets")
			}
			if i > 0 {
				minimum += float64(n-prev) * durationBuckets[i-1]
			}
			maximum += float64(n-prev) * durationBuckets[i]
			prev = n
		}
		minimum += float64(d.Count-prev) * durationBuckets[len(durationBuckets)-1]
		if d.SumSeconds+1e-7 < minimum || (prev == d.Count && d.SumSeconds > maximum+1e-7) {
			return errors.New("invalid histogram sum")
		}
		key := "duration/" + string(d.Family) + "/" + string(d.Status) + "/" + string(d.Harness)
		if seen[key] {
			return errors.New("duplicate duration measurement")
		}
		seen[key] = true
	}
	for _, a := range s.OldestPending {
		if (a.Family != FamilyCandidateProjection && a.Family != FamilyFragmentProjection) || !finiteNonnegative(a.Seconds) {
			return errors.New("invalid pending age")
		}
		key := "age/" + string(a.Family)
		if seen[key] {
			return errors.New("duplicate pending age")
		}
		seen[key] = true
	}
	return nil
}

var countDesc = map[Family]*prometheus.Desc{
	FamilyWorkerRunTerminal:   prometheus.NewDesc("switchback_worker_run_terminal", "Absolute WorkerRun terminal results", []string{"status", "harness"}, commonLabels),
	FamilyAgentTurnTerminal:   prometheus.NewDesc("switchback_agent_turn_terminal", "Absolute AgentTurn terminal results", []string{"status", "harness"}, commonLabels),
	FamilySessionState:        prometheus.NewDesc("switchback_session_state", "Current sessions by state", []string{"status"}, commonLabels),
	FamilyRuntimeState:        prometheus.NewDesc("switchback_runtime_state", "Current runtimes by state", []string{"status"}, commonLabels),
	FamilyQueueState:          prometheus.NewDesc("switchback_queue_state", "Current queue entries by state", []string{"status"}, commonLabels),
	FamilyCandidateProjection: prometheus.NewDesc("switchback_candidate_projection", "Current candidates by projection state", []string{"status"}, commonLabels),
	FamilyFragmentProjection:  prometheus.NewDesc("switchback_fragment_projection", "Current fragments by projection state", []string{"status"}, commonLabels),
}
var durationDesc = map[Family]*prometheus.Desc{
	FamilyWorkerRunTerminal: prometheus.NewDesc("switchback_worker_run_duration_seconds", "WorkerRun terminal duration", []string{"status", "harness"}, commonLabels),
	FamilyAgentTurnTerminal: prometheus.NewDesc("switchback_agent_turn_duration_seconds", "AgentTurn terminal duration", []string{"status", "harness"}, commonLabels),
}
var ageDesc = map[Family]*prometheus.Desc{
	FamilyCandidateProjection: prometheus.NewDesc("switchback_candidate_oldest_pending_age_seconds", "Oldest pending candidate age", nil, commonLabels),
	FamilyFragmentProjection:  prometheus.NewDesc("switchback_fragment_oldest_pending_age_seconds", "Oldest pending fragment age", nil, commonLabels),
}
var healthDesc = map[string]*prometheus.Desc{
	"worker_readers":   prometheus.NewDesc("switchback_telemetry_worker_readers", "Admitted Worker output readers including closing readers", nil, commonLabels),
	"worker_available": prometheus.NewDesc("switchback_telemetry_worker_authority_current", "Latest Worker authority discovery succeeded", nil, commonLabels),
	"worker_loss":      prometheus.NewDesc("switchback_telemetry_worker_gaps_total", "Worker observation gaps; not a byte-loss estimate", []string{"reason"}, commonLabels),
	"available":        prometheus.NewDesc("switchback_telemetry_sample_available", "Whether any successful sample exists", nil, commonLabels),
	"current":          prometheus.NewDesc("switchback_telemetry_sample_current", "Whether the latest sampling attempt succeeded", nil, commonLabels),
	"success":          prometheus.NewDesc("switchback_telemetry_sample_last_success_timestamp_seconds", "Last successful sample time", nil, commonLabels),
	"failures":         prometheus.NewDesc("switchback_telemetry_sample_failures_total", "Failed samples", nil, commonLabels),
	"depth":            prometheus.NewDesc("switchback_telemetry_queue_depth", "Queued events", nil, commonLabels),
	"capacity":         prometheus.NewDesc("switchback_telemetry_queue_capacity", "Event queue capacity", nil, commonLabels),
	"drop":             prometheus.NewDesc("switchback_telemetry_dropped_total", "Dropped events by bounded reason", []string{"reason"}, commonLabels),
	"first":            prometheus.NewDesc("switchback_telemetry_drop_first_timestamp_seconds", "First dropped event time by reason", []string{"reason"}, commonLabels),
	"last":             prometheus.NewDesc("switchback_telemetry_drop_last_timestamp_seconds", "Last dropped event time by reason", []string{"reason"}, commonLabels),
	"accepted":         prometheus.NewDesc("switchback_telemetry_receiver_accepted_total", "Events accepted by receiver", nil, commonLabels),
	"model_available":  prometheus.NewDesc("switchback_model_measurement_available", "Availability of model measurements in the local sample", []string{"measurement"}, commonLabels),
}

// Describe implements prometheus.Collector.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range countDesc {
		ch <- d
	}
	for _, d := range durationDesc {
		ch <- d
	}
	for _, d := range ageDesc {
		ch <- d
	}
	for _, d := range healthDesc {
		ch <- d
	}
}

// Collect implements prometheus.Collector and reads one coherent snapshot.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	g := func(k string, v float64) {
		ch <- prometheus.MustNewConstMetric(healthDesc[k], prometheus.GaugeValue, v)
	}
	c := func(k string, v float64) {
		ch <- prometheus.MustNewConstMetric(healthDesc[k], prometheus.CounterValue, v)
	}
	if m.available {
		g("available", 1)
		g("success", m.lastSuccess)
	} else {
		g("available", 0)
	}
	if m.current {
		g("current", 1)
	} else {
		g("current", 0)
	}
	c("failures", float64(m.failures))
	g("worker_readers", float64(m.workerReaders))
	if m.workerAvailable {
		g("worker_available", 1)
	} else {
		g("worker_available", 0)
	}
	for i, name := range workerLossNames {
		ch <- prometheus.MustNewConstMetric(healthDesc["worker_loss"], prometheus.CounterValue, float64(m.workerLoss[i]), name)
	}
	for _, measurement := range []string{"ttft", "cost", "usage"} {
		ch <- prometheus.MustNewConstMetric(healthDesc["model_available"], prometheus.GaugeValue, 0, measurement)
	}
	g("depth", m.queueDepth)
	g("capacity", m.queueCapacity)
	c("accepted", float64(m.accepted))
	for _, r := range []LossReason{LossInvalid, LossQueueFull, LossScanFailed, LossRemoteRejected, LossTransportFailed, LossShutdown} {
		ch <- prometheus.MustNewConstMetric(healthDesc["drop"], prometheus.CounterValue, float64(m.drops[r]), string(r))
		if m.firstDrop[r] > 0 {
			ch <- prometheus.MustNewConstMetric(healthDesc["first"], prometheus.GaugeValue, m.firstDrop[r], string(r))
			ch <- prometheus.MustNewConstMetric(healthDesc["last"], prometheus.GaugeValue, m.lastDrop[r], string(r))
		}
	}
	if !m.available {
		return
	}
	for _, v := range m.snapshot.Counts {
		labels := []string{string(v.Status)}
		if v.Family == FamilyWorkerRunTerminal || v.Family == FamilyAgentTurnTerminal {
			labels = append(labels, string(v.Harness))
		}
		ch <- prometheus.MustNewConstMetric(countDesc[v.Family], prometheus.GaugeValue, v.Value, labels...)
	}
	for _, v := range m.snapshot.Durations {
		b := make(map[float64]uint64, 11)
		for i, n := range v.Buckets {
			b[durationBuckets[i]] = n
		}
		ch <- prometheus.MustNewConstHistogram(durationDesc[v.Family], v.Count, v.SumSeconds, b, string(v.Status), string(v.Harness))
	}
	for _, v := range m.snapshot.OldestPending {
		ch <- prometheus.MustNewConstMetric(ageDesc[v.Family], prometheus.GaugeValue, v.Seconds)
	}
}
func (m *Metrics) dropped(r LossReason, n uint64, now time.Time) {
	m.mu.Lock()
	if m.drops[r] == 0 {
		m.firstDrop[r] = float64(now.Unix())
	}
	m.drops[r] += n
	m.lastDrop[r] = float64(now.Unix())
	m.mu.Unlock()
}
func (m *Metrics) queue(n int)            { m.mu.Lock(); m.queueDepth = float64(n); m.mu.Unlock() }
func (m *Metrics) receiverAccepted(n int) { m.mu.Lock(); m.accepted += uint64(n); m.mu.Unlock() }
