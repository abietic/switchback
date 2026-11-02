package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

var observationServices = []string{"langfuse-web", "langfuse-worker", "langfuse-clickhouse", "langfuse-redis", "langfuse-minio"}

// ServiceInterval is derived from adjacent independent inspections/probes and
// the uninterrupted Docker event subscription between them. Running alone is
// deliberately not a health proof. A collector gap produces no interval.
type ServiceInterval struct {
	Project        string    `json:"project"`
	Service        string    `json:"service"`
	ContainerID    string    `json:"container_id"`
	From           time.Time `json:"from"`
	To             time.Time `json:"to"`
	State          string    `json:"state"`
	EvidenceDigest string    `json:"evidence_digest"`
}

func readClaim(project, output string) (map[string]string, error) {
	if !projectPattern.MatchString(project) || !filepath.IsAbs(output) || filepath.Clean(output) != output {
		return nil, fmt.Errorf("claim identity invalid")
	}
	canonical, err := filepath.EvalSymlinks(output)
	if err != nil || canonical != output {
		return nil, fmt.Errorf("claim output must be canonical")
	}
	b, err := os.ReadFile(filepath.Join(output, "claim.json"))
	if err != nil {
		return nil, err
	}
	var c map[string]string
	if json.Unmarshal(b, &c) != nil || c["project"] != project || c["output"] != output {
		return nil, fmt.Errorf("claim mismatch")
	}
	return c, nil
}

