package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fullStackMatrixFixture(t *testing.T) (string, string, fullStackMatrixRecipe) {
	t.Helper()
	project, output := fullStackColdFixture(t)
	data, err := os.ReadFile(filepath.Join(output, "smoke-passed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var smoke RawEvidence
	if err = json.Unmarshal(data, &smoke); err != nil {
		t.Fatal(err)
	}
	base := smoke.CapturedAt.Add(time.Minute)
	health := "healthy"
	capture := ProjectCapture{Schema: "observability.project_capture.v1", Project: project, StartedAt: base, CompletedAt: base.Add(time.Second), BackendHealth: &HTTPHealthProbe{URL: "http://127.0.0.1:1/healthz", StartedAt: base, CompletedAt: base.Add(time.Second), Status: 200}}
	for _, service := range append([]string{"switchback-backend", "postgres", "observation-fake-model"}, fullStackObservationServices...) {
		c := CapturedContainer{Project: project, Service: service, ID: service + "-container", ImageID: "sha256:" + strings.Repeat("a", 64), Running: true, StartedAt: base.Add(-time.Minute)}
		if service == "postgres" {
			c.Health = &health
		}
		if service == "observation-fake-model" {
			c.ID = smoke.Expected[0].FixtureContainerID
		}
		capture.Containers = append(capture.Containers, c)
	}
	startedAt := base.Add(2 * time.Second)
	started := struct {
		Project, Session, Runtime, Fixture string
		Generation                         uint64
		At                                 time.Time
	}{project, "", "", smoke.Expected[0].FixtureContainerID, 0, startedAt}
	m, err := measurementFromRaw(smoke)
	if err != nil {
		t.Fatal(err)
	}
	started.Session, started.Runtime, started.Generation = m.SessionID, m.RuntimeID, m.Generation
	write := func(name string, value any) {
		t.Helper()
		if err := writeEvidence(output, name, value); err != nil {
			t.Fatal(err)
		}
	}
	write("full-stack-matrix-started.json", started)
	write("full-stack-matrix-initial.json", capture)
	worker := fullStackWorkerCapture{Container: CapturedContainer{ID: "worker-container", Running: true, ImageID: "sha256:" + strings.Repeat("c", 64), StartedAt: base.Add(-time.Minute)}, Labels: map[string]string{"com.switchback.worker-session-id": m.SessionID, "com.switchback.runtime-instance-id": m.RuntimeID, "com.switchback.runtime-generation": fmt.Sprint(m.Generation)}, Session: m.SessionID, Runtime: m.RuntimeID, Generation: m.Generation, ObservedAt: base}
	write("full-stack-matrix-worker-initial.json", worker)
	recipe := fullStackMatrixRecipe{Schema: "observability.full_stack_matrix_recipe.v1", Project: project, Smoke: "smoke-passed.json", Started: "full-stack-matrix-started.json", Initial: "full-stack-matrix-initial.json", Final: "full-stack-matrix-final.json", WorkerInitial: "full-stack-matrix-worker-initial.json", WorkerFinal: "full-stack-matrix-worker-final.json"}
	for i := 0; i < 20; i++ {
		pair := fullStackMatrixPair{}
		for j, state := range []string{"healthy", "unavailable"} {
			ordinal := 2*i + j
			nonce := fmt.Sprintf("%sf%02d%s", strings.TrimPrefix(project, "switchback-obsv-"), i, map[string]string{"healthy": "h", "unavailable": "o"}[state])
			at := startedAt.Add(time.Duration(ordinal+1) * 10 * time.Second)
			var raw RawEvidence
			if err = json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			raw.MeasurementID = nonce
			turns := []string{nonce + "-turn-a", nonce + "-turn-b"}
			for k := 0; k < 2; k++ {
				raw.Settlements[k].TurnID = turns[k]
				raw.Settlements[k].MessageID = nonce + fmt.Sprint("-event-", k)
				raw.Settlements[k].From = at.Add(time.Duration(2+2*k) * time.Second)
				raw.Settlements[k].To = raw.Settlements[k].From.Add(time.Duration(10+10*k) * time.Millisecond)
				*raw.Settlements[k].Milliseconds = float64(10 + 10*k)
				raw.Expected[k].TurnID = turns[k]
				raw.Expected[k].ObservationID = nonce + fmt.Sprint("-model-", k)
			}
			raw.Dispatch.TurnID = turns[1]
			raw.Dispatch.MessageID = nonce + "-command"
			raw.Dispatch.From = raw.Settlements[0].From
			raw.Dispatch.To = raw.Dispatch.From.Add(1030 * time.Millisecond)
			*raw.Dispatch.Milliseconds = 1030
			for k := range raw.FragmentProofs {
				p := &raw.FragmentProofs[k]
				p.SourceID = nonce + "-" + p.SourceID
				p.RunID = nonce + "-" + p.RunID
				if p.TurnID == m.TurnIDs[0] {
					p.TurnID = turns[0]
				} else if p.TurnID == m.TurnIDs[1] {
					p.TurnID = turns[1]
				}
				for x, e := range m.Expected {
					if p.ObservationID == e.ObservationID {
						p.ObservationID = raw.Expected[x].ObservationID
					}
				}
			}
			rows := []fullStackTurn{{ID: turns[0], SessionID: m.SessionID, Status: "completed", CreatedAt: at.Add(time.Second), TerminalAt: at.Add(2 * time.Second)}, {ID: turns[1], SessionID: m.SessionID, Status: "completed", CreatedAt: at.Add(3 * time.Second), TerminalAt: at.Add(4 * time.Second)}}
			raw.Turns, _ = json.Marshal(rows)
			raw.CapturedAt = at.Add(5 * time.Second)
			originalName := "sample-" + nonce + "-original.json"
			recoveredName := "sample-" + nonce + "-recovered.json"
			write(originalName, raw)
			raw.CapturedAt = at.Add(6 * time.Second)
			write(recoveredName, raw)
			sub := EventSubscription{ID: nonce + "-subscription", Project: project, Scope: "project_and_exact_worker", WorkerContainerID: worker.Container.ID, From: at, To: at.Add(7 * time.Second), End: "requested_stop", Handshake: &SubscriptionHandshake{ContainerID: "postgres-container", ExecID: nonce + "-handshake", From: at, To: at}}
			for _, action := range []string{"exec_create", "exec_die"} {
				sub.Events = append(sub.Events, ServiceEvent{Project: project, Service: "postgres", ContainerID: "postgres-container", ExecID: sub.Handshake.ExecID, At: at, Action: action})
			}
			for _, service := range fullStackObservationServices {
				for _, probeAt := range []time.Time{at.Add(time.Second), at.Add(6 * time.Second)} {
					kind := "independent_health_probe"
					if state == "unavailable" {
						kind = "exact_inspect"
					}
					sub.Probes = append(sub.Probes, ServiceProbe{Project: project, Service: service, ContainerID: service + "-container", StartedAt: base.Add(-time.Minute), From: probeAt, To: probeAt, State: state, Kind: kind})
				}
			}
			intervals := IntervalEvidence{Subscriptions: []EventSubscription{sub}}
			intervals.Intervals, err = intervals.derive(project)
			if err != nil {
				t.Fatal(err)
			}
			intervalName := "intervals-" + sub.ID + ".json"
			write(intervalName, intervals)
			window := fullStackMatrixWindow{Original: originalName, Recovered: recoveredName, Intervals: intervalName}
			if j == 0 {
				pair.Healthy = window
			} else {
				pair.Outage = window
			}
		}
		recipe.Pairs = append(recipe.Pairs, pair)
	}
	capture.StartedAt = startedAt.Add(41 * 10 * time.Second)
	capture.CompletedAt = capture.StartedAt.Add(time.Second)
	capture.BackendHealth.StartedAt = capture.StartedAt
	capture.BackendHealth.CompletedAt = capture.CompletedAt
	write(recipe.Final, capture)
	worker.ObservedAt = capture.CompletedAt
	write(recipe.WorkerFinal, worker)
	write("full-stack-matrix-recipe.json", recipe)
	return project, output, recipe
}

func TestFullStackMatrixVerifierAcceptsCompletePairedEvidence(t *testing.T) {
	project, output, _ := fullStackMatrixFixture(t)
	if err := verifyFullStackMatrix(project, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "full-stack-matrix-passed.json"))
	if err != nil || !strings.Contains(string(data), `"paired_outage_only"`) {
		t.Fatalf("scoped pass missing: %v", err)
	}
}

func TestFullStackMatrixRejectsDispatchBeforeSecondTurnCreation(t *testing.T) {
	project, output, recipe := fullStackMatrixFixture(t)
	for _, name := range []string{recipe.Pairs[0].Healthy.Original, recipe.Pairs[0].Healthy.Recovered} {
		rewriteFullStackFile(t, output, name, func(e *RawEvidence) {
			e.Dispatch.To = e.Dispatch.From.Add(30 * time.Millisecond)
			*e.Dispatch.Milliseconds = 30
		})
	}
	if err := verifyFullStackMatrix(project, output); err == nil || !strings.Contains(err.Error(), "ACK precedes second Turn creation") {
		t.Fatalf("next dispatch ACK preceding second Turn creation was not specifically rejected: %v", err)
	}
}

func TestFullStackMatrixRejectsUnrelatedWorkerLifecycleEvent(t *testing.T) {
	project, output, recipe := fullStackMatrixFixture(t)
	rewriteFullStackFile(t, output, recipe.Pairs[0].Healthy.Intervals, func(e *IntervalEvidence) {
		s := &e.Subscriptions[0]
		s.Events = append(s.Events, ServiceEvent{Project: project, Service: "resident-worker", ContainerID: "unrelated-worker", At: s.From.Add(2 * time.Second), Action: "exec_create"})
		e.Intervals, _ = e.derive(project)
	})
	if err := verifyFullStackMatrix(project, output); err == nil || !strings.Contains(err.Error(), "Worker event identity invalid") {
		t.Fatalf("unrelated Worker event was not specifically rejected: %v", err)
	}
}

func TestFullStackMatrixRejectsMissingWorkerSubscriptionCoverage(t *testing.T) {
	project, output, recipe := fullStackMatrixFixture(t)
	rewriteFullStackFile(t, output, recipe.Pairs[0].Healthy.Intervals, func(e *IntervalEvidence) { e.Subscriptions[0].WorkerContainerID = "" })
	if err := verifyFullStackMatrix(project, output); err == nil || !strings.Contains(err.Error(), "lacks exact resident Worker coverage") {
		t.Fatalf("project-only stream was not specifically rejected: %v", err)
	}
}

func TestFullStackMatrixVerifierRejectsBrokenEvidence(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(*testing.T, string, fullStackMatrixRecipe)
	}{
		{"missing ninth service", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Intervals, func(e *IntervalEvidence) {
				var probes []ServiceProbe
				for _, p := range e.Subscriptions[0].Probes {
					if p.Service != "grafana" {
						probes = append(probes, p)
					}
				}
				e.Subscriptions[0].Probes = probes
				e.Intervals, _ = e.derive(r.Project)
			})
		}},
		{"two incomplete subscriptions", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Intervals, func(e *IntervalEvidence) {
				first := e.Subscriptions[0]
				second := first
				second.ID = first.ID + "-second"
				first.To = first.From.Add(3 * time.Second)
				second.From = first.To
				first.Probes = first.Probes[:len(first.Probes)/2]
				second.Probes = second.Probes[len(second.Probes)/2:]
				e.Subscriptions = []EventSubscription{first, second}
				e.Intervals, _ = e.derive(r.Project)
			})
		}},
		{"resident Worker pause", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			data, err := os.ReadFile(filepath.Join(o, r.WorkerInitial))
			if err != nil {
				t.Fatal(err)
			}
			var worker fullStackWorkerCapture
			if json.Unmarshal(data, &worker) != nil {
				t.Fatal("worker fixture")
			}
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Intervals, func(e *IntervalEvidence) {
				s := &e.Subscriptions[0]
				s.Events = append(s.Events, ServiceEvent{Project: r.Project, Service: "resident-worker", ContainerID: worker.Container.ID, At: s.From.Add(2 * time.Second), Action: "pause", WorkerSessionID: worker.Session, RuntimeID: worker.Runtime, Generation: worker.Generation})
				e.Intervals, _ = e.derive(r.Project)
			})
		}},
		{"cross claim", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Original, func(e *RawEvidence) { e.Project = "switchback-obsv-other123" })
		}},
		{"duplicate Turn", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			var first RawEvidence
			data, err := os.ReadFile(filepath.Join(o, r.Pairs[0].Healthy.Original))
			if err != nil || json.Unmarshal(data, &first) != nil {
				t.Fatal(err)
			}
			for _, name := range []string{r.Pairs[1].Healthy.Original, r.Pairs[1].Healthy.Recovered} {
				rewriteFullStackFile(t, o, name, func(e *RawEvidence) {
					old := []string{e.Settlements[0].TurnID, e.Settlements[1].TurnID}
					for i := 0; i < 2; i++ {
						e.Settlements[i].TurnID = first.Settlements[i].TurnID
						e.Expected[i].TurnID = first.Settlements[i].TurnID
					}
					e.Dispatch.TurnID = first.Settlements[1].TurnID
					for i := range e.FragmentProofs {
						for j := 0; j < 2; j++ {
							if e.FragmentProofs[i].TurnID == old[j] {
								e.FragmentProofs[i].TurnID = first.Settlements[j].TurnID
							}
						}
					}
					var rows []fullStackTurn
					_ = json.Unmarshal(e.Turns, &rows)
					for i := range rows {
						rows[i].ID = first.Settlements[i].TurnID
					}
					e.Turns, _ = json.Marshal(rows)
				})
			}
		}},
		{"source changed", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Recovered, func(e *RawEvidence) { e.FragmentProofs[0].Digest = "sha256:" + strings.Repeat("b", 64) })
		}},
		{"core replacement", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Final, func(e *ProjectCapture) {
				for i := range e.Containers {
					if e.Containers[i].Service == "postgres" {
						e.Containers[i].StartedAt = e.Containers[i].StartedAt.Add(time.Second)
					}
				}
			})
		}},
		{"latency above limit", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			rewriteFullStackFile(t, o, r.Pairs[0].Healthy.Original, func(e *RawEvidence) {
				*e.Settlements[0].Milliseconds = 5100
				e.Settlements[0].To = e.Settlements[0].From.Add(5100 * time.Millisecond)
			})
		}},
		{"outage p95 increase", func(t *testing.T, o string, r fullStackMatrixRecipe) {
			for _, pair := range r.Pairs {
				for _, name := range []string{pair.Outage.Original, pair.Outage.Recovered} {
					rewriteFullStackFile(t, o, name, func(e *RawEvidence) {
						*e.Settlements[0].Milliseconds = 1200
						e.Settlements[0].To = e.Settlements[0].From.Add(1200 * time.Millisecond)
					})
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, output, recipe := fullStackMatrixFixture(t)
			tc.breakIt(t, output, recipe)
			if err := verifyFullStackMatrix(project, output); err == nil {
				t.Fatal("broken evidence accepted")
			}
			if _, err := os.Stat(filepath.Join(output, "full-stack-matrix-passed.json")); !os.IsNotExist(err) {
				t.Fatal("failed evidence wrote PASS")
			}
		})
	}
}
