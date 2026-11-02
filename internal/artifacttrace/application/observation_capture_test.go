package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	langfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	app "github.com/abietic/switchback/internal/artifacttrace/application"
	domain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/secretcheck"
)

func captureFrame(t *testing.T) []byte {
	t.Helper()
	f := domain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: domain.FragmentRunBasis, Scope: domain.ObservationScope{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run"}, InitialTurnID: "turn", StartedAt: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), InputJSON: `{"n":9007199254740993}`}
	b, err := domain.EncodeObservationFragment(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func captureSetup(t *testing.T) (app.ObservationCapture, app.ObservationProvenance) {
	t.Helper()
	scanner, err := secretcheck.New([]secretcheck.KnownToken{{Label: "original", Value: "original-secret"}, {Label: "backend", Value: "backend-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	return app.ObservationCapture{Compiler: langfuse.FragmentCompiler{}, Scanner: scanner, Scope: app.ObservationScanScope{WorkerSessionID: "session", RuntimeInstanceID: "runtime", RuntimeGeneration: 1}}, app.ObservationProvenance{CheckpointDigest: digest, InputDigest: digest, ReportDigest: digest}
}

func TestObservationCaptureReceiptBindsExactFrameAndWire(t *testing.T) {
	c, p := captureSetup(t)
	frame := captureFrame(t)
	got := c.Prepare(context.Background(), frame, json.RawMessage(`{"input":{"n":9007199254740993},"report":"done"}`), p)
	if got.ErrorCode != "" {
		t.Fatal(got.ErrorCode)
	}
	var receipt struct {
		SchemaVersion     string                    `json:"schema_version"`
		ProjectionVersion string                    `json:"projection_version"`
		CompilerRevision  string                    `json:"compiler_revision"`
		PolicyRevision    string                    `json:"policy_revision"`
		FrameSHA256       string                    `json:"frame_sha256"`
		WireSHA256        string                    `json:"wire_sha256"`
		Scope             app.ObservationScanScope  `json:"scope"`
		Provenance        app.ObservationProvenance `json:"provenance"`
	}
	if err := json.Unmarshal(got.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	wire, err := c.Compiler.CompileObservationFragment(context.Background(), got.Frame)
	if err != nil {
		t.Fatal(err)
	}
	fhash, whash := sha256.Sum256(got.Frame), sha256.Sum256(wire)
	if receipt.SchemaVersion != "observation.scan.receipt.v1" || receipt.ProjectionVersion != "langfuse.projection.v3" || receipt.CompilerRevision != "langfuse.fragment.compiler.v1" || receipt.PolicyRevision != "runtime_known_tokens.v1" || receipt.FrameSHA256 != "sha256:"+hex.EncodeToString(fhash[:]) || receipt.WireSHA256 != "sha256:"+hex.EncodeToString(whash[:]) || receipt.Scope != c.Scope || receipt.Provenance != p {
		t.Fatalf("receipt contract: %#v", receipt)
	}
	c.Compiler = alteredWireCompiler{}
	changed := c.Prepare(context.Background(), frame, json.RawMessage(`{}`), p)
	if changed.ErrorCode != "" || string(changed.Receipt) == string(got.Receipt) {
		t.Fatal("wire change not bound")
	}
}

type alteredWireCompiler struct{}

func (alteredWireCompiler) CompileObservationFragment(context.Context, []byte) ([]byte, error) {
	return []byte(`{"batch":[],"changed":true}`), nil
}

func TestObservationCaptureFailuresReturnOnlySafeCode(t *testing.T) {
	for _, tc := range []struct {
		name, evidence string
		change         func(*app.ObservationCapture, *app.ObservationProvenance)
		code           string
	}{
		{"original token", `{"discarded":"original-secret"}`, nil, "credential_detected"},
		{"backend token", `{"discarded":"backend-secret"}`, nil, "credential_detected"},
		{"escaped token", `{"discarded":"original-\u0073ecret"}`, nil, "credential_detected"},
		{"duplicate evidence", `{"a":1,"a":2}`, nil, "source_invalid"},
		{"scope missing", `{}`, func(c *app.ObservationCapture, _ *app.ObservationProvenance) { c.Scope.RuntimeInstanceID = "" }, "scope_invalid"},
		{"scope wrong", `{}`, func(c *app.ObservationCapture, _ *app.ObservationProvenance) { c.Scope.WorkerSessionID = "other" }, "scope_invalid"},
		{"provenance missing", `{}`, func(_ *app.ObservationCapture, p *app.ObservationProvenance) { p.CheckpointDigest = "" }, "provenance_invalid"},
		{"provenance token", `{}`, func(_ *app.ObservationCapture, p *app.ObservationProvenance) { p.InputDigest = "original-secret" }, "provenance_invalid"},
		{"timeout invalid", `{}`, func(c *app.ObservationCapture, _ *app.ObservationProvenance) { c.Timeout = 2 * time.Second }, "capture_invalid"},
		{"scanner unavailable", `{}`, func(c *app.ObservationCapture, _ *app.ObservationProvenance) { c.Scanner = errorScan{} }, "scanner_unavailable"},
		{"scanner cancellation", `{}`, func(c *app.ObservationCapture, _ *app.ObservationProvenance) { c.Scanner = errorScan{context.Canceled} }, "capture_cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := captureSetup(t)
			if tc.change != nil {
				tc.change(&c, &p)
			}
			got := c.Prepare(context.Background(), captureFrame(t), json.RawMessage(tc.evidence), p)
			if got.ErrorCode != tc.code || len(got.Frame) != 0 || len(got.Receipt) != 0 {
				t.Fatalf("unsafe capture: code=%s frame=%d receipt=%d", got.ErrorCode, len(got.Frame), len(got.Receipt))
			}
		})
	}
}

type errorScan struct{ err error }

func (s errorScan) ScanJSON(context.Context, string, any) error {
	if s.err != nil {
		return s.err
	}
	return errors.New("original-secret")
}

func TestObservationCaptureScansScopeFrameAndWire(t *testing.T) {
	for _, where := range []string{"scope", "frame", "wire"} {
		t.Run(where, func(t *testing.T) {
			c, p := captureSetup(t)
			frame := captureFrame(t)
			switch where {
			case "scope":
				c.Scope.RuntimeInstanceID = "original-secret"
			case "frame":
				frame = []byte(strings.Replace(string(frame), `"workspace"`, `"original-secret"`, 1))
			case "wire":
				c.Compiler = secretWireCompiler{}
			}
			got := c.Prepare(context.Background(), frame, json.RawMessage(`{}`), p)
			if got.ErrorCode != "credential_detected" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
				t.Fatalf("unsafe %s result: %s", where, got.ErrorCode)
			}
		})
	}
}

type secretWireCompiler struct{}

func (secretWireCompiler) CompileObservationFragment(context.Context, []byte) ([]byte, error) {
	return []byte(`{"token":"backend-\u0073ecret"}`), nil
}

func TestObservationCaptureContextBudget(t *testing.T) {
	c, p := captureSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := c.Prepare(ctx, captureFrame(t), json.RawMessage(`{}`), p)
	if got.ErrorCode != "capture_cancelled" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
		t.Fatal("cancelled result")
	}
	c.Scanner = budgetScan{t: t}
	got = c.Prepare(context.Background(), captureFrame(t), json.RawMessage(`{}`), p)
	if got.ErrorCode != "capture_cancelled" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
		t.Fatal("deadline result")
	}
}

type budgetScan struct{ t *testing.T }

func (s budgetScan) ScanJSON(ctx context.Context, _ string, _ any) error {
	s.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 250*time.Millisecond {
		s.t.Error("default budget absent")
	}
	return context.DeadlineExceeded
}

func TestObservationCaptureOriginalEvidenceValidation(t *testing.T) {
	for _, raw := range []json.RawMessage{[]byte("{\"bad\":\"\xff\"}"), []byte(`{} {}`), []byte(`null`)} {
		c, p := captureSetup(t)
		got := c.Prepare(context.Background(), captureFrame(t), raw, p)
		if got.ErrorCode != "source_invalid" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
			t.Fatal("invalid original evidence accepted", got.ErrorCode)
		}
	}
}

func TestObservationCaptureOriginalBytesBeforeSerialization(t *testing.T) {
	type reportBytes []byte
	var byteArray [15]byte
	copy(byteArray[:], "original-secret")
	for _, evidence := range []any{byteArray, []byte("original-secret"), struct {
		Report reportBytes `json:"report"`
	}{reportBytes("original-secret")}, map[string]any{"nested": []any{[]byte("original-secret")}}} {
		c, p := captureSetup(t)
		got := c.Prepare(context.Background(), captureFrame(t), evidence, p)
		if got.ErrorCode != "credential_detected" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
			t.Fatalf("original bytes not rejected: %s", got.ErrorCode)
		}
	}
}

func TestObservationCaptureRejectsUnsupportedEvidenceBeforeExecution(t *testing.T) {
	calls := 0
	cycle := map[string]any{}
	cycle["self"] = cycle
	var deep any = "safe"
	for range 80 {
		deep = []any{deep}
	}
	for name, evidence := range map[string]any{
		"custom": &captureMarshaler{Calls: &calls}, "text": captureTextMarshaler{Calls: &calls}, "nested custom": map[string]any{"custom": &captureMarshaler{Calls: &calls}},
		"cycle": cycle, "depth": deep, "large": strings.Repeat("safe", (32<<20)/4), "wide": make([]int, 1<<18), "invalid utf8": "bad-\xff", "unsupported": func() {},
	} {
		t.Run(name, func(t *testing.T) {
			c, p := captureSetup(t)
			got := c.Prepare(context.Background(), captureFrame(t), evidence, p)
			if got.ErrorCode != "source_invalid" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
				t.Fatalf("unsupported evidence accepted: code=%s", got.ErrorCode)
			}
			if calls != 0 {
				t.Fatal("custom serializer executed")
			}
		})
	}
}

