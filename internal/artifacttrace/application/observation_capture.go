package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abietic/switchback/internal/artifacttrace/domain"
	"github.com/abietic/switchback/internal/platform/secretcheck"
)

// These trusted local ports must honor context cancellation. Capture never
// detaches a goroutine holding source content or the original credential scope.
type ObservationWireCompiler interface {
	CompileObservationFragment(context.Context, []byte) ([]byte, error)
}

type ObservationScanner interface {
	ScanJSON(context.Context, string, any) error
}

type ObservationScanScope struct {
	WorkerSessionID   string `json:"worker_session_id"`
	RuntimeInstanceID string `json:"runtime_instance_id"`
	RuntimeGeneration uint64 `json:"runtime_generation"`
}

type ObservationProvenance struct {
	EnvelopeDigest   string `json:"envelope_digest,omitempty"`
	CheckpointDigest string `json:"checkpoint_digest,omitempty"`
	InputDigest      string `json:"input_digest,omitempty"`
	ReportDigest     string `json:"report_digest,omitempty"`
}

// PreparedObservation is backend-only prepared evidence, not authority to
// accept an event or publish a source. Journal/checkpoint owners revalidate its
// durable provenance in the transaction that seals the receipt.
type PreparedObservation struct {
	Frame     []byte
	Receipt   []byte
	ErrorCode string
}

// SealedObservationBundle crosses Backend application APIs only. No scanner,
// credentials or caller-supplied acceptance authority can be serialized here.
type SealedObservationBundle struct {
	SchemaVersion string                `json:"schema_version"`
	Observations  []PreparedObservation `json:"observations"`
}

type ObservationScanReceipt struct {
	SchemaVersion     string                `json:"schema_version"`
	ProjectionVersion string                `json:"projection_version"`
	CompilerRevision  string                `json:"compiler_revision"`
	PolicyRevision    string                `json:"policy_revision"`
	Scope             ObservationScanScope  `json:"scope"`
	FrameSHA256       string                `json:"frame_sha256"`
	WireSHA256        string                `json:"wire_sha256"`
	Provenance        ObservationProvenance `json:"provenance"`
}

func ObservationSafeCode(code string) bool {
	switch code {
	case "basis_scan_unavailable", "capture_cancelled", "capture_invalid", "compiler_failed", "credential_detected", "provenance_invalid", "scanner_unavailable", "scope_invalid", "source_invalid":
		return true
	}
	return false
}

func DecodeObservationScanReceipt(raw, frame []byte) (ObservationScanReceipt, error) {
	var r ObservationScanReceipt
	if strictObservationJSON(raw, &r) != nil {
		return r, errors.New("capture_invalid")
	}
	f, err := domain.DecodeObservationFragment(frame)
	if err != nil || r.SchemaVersion != "observation.scan.receipt.v1" || r.ProjectionVersion != "langfuse.projection.v3" || r.CompilerRevision != "langfuse.fragment.compiler.v1" || r.PolicyRevision != "runtime_known_tokens.v1" || r.FrameSHA256 != observationCaptureDigest(frame) || !validObservationProvenance(f.Kind, r.Provenance) || !validObservationProvenance(domain.FragmentModel, ObservationProvenance{EnvelopeDigest: r.WireSHA256}) || r.Scope.WorkerSessionID != f.Scope.WorkerSessionID || r.Scope.RuntimeInstanceID == "" || r.Scope.RuntimeGeneration == 0 || f.Turn != nil && (r.Scope.RuntimeInstanceID != string(f.Turn.RuntimeInstanceID) || r.Scope.RuntimeGeneration != uint64(f.Turn.RuntimeGeneration)) {
		return ObservationScanReceipt{}, errors.New("capture_invalid")
	}
	return r, nil
}

