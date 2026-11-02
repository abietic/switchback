package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const nativeCollectorProject = "switchback-obsv-a1234567"

func TestMeasurementPreservesUnmatchedHandshakeAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var attempt SubscriptionHandshake
		io := controlledCollectorIO(func(context.Context, string) ([]ServiceProbe, error) {
			t.Fatal("probe ran before accepted handshake")
			return nil, nil
		})
		io.handshake = func(context.Context) (SubscriptionHandshake, error) {
			attempt = SubscriptionHandshake{ContainerID: "postgres", From: time.Now().UTC().Add(time.Nanosecond), To: time.Now().UTC().Add(2 * time.Nanosecond)}
			return attempt, nil
		}
		_, evidence, err := measureWithIntervalsForServices(ctx, nativeCollectorProject, "healthy", observationServices, io, func() (RawEvidence, error) {
			t.Fatal("measurement ran before accepted handshake")
			return RawEvidence{}, nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "handshake_events") {
			t.Fatalf("error = %v, want staged handshake cancellation", err)
		}
		data, marshalErr := json.Marshal(evidence)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var persisted struct {
			Subscriptions []struct {
				Attempt   *SubscriptionHandshake `json:"handshake_attempt"`
				Handshake *SubscriptionHandshake `json:"subscription_handshake"`
				Failure   string                 `json:"handshake_failure_stage"`
			} `json:"subscriptions"`
		}
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatal(err)
		}
		if len(persisted.Subscriptions) != 1 {
			t.Fatalf("lost subscription: %s", data)
		}
		s := persisted.Subscriptions[0]
		if s.Attempt == nil || *s.Attempt != attempt || s.Handshake != nil || s.Failure != "handshake_events" {
			t.Fatalf("lost or accepted failed attempt: %s", data)
		}
		if evidence.validateWindowForServices(nativeCollectorProject, "healthy", attempt.From, attempt.To, observationServices) == nil {
			t.Fatal("failed handshake accepted as coverage")
		}
	})
}

func TestMeasurementPreservesFailedHandshakeCommandWithoutOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempt := SubscriptionHandshake{ContainerID: "postgres", From: time.Now().UTC(), To: time.Now().UTC()}
		cause := errors.New("secret-command-output")
		io := controlledCollectorIO(func(context.Context, string) ([]ServiceProbe, error) {
			t.Fatal("probe ran after command failure")
			return nil, nil
		})
		io.handshake = func(context.Context) (SubscriptionHandshake, error) { return attempt, cause }
		_, evidence, err := measureWithIntervalsForServices(context.Background(), nativeCollectorProject, "healthy", observationServices, io, func() (RawEvidence, error) {
			t.Fatal("measurement ran after command failure")
			return RawEvidence{}, nil
		})
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "handshake_command") {
			t.Fatalf("lost command cause/stage: %v", err)
		}
		data, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip IntervalEvidence
		if err := json.Unmarshal(data, &roundTrip); err != nil {
			t.Fatal(err)
		}
		if len(roundTrip.Subscriptions) != 1 {
			t.Fatalf("lost failed attempt: %s", data)
		}
		s := roundTrip.Subscriptions[0]
		if s.HandshakeAttempt == nil || *s.HandshakeAttempt != attempt || s.Handshake != nil || s.HandshakeFailureStage != "handshake_command" || strings.Contains(string(data), "secret-command-output") {
			t.Fatalf("unsafe/incomplete command evidence: %s", data)
		}
		if evidence.validateWindowForServices(nativeCollectorProject, "healthy", attempt.From, attempt.To, observationServices) == nil {
			t.Fatal("command failure accepted as coverage")
		}
	})
}