type captureMarshaler struct {
	Calls  *int
	Secret string
}

func (v *captureMarshaler) MarshalJSON() ([]byte, error) {
	*v.Calls++
	return []byte(`{"safe":true}`), nil
}

type captureTextMarshaler struct{ Calls *int }

func (v captureTextMarshaler) MarshalText() ([]byte, error) { *v.Calls++; return []byte("safe"), nil }

type captureValueZero struct{ Calls *int }

func (v captureValueZero) IsZero() bool { *v.Calls++; return false }

type capturePointerZero struct{ Calls *int }

func (v *capturePointerZero) IsZero() bool { *v.Calls++; return false }

type captureZeroer interface {
	IsZero() bool
}

func TestObservationCaptureRejectsOmitZeroHooksBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		evidence func(*int) any
	}{
		{"value receiver", func(calls *int) any {
			return struct {
				Data captureValueZero `json:"data,omitzero"`
			}{Data: captureValueZero{Calls: calls}}
		}},
		{"pointer field", func(calls *int) any {
			return struct {
				Data *capturePointerZero `json:"data,omitzero"`
			}{Data: &capturePointerZero{Calls: calls}}
		}},
		{"pointer receiver on non-addressable value", func(calls *int) any {
			return struct {
				Data capturePointerZero `json:"data,omitzero"`
			}{Data: capturePointerZero{Calls: calls}}
		}},
		{"interface field", func(calls *int) any {
			return struct {
				Data captureZeroer `json:"data,omitzero"`
			}{Data: captureValueZero{Calls: calls}}
		}},
		{"nested field", func(calls *int) any {
			return struct {
				Nested struct {
					Data captureValueZero `json:"data,omitzero"`
				} `json:"nested"`
			}{Nested: struct {
				Data captureValueZero `json:"data,omitzero"`
			}{Data: captureValueZero{Calls: calls}}}
		}},
		{"dash name with option", func(calls *int) any {
			return struct {
				Data captureValueZero `json:"-,omitzero"`
			}{Data: captureValueZero{Calls: calls}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, p := captureSetup(t)
			got := c.Prepare(context.Background(), captureFrame(t), tc.evidence(&calls), p)
			if got.ErrorCode != "source_invalid" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
				t.Fatalf("unsafe capture: code=%q frame=%d receipt=%d", got.ErrorCode, len(got.Frame), len(got.Receipt))
			}
			if calls != 0 {
				t.Fatalf("IsZero executed %d times", calls)
			}
		})
	}
}