func EncodeSealedObservationBundle(b SealedObservationBundle) ([]byte, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, errors.New("capture_invalid")
	}
	if _, err = DecodeSealedObservationBundle(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func DecodeSealedObservationBundle(raw []byte) (SealedObservationBundle, error) {
	var b SealedObservationBundle
	if strictObservationJSON(raw, &b) != nil || b.SchemaVersion != "observation.capture.bundle.v1" || len(b.Observations) < 1 || len(b.Observations) > 2 {
		return SealedObservationBundle{}, errors.New("capture_invalid")
	}
	kinds := map[domain.FragmentKind]bool{}
	for _, p := range b.Observations {
		if p.ErrorCode != "" {
			if !ObservationSafeCode(p.ErrorCode) || len(p.Frame) != 0 || len(p.Receipt) != 0 {
				return SealedObservationBundle{}, errors.New("capture_invalid")
			}
			continue
		}
		f, err := domain.DecodeObservationFragment(p.Frame)
		if err != nil {
			return SealedObservationBundle{}, errors.New("capture_invalid")
		}
		if _, err := DecodeObservationScanReceipt(p.Receipt, p.Frame); err != nil || kinds[f.Kind] {
			return SealedObservationBundle{}, errors.New("capture_invalid")
		}
		kinds[f.Kind] = true
	}
	if kinds[domain.FragmentModel] && len(b.Observations) != 1 || kinds[domain.FragmentRunBasis] && len(b.Observations) != 2 {
		return SealedObservationBundle{}, errors.New("capture_invalid")
	}
	return b, nil
}

func strictObservationJSON(raw []byte, dst any) error {
	if len(raw) == 0 || len(raw) > maxObservationCaptureBytes || secretcheck.ValidateProofJSON(context.Background(), json.RawMessage(raw), maxObservationCaptureBytes) != nil {
		return errors.New("capture_invalid")
	}
	unique := json.NewDecoder(bytes.NewReader(raw))
	unique.UseNumber()
	if !uniqueBundleValue(unique) {
		return errors.New("capture_invalid")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errors.New("capture_invalid")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("capture_invalid")
	}
	return nil
}

func uniqueBundleValue(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			name = strings.ToLower(name)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !uniqueBundleValue(d) {
				return false
			}
		}
	case '[':
		for d.More() {
			if !uniqueBundleValue(d) {
				return false
			}
		}
	default:
		return false
	}
	end, err := d.Token()
	return err == nil && (delimiter == '{' && end == json.Delim('}') || delimiter == '[' && end == json.Delim(']'))
}

type ObservationCapture struct {
	Compiler ObservationWireCompiler
	// Scanner combines the original Session/Runtime/generation scanner and the
	// current Backend scanner, acquired by the trusted caller for this call.
	Scanner ObservationScanner
	Scope   ObservationScanScope
	Timeout time.Duration
}

// Match the existing Trace JSONL/model observation source limit. The context
// deadline bounds cooperative work, while this limit bounds local JSON work.
const maxObservationCaptureBytes = 32 << 20

// Prepare receives frame bytes and the complete original JSON-serializable
// evidence. It checks provenance shape; durable truth binding belongs to the
// accepting journal/checkpoint transaction. Zero Timeout means 250 ms.
func (c ObservationCapture) Prepare(ctx context.Context, frame []byte, evidence any, provenance ObservationProvenance) PreparedObservation {
	fail := func(code string) PreparedObservation { return PreparedObservation{ErrorCode: code} }
	if c.Compiler == nil || c.Scanner == nil || c.Timeout < 0 || c.Timeout > time.Second {
		return fail("capture_invalid")
	}
	if !captureCanonical(c.Scope.WorkerSessionID) || !captureCanonical(c.Scope.RuntimeInstanceID) || c.Scope.RuntimeGeneration == 0 {
		return fail("scope_invalid")
	}
	if c.Timeout == 0 {
		c.Timeout = 250 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	if ctx.Err() != nil {
		return fail("capture_cancelled")
	}
	if len(frame) > maxObservationCaptureBytes {
		return fail("source_invalid")
	}
	f, err := domain.DecodeObservationFragment(frame)
	if err != nil {
		return fail("source_invalid")
	}
	if f.Scope.WorkerSessionID != c.Scope.WorkerSessionID || f.Turn != nil && (string(f.Turn.RuntimeInstanceID) != c.Scope.RuntimeInstanceID || uint64(f.Turn.RuntimeGeneration) != c.Scope.RuntimeGeneration) {
		return fail("scope_invalid")
	}
	if !validObservationProvenance(f.Kind, provenance) {
		return fail("provenance_invalid")
	}
	if code := c.scan(ctx, "observation.scope", c.Scope); code != "" {
		return fail(code)
	}
	if ctx.Err() != nil {
		return fail("capture_cancelled")
	}
	if err := secretcheck.ValidateProofJSON(ctx, evidence, maxObservationCaptureBytes); err != nil {
		if ctx.Err() != nil {
			return fail("capture_cancelled")
		}
		return fail("source_invalid")
	}
	if code := c.scan(ctx, "observation.original", evidence); code != "" {
		return fail(code)
	}
	raw, err := json.Marshal(evidence)
	if err != nil || len(raw) > maxObservationCaptureBytes || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fail("source_invalid")
	}
	if code := c.scanJSON(ctx, "observation.original", raw); code != "" {
		return fail(code)
	}
	canonical, err := domain.EncodeObservationFragment(f)
	if err != nil {
		return fail("source_invalid")
	}
	if code := c.scanJSON(ctx, "observation.frame", canonical); code != "" {
		return fail(code)
	}
	wire, err := c.Compiler.CompileObservationFragment(ctx, canonical)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fail("capture_cancelled")
	}
	if err != nil {
		return fail("compiler_failed")
	}
	if code := c.scanJSON(ctx, "observation.wire", wire); code != "" {
		return fail(code)
	}
	receipt, err := json.Marshal(struct {
		SchemaVersion     string                `json:"schema_version"`
		ProjectionVersion string                `json:"projection_version"`
		CompilerRevision  string                `json:"compiler_revision"`
		PolicyRevision    string                `json:"policy_revision"`
		Scope             ObservationScanScope  `json:"scope"`
		FrameSHA256       string                `json:"frame_sha256"`
		WireSHA256        string                `json:"wire_sha256"`
		Provenance        ObservationProvenance `json:"provenance"`
	}{"observation.scan.receipt.v1", "langfuse.projection.v3", "langfuse.fragment.compiler.v1", "runtime_known_tokens.v1", c.Scope, observationCaptureDigest(canonical), observationCaptureDigest(wire), provenance})
	if err != nil {
		return fail("capture_invalid")
	}
	if code := c.scan(ctx, "observation.receipt", json.RawMessage(receipt)); code != "" {
		return fail(code)
	}
	if ctx.Err() != nil {
		return fail("capture_cancelled")
	}
	return PreparedObservation{Frame: canonical, Receipt: receipt}
}

