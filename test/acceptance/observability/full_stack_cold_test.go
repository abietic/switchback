package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fullStackColdFixture(t *testing.T) (string, string) {
	t.Helper()
	r := evidenceReport(t)
	p := r.Physical
	project := p.Project
	output := writeNativeCollectorClaim(t)
	base := p.ColdBefore.CompletedAt
	for _, service := range []string{"prometheus", "alloy", "loki", "grafana"} {
		p.ColdBefore.AbsentServices = append(p.ColdBefore.AbsentServices, service)
		p.ColdAfter.AbsentServices = append(p.ColdAfter.AbsentServices, service)
		for i := range p.Intervals.Subscriptions {
			if p.Intervals.Subscriptions[i].ID != "cold" {
				continue
			}
			s := &p.Intervals.Subscriptions[i]
			for _, at := range []time.Time{p.ColdBefore.StartedAt, p.ColdAfter.CompletedAt.Add(time.Millisecond)} {
				s.Probes = append(s.Probes, ServiceProbe{Project: project, Service: service, From: at, To: at, State: "unavailable", Kind: "exact_inspect"})
			}
		}
	}
	for i := range p.Intervals.Subscriptions {
		s := &p.Intervals.Subscriptions[i]
		if s.ID != "cold" {
			continue
		}
		s.To = p.ColdAfter.CompletedAt.Add(2 * time.Millisecond)
		for j := range s.Probes {
			if s.Probes[j].To.Before(base) || (s.Probes[j].To.After(p.Cold.To) && s.Probes[j].To.Before(p.ColdAfter.CompletedAt)) {
				if s.Probes[j].To.Before(base) {
					s.Probes[j].From = p.ColdBefore.StartedAt
					s.Probes[j].To = s.Probes[j].From
					continue
				}
				s.Probes[j].From = p.ColdAfter.CompletedAt.Add(time.Millisecond)
				s.Probes[j].To = s.Probes[j].From
			}
		}
	}
	p.Intervals.Intervals, _ = p.Intervals.derive(project)
	p.ColdAfter.Containers = append(p.ColdAfter.Containers, CapturedContainer{Project: project, Service: "observation-fake-model", ID: "fixture-container", ImageID: "sha256:" + strings.Repeat("a", 64), Running: true, StartedAt: base.Add(-time.Second)})
	p.ColdAfter.Containers[0].ImageID = "sha256:" + strings.Repeat("b", 64)
	write := func(name string, value any) {
		t.Helper()
		if err := writeEvidence(output, name, value); err != nil {
			t.Fatal(err)
		}
	}
	write("cold-before.json", p.ColdBefore)
	write("cold-after.json", p.ColdAfter)
	coldIntervals := IntervalEvidence{Subscriptions: []EventSubscription{p.Intervals.Subscriptions[0]}}
	coldIntervals.Intervals, _ = coldIntervals.derive(project)
	write("intervals-cold.json", coldIntervals)
	raw := map[string]any{
		"project": project, "measurement_id": p.Cold.ID, "controlled_acp_fixture": true,
		"fixture_checksum": "fixture", "v2_bytes_digest": p.Cold.LegacyBefore,
		"expected_models": p.Cold.Expected, "fragment_proofs": p.Cold.Sources,
		"runtimes": []map[string]any{{"runtime_instance_id": p.Cold.RuntimeID, "worker_session_id": p.Cold.SessionID, "generation": p.Cold.Generation}},
		"measured_settlements": []map[string]any{
			{"agent_turn_id": p.Cold.TurnIDs[0], "terminal_message_id": p.Cold.CommandIDs[0], "terminal_receipt_at": p.Cold.Settlements[0].From, "completed_at": p.Cold.Settlements[0].To, "terminal_receipt_to_settlement_ms": 10},
			{"agent_turn_id": p.Cold.TurnIDs[1], "terminal_message_id": p.Cold.CommandIDs[1], "terminal_receipt_at": p.Cold.Settlements[1].From, "completed_at": p.Cold.Settlements[1].To, "terminal_receipt_to_settlement_ms": 20},
		},
		"measured_next_dispatch": map[string]any{"agent_turn_id": p.Cold.TurnIDs[1], "command_message_id": p.Cold.Dispatch.MessageID, "terminal_receipt_at": p.Cold.Dispatch.From, "acknowledged_at": p.Cold.Dispatch.To, "terminal_receipt_to_next_dispatch_ms": 30},
		"turns":                  []map[string]any{{"agent_turn_id": p.Cold.TurnIDs[0], "worker_session_id": p.Cold.SessionID, "status": "completed", "created_at": base.Add(250 * time.Millisecond), "terminal_at": base.Add(time.Second)}, {"agent_turn_id": p.Cold.TurnIDs[1], "worker_session_id": p.Cold.SessionID, "status": "completed", "created_at": base.Add(900 * time.Millisecond), "terminal_at": base.Add(1200 * time.Millisecond)}},
		"captured_at":            base.Add(1400 * time.Millisecond),
	}
	write("smoke-pending.json", raw)
	raw["captured_at"] = base.Add(time.Minute)
	write("smoke-passed.json", raw)
	return project, output
}