func TestMeasurementKeepsAttemptSeparateFromAcceptedHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		io := controlledCollectorIO(func(_ context.Context, project string) ([]ServiceProbe, error) {
			return nativeProbes(project, "healthy"), nil
		})
		_, evidence, err := measureWithIntervalsForServices(context.Background(), nativeCollectorProject, "healthy", observationServices, io, func() (RawEvidence, error) {
			time.Sleep(time.Millisecond)
			return nativeTiming(), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		s := evidence.Subscriptions[0]
		if s.HandshakeAttempt == nil || s.HandshakeAttempt.ExecID != "" || s.Handshake == nil || s.Handshake.ExecID != "handshake" || s.HandshakeFailureStage != "" {
			t.Fatalf("attempt conflated with accepted events: %#v", s)
		}
		data, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		var persisted IntervalEvidence
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatal(err)
		}
		if err := persisted.validateWindowForServices(nativeCollectorProject, "healthy", s.Handshake.To, s.Probes[len(s.Probes)-1].To, observationServices); err != nil {
			t.Fatalf("successful evidence rejected: %v", err)
		}
	})
}

func TestCollectIntervalsJoinsAndPersistsFailedDeadlineCollection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := writeNativeCollectorClaim(t)
		io, closingProbe, stopCalled, allowReaderExit, readerJoined := blockedClosingCollectorIO()
		wrapperReady := make(chan struct{})
		deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), time.Second)
		defer deadlineCancel()
		ctx, requestFinish := context.WithCancel(deadlineCtx)
		result := make(chan error, 1)
		go func() {
			result <- collectIntervalsWithIO(ctx, nativeCollectorProject, output, io, func() { close(wrapperReady) }, saveIntervals)
		}()

		<-wrapperReady
		requestFinish()
		<-closingProbe
		<-stopCalled
		var earlyErr error
		returnedEarly := false
		select {
		case earlyErr = <-result:
			returnedEarly = true
		default:
		}
		close(allowReaderExit)
		if !returnedEarly {
			earlyErr = <-result
		} else {
			t.Errorf("collector wrapper returned before owned reader joined: %v", earlyErr)
		}
		<-readerJoined
		if !errors.Is(earlyErr, context.DeadlineExceeded) {
			t.Errorf("error = %v, want original deadline cause", earlyErr)
		}

		entries, err := os.ReadDir(output)
		if err != nil {
			t.Fatal(err)
		}
		var evidenceFiles []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "intervals-") && strings.HasSuffix(entry.Name(), ".json") {
				evidenceFiles = append(evidenceFiles, entry.Name())
			}
		}
		if len(evidenceFiles) != 1 {
			t.Fatalf("persisted interval evidence files = %v, want one", evidenceFiles)
		}
		data, err := os.ReadFile(filepath.Join(output, evidenceFiles[0]))
		if err != nil {
			t.Fatal(err)
		}
		var evidence IntervalEvidence
		if err = json.Unmarshal(data, &evidence); err != nil {
			t.Fatal(err)
		}
		if len(evidence.Subscriptions) != 1 {
			t.Fatalf("persisted subscriptions = %d, want one", len(evidence.Subscriptions))
		}
		subscription := evidence.Subscriptions[0]
		if subscription.End == "requested_stop" || len(subscription.Probes) == 0 || len(subscription.Events) == 0 {
			t.Fatalf("failed evidence was not preserved: %#v", subscription)
		}
	})
}

func TestCollectIntervalsCombinesSaveFailureWithFinishCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := writeNativeCollectorClaim(t)
		io, closingProbe, stopCalled, allowReaderExit, readerJoined := blockedClosingCollectorIO()
		wrapperReady := make(chan struct{})
		deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), time.Second)
		defer deadlineCancel()
		ctx, requestFinish := context.WithCancel(deadlineCtx)
		saveFailure := errors.New("persist failed evidence")
		saved := make(chan IntervalEvidence, 1)
		result := make(chan error, 1)
		go func() {
			result <- collectIntervalsWithIO(ctx, nativeCollectorProject, output, io, func() { close(wrapperReady) }, func(_, _ string, evidence IntervalEvidence) error {
				saved <- evidence
				return saveFailure
			})
		}()

		<-wrapperReady
		requestFinish()
		<-closingProbe
		<-stopCalled
		var earlyErr error
		returnedEarly := false
		select {
		case earlyErr = <-result:
			returnedEarly = true
		default:
		}
		close(allowReaderExit)
		if !returnedEarly {
			earlyErr = <-result
		} else {
			t.Errorf("collector wrapper returned before owned reader joined: %v", earlyErr)
		}
		<-readerJoined
		if !errors.Is(earlyErr, context.DeadlineExceeded) || !errors.Is(earlyErr, saveFailure) {
			t.Errorf("error = %v, want joined deadline and save causes", earlyErr)
		}
		evidence := <-saved
		if len(evidence.Subscriptions) != 1 || len(evidence.Subscriptions[0].Probes) == 0 || len(evidence.Subscriptions[0].Events) == 0 {
			t.Fatalf("save received incomplete failed evidence: %#v", evidence)
		}
	})
}