func writeEvidence(output, name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(output, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

type subscriptionExit struct {
	At          time.Time
	Err         error
	Interrupted bool
}

func classifySubscriptionExit(requested time.Time, exit subscriptionExit) string {
	if !requested.IsZero() && !exit.At.Before(requested) && exit.Interrupted {
		return "requested_stop"
	}
	return "unexpected_eof"
}

// intervalCollection owns the subscription, probes and their completion. Only
// the collector goroutine records probes; readiness/finish acknowledgements
// therefore cannot race ahead of evidence recording.
type intervalCollection struct {
	ready    chan error
	finish   chan struct{}
	done     chan struct{}
	evidence IntervalEvidence
	err      error
}
type intervalCollectorIO struct {
	subscribe func(time.Time, func(ServiceEvent)) (<-chan subscriptionExit, func(), error)
	handshake func(context.Context) (SubscriptionHandshake, error)
	probe     func(context.Context, string) ([]ServiceProbe, error)
	worker    *fullStackWorkerCapture
}

func startIntervalCollection(ctx context.Context, project, state string, io intervalCollectorIO) *intervalCollection {
	return startIntervalCollectionForServices(ctx, project, state, observationServices, io)
}

func startIntervalCollectionForServices(ctx context.Context, project, state string, services []string, io intervalCollectorIO) *intervalCollection {
	c := &intervalCollection{ready: make(chan error, 1), finish: make(chan struct{}), done: make(chan struct{})}
	go c.run(ctx, project, state, services, io)
	return c
}
func (c *intervalCollection) waitReady(ctx context.Context) error {
	select {
	case err := <-c.ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *intervalCollection) finishAndWait(ctx context.Context) (IntervalEvidence, error) {
	select {
	case c.finish <- struct{}{}:
	case <-c.done:
	case <-ctx.Done():
		return IntervalEvidence{}, ctx.Err()
	}
	select {
	case <-c.done:
		return c.evidence, c.err
	case <-ctx.Done():
		return IntervalEvidence{}, ctx.Err()
	}
}
func (c *intervalCollection) run(ctx context.Context, project, state string, services []string, io intervalCollectorIO) {
	defer close(c.done)
	from := time.Now().UTC()
	s := EventSubscription{ID: fmt.Sprint(from.UnixNano()), Project: project, From: from}
	if io.worker != nil {
		s.Scope = "project_and_exact_worker"
		s.WorkerContainerID = io.worker.Container.ID
	}
	var mu sync.Mutex
	recorded := make(chan struct{}, 1)
	ended, stop, err := io.subscribe(from, func(e ServiceEvent) {
		mu.Lock()
		s.Events = append(s.Events, e)
		mu.Unlock()
		select {
		case recorded <- struct{}{}:
		default:
		}
	})
	if err != nil {
		c.err = err
		c.ready <- err
		return
	}
	defer stop()
	var exit subscriptionExit
	joined := false
	ready := false
	normal := false
	var requested time.Time
	handshakeStage := "handshake_command"
	if io.handshake == nil {
		err = fmt.Errorf("collector subscription handshake missing")
	} else {
		var h SubscriptionHandshake
		h, err = io.handshake(ctx)
		attempt := h
		s.HandshakeAttempt = &attempt
		if err == nil {
			handshakeStage = "handshake_events"
		}
		for err == nil {
			select {
			case exit = <-ended:
				joined = true
				err = fmt.Errorf("collector event EOF before handshake")
			default:
			}
			if err != nil {
				break
			}
			mu.Lock()
			execID := recordedHandshake(project, from, h, s.Events)
			mu.Unlock()
			if execID != "" {
				h.ExecID = execID
				s.Handshake = &h
				break
			}
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case exit = <-ended:
				joined = true
				err = fmt.Errorf("collector event EOF before handshake")
			case <-recorded:
			}
		}
	}
	if err != nil {
		s.HandshakeFailureStage = handshakeStage
	}
	probe := func() error {
		probes, err := io.probe(ctx, project)
		if err != nil {
			return err
		}
		// Persist each actual probe, including failed/wrong-state attempts.
		s.Probes = append(s.Probes, probes...)
		seen := map[string]bool{}
		for _, p := range probes {
			if p.Project != project || seen[p.Service] || p.From.Before(s.From) || p.To.Before(p.From) || (state != "" && p.State != state) {
				return fmt.Errorf("collector recorded probe state/identity mismatch")
			}
			if p.State == "healthy" && (p.Kind != "independent_health_probe" || p.ContainerID == "" || p.StartedAt.IsZero()) {
				return fmt.Errorf("collector healthy identity proof missing")
			}
			if p.State == "unavailable" && p.Kind != "exact_inspect" {
				return fmt.Errorf("collector unavailable inspect missing")
			}
			seen[p.Service] = true
		}
		for _, service := range services {
			if !seen[service] {
				return fmt.Errorf("collector service probe missing")
			}
		}
		return nil
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	if err == nil {
		err = probe()
	}
	if err == nil {
		select {
		case exit = <-ended:
			joined = true
			err = fmt.Errorf("collector event EOF before readiness")
		default:
		}
	}
	if err == nil {
		ready = true
		c.ready <- nil
	}
	for err == nil {
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case exit = <-ended:
			joined = true
			err = fmt.Errorf("collector unexpected event EOF")
		case <-c.finish:
			// A new recorded probe after the caller's real endpoint closes coverage.
			err = probe()
			if err == nil {
				normal = true
			}
		case <-ticker.C:
			err = probe()
		}
		if normal {
			break
		}
	}
	requested = time.Now().UTC()
	if ctx.Err() != nil {
		normal = false
		err = errors.Join(err, ctx.Err())
	}
	stop()
	if !joined {
		exit = <-ended
	}
	s.To = exit.At
	s.End = classifySubscriptionExit(requested, exit)
	if !normal || s.End != "requested_stop" {
		if err == nil {
			err = fmt.Errorf("collector unexpected event EOF")
		}
		if s.End == "requested_stop" {
			s.End = "cancelled_or_probe_error"
		}
	}
	if !ready {
		if s.HandshakeFailureStage != "" {
			err = fmt.Errorf("collector %s: %w", s.HandshakeFailureStage, err)
		}
		c.ready <- err
	}
	mu.Lock()
	c.evidence = IntervalEvidence{Subscriptions: []EventSubscription{s}}
	mu.Unlock()
	c.evidence.Intervals, c.err = c.evidence.derive(project)
	c.err = errors.Join(c.err, err)
}

func dockerIntervalCollector(project string) intervalCollectorIO {
	return intervalCollectorIO{probe: probeObservationServices, handshake: func(ctx context.Context) (SubscriptionHandshake, error) {
		c, err := exactServiceContainer(ctx, project, "postgres")
		if err != nil {
			return SubscriptionHandshake{}, err
		}
		if !c.Running {
			return SubscriptionHandshake{}, fmt.Errorf("collector handshake postgres is not running")
		}
		h := SubscriptionHandshake{ContainerID: c.ID, From: time.Now().UTC()}
		// Fixed read-only readiness command; Output/Env/health logs are not captured.
		err = exec.CommandContext(ctx, "docker", "exec", c.ID, "pg_isready").Run()
		h.To = time.Now().UTC()
		if err != nil {
			return h, fmt.Errorf("collector subscription handshake probe failed")
		}
		return h, nil
	}, subscribe: dockerEventSubscriber(project, nil)}
}

// dockerEventSubscriber owns one process, reader and exit classification.
// Legacy subscriptions retain the Compose-project server filter. Matrix
// subscriptions use the same stream lifecycle with exact Worker scope.
func dockerEventSubscriber(project string, worker *fullStackWorkerCapture) func(time.Time, func(ServiceEvent)) (<-chan subscriptionExit, func(), error) {
	return func(from time.Time, record func(ServiceEvent)) (<-chan subscriptionExit, func(), error) {
		commandCtx, stop := context.WithCancel(context.Background())
		format := `{"id":{{json .Actor.ID}},"project":{{json (index .Actor.Attributes "com.docker.compose.project")}},"service":{{json (index .Actor.Attributes "com.docker.compose.service")}},"exec_id":{{json (index .Actor.Attributes "execID")}},"action":{{json .Action}},"nano":{{.TimeNano}}}`
		args := []string{"events", "--since", from.Format(time.RFC3339Nano), "--filter", "type=container"}
		if worker == nil {
			args = append(args, "--filter", "label=com.docker.compose.project="+project)
		} else {
			format = `{"id":{{json .Actor.ID}},"project":{{json (index .Actor.Attributes "com.docker.compose.project")}},"service":{{json (index .Actor.Attributes "com.docker.compose.service")}},"worker_session_id":{{json (index .Actor.Attributes "com.switchback.worker-session-id")}},"runtime_instance_id":{{json (index .Actor.Attributes "com.switchback.runtime-instance-id")}},"runtime_generation":{{json (index .Actor.Attributes "com.switchback.runtime-generation")}},"exec_id":{{json (index .Actor.Attributes "execID")}},"action":{{json .Action}},"nano":{{.TimeNano}}}`
		}
		args = append(args, "--format", format)
		cmd := exec.CommandContext(commandCtx, "docker", args...)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			stop()
			return nil, nil, err
		}
		if err = cmd.Start(); err != nil {
			stop()
			return nil, nil, fmt.Errorf("start owned events: %w", err)
		}
		done := make(chan subscriptionExit, 1)
		go func() {
			var decodeErr error
			scanner := bufio.NewScanner(pipe)
			scanner.Buffer(make([]byte, 4096), 1<<20)
			for scanner.Scan() {
				var raw matrixDockerEvent
				if json.Unmarshal(scanner.Bytes(), &raw) != nil {
					decodeErr = fmt.Errorf("event decode failed")
					_ = cmd.Process.Kill()
					break
				}
				if worker != nil {
					e, owned, selectErr := matrixOwnedEvent(project, *worker, raw)
					if selectErr != nil {
						decodeErr = selectErr
						_ = cmd.Process.Kill()
						break
					}
					if owned {
						record(e)
					}
					continue
				}
				action := raw.Action
				for _, safe := range []string{"exec_create", "exec_start", "exec_die"} {
					if strings.HasPrefix(action, safe) {
						action = safe
					}
				}
				record(ServiceEvent{Project: raw.Project, Service: raw.Service, ContainerID: raw.ID, At: time.Unix(0, raw.Nano).UTC(), Action: action, ExecID: raw.ExecID})
			}
			scanErr := scanner.Err()
			at := time.Now().UTC()
			waitErr := cmd.Wait()
			result := subscriptionExit{At: at, Err: waitErr}
			if decodeErr != nil {
				result.Err = decodeErr
			} else if scanErr != nil {
				result.Err = scanErr
			} else if cmd.ProcessState != nil {
				if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
					result.Interrupted = true
				}
			}
			done <- result
		}()
		return done, stop, nil
	}
}
func saveIntervals(project, output string, e IntervalEvidence) error {
	if len(e.Subscriptions) != 1 {
		return fmt.Errorf("collector subscription missing")
	}
	name := "intervals-" + e.Subscriptions[0].ID + ".json"
	if err := writeEvidence(output, name, e); err != nil {
		return err
	}
	fmt.Println(filepath.Join(output, name))
	return nil
}