func TestObservationCaptureAcceptsOriginalEnvelopeAndTime(t *testing.T) {
	c, p := captureSetup(t)
	evidence := contractsv1.RuntimeControlEnvelope{SchemaVersion: "worker.control.v1", MessageID: "event", Payload: json.RawMessage(`{"input":"safe"}`), SentAt: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)}
	got := c.Prepare(context.Background(), captureFrame(t), evidence, p)
	if got.ErrorCode != "" || len(got.Receipt) == 0 {
		t.Fatal("plain envelope rejected", got.ErrorCode)
	}
}

func TestObservationCaptureRejectsUppercaseProvenance(t *testing.T) {
	for _, field := range []string{"checkpoint", "input", "report", "envelope"} {
		t.Run(field, func(t *testing.T) {
			c, p := captureSetup(t)
			upper := "sha256:" + strings.Repeat("A", 64)
			switch field {
			case "checkpoint":
				p.CheckpointDigest = upper
			case "input":
				p.InputDigest = upper
			case "report":
				p.ReportDigest = upper
			case "envelope":
				p.EnvelopeDigest = upper
			}
			got := c.Prepare(context.Background(), captureFrame(t), json.RawMessage(`{}`), p)
			if got.ErrorCode != "provenance_invalid" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
				t.Fatal("noncanonical digest accepted", got.ErrorCode)
			}
		})
	}
}