func TestFullStackColdVerifierAcceptsOnlySeparateProof(t *testing.T) {
	project, output := fullStackColdFixture(t)
	if err := verifyFullStackCold(project, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "full-stack-cold-passed.json"))
	if err != nil || !strings.Contains(string(data), "cold_start_only") {
		t.Fatalf("separate scoped pass missing: %v", err)
	}
}

func TestFullStackColdRejectsLegacyFiveServiceCapture(t *testing.T) {
	project, output := fullStackColdFixture(t)
	path := filepath.Join(output, "cold-before.json")
	data, _ := os.ReadFile(path)
	var c ProjectCapture
	_ = json.Unmarshal(data, &c)
	c.AbsentServices = c.AbsentServices[:len(c.AbsentServices)-4]
	data, _ = json.Marshal(c)
	_ = os.WriteFile(path, data, 0600)
	if err := verifyFullStackCold(project, output); err == nil {
		t.Fatal("legacy five-service capture accepted as full-stack")
	}
	if _, err := os.Stat(filepath.Join(output, "full-stack-cold-passed.json")); !os.IsNotExist(err) {
		t.Fatal("failed proof wrote pass file")
	}
}

func rewriteFullStackFile[T any](t *testing.T, output, name string, change func(*T)) {
	t.Helper()
	path := filepath.Join(output, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	change(&value)
	data, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestFullStackColdRejectsBrokenClaimEvidence(t *testing.T) {
	cases := []struct {
		name          string
		breakEvidence func(*testing.T, string)
	}{
		{"missing extra before", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-before.json", func(c *ProjectCapture) { c.AbsentServices = c.AbsentServices[:len(c.AbsentServices)-1] })
		}},
		{"missing extra after", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-after.json", func(c *ProjectCapture) { c.AbsentServices = c.AbsentServices[:len(c.AbsentServices)-1] })
		}},
		{"running extra service", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-after.json", func(c *ProjectCapture) {
				c.AbsentServices = c.AbsentServices[:len(c.AbsentServices)-1]
				c.Containers = append(c.Containers, CapturedContainer{Project: c.Project, Service: "grafana", ID: "grafana", Running: true})
			})
		}},
		{"duplicate absence", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-after.json", func(c *ProjectCapture) { c.AbsentServices = append(c.AbsentServices, "grafana") })
		}},
		{"contradictory presence", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-after.json", func(c *ProjectCapture) {
				c.Containers = append(c.Containers, CapturedContainer{Project: c.Project, Service: "grafana", ID: "grafana"})
			})
		}},
		{"duplicate container identity", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-after.json", func(c *ProjectCapture) { c.Containers[1].ID = c.Containers[0].ID })
		}},
		{"wrong project", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "cold-after.json", func(c *ProjectCapture) { c.Containers[0].Project = "switchback-obsv-other123" })
		}},
		{"unexpected subscription EOF", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "intervals-cold.json", func(e *IntervalEvidence) { e.Subscriptions[0].End = "unexpected_eof" })
		}},
		{"missing ninth probe", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "intervals-cold.json", func(e *IntervalEvidence) {
				var p []ServiceProbe
				for _, v := range e.Subscriptions[0].Probes {
					if v.Service != "grafana" {
						p = append(p, v)
					}
				}
				e.Subscriptions[0].Probes = p
			})
		}},
		{"source changed after recovery", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "smoke-passed.json", func(e *RawEvidence) { e.FragmentProofs[0].Digest = "sha256:" + strings.Repeat("b", 64) })
		}},
		{"runtime identity changed", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "smoke-passed.json", func(e *RawEvidence) {
				e.Runtimes = json.RawMessage(`[{"runtime_instance_id":"replacement","worker_session_id":"session","generation":1}]`)
			})
		}},
		{"original Turn outside window", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "smoke-pending.json", func(e *RawEvidence) {
				var turns []map[string]any
				_ = json.Unmarshal(e.Turns, &turns)
				turns[0]["created_at"] = time.Time{}
				e.Turns, _ = json.Marshal(turns)
			})
		}},
		{"late original capture", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "smoke-pending.json", func(e *RawEvidence) { e.CapturedAt = e.CapturedAt.Add(time.Minute) })
		}},
		{"latency over five seconds", func(t *testing.T, o string) {
			rewriteFullStackFile(t, o, "smoke-pending.json", func(e *RawEvidence) {
				e.Settlements[0].Milliseconds = new(float64)
				*e.Settlements[0].Milliseconds = 5100
				e.Settlements[0].To = e.Settlements[0].From.Add(5100 * time.Millisecond)
			})
		}},
		{"symlinked input", func(t *testing.T, o string) {
			path := filepath.Join(o, "cold-before.json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(o, "cold-after.json"), path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, output := fullStackColdFixture(t)
			tc.breakEvidence(t, output)
			if err := verifyFullStackCold(project, output); err == nil {
				t.Fatal("broken full-stack evidence accepted")
			}
			if _, err := os.Stat(filepath.Join(output, "full-stack-cold-passed.json")); !os.IsNotExist(err) {
				t.Fatal("failure wrote full-stack pass file")
			}
		})
	}
}