// collectIntervals' signal is a request for the final recorded probe and join.
// Cancellation of the internal collector itself remains fail-closed.
func collectIntervals(ctx context.Context, project, output string) error {
	return collectIntervalsWithIO(ctx, project, output, dockerIntervalCollector(project), func() {
		fmt.Printf("collector_ready project=%s\n", project)
	}, saveIntervals)
}

func collectIntervalsWithIO(ctx context.Context, project, output string, io intervalCollectorIO, announceReady func(), save func(string, string, IntervalEvidence) error) error {
	return collectIntervalsWithServices(ctx, project, output, observationServices, io, announceReady, save)
}

func collectIntervalsWithServices(ctx context.Context, project, output string, services []string, io intervalCollectorIO, announceReady func(), save func(string, string, IntervalEvidence) error) error {
	if _, err := readClaim(project, output); err != nil {
		return err
	}
	collectorCtx, cancel := context.WithCancel(context.Background())
	if deadline, ok := ctx.Deadline(); ok {
		cancel()
		collectorCtx, cancel = context.WithDeadline(context.Background(), deadline)
	}
	defer cancel()
	c := startIntervalCollectionForServices(collectorCtx, project, "", services, io)
	if readyErr := c.waitReady(ctx); readyErr != nil {
		cancel()
		<-c.done
		var saveErr error
		if len(c.evidence.Subscriptions) > 0 {
			saveErr = save(project, output, c.evidence)
		}
		return errors.Join(readyErr, saveErr)
	}
	announceReady()
	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			deadlineErr := ctx.Err()
			cancel()
			<-c.done
			saveErr := save(project, output, c.evidence)
			return errors.Join(deadlineErr, saveErr)
		}
	case <-c.done:
	}
	e, finishErr := c.finishAndWait(collectorCtx)
	if finishErr != nil {
		cancel()
		<-c.done
		e = c.evidence
	}
	saveErr := save(project, output, e)
	if err := errors.Join(finishErr, saveErr); err != nil {
		return err
	}
	// Cold collection must finish successfully before any Langfuse restoration.
	if raw, readErr := os.ReadFile(filepath.Join(output, "smoke-pending.json")); readErr == nil {
		var pending RawEvidence
		if json.Unmarshal(raw, &pending) != nil {
			return fmt.Errorf("cold pending evidence invalid")
		}
		m, err := measurementFromRaw(pending)
		if err != nil {
			return err
		}
		raw, err = os.ReadFile(filepath.Join(output, "cold-before.json"))
		if err != nil {
			return err
		}
		var before ProjectCapture
		if json.Unmarshal(raw, &before) != nil || before.Project != project {
			return fmt.Errorf("cold before capture invalid")
		}
		if len(services) > len(observationServices) {
			raw, err = os.ReadFile(filepath.Join(output, "cold-after.json"))
			if err != nil {
				return err
			}
			var after ProjectCapture
			if json.Unmarshal(raw, &after) != nil || after.Project != project {
				return fmt.Errorf("cold after capture invalid")
			}
			return e.validateWindowForServices(project, "unavailable", before.StartedAt, after.CompletedAt, services)
		}
		return e.validateWindowForServices(project, "unavailable", before.CompletedAt, m.To, services)
	}
	return nil
}