func TestObservationCaptureRejectsRawComplexityAndByteSerializers(t *testing.T) {
	byteMarshalerCalls = 0
	for _, evidence := range []any{json.RawMessage(strings.Repeat("[", 80) + `"safe"` + strings.Repeat("]", 80)), []captureByteMarshaler{1}, map[string]any{"json": strings.Repeat("[", 80) + `"safe"` + strings.Repeat("]", 80)}} {
		c, p := captureSetup(t)
		got := c.Prepare(context.Background(), captureFrame(t), evidence, p)
		if got.ErrorCode != "source_invalid" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
			t.Error("unsupported raw/byte evidence accepted", got.ErrorCode)
		}
	}
	if byteMarshalerCalls != 0 {
		t.Fatal("element serializer executed")
	}
}

var byteMarshalerCalls int

type captureByteMarshaler byte

func (captureByteMarshaler) MarshalJSON() ([]byte, error) {
	byteMarshalerCalls++
	return []byte(`1`), nil
}

func TestObservationCaptureScansFinalReceipt(t *testing.T) {
	c, p := captureSetup(t)
	c.Scanner, _ = secretcheck.New([]secretcheck.KnownToken{{Label: "receipt-only", Value: strings.Repeat("a", 15)}})
	got := c.Prepare(context.Background(), captureFrame(t), json.RawMessage(`{}`), p)
	if got.ErrorCode != "credential_detected" || len(got.Frame) != 0 || len(got.Receipt) != 0 {
		t.Fatal("receipt-only credential was not rejected", got.ErrorCode)
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("private scanner error")} {
		c, p = captureSetup(t)
		c.Scanner = receiptErrorScan{err: err}
		got = c.Prepare(context.Background(), captureFrame(t), json.RawMessage(`{}`), p)
		code := "scanner_unavailable"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "capture_cancelled"
		}
		if got.ErrorCode != code || len(got.Frame) != 0 || len(got.Receipt) != 0 {
			t.Fatal("receipt scan failure did not discard content", got.ErrorCode)
		}
	}
}

type receiptErrorScan struct{ err error }

func (s receiptErrorScan) ScanJSON(_ context.Context, source string, _ any) error {
	if source == "observation.receipt" {
		return s.err
	}
	return nil
}