func writeNativeCollectorClaim(t *testing.T) string {
	t.Helper()
	output, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := json.Marshal(map[string]string{"project": nativeCollectorProject, "output": output})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(output, "claim.json"), claim, 0600); err != nil {
		t.Fatal(err)
	}
	return output
}

func blockedClosingCollectorIO() (intervalCollectorIO, <-chan struct{}, <-chan struct{}, chan struct{}, <-chan struct{}) {
	closingProbe := make(chan struct{})
	stopCalled := make(chan struct{})
	allowReaderExit := make(chan struct{})
	readerJoined := make(chan struct{})
	var probes atomic.Int32
	var closingOnce, stopOnce sync.Once
	var handshake SubscriptionHandshake
	io := intervalCollectorIO{
		handshake: func(context.Context) (SubscriptionHandshake, error) { return handshake, nil },
		probe: func(ctx context.Context, project string) ([]ServiceProbe, error) {
			if probes.Add(1) == 1 {
				return nativeProbes(project, "healthy"), nil
			}
			closingOnce.Do(func() { close(closingProbe) })
			<-ctx.Done()
			return nil, ctx.Err()
		},
		subscribe: func(_ time.Time, record func(ServiceEvent)) (<-chan subscriptionExit, func(), error) {
			handshake = recordNativeHandshake(record)
			ended := make(chan subscriptionExit)
			return ended, func() {
				stopOnce.Do(func() {
					close(stopCalled)
					go func() {
						<-allowReaderExit
						ended <- subscriptionExit{At: time.Now().UTC(), Interrupted: true}
						close(readerJoined)
					}()
				})
			}, nil
		},
	}
	return io, closingProbe, stopCalled, allowReaderExit, readerJoined
}

func TestMeasurementWaitsForRecordedInitialProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		initial := make(chan struct{})
		measured := make(chan bool, 1)
		var recorded atomic.Bool
		io := controlledCollectorIO(func(ctx context.Context, project string) ([]ServiceProbe, error) {
			select {
			case <-initial:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			recorded.Store(true)
			return nativeProbes(project, "healthy"), nil
		})
		done := make(chan error, 1)
		go func() {
			_, _, err := measureWithIntervals(ctx, "switchback-obsv-a1234567", "healthy", io, func() (RawEvidence, error) { measured <- recorded.Load(); return nativeTiming(), nil })
			done <- err
		}()
		synctest.Wait()
		select {
		case ready := <-measured:
			close(initial)
			<-done
			if !ready {
				t.Fatal("measurement began before initial recorded probe")
			}
		default:
			close(initial)
			if !<-measured {
				t.Fatal("measurement began before initial recorded probe")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestFullStackMatrixMeasurementRejectsMissingNinthProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	io := controlledCollectorIO(func(context.Context, string) ([]ServiceProbe, error) {
		return nativeProbes(nativeCollectorProject, "healthy"), nil
	})
	_, evidence, err := measureWithIntervalsForServices(ctx, nativeCollectorProject, "healthy", fullStackObservationServices, io, func() (RawEvidence, error) {
		t.Fatal("measurement started without nine-service readiness")
		return RawEvidence{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "service probe missing") || len(evidence.Subscriptions) != 1 {
		t.Fatalf("missing ninth-service proof accepted or discarded: %v %#v", err, evidence)
	}
}

func TestFullStackMatrixEventScopeKeepsOnlyOwnedProjectAndExactWorker(t *testing.T) {
	worker := fullStackWorkerCapture{Container: CapturedContainer{ID: "worker-exact"}, Session: "session", Runtime: "runtime", Generation: 3}
	project := nativeCollectorProject
	cases := []struct {
		name                 string
		event                matrixDockerEvent
		wantOwned, wantError bool
		wantService          string
	}{
		{"owned compose", matrixDockerEvent{ID: "pg", Project: project, Service: "postgres", Action: "exec_create", Nano: 1}, true, false, "postgres"},
		{"exact worker", matrixDockerEvent{ID: "worker-exact", WorkerSessionID: "session", RuntimeID: "runtime", Generation: "3", Action: "pause", Nano: 2}, true, false, "resident-worker"},
		{"unrelated", matrixDockerEvent{ID: "other", Project: "other-project", Service: "postgres", Action: "pause", Nano: 3}, false, false, ""},
		{"worker wrong generation", matrixDockerEvent{ID: "worker-exact", WorkerSessionID: "session", RuntimeID: "runtime", Generation: "4", Action: "pause", Nano: 4}, false, true, ""},
		{"worker wrong session", matrixDockerEvent{ID: "worker-exact", WorkerSessionID: "other", RuntimeID: "runtime", Generation: "3", Action: "pause", Nano: 5}, false, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, owned, err := matrixOwnedEvent(project, worker, tc.event)
			if owned != tc.wantOwned || (err != nil) != tc.wantError || e.Service != tc.wantService {
				t.Fatalf("scope result = %#v %v %v", e, owned, err)
			}
		})
	}
}

func nativeProbes(project, state string) []ServiceProbe {
	at := time.Now().UTC()
	var probes []ServiceProbe
	for _, service := range observationServices {
		probes = append(probes, ServiceProbe{Project: project, Service: service, ContainerID: service, StartedAt: time.Unix(1, 0).UTC(), From: at, To: at, State: state, Kind: "independent_health_probe"})
	}
	return probes
}
func nativeTiming() RawEvidence {
	defer time.Sleep(time.Nanosecond) // Advance the controlled test clock past its second receipt.
	at := time.Now().UTC()
	r := RawEvidence{Project: "switchback-obsv-a1234567", MeasurementID: "native", Runtimes: json.RawMessage(`[{"runtime_instance_id":"runtime","worker_session_id":"session","generation":1}]`)}
	input := map[string]any{"measured_settlements": []map[string]any{{"agent_turn_id": "a", "terminal_message_id": "ta", "terminal_receipt_at": at, "completed_at": at, "terminal_receipt_to_settlement_ms": 0}, {"agent_turn_id": "b", "terminal_message_id": "tb", "terminal_receipt_at": at.Add(time.Nanosecond), "completed_at": at.Add(time.Nanosecond), "terminal_receipt_to_settlement_ms": 0}}, "measured_next_dispatch": map[string]any{"agent_turn_id": "b", "command_message_id": "cb", "terminal_receipt_at": at, "acknowledged_at": at, "terminal_receipt_to_next_dispatch_ms": 0}}
	data, _ := json.Marshal(input)
	_ = json.Unmarshal(data, &r)
	return r
}
func controlledCollectorIO(probe func(context.Context, string) ([]ServiceProbe, error)) intervalCollectorIO {
	var h SubscriptionHandshake
	return intervalCollectorIO{probe: probe, handshake: func(context.Context) (SubscriptionHandshake, error) { return h, nil }, subscribe: func(_ time.Time, record func(ServiceEvent)) (<-chan subscriptionExit, func(), error) {
		h = recordNativeHandshake(record)
		done := make(chan subscriptionExit, 1)
		var once sync.Once
		return done, func() { once.Do(func() { done <- subscriptionExit{At: time.Now().UTC(), Interrupted: true} }) }, nil
	}}
}

func recordNativeHandshake(record func(ServiceEvent)) SubscriptionHandshake {
	h := SubscriptionHandshake{ContainerID: "postgres", From: time.Now().UTC(), To: time.Now().UTC()}
	for _, action := range []string{"exec_create", "exec_die"} {
		record(ServiceEvent{Project: "switchback-obsv-a1234567", Service: "postgres", ContainerID: "postgres", ExecID: "handshake", At: h.From, Action: action})
	}
	return h
}

func TestMeasurementCannotRestoreStateBeforeRecordedClosingProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		post := make(chan struct{})
		done := make(chan error, 1)
		var calls atomic.Int32
		var restored atomic.Bool
		io := controlledCollectorIO(func(ctx context.Context, project string) ([]ServiceProbe, error) {
			if calls.Add(1) > 1 {
				select {
				case <-post:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nativeProbes(project, "healthy"), nil
		})
		go func() {
			_, _, err := measureWithIntervals(ctx, "switchback-obsv-a1234567", "healthy", io, func() (RawEvidence, error) { return nativeTiming(), nil })
			restored.Store(true)
			done <- err
		}()
		synctest.Wait()
		if restored.Load() {
			t.Fatal("state restored before closing probe was recorded")
		}
		close(post)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !restored.Load() {
			t.Fatal("completed coverage did not release caller")
		}
	})
}

func TestCollectorRejectsEOFCancellationAndClosingProbeFailure(t *testing.T) {
	for _, failure := range []string{"eof-before-ready", "eof-before-close", "cancel", "probe-error", "wrong-state", "wrong-container", "wrong-process"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ended := make(chan subscriptionExit, 1)
				var once sync.Once
				var calls int
				var h SubscriptionHandshake
				io := intervalCollectorIO{handshake: func(context.Context) (SubscriptionHandshake, error) { return h, nil }, subscribe: func(_ time.Time, record func(ServiceEvent)) (<-chan subscriptionExit, func(), error) {
					h = recordNativeHandshake(record)
					if failure == "eof-before-ready" {
						once.Do(func() { ended <- subscriptionExit{At: time.Now().UTC()} })
					}
					return ended, func() { once.Do(func() { ended <- subscriptionExit{At: time.Now().UTC(), Interrupted: true} }) }, nil
				}, probe: func(_ context.Context, project string) ([]ServiceProbe, error) {
					calls++
					probes := nativeProbes(project, "healthy")
					if calls > 1 {
						switch failure {
						case "probe-error":
							return nil, errors.New("independent probe unavailable")
						case "wrong-state":
							probes[0].State = "unknown"
						case "wrong-container":
							probes[0].ContainerID = "replacement"
						case "wrong-process":
							probes[0].StartedAt = probes[0].StartedAt.Add(time.Second)
						}
					}
					return probes, nil
				}}
				measured := false
				_, e, err := measureWithIntervals(ctx, "switchback-obsv-a1234567", "healthy", io, func() (RawEvidence, error) {
					measured = true
					raw := nativeTiming()
					if failure == "eof-before-close" {
						once.Do(func() { ended <- subscriptionExit{At: time.Now().UTC()} })
					}
					if failure == "cancel" {
						cancel()
					}
					return raw, nil
				})
				if err == nil {
					t.Fatal("invalid collector lifecycle accepted")
				}
				if failure == "eof-before-ready" && measured {
					t.Fatal("measurement ran after pre-ready EOF")
				}
				if len(e.Subscriptions) != 1 {
					t.Fatal("failed collection evidence discarded")
				}
				if failure == "cancel" && e.Subscriptions[0].End == "requested_stop" {
					t.Fatal("cancellation relabeled normal completion")
				}
			})
		})
	}
}