// measureWithIntervals is the matrix's single measurement lifecycle boundary.
func measureWithIntervals(ctx context.Context, project, state string, io intervalCollectorIO, measure func() (RawEvidence, error)) (RawEvidence, IntervalEvidence, error) {
	return measureWithIntervalsForServices(ctx, project, state, observationServices, io, measure)
}

func measureWithIntervalsForServices(ctx context.Context, project, state string, services []string, io intervalCollectorIO, measure func() (RawEvidence, error)) (RawEvidence, IntervalEvidence, error) {
	collectorCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := startIntervalCollectionForServices(collectorCtx, project, state, services, io)
	if err := c.waitReady(ctx); err != nil {
		cancel()
		<-c.done
		return RawEvidence{}, c.evidence, errors.Join(err, c.err)
	}
	raw, err := measure()
	e, collectErr := c.finishAndWait(ctx)
	if collectErr != nil {
		cancel()
		<-c.done
		e = c.evidence
	}
	if err == nil {
		err = collectErr
	}
	if err == nil {
		m, timingErr := measurementFromRaw(raw)
		err = timingErr
		if err == nil {
			err = e.validateWindowForServices(project, state, m.From, m.To, services)
		}
	}
	return raw, e, err
}

func probeObservationServices(ctx context.Context, project string) ([]ServiceProbe, error) {
	containers, err := inspectOwnedServiceContainers(ctx, project)
	if err != nil {
		return nil, err
	}
	results := make([]ServiceProbe, len(observationServices))
	var wg sync.WaitGroup
	for i, service := range observationServices {
		wg.Add(1)
		go func(i int, service string) {
			defer wg.Done()
			c, exists := containers[service]
			p := ServiceProbe{Project: project, Service: service, ContainerID: c.ID, StartedAt: c.StartedAt, From: time.Now().UTC(), State: "unavailable", Kind: "exact_inspect"}
			if exists && c.Running {
				p.State = "unknown"
				p.Kind = "independent_health_probe"
				var argv []string
				switch service {
				case "langfuse-web":
					argv = []string{"node", "-e", `fetch('http://127.0.0.1:3000/api/public/health?failIfDatabaseUnavailable=true',{signal:AbortSignal.timeout(2000)}).then(r=>process.exit(r.status===200?0:1)).catch(()=>process.exit(1))`}
				case "langfuse-worker":
					argv = []string{"node", "-e", `fetch('http://127.0.0.1:3030/api/ready',{signal:AbortSignal.timeout(2000)}).then(r=>process.exit(r.status===200?0:1)).catch(()=>process.exit(1))`}
				case "langfuse-clickhouse":
					argv = []string{"wget", "--no-verbose", "--tries=1", "--spider", "http://127.0.0.1:8123/ping"}
				case "langfuse-minio":
					argv = []string{"mc", "ready", "local"}
				case "langfuse-redis":
					argv = []string{"sh", "-c", `REDISCLI_AUTH="$REDIS_AUTH" redis-cli ping | grep -qx PONG`}
				}
				probeCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
				err := exec.CommandContext(probeCtx, "docker", append([]string{"exec", c.ID}, argv...)...).Run()
				cancel()
				if err == nil {
					p.State = "healthy"
				}
			}
			p.To = time.Now().UTC()
			results[i] = p
		}(i, service)
	}
	wg.Wait()
	return results, nil
}