func validObservationProvenance(kind domain.FragmentKind, p ObservationProvenance) bool {
	for _, value := range []string{p.EnvelopeDigest, p.CheckpointDigest, p.InputDigest, p.ReportDigest} {
		if value == "" {
			continue
		}
		if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
			return false
		}
		if _, err := hex.DecodeString(value[7:]); err != nil {
			return false
		}
	}
	if kind == domain.FragmentModel {
		return p.EnvelopeDigest != ""
	}
	return p.CheckpointDigest != "" && p.InputDigest != "" && p.ReportDigest != ""
}

func captureCanonical(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
func observationCaptureDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (c ObservationCapture) scanJSON(ctx context.Context, source string, raw []byte) string {
	if ctx.Err() != nil {
		return "capture_cancelled"
	}
	if len(raw) == 0 || len(raw) > maxObservationCaptureBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return "source_invalid"
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	// Validate and scan decoded strings as well as raw JSON: escape spelling
	// must not hide a token in an original envelope or embedded I/O text.
	if code := c.scanValue(ctx, source, d); code != "" {
		return code
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return "source_invalid"
	}
	return c.scan(ctx, source, json.RawMessage(raw))
}

func (c ObservationCapture) scanValue(ctx context.Context, source string, d *json.Decoder) string {
	if ctx.Err() != nil {
		return "capture_cancelled"
	}
	token, err := d.Token()
	if err != nil {
		return "source_invalid"
	}
	if value, ok := token.(string); ok {
		if code := c.scan(ctx, source, value); code != "" {
			return code
		}
		// json-text frames contain a full JSON document inside a string.
		if json.Valid([]byte(value)) {
			return c.scanJSON(ctx, source, []byte(value))
		}
		return ""
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return ""
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return "source_invalid"
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return "source_invalid"
			}
			seen[name] = true
			if code := c.scan(ctx, source, name); code != "" {
				return code
			}
		}
		if code := c.scanValue(ctx, source, d); code != "" {
			return code
		}
	}
	end, err := d.Token()
	if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return "source_invalid"
	}
	return ""
}

func (c ObservationCapture) scan(ctx context.Context, source string, value any) string {
	if ctx.Err() != nil {
		return "capture_cancelled"
	}
	err := c.Scanner.ScanJSON(ctx, source, value)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "capture_cancelled"
	}
	if secretcheck.IsExposure(err) {
		return "credential_detected"
	}
	if err != nil {
		return "scanner_unavailable"
	}
	return ""
}
