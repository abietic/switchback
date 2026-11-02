package domain

import (
	"strings"
	"testing"
	"time"
)

func TestObservationFragmentIdentityExcludesPayload(t *testing.T) {
	frame := ObservationFragment{
		SchemaVersion: "observation.fragment.v1", Kind: FragmentRunBasis,
		Scope:         ObservationScope{"tenant-1", "user-1", "workspace-1", "session-1", "run-1"},
		InitialTurnID: "turn-1", StartedAt: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC),
		InputJSON: "{ \"n\":9007199254740993 }",
	}
	original, err := ObservationFragmentID(frame)
	if err != nil {
		t.Fatal(err)
	}
	frame.InputJSON = "{\"n\":9007199254740994}"
	changed, err := ObservationFragmentID(frame)
	if err != nil {
		t.Fatal(err)
	}
	if changed != original {
		t.Fatal("payload must not allocate a second logical source")
	}
	encoded, err := EncodeObservationFragment(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObservationFragment(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.InputJSON != frame.InputJSON {
		t.Fatal("I/O text changed")
	}
}

func TestObservationBasisSourceIdentityRequiresNoContent(t *testing.T) {
	for _, kind := range []FragmentKind{FragmentRunBasis, FragmentTurnBasis} {
		frame := fragmentFixture(kind)
		want, err := ObservationFragmentID(frame)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ObservationBasisSourceID(kind, frame.Scope, frame.Turn)
		if err != nil || got != want {
			t.Fatalf("no-content identity=%s want=%s err=%v", got, want, err)
		}
		frame.Scope.WorkspaceID = ""
		if _, err := ObservationBasisSourceID(kind, frame.Scope, frame.Turn); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	if _, err := ObservationBasisSourceID(FragmentModel, fragmentFixture(FragmentRunBasis).Scope, nil); err == nil {
		t.Fatal("model used basis identity API")
	}
}

func fragmentFixture(kind FragmentKind) ObservationFragment {
	start := time.Date(2026, 9, 13, 0, 0, 0, 123456789, time.UTC)
	end := start.Add(time.Second)
	frame := ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: kind, Scope: ObservationScope{"tenant-1", "user-1", "workspace-1", "session-1", "run-1"}, StartedAt: start, InputJSON: "{ \"n\":9007199254740993 }"}
	if kind == FragmentRunBasis {
		frame.InitialTurnID = "turn-1"
		return frame
	}
	frame.Turn = &TurnIdentity{TenantID: "tenant-1", UserID: "user-1", WorkspaceID: "workspace-1", WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1", AgentSessionID: "agent-1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1}
	frame.EndedAt, frame.OutputJSON = &end, `{ "report": "done" }`
	if kind == FragmentModel {
		frame.ObservationID, frame.Model, frame.Route, frame.Status = "model-1", "model", "route", "ok"
		frame.InputTokens, frame.OutputTokens = 2, 3
	}
	return frame
}

func TestObservationFragmentValidation(t *testing.T) {
	for _, kind := range []FragmentKind{FragmentRunBasis, FragmentTurnBasis, FragmentModel} {
		t.Run(string(kind), func(t *testing.T) {
			frame := fragmentFixture(kind)
			data, err := EncodeObservationFragment(frame)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeObservationFragment(data)
			if err != nil {
				t.Fatal(err)
			}
			if got.InputJSON != frame.InputJSON || got.OutputJSON != frame.OutputJSON {
				t.Fatal("I/O text changed")
			}
		})
	}
	tests := []struct {
		name   string
		kind   FragmentKind
		change func(*ObservationFragment)
	}{
		{"run turn", FragmentRunBasis, func(f *ObservationFragment) { f.Turn = fragmentFixture(FragmentTurnBasis).Turn }},
		{"run end", FragmentRunBasis, func(f *ObservationFragment) { f.EndedAt = &f.StartedAt }},
		{"run output", FragmentRunBasis, func(f *ObservationFragment) { f.OutputJSON = `null` }},
		{"run initial", FragmentRunBasis, func(f *ObservationFragment) { f.InitialTurnID = "" }},
		{"turn model", FragmentTurnBasis, func(f *ObservationFragment) { f.Model = "model" }},
		{"missing identity", FragmentModel, func(f *ObservationFragment) { f.Turn = nil }},
		{"cross scope", FragmentModel, func(f *ObservationFragment) { f.Turn.WorkspaceID = "other" }},
		{"missing turn", FragmentModel, func(f *ObservationFragment) { f.Turn.AgentTurnID = "" }},
		{"missing observation", FragmentModel, func(f *ObservationFragment) { f.ObservationID = "" }},
		{"missing model", FragmentModel, func(f *ObservationFragment) { f.Model = "" }},
		{"missing route", FragmentModel, func(f *ObservationFragment) { f.Route = "" }},
		{"missing status", FragmentModel, func(f *ObservationFragment) { f.Status = "" }},
		{"missing input", FragmentModel, func(f *ObservationFragment) { f.InputJSON = "" }},
		{"missing start", FragmentModel, func(f *ObservationFragment) { f.StartedAt = time.Time{} }},
		{"missing end", FragmentModel, func(f *ObservationFragment) { f.EndedAt = nil }},
		{"backward time", FragmentModel, func(f *ObservationFragment) { e := f.StartedAt.Add(-time.Second); f.EndedAt = &e }},
		{"success null", FragmentModel, func(f *ObservationFragment) { f.OutputJSON = `null` }},
		{"invalid UTF8", FragmentModel, func(f *ObservationFragment) { f.InputJSON = "\"\xff\"" }},
		{"duplicate input", FragmentModel, func(f *ObservationFragment) { f.InputJSON = `{"a":1,"a":2}` }},
		{"trailing input", FragmentModel, func(f *ObservationFragment) { f.InputJSON = `{} {}` }},
		{"negative usage", FragmentModel, func(f *ObservationFragment) { f.InputTokens = -1 }},
		{"unsafe usage", FragmentModel, func(f *ObservationFragment) { f.InputTokens = 1<<53 - 1; f.OutputTokens = 1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := fragmentFixture(tc.kind)
			tc.change(&f)
			b, err := EncodeObservationFragment(f)
			if err == nil || err.Error() != "source_invalid" || len(b) != 0 {
				t.Fatalf("unsafe result: error=%v bytes=%d", err, len(b))
			}
		})
	}
	f := fragmentFixture(FragmentModel)
	f.Status, f.OutputJSON = "failed", " null \n"
	b, err := EncodeObservationFragment(f)
	if err != nil {
		t.Fatal(err)
	}
	d, err := DecodeObservationFragment(b)
	if err != nil || d.OutputJSON != f.OutputJSON {
		t.Fatal("failed callback null must survive")
	}
}

func TestObservationFragmentStrictDecode(t *testing.T) {
	b, _ := EncodeObservationFragment(fragmentFixture(FragmentRunBasis))
	for _, data := range []string{string(b) + ` {}`, strings.Replace(string(b), `"schema_version":`, `"unexpected":1,"schema_version":`, 1), strings.Replace(string(b), `"kind":`, `"kind":"model","kind":`, 1), strings.Replace(string(b), `"tenant_id":`, `"tenant_id":"evil","tenant_id":`, 1), strings.Replace(string(b), `"initial_turn_id":`, `"turn":null,"initial_turn_id":`, 1)} {
		f, err := DecodeObservationFragment([]byte(data))
		if err == nil || err.Error() != "source_invalid" || f.InputJSON != "" {
			t.Fatalf("unsafe decode: %v", err)
		}
	}
}

func TestObservationFragmentLogicalIdentity(t *testing.T) {
	f := fragmentFixture(FragmentModel)
	original, _ := ObservationFragmentID(f)
	if original != "sha256:dc9d8b92d8d1ab2d573a18e4bc9c368d4ff19a7fd9941ce84cff05834167c64a" {
		t.Fatal("logical identity golden changed", original)
	}
	f.Turn.TraceStreamID, f.Turn.LangfuseTraceID, f.Turn.RuntimeNetwork.ID = "old", "old", "network"
	f.StartedAt = f.StartedAt.Add(time.Millisecond)
	got, err := ObservationFragmentID(f)
	if err != nil || got != original {
		t.Fatal("non-identity content changed ID", err)
	}
	f.Turn.AgentTurnID = "turn-2"
	got, _ = ObservationFragmentID(f)
	if got == original {
		t.Fatal("turn identity did not change ID")
	}
}

func TestObservationFragmentRejectsOversizeAndCaseAliases(t *testing.T) {
	f := fragmentFixture(FragmentRunBasis)
	f.InputJSON = `"` + strings.Repeat("x", 32<<20) + `"`
	if b, err := EncodeObservationFragment(f); err == nil || len(b) != 0 {
		t.Fatal("oversized source accepted")
	}
	b, _ := EncodeObservationFragment(fragmentFixture(FragmentRunBasis))
	for _, raw := range []string{strings.Replace(string(b), `"tenant_id":`, `"Tenant_ID":`, 1), strings.Replace(string(b), `"initial_turn_id":`, `"Initial_Turn_ID":`, 1)} {
		if f, err := DecodeObservationFragment([]byte(raw)); err == nil || f.InputJSON != "" {
			t.Fatal("noncanonical field spelling accepted")
		}
	}
}