func inspectOwnedServiceContainers(ctx context.Context, project string) (map[string]CapturedContainer, error) {
	list, err := exec.CommandContext(ctx, "docker", "ps", "--all", "--no-trunc", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.ID}}").Output()
	if err != nil {
		return nil, fmt.Errorf("owned service inventory failed")
	}
	containers := map[string]CapturedContainer{}
	for _, id := range strings.Fields(string(list)) {
		raw, e := exec.CommandContext(ctx, "docker", "inspect", "--format", inspectTemplate, id).Output()
		if e != nil {
			return nil, fmt.Errorf("owned service inspect failed")
		}
		var c CapturedContainer
		if json.Unmarshal(raw, &c) != nil || c.Project != project || c.ID != id {
			return nil, fmt.Errorf("service ownership changed")
		}
		if _, ok := containers[c.Service]; ok {
			return nil, fmt.Errorf("duplicate service identity")
		}
		containers[c.Service] = c
	}
	return containers, nil
}

type ServiceProbe struct {
	Project     string    `json:"project"`
	Service     string    `json:"service"`
	ContainerID string    `json:"container_id"`
	StartedAt   time.Time `json:"container_started_at"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	State       string    `json:"state"`
	Kind        string    `json:"kind"`
}

type ServiceEvent struct {
	ExecID          string    `json:"exec_id,omitempty"`
	Project         string    `json:"project"`
	Service         string    `json:"service"`
	ContainerID     string    `json:"container_id"`
	At              time.Time `json:"at"`
	Action          string    `json:"action"`
	WorkerSessionID string    `json:"worker_session_id,omitempty"`
	RuntimeID       string    `json:"runtime_instance_id,omitempty"`
	Generation      uint64    `json:"runtime_generation,omitempty"`
}

type EventSubscription struct {
	HandshakeAttempt      *SubscriptionHandshake `json:"handshake_attempt,omitempty"`
	HandshakeFailureStage string                 `json:"handshake_failure_stage,omitempty"`
	Scope                 string                 `json:"scope,omitempty"`
	WorkerContainerID     string                 `json:"worker_container_id,omitempty"`
	Handshake             *SubscriptionHandshake `json:"subscription_handshake,omitempty"`
	ID                    string                 `json:"id"`
	Project               string                 `json:"project"`
	From                  time.Time              `json:"from"`
	To                    time.Time              `json:"to"`
	End                   string                 `json:"end"` // requested_stop or unexpected_eof/error
	Events                []ServiceEvent         `json:"events"`
	Probes                []ServiceProbe         `json:"probes"`
}

type SubscriptionHandshake struct {
	ContainerID string    `json:"postgres_container_id"`
	ExecID      string    `json:"exec_id"`
	From        time.Time `json:"command_started_at"`
	To          time.Time `json:"command_finished_at"`
}

func recordedHandshake(project string, subscriptionFrom time.Time, h SubscriptionHandshake, events []ServiceEvent) string {
	if h.ContainerID == "" || h.From.Before(subscriptionFrom) || h.To.Before(h.From) {
		return ""
	}
	created := map[string]time.Time{}
	for _, e := range events {
		if e.Project != project || e.Service != "postgres" || e.ContainerID != h.ContainerID || e.ExecID == "" || e.At.Before(h.From) || e.At.After(h.To) {
			continue
		}
		if e.Action == "exec_create" {
			created[e.ExecID] = e.At
		}
		if at, ok := created[e.ExecID]; ok && e.Action == "exec_die" && !e.At.Before(at) {
			return e.ExecID
		}
	}
	return ""
}

type IntervalEvidence struct {
	Subscriptions []EventSubscription `json:"subscriptions"`
	Intervals     []ServiceInterval   `json:"intervals"`
}

func evidenceDigest(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func intervalCovers(interval ServiceInterval, from, to time.Time) bool {
	return !interval.From.After(from) && !interval.To.Before(to) && !to.Before(from)
}

func (e IntervalEvidence) derive(project string) ([]ServiceInterval, error) {
	var result []ServiceInterval
	seen := map[string]bool{}
	for _, stream := range e.Subscriptions {
		if stream.Project != project || stream.ID == "" || seen[stream.ID] || stream.From.IsZero() || !stream.To.After(stream.From) {
			return nil, fmt.Errorf("interval subscription identity/order invalid")
		}
		seen[stream.ID] = true
		// No extrapolation across a lost event stream, even with healthy probes.
		if stream.End != "requested_stop" {
			continue
		}
		if stream.Handshake == nil || stream.Handshake.ExecID == "" || stream.Handshake.To.After(stream.To) || recordedHandshake(project, stream.From, *stream.Handshake, stream.Events) != stream.Handshake.ExecID {
			return nil, fmt.Errorf("interval subscription handshake missing or invalid")
		}
		groups := map[string][]ServiceProbe{}
		for _, p := range stream.Probes {
			if p.Project != project || p.Service == "" || p.From.Before(stream.Handshake.To) || p.To.After(stream.To) || p.To.Before(p.From) {
				return nil, fmt.Errorf("interval probe identity/window invalid")
			}
			if p.State == "healthy" && (p.ContainerID == "" || p.StartedAt.IsZero() || p.Kind != "independent_health_probe") {
				return nil, fmt.Errorf("interval healthy state lacks independent probe")
			}
			if p.State == "unavailable" && p.Kind != "exact_inspect" {
				return nil, fmt.Errorf("interval unavailable state lacks inspect")
			}
			groups[p.Service] = append(groups[p.Service], p)
		}
		for _, event := range stream.Events {
			if event.Project != project || event.ContainerID == "" || event.Service == "" || event.At.Before(stream.From) || event.At.After(stream.To) {
				return nil, fmt.Errorf("interval event identity/window invalid")
			}
		}
		for service, probes := range groups {
			sort.Slice(probes, func(i, j int) bool { return probes[i].From.Before(probes[j].From) })
			for i := 1; i < len(probes); i++ {
				a, b := probes[i-1], probes[i]
				if a.State != b.State || (a.State != "healthy" && a.State != "unavailable") || a.ContainerID != b.ContainerID || !a.StartedAt.Equal(b.StartedAt) || b.From.Before(a.To) || b.To.Sub(a.From) > 5*time.Second {
					continue
				}
				var events []ServiceEvent
				stable := true
				for _, ev := range stream.Events {
					if ev.Service != service || ev.At.Before(a.From) || ev.At.After(b.To) {
						continue
					}
					events = append(events, ev)
					// Exec probe events are non-state-changing. Any other transition
					// invalidates this span; next exact probes establish new identity.
					if ev.ContainerID != a.ContainerID || (ev.Action != "exec_create" && ev.Action != "exec_start" && ev.Action != "exec_die" && ev.Action != "health_status: healthy") {
						stable = false
					}
				}
				if stable {
					result = append(result, ServiceInterval{Project: project, Service: service, ContainerID: a.ContainerID, From: a.From, To: b.To, State: a.State, EvidenceDigest: evidenceDigest(struct {
						Subscription  string
						Before, After ServiceProbe
						Events        []ServiceEvent
					}{stream.ID, a, b, events})})
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.From.Before(b.From)
	})
	return result, nil
}

func (e IntervalEvidence) validateWindow(project, state string, from, to time.Time) error {
	return e.validateWindowForServices(project, state, from, to, observationServices)
}

func (e IntervalEvidence) validateWindowForServices(project, state string, from, to time.Time, services []string) error {
	if from.IsZero() || to.Before(from) {
		return fmt.Errorf("interval measurement window invalid")
	}
	derived, err := e.derive(project)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(derived, e.Intervals) {
		return fmt.Errorf("interval digest or source evidence mismatch")
	}
	for _, service := range services {
		cursor := from
		for _, v := range derived {
			if v.Service != service || v.State != state || v.To.Before(cursor) || v.From.After(cursor) {
				continue
			}
			if v.To.After(cursor) {
				cursor = v.To
			}
		}
		if cursor.Before(to) {
			return fmt.Errorf("interval %s lacks continuous %s coverage", service, state)
		}
	}
	return nil
}