func TestSubscriptionReadyRequiresCurrentRecordedExactExec(t *testing.T) {
	for _, defect := range []string{"missing", "wrong-container", "wrong-project", "wrong-exec-id", "stale", "early-eof", "cancel"} {
		t.Run(defect, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ended := make(chan subscriptionExit, 1)
				var once sync.Once
				var h SubscriptionHandshake
				io := intervalCollectorIO{probe: func(context.Context, string) ([]ServiceProbe, error) {
					t.Error("probes started without subscription handshake")
					return nil, errors.New("unreachable")
				}, handshake: func(context.Context) (SubscriptionHandshake, error) { return h, nil }, subscribe: func(from time.Time, record func(ServiceEvent)) (<-chan subscriptionExit, func(), error) {
					h = SubscriptionHandshake{ContainerID: "postgres", From: from, To: from}
					for _, action := range []string{"exec_create", "exec_die"} {
						e := ServiceEvent{Project: "switchback-obsv-a1234567", Service: "postgres", ContainerID: "postgres", ExecID: "exec", At: from, Action: action}
						switch defect {
						case "missing", "cancel":
							continue
						case "wrong-container":
							e.ContainerID = "foreign"
						case "wrong-project":
							e.Project = "foreign"
						case "wrong-exec-id":
							if action == "exec_die" {
								e.ExecID = "other"
							}
						case "stale":
							e.At = from.Add(-time.Second)
						case "early-eof":
							continue
						}
						record(e)
					}
					if defect == "early-eof" {
						once.Do(func() { ended <- subscriptionExit{At: from} })
					}
					return ended, func() { once.Do(func() { ended <- subscriptionExit{At: time.Now().UTC(), Interrupted: true} }) }, nil
				}}
				c := startIntervalCollection(ctx, "switchback-obsv-a1234567", "healthy", io)
				synctest.Wait()
				select {
				case err := <-c.ready:
					if err == nil {
						t.Fatal("unproven subscription reported ready")
					}
				default:
				}
				cancel()
				<-c.done
				if c.err == nil {
					t.Fatal("missing handshake accepted")
				}
			})
		})
	}
}