func TestFullStackColdCollectorRequiresNinthActualProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	io := controlledCollectorIO(func(context.Context, string) ([]ServiceProbe, error) {
		probes := nativeProbes(nativeCollectorProject, "unavailable")
		for i := range probes {
			probes[i].Kind = "exact_inspect"
		}
		return probes, nil
	})
	c := startIntervalCollectionForServices(ctx, nativeCollectorProject, "unavailable", fullStackObservationServices, io)
	if err := c.waitReady(ctx); err == nil || !strings.Contains(err.Error(), "service probe missing") {
		t.Fatalf("five-service collector accepted as nine-service proof: %v", err)
	}
	<-c.done
}

func TestFullStackColdRejectsCoverageSplicedAcrossSubscriptions(t *testing.T) {
	project, output := fullStackColdFixture(t)
	var before, after ProjectCapture
	for _, item := range []struct {
		name string
		dst  *ProjectCapture
	}{{"cold-before.json", &before}, {"cold-after.json", &after}} {
		data, err := os.ReadFile(filepath.Join(output, item.name))
		if err != nil || json.Unmarshal(data, item.dst) != nil {
			t.Fatalf("fixture capture %s: %v", item.name, err)
		}
	}
	if err := os.Remove(filepath.Join(output, "intervals-cold.json")); err != nil {
		t.Fatal(err)
	}
	middle := before.CompletedAt.Add(500 * time.Millisecond)
	for i, endpoints := range [][2]time.Time{{before.StartedAt, middle}, {middle, after.CompletedAt}} {
		from, to := endpoints[0], endpoints[1]
		id := "cold-part-" + string(rune('a'+i))
		handshake := &SubscriptionHandshake{ContainerID: "postgres", ExecID: id + "-handshake", From: from, To: from}
		stream := EventSubscription{ID: id, Project: project, From: from, To: to.Add(time.Millisecond), End: "requested_stop", Handshake: handshake}
		for _, action := range []string{"exec_create", "exec_die"} {
			stream.Events = append(stream.Events, ServiceEvent{Project: project, Service: "postgres", ContainerID: "postgres", ExecID: handshake.ExecID, At: from, Action: action})
		}
		for _, service := range fullStackObservationServices {
			for _, at := range []time.Time{from, to} {
				stream.Probes = append(stream.Probes, ServiceProbe{Project: project, Service: service, From: at, To: at, State: "unavailable", Kind: "exact_inspect"})
			}
		}
		part := IntervalEvidence{Subscriptions: []EventSubscription{stream}}
		var err error
		part.Intervals, err = part.derive(project)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeEvidence(output, "intervals-"+id+".json", part); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyFullStackCold(project, output); err == nil {
		t.Fatal("separate event subscriptions were spliced into a full cold proof")
	}
	if _, err := os.Stat(filepath.Join(output, "full-stack-cold-passed.json")); !os.IsNotExist(err) {
		t.Fatal("spliced coverage wrote a pass file")
	}
}