func TestEventEOFCannotBecomeRequestedStop(t *testing.T) {
	at := time.Now().UTC()
	if got := classifySubscriptionExit(at.Add(time.Second), subscriptionExit{At: at, Err: errors.New("events disconnected")}); got == "requested_stop" {
		t.Fatal("earlier event EOF became requested_stop when cancellation raced with completion")
	}
}

func TestIntervalsCannotBridgeRestartEventsOrCollectorEOF(t *testing.T) {
	r := evidenceReport(t)
	p := r.Physical
	measurement := r.Pairs[0].Healthy.Measurement
	s := &p.Intervals.Subscriptions[1]
	s.Events = append(s.Events, ServiceEvent{Project: p.Project, Service: "langfuse-web", ContainerID: "langfuse-web", At: measurement.From.Add(500 * time.Millisecond), Action: "die"})
	var err error
	p.Intervals.Intervals, err = p.Intervals.derive(p.Project)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Intervals.validateWindow(p.Project, "healthy", measurement.From, measurement.To); err == nil {
		t.Fatal("bridged container death between probes")
	}
	s.Events = s.Events[:2]
	s.End = "unexpected_eof"
	p.Intervals.Intervals, err = p.Intervals.derive(p.Project)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Intervals.validateWindow(p.Project, "healthy", measurement.From, measurement.To); err == nil {
		t.Fatal("bridged interrupted subscription using healthy probes")
	}
}

func TestIntervalsRequireProbeBoundAndCurrentStartIdentity(t *testing.T) {
	for _, mode := range []string{"gap", "restart-same-container"} {
		t.Run(mode, func(t *testing.T) {
			r := evidenceReport(t)
			p := r.Physical
			measurement := r.Pairs[0].Healthy.Measurement
			s := &p.Intervals.Subscriptions[1]
			if mode == "gap" {
				s.Probes[1].From = s.Probes[0].From.Add(6 * time.Second)
				s.Probes[1].To = s.Probes[1].From
				s.To = s.Probes[1].To.Add(time.Second)
			} else {
				s.Probes[1].StartedAt = s.Probes[0].StartedAt.Add(time.Second)
			}
			var err error
			p.Intervals.Intervals, err = p.Intervals.derive(p.Project)
			if err != nil {
				t.Fatal(err)
			}
			if err = p.Intervals.validateWindow(p.Project, "healthy", measurement.From, measurement.To); err == nil {
				t.Fatal("unsupported interval coverage")
			}
		})
	}
}
