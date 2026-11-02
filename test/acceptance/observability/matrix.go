package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

type frozenLegacyEvidence struct {
	Metadata      artifactapp.ProjectionSourceMetadata
	Turn          artifactapp.TurnObservationSource
	ObjectDigests []string
}

// Verify immutable evidence, not v2 projectability: a valid frozen gap must
// remain a gap after late v3 capture. No raw object content is exported here.
func readFrozenLegacy(ctx context.Context, metadata artifactapp.ProjectionSourceMetadataStore, objects workerapp.ObjectStore, ref artifactapp.ProjectionSourceRef) (frozenLegacyEvidence, error) {
	var result frozenLegacyEvidence
	m, err := metadata.LoadProjectionSourceMetadata(ctx, ref)
	if err != nil {
		return result, err
	}
	if m.Manifest.Validate() != nil || m.RootManifest.Validate() != nil || m.Report.Validate() != nil || string(m.Manifest.TraceStreamID) != ref.TraceStreamID || m.ManifestObject.Checksum != ref.ManifestChecksum || m.RootManifest.TenantID != m.Root.TenantID || m.RootManifest.UserID != m.Root.UserID || m.RootManifest.WorkspaceID != m.Root.WorkspaceID || string(m.RootManifest.WorkerSessionID) != m.Root.WorkerSessionID || string(m.RootManifest.WorkerRunID) != m.Root.WorkerRunID {
		return result, fmt.Errorf("frozen v2 metadata identity invalid")
	}
	verify := func(object workerapp.StoredObject) ([]byte, error) {
		if object.Key == "" || object.Size < 0 || object.Size > 64<<20 || !digestPattern.MatchString(object.Checksum) {
			return nil, fmt.Errorf("frozen object metadata invalid")
		}
		r, err := objects.Open(ctx, object.Key)
		if err != nil {
			return nil, fmt.Errorf("frozen object unavailable")
		}
		defer r.Close()
		data, err := io.ReadAll(io.LimitReader(r, object.Size+1))
		if err != nil {
			return nil, fmt.Errorf("frozen object read failed")
		}
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if int64(len(data)) != object.Size || digest != object.Checksum {
			return nil, fmt.Errorf("frozen object integrity mismatch")
		}
		result.ObjectDigests = append(result.ObjectDigests, digest)
		return data, nil
	}
	manifestBytes, err := verify(m.ManifestObject)
	if err != nil {
		return result, err
	}
	decoded, err := contractsv1.DecodeTraceManifest(manifestBytes)
	if err != nil || !reflect.DeepEqual(decoded, m.Manifest) {
		return result, fmt.Errorf("frozen manifest object mismatch")
	}
	for _, manifest := range []contractsv1.TraceManifest{m.RootManifest, m.Manifest} {
		for _, segment := range manifest.Segments {
			key, ok := strings.CutPrefix(segment.URI, "object://")
			if !ok {
				return result, fmt.Errorf("frozen segment URI invalid")
			}
			if _, err = verify(workerapp.StoredObject{Key: key, Size: segment.SizeBytes, Checksum: "sha256:" + segment.SHA256}); err != nil {
				return result, err
			}
		}
		if _, err = artifactapp.ReadTurnObservationSource(ctx, objects, manifest); err != nil {
			return result, err
		}
	}
	key, ok := strings.CutPrefix(m.Report.URI, "object://")
	if !ok {
		return result, fmt.Errorf("frozen report URI invalid")
	}
	if _, err = verify(workerapp.StoredObject{Key: key, Size: m.Report.SizeBytes, Checksum: "sha256:" + m.Report.SHA256}); err != nil {
		return result, err
	}
	result.Turn, err = artifactapp.ReadTurnObservationSource(ctx, objects, m.Manifest)
	result.Metadata = m
	return result, err
}

func awaitSupplementProofs(ctx context.Context, pool, proofPool *pgxpool.Pool, objects *artifactlocal.ObjectStore, turns []string, expected []ExpectedModel, requireVerified bool) ([]FragmentProof, error) {
	for {
		proofs, ready, err := readSupplementProofs(ctx, pool, proofPool, objects, turns, expected, requireVerified)
		if err != nil {
			return nil, err
		}
		if ready {
			return proofs, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("exact v3 source set not ready: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func readSupplementProofs(ctx context.Context, pool, proofPool *pgxpool.Pool, objects *artifactlocal.ObjectStore, turns []string, expected []ExpectedModel, requireVerified bool) ([]FragmentProof, bool, error) {
	rows, err := pool.Query(ctx, `SELECT c.kind,c.source_id,c.frame_digest,c.frame,c.created_at,p.published_at,j.version,j.status,j.next_attempt_at FROM observation_candidates c LEFT JOIN observation_publications p USING(kind,source_id,frame_digest) LEFT JOIN observation_fragment_jobs j ON j.kind=c.kind AND j.source_id=c.source_id AND j.frame_digest=c.frame_digest AND j.version=$2 WHERE c.worker_run_id IN(SELECT worker_run_id FROM agent_turns WHERE agent_turn_id=ANY($1)) ORDER BY c.kind,c.source_id`, turns, fragmentVersion)
	if err != nil {
		return nil, false, err
	}
	type row struct {
		ref                 artifactapp.FragmentRef
		frame               []byte
		accepted            time.Time
		published, verified *time.Time
		version, status     *string
	}
	var records []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.ref.Kind, &r.ref.SourceID, &r.ref.FrameDigest, &r.frame, &r.accepted, &r.published, &r.version, &r.status, &r.verified); err != nil {
			rows.Close()
			return nil, false, err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	loader := artifactapp.FragmentLoader{Metadata: artifactpostgres.NewObservationLedger(proofPool), Objects: objects}
	known := map[string]string{}
	keys := map[string]string{}
	for _, e := range expected {
		known[e.ObservationID] = e.TurnID
		keys[e.ObservationID] = e.FixtureKey
	}
	selectedTurns := map[string]bool{}
	for _, turn := range turns {
		selectedTurns[turn] = true
	}
	models, bases, roots := map[string]bool{}, map[string]bool{}, map[string]bool{}
	proofs := []FragmentProof{}
	for _, r := range records {
		if len(r.frame) == 0 {
			return nil, false, fmt.Errorf("blocked v3 source without sealed frame")
		}
		f, e := artifactdomain.DecodeObservationFragment(r.frame)
		if e != nil {
			return nil, false, e
		}
		turn := ""
		if f.Turn != nil {
			turn = string(f.Turn.AgentTurnID)
			if !selectedTurns[turn] {
				continue
			}
		}
		if f.Kind == artifactdomain.FragmentModel {
			if known[f.ObservationID] != turn {
				return nil, false, fmt.Errorf("unexpected model outside frozen fixture set")
			}
			models[f.ObservationID] = true
			if !strings.Contains(f.InputJSON, "switchback-obsv-stream:"+keys[f.ObservationID]) || !strings.Contains(f.OutputJSON, "deterministic late response "+turn) || f.InputTokens != 11 || f.OutputTokens != 7 || f.Model == "" {
				return nil, false, fmt.Errorf("known fixture model I/O or usage missing")
			}
		}
		if f.Kind == artifactdomain.FragmentRunBasis {
			roots[f.Scope.WorkerRunID] = true
		}
		if f.Kind == artifactdomain.FragmentTurnBasis {
			bases[turn] = true
		}
		if r.published == nil || r.version == nil || r.status == nil {
			return nil, false, nil
		}
		if *r.version != fragmentVersion || *r.status == "blocked" {
			return nil, false, fmt.Errorf("v3 projection blocked or mixed version")
		}
		loaded, e := loader.Load(ctx, artifactapp.FragmentJob{Source: r.ref, Version: fragmentVersion})
		if e != nil {
			return nil, false, e
		}
		if len(loaded.Frames) == 0 {
			return nil, false, fmt.Errorf("source loader returned empty frames")
		}
		proof := FragmentProof{Version: fragmentVersion, Kind: r.ref.Kind, SourceID: r.ref.SourceID, Digest: r.ref.FrameDigest, RunID: f.Scope.WorkerRunID, TurnID: turn, ObservationID: f.ObservationID, AcceptedAt: r.accepted, SealedAt: r.accepted, PublishedAt: *r.published, InputDigest: artifactdomain.Checksum([]byte(f.InputJSON)), OutputDigest: artifactdomain.Checksum([]byte(f.OutputJSON))}
		receipt, e := artifactapp.DecodeObservationScanReceipt(loaded.Receipts[0], loaded.Frames[0])
		if e != nil {
			return nil, false, e
		}
		proof.SessionID = receipt.Scope.WorkerSessionID
		proof.RuntimeID = receipt.Scope.RuntimeInstanceID
		proof.Generation = receipt.Scope.RuntimeGeneration
		if *r.status == "passed" && r.verified != nil {
			proof.VerifiedAt = *r.verified
		} else if requireVerified {
			return nil, false, nil
		}
		proofs = append(proofs, proof)
	}
	if len(models) != len(known) || len(bases) != len(turns) || len(roots) == 0 {
		return nil, false, nil
	}
	for _, p := range proofs {
		if !roots[p.RunID] {
			return nil, false, fmt.Errorf("exact parent run source missing")
		}
	}
	return proofs, true, nil
}

// measurementFromRaw binds the latency numbers to their actual journal and ACK
// timestamps. Snapshot time is not used to invent a measurement window.
func measurementFromRaw(raw RawEvidence) (Measurement, error) {
	m := Measurement{Project: raw.Project, ID: raw.MeasurementID, Expected: raw.Expected, Sources: raw.FragmentProofs, LegacyBefore: raw.LegacyDigest, LegacyAfter: raw.LegacyDigest}
	var runtimes []struct {
		Runtime    string `json:"runtime_instance_id"`
		Session    string `json:"worker_session_id"`
		Generation uint64 `json:"generation"`
	}
	if json.Unmarshal(raw.Runtimes, &runtimes) != nil || len(runtimes) != 1 {
		return m, fmt.Errorf("measurement runtime identity missing")
	}
	m.SessionID = runtimes[0].Session
	m.RuntimeID = runtimes[0].Runtime
	m.Generation = runtimes[0].Generation
	if len(raw.Settlements) != 2 || raw.Dispatch == nil {
		return m, fmt.Errorf("measurement timestamp set missing")
	}
	for _, s := range raw.Settlements {
		if s.TurnID == "" || s.MessageID == "" || s.From.IsZero() || s.To.Before(s.From) || s.Milliseconds == nil || abs(float64(s.To.Sub(s.From))/float64(time.Millisecond)-*s.Milliseconds) > .001 {
			return m, fmt.Errorf("measurement settlement timestamp/latency mismatch")
		}
		m.TurnIDs = append(m.TurnIDs, s.TurnID)
		m.CommandIDs = append(m.CommandIDs, s.MessageID)
		m.Settlements = append(m.Settlements, TransitionTiming{TurnID: s.TurnID, MessageID: s.MessageID, From: s.From, To: s.To, Milliseconds: *s.Milliseconds})
		if m.From.IsZero() || s.From.Before(m.From) {
			m.From = s.From
		}
		if s.To.After(m.To) {
			m.To = s.To
		}
	}
	d := raw.Dispatch
	if d.MessageID == "" || d.TurnID != m.TurnIDs[1] || !d.From.Equal(raw.Settlements[0].From) || d.To.Before(d.From) || d.Milliseconds == nil || abs(float64(d.To.Sub(d.From))/float64(time.Millisecond)-*d.Milliseconds) > .001 {
		return m, fmt.Errorf("measurement exact next dispatch timestamp mismatch")
	}
	if d.To.After(m.To) {
		m.To = d.To
	}
	m.Dispatch = TransitionTiming{TurnID: d.TurnID, MessageID: d.MessageID, From: d.From, To: d.To, Milliseconds: *d.Milliseconds}
	return m, m.validateTiming()
}
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func supplementEvidenceSample(before, after RawEvidence) (Sample, error) {
	if !before.Controlled || !after.Controlled || before.Project != after.Project || before.MeasurementID != after.MeasurementID || before.FixtureChecksum == "" || before.FixtureChecksum != after.FixtureChecksum || before.LegacyDigest == "" || before.LegacyDigest != after.LegacyDigest || !reflect.DeepEqual(before.Expected, after.Expected) || len(before.FragmentProofs) != len(after.FragmentProofs) {
		return Sample{}, fmt.Errorf("source immutable measurement evidence changed")
	}
	m, err := measurementFromRaw(before)
	if err != nil {
		return Sample{}, err
	}
	recovered, err := measurementFromRaw(after)
	if err != nil {
		return Sample{}, err
	}
	if m.SessionID != recovered.SessionID || m.RuntimeID != recovered.RuntimeID || m.Generation != recovered.Generation || !reflect.DeepEqual(m.TurnIDs, recovered.TurnIDs) || !reflect.DeepEqual(m.Settlements, recovered.Settlements) || !reflect.DeepEqual(m.Dispatch, recovered.Dispatch) || !m.From.Equal(recovered.From) || !m.To.Equal(recovered.To) {
		return Sample{}, fmt.Errorf("source execution identity changed")
	}
	for i, b := range before.FragmentProofs {
		a := after.FragmentProofs[i]
		b.VerifiedAt = a.VerifiedAt
		if !reflect.DeepEqual(b, a) {
			return Sample{}, fmt.Errorf("source exact ref/version/content changed")
		}
	}
	m.Sources = after.FragmentProofs
	if err = m.validate(before.Project); err != nil {
		return Sample{}, err
	}
	sample := Sample{ID: m.ID, Measurement: m, NextDispatchMS: *before.Dispatch.Milliseconds, SourceVerified: true, IdentityStable: true}
	for _, s := range before.Settlements {
		sample.SettlementsMS = append(sample.SettlementsMS, *s.Milliseconds)
	}
	return sample, nil
}

type matrixDriver struct {
	project, output, backend, session, runtime, fixture string
	generation                                          uint64
	pool                                                *pgxpool.Pool
	proofPool                                           *pgxpool.Pool
	objects                                             *artifactlocal.ObjectStore
	intervals                                           IntervalEvidence
	measurements                                        []MeasuredWindow
}

func openMatrixDriver(ctx context.Context, project, output, backend string) (*matrixDriver, error) {
	if _, err := readClaim(project, output); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(output, "acceptance.env"))
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			values[k] = v
		}
	}
	u, err := url.Parse(backend)
	if err != nil || u.Scheme != "http" || u.User != nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() != values["SWITCHBACK_BACKEND_PORT"] || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("matrix Backend does not match claim")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("OBSERVABILITY_DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	if !stringsSafeDatabase(cfg.ConnConfig.Database, project) || !net.ParseIP(cfg.ConnConfig.Host).IsLoopback() || fmt.Sprint(cfg.ConnConfig.Port) != values["OBSERVABILITY_POSTGRES_PORT"] {
		return nil, fmt.Errorf("matrix database does not match claim")
	}
	proofPool, err := openFragmentProofPool(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		proofPool.Close()
		return nil, err
	}
	objects, err := artifactlocal.NewObjectStore(filepath.Join(output, "data"))
	if err != nil {
		pool.Close()
		proofPool.Close()
		return nil, err
	}
	return &matrixDriver{project: project, output: output, backend: backend, pool: pool, proofPool: proofPool, objects: objects}, nil
}

// Only the production fragment proof loader receives this connection. Its
// exact metadata reads use FOR SHARE, which PostgreSQL rejects in a read-only
// transaction; the driver's direct metadata queries retain their read-only pool.
func openFragmentProofPool(ctx context.Context, validated *pgxpool.Config) (*pgxpool.Pool, error) {
	cfg := validated.Copy()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "off"
	cfg.MaxConns = 1
	cfg.MinConns = 0
	return pgxpool.NewWithConfig(ctx, cfg)
}

func (d *matrixDriver) request(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.backend+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Gateway request unavailable")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 202 {
		return fmt.Errorf("Gateway %s status %d", path, res.StatusCode)
	}
	return nil
}

func (d *matrixDriver) followUp(ctx context.Context, nonce string, ordinal int) (string, error) {
	var run, turn, runtime string
	var sr, rr, tr, generation uint64
	err := d.pool.QueryRow(ctx, `SELECT s.session_revision,r.worker_run_id,r.revision,t.agent_turn_id,t.revision,ri.runtime_instance_id,ri.generation FROM worker_sessions s JOIN runtime_instances ri ON ri.runtime_instance_id=s.active_runtime_instance_id JOIN agent_turns t ON t.worker_session_id=s.worker_session_id JOIN worker_runs r ON r.worker_run_id=t.worker_run_id WHERE s.worker_session_id=$1 AND s.active_worker_run_id IS NULL AND s.bound_task_id IS NULL AND t.status='completed' ORDER BY t.created_at DESC LIMIT 1`, d.session).Scan(&sr, &run, &rr, &turn, &tr, &runtime, &generation)
	if err != nil {
		return "", err
	}
	if runtime != d.runtime || generation != d.generation {
		return "", fmt.Errorf("target resident Runtime changed")
	}
	key := nonce + fmt.Sprint("-", ordinal)
	if err = d.request(ctx, "/api/v1/worker-sessions/"+d.session+"/follow-up", map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": key, "expected_session_revision": sr, "worker_run_id": run, "expected_run_revision": rr, "agent_turn_id": turn, "expected_turn_revision": tr, "content": "Controlled additional measurement switchback-obsv-late:" + nonce}); err != nil {
		return "", err
	}
	for {
		var created, status string
		var settled bool
		err = d.pool.QueryRow(ctx, `SELECT t.agent_turn_id,t.status,EXISTS(SELECT 1 FROM worker_turn_settlements s WHERE s.agent_turn_id=t.agent_turn_id AND s.phase='completed') FROM agent_turns t WHERE t.worker_session_id=$1 AND t.idempotency_key=$2`, d.session, key).Scan(&created, &status, &settled)
		if err == nil && status == "completed" && settled {
			return created, nil
		}
		if err == nil && (status == "failed" || status == "cancelled" || status == "timed_out") {
			return "", fmt.Errorf("controlled additional Turn %s", status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (d *matrixDriver) measure(ctx context.Context, nonce, state string) (RawEvidence, error) {
	raw, intervals, err := measureWithIntervals(ctx, d.project, state, dockerIntervalCollector(d.project), func() (RawEvidence, error) { return d.measureRaw(ctx, nonce) })
	if len(intervals.Subscriptions) > 0 {
		if saveErr := saveIntervals(d.project, d.output, intervals); saveErr != nil {
			return raw, saveErr
		}
		d.intervals.Subscriptions = append(d.intervals.Subscriptions, intervals.Subscriptions...)
	}
	if err != nil {
		return raw, err
	}
	m, err := measurementFromRaw(raw)
	if err == nil {
		d.measurements = append(d.measurements, MeasuredWindow{State: state, Measurement: m})
	}
	return raw, err
}

func (d *matrixDriver) validateCoverage() error {
	var err error
	d.intervals.Intervals, err = d.intervals.derive(d.project)
	if err != nil {
		return err
	}
	for _, w := range d.measurements {
		if err = w.Measurement.validateTiming(); err != nil {
			return err
		}
		if err = d.intervals.validateWindow(d.project, w.State, w.Measurement.From, w.Measurement.To); err != nil {
			return err
		}
	}
	return nil
}

func (d *matrixDriver) measureRaw(ctx context.Context, nonce string) (RawEvidence, error) {
	var result RawEvidence
	for i := 1; i <= 2; i++ {
		turn, err := d.followUp(ctx, nonce, i)
		if err != nil {
			return result, err
		}
		var ids []string
		_ = json.Unmarshal(result.Turns, &ids)
		ids = append(ids, turn)
		result.Turns, _ = json.Marshal(ids)
	}
	var turns []string
	if err := json.Unmarshal(result.Turns, &turns); err != nil {
		return result, err
	}
	expected, err := releaseExpected(ctx, d.pool, d.project, d.output, nonce, d.fixture, turns)
	if err != nil {
		return result, err
	}
	result, err = d.snapshot(ctx, nonce, turns, expected, false)
	if err != nil {
		return result, err
	}
	if err = writeEvidence(d.output, "sample-"+nonce+"-original.json", result); err != nil {
		return result, err
	}
	// Preserve actual failing timings too; the file is raw evidence, not PASS.
	if _, err = measurementFromRaw(result); err != nil {
		return result, err
	}
	return result, nil
}

func (d *matrixDriver) snapshot(ctx context.Context, nonce string, turns []string, expected []ExpectedModel, verified bool) (RawEvidence, error) {
	r := RawEvidence{Project: d.project, MeasurementID: nonce, Controlled: true, CapturedAt: time.Now().UTC(), Expected: expected}
	var err error
	r.FixtureChecksum, err = fixtureDigest()
	if err != nil {
		return r, err
	}
	var raw []byte
	err = d.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('agent_turn_id',s.agent_turn_id,'terminal_message_id',s.terminal_message_id,'terminal_receipt_at',m.created_at,'completed_at',s.completed_at,'terminal_receipt_to_settlement_ms',extract(epoch FROM(s.completed_at-m.created_at))*1000) ORDER BY array_position($1::text[],s.agent_turn_id)) FROM worker_turn_settlements s JOIN runtime_control_messages m ON m.runtime_instance_id=s.runtime_instance_id AND m.direction='event' AND m.message_id=s.terminal_message_id WHERE s.agent_turn_id=ANY($1)`, turns).Scan(&raw)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(raw, &r.Settlements); err != nil {
		return r, err
	}
	err = d.pool.QueryRow(ctx, `SELECT jsonb_build_object('agent_turn_id',c.envelope->>'agent_turn_id','command_message_id',c.message_id,'acknowledged_at',c.acknowledged_at,'terminal_receipt_at',m.created_at,'terminal_receipt_to_next_dispatch_ms',extract(epoch FROM(c.acknowledged_at-m.created_at))*1000) FROM worker_turn_settlements s JOIN runtime_control_messages m ON m.runtime_instance_id=s.runtime_instance_id AND m.direction='event' AND m.message_id=s.terminal_message_id CROSS JOIN runtime_control_messages c WHERE s.agent_turn_id=$1 AND c.runtime_instance_id=s.runtime_instance_id AND c.direction='command' AND c.envelope->>'message_type'='agent_turn.start' AND c.envelope->>'agent_turn_id'=$2 AND c.acknowledged_at IS NOT NULL`, turns[0], turns[1]).Scan(&raw)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(raw, &r.Dispatch); err != nil {
		return r, err
	}
	err = d.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('runtime_instance_id',runtime_instance_id,'worker_session_id',worker_session_id,'generation',generation)) FROM runtime_instances WHERE runtime_instance_id=$1 AND worker_session_id=$2 AND generation=$3`, d.runtime, d.session, d.generation).Scan(&raw)
	if err != nil {
		return r, err
	}
	r.Runtimes = json.RawMessage(raw)
	err = d.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(t) ORDER BY t.created_at) FROM agent_turns t WHERE agent_turn_id=ANY($1)`, turns).Scan(&raw)
	if err != nil {
		return r, err
	}
	r.Turns = json.RawMessage(raw)
	// v2 source bytes and jobs remain independently frozen. Hash the actual
	// verified bytes/metadata; an empty old job set is retained, never called v3.
	rows, err := d.pool.Query(ctx, `SELECT trace_stream_id,manifest_checksum FROM trace_manifests WHERE agent_turn_id=ANY($1) ORDER BY trace_stream_id`, turns)
	if err != nil {
		return r, err
	}
	var refs []artifactapp.ProjectionSourceRef
	for rows.Next() {
		var ref artifactapp.ProjectionSourceRef
		if err = rows.Scan(&ref.TraceStreamID, &ref.ManifestChecksum); err != nil {
			rows.Close()
			return r, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, err
	}
	// Do not silently accept an unbound manifest selector.
	if len(refs) != 2 {
		return r, fmt.Errorf("exact frozen v2 manifest refs missing")
	}
	var legacy []frozenLegacyEvidence
	for _, ref := range refs {
		source, e := readFrozenLegacy(ctx, artifactpostgres.NewProjectionJobStore(d.pool), d.objects, ref)
		if e != nil {
			return r, e
		}
		legacy = append(legacy, source)
	}
	r.LegacyDigest = evidenceDigest(legacy)
	var frozenRows []byte
	err = d.pool.QueryRow(ctx, `SELECT jsonb_build_object('manifests',(SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY m.trace_stream_id),'[]') FROM trace_manifests m WHERE m.agent_turn_id=ANY($1)),'settlement_checkpoints',(SELECT coalesce(jsonb_agg(jsonb_build_object('turn',s.agent_turn_id,'phase_state',s.phase_state,'phase_state_digest',s.phase_state_digest) ORDER BY s.agent_turn_id),'[]') FROM worker_turn_settlements s WHERE s.agent_turn_id=ANY($1)),'v2_jobs',(SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY j.trace_stream_id),'[]') FROM observability_projection_jobs j JOIN trace_manifests m USING(trace_stream_id) WHERE m.agent_turn_id=ANY($1) AND j.schema_version='langfuse.projection.v2'))`, turns).Scan(&frozenRows)
	if err != nil {
		return r, err
	}
	r.LegacyDigest = evidenceDigest(struct {
		Sources []frozenLegacyEvidence
		Frozen  json.RawMessage
	}{legacy, frozenRows})
	r.FragmentProofs, err = awaitSupplementProofs(ctx, d.pool, d.proofPool, d.objects, turns, expected, verified)
	if err == nil {
		err = awaitCompletedReaders(ctx, d.project, expected)
	}
	return r, err
}

func (d *matrixDriver) compose(ctx context.Context, action string, services []string) error {
	if action != "start" && action != "stop" {
		return fmt.Errorf("unsupported owned Compose action")
	}
	for _, service := range services {
		if _, err := exactServiceContainer(ctx, d.project, service); err != nil {
			return err
		}
	}
	args := []string{"compose", "--env-file", filepath.Join(d.output, "acceptance.env"), "-p", d.project, "-f", "deploy/local/compose.yaml", "-f", "test/acceptance/observability/compose.override.yaml", action}
	args = append(args, services...)
	started := time.Now().UTC()
	err := exec.CommandContext(ctx, "docker", args...).Run()
	_ = writeEvidence(d.output, fmt.Sprintf("action-%d.json", started.UnixNano()), map[string]any{"project": d.project, "action": action, "services": services, "from": started, "to": time.Now().UTC(), "succeeded": err == nil})
	if err != nil {
		return fmt.Errorf("owned Compose %s failed", action)
	}
	return nil
}

func (d *matrixDriver) waitServices(ctx context.Context, state string) error {
	for {
		probes, err := probeObservationServices(ctx, d.project)
		if err != nil {
			return err
		}
		ready := true
		for _, p := range probes {
			if p.State != state {
				ready = false
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("service state %s not reached", state)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (d *matrixDriver) recoverSample(ctx context.Context, original RawEvidence) (RawEvidence, error) {
	var turns []string
	for _, s := range original.Settlements {
		turns = append(turns, s.TurnID)
	}
	r, err := d.snapshot(ctx, original.MeasurementID, turns, original.Expected, true)
	if err != nil {
		return r, err
	}
	if _, err = supplementEvidenceSample(original, r); err != nil {
		return r, err
	}
	err = writeEvidence(d.output, "sample-"+r.MeasurementID+"-recovered.json", r)
	return r, err
}

func runMatrix(project, output, backend string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := openMatrixDriver(ctx, project, output, backend)
	if err != nil {
		return err
	}
	defer d.pool.Close()
	defer d.proofPool.Close()
	data, err := os.ReadFile(filepath.Join(output, "smoke-passed.json"))
	if err != nil {
		return fmt.Errorf("matrix requires successful fresh smoke recovery first")
	}
	var smoke RawEvidence
	if json.Unmarshal(data, &smoke) != nil {
		return fmt.Errorf("smoke decode failed")
	}
	sample, err := supplementEvidenceSample(smoke, smoke)
	if err != nil {
		return err
	}
	d.session, d.runtime, d.generation = sample.Measurement.SessionID, sample.Measurement.RuntimeID, sample.Measurement.Generation
	fixture, err := exactServiceContainer(ctx, project, "observation-fake-model")
	if err != nil {
		return err
	}
	d.fixture = fixture.ID
	if _, err = os.Stat(filepath.Join(output, "matrix-started.json")); err == nil {
		return fmt.Errorf("matrix already started; preserve failed evidence and use a fresh claim")
	}
	if err = writeEvidence(output, "matrix-started.json", map[string]any{"project": project, "session": d.session, "runtime": d.runtime, "generation": d.generation, "at": time.Now().UTC()}); err != nil {
		return err
	}
	recipe := ReportRecipe{}
	physical := PhysicalEvidence{Project: project, Cold: sample.Measurement}
	for _, item := range []struct {
		name  string
		value any
	}{{"cold-before.json", &physical.ColdBefore}, {"cold-after.json", &physical.ColdAfter}} {
		raw, e := os.ReadFile(filepath.Join(output, item.name))
		if e != nil {
			return e
		}
		if e = json.Unmarshal(raw, item.value); e != nil {
			return e
		}
	}
	if err = verifyColdStart(physical.ColdBefore, physical.ColdAfter); err != nil {
		return err
	}
	if err = loadIntervals(output, &physical); err != nil {
		return err
	}
	if err = physical.validateCold(); err != nil {
		return err
	}
	commit, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	recipe.SourceCommit = strings.TrimSpace(string(commit))
	recipe.WorkerImage = fixture.ImageID
	backendContainer, err := exactServiceContainer(ctx, project, "switchback-backend")
	if err != nil {
		return err
	}
	recipe.BackendImage = backendContainer.ImageID
	for i := 0; i < 20; i++ {
		var pair struct {
			Healthy EvidencePaths `json:"healthy"`
			Outage  EvidencePaths `json:"outage"`
		}
		for _, state := range []string{"healthy", "unavailable"} {
			bounded, done := context.WithTimeout(ctx, 5*time.Minute)
			action := "start"
			if state == "unavailable" {
				action = "stop"
			}
			err = d.compose(bounded, action, observationServices)
			if err == nil {
				err = d.waitServices(bounded, state)
			}
			nonce := fmt.Sprintf("%sm%02d%s", strings.TrimPrefix(project, "switchback-obsv-"), i, map[string]string{"healthy": "h", "unavailable": "o"}[state])
			var original RawEvidence
			if err == nil {
				original, err = d.measure(bounded, nonce, state)
			}
			if err == nil && state == "unavailable" {
				err = d.compose(bounded, "start", observationServices)
				if err == nil {
					err = d.waitServices(bounded, "healthy")
				}
			}
			if err == nil {
				_, err = d.recoverSample(bounded, original)
			}
			done()
			if err != nil {
				return fmt.Errorf("matrix pair %d %s failed: %w", i, state, err)
			}
			paths := EvidencePaths{Original: filepath.Join(output, "sample-"+nonce+"-original.json"), Recovered: filepath.Join(output, "sample-"+nonce+"-recovered.json")}
			if state == "healthy" {
				pair.Healthy = paths
			} else {
				pair.Outage = paths
			}
		}
		recipe.Pairs = append(recipe.Pairs, pair)
	}
	// Fault requests arise from natural production projection/retry. The
	// driver never calls ingestion itself to manufacture a connected event.
	for _, kind := range []string{"ingestion", "query"} {
		bounded, done := context.WithTimeout(ctx, 5*time.Minute)
		_, err = fixtureControl(bounded, project, d.fixture, "fault", kind, "")
		if err != nil {
			done()
			return err
		}
		nonce := strings.TrimPrefix(project, "switchback-obsv-") + "fault" + kind
		// A separate real source supplies the outstanding natural projection
		// request before the latency window begins. It is never counted as a pair.
		warmup, e := d.measure(bounded, nonce+"warmup", "healthy")
		if e != nil {
			done()
			return e
		}
		if e := d.awaitFaultConnection(bounded, kind); e != nil {
			done()
			return e
		}
		original, e := d.measure(bounded, nonce, "healthy")
		if e != nil {
			done()
			return e
		}
		// Require an actual connected then cancelled request before recovery.
		fault, e := d.waitFault(bounded, kind, original)
		if e != nil {
			done()
			return e
		}
		offLedger, e := fixtureControl(bounded, project, d.fixture, "fault", "off", "")
		if e != nil {
			done()
			return e
		}
		fault.RecoveredAt, e = faultRecoveryTime(offLedger, fault)
		if e != nil {
			done()
			return e
		}
		recovered, e := d.recoverSample(bounded, original)
		if e != nil {
			done()
			return e
		}
		m, e := measurementFromRaw(recovered)
		if e != nil {
			done()
			return e
		}
		fault.Measurement = m
		physical.Faults = append(physical.Faults, fault)
		warmupRecovered, e := d.recoverSample(bounded, warmup)
		if e != nil {
			done()
			return e
		}
		warmupMeasurement, e := measurementFromRaw(warmupRecovered)
		if e != nil {
			done()
			return e
		}
		physical.Additional = append(physical.Additional, MeasuredWindow{State: "healthy", Measurement: warmupMeasurement})
		done()
	}
	if err = d.validateCoverage(); err != nil {
		return err
	}
	if err = d.cleanupRecovery(ctx, &physical); err != nil {
		return err
	}
	if err = d.validateCoverage(); err != nil {
		return err
	}
	if err = loadIntervals(output, &physical); err != nil {
		return err
	}
	if err = physical.validateBeforeInfrastructureCleanup(); err != nil {
		return err
	}
	if err = d.cleanupOwned(ctx, &physical); err != nil {
		return err
	}
	if err = physical.validate(); err != nil {
		return err
	}
	if err = writeEvidence(output, "physical-evidence.json", physical); err != nil {
		return err
	}
	recipe.Physical = filepath.Join(output, "physical-evidence.json")
	return writeEvidence(output, "matrix-recipe.json", recipe)
}

func loadIntervals(output string, physical *PhysicalEvidence) error {
	physical.Intervals = IntervalEvidence{}
	files, err := filepath.Glob(filepath.Join(output, "intervals-*.json"))
	if err != nil {
		return err
	}
	for _, file := range files {
		data, e := os.ReadFile(file)
		if e != nil {
			return e
		}
		var intervals IntervalEvidence
		if json.Unmarshal(data, &intervals) != nil {
			return fmt.Errorf("interval file invalid")
		}
		physical.Intervals.Subscriptions = append(physical.Intervals.Subscriptions, intervals.Subscriptions...)
	}
	physical.Intervals.Intervals, err = physical.Intervals.derive(physical.Project)
	return err
}

func faultRecoveryTime(raw []byte, f FaultProof) (time.Time, error) {
	var records []struct {
		Instance  string    `json:"instance"`
		ID        uint64    `json:"request_id"`
		Kind      string    `json:"kind"`
		Connected time.Time `json:"connected_at"`
		Cancelled time.Time `json:"cancelled_at"`
		Recovered time.Time `json:"recovered_at"`
	}
	if json.Unmarshal(raw, &records) != nil {
		return time.Time{}, fmt.Errorf("fault recovery ledger invalid")
	}
	for _, r := range records {
		if r.Instance == f.Instance && fmt.Sprint(r.ID) == f.RequestID && r.Kind == f.Kind && r.Connected.Equal(f.ConnectedAt) && r.Cancelled.Equal(f.CancelledAt) && r.Recovered.After(r.Cancelled) {
			return r.Recovered, nil
		}
	}
	return time.Time{}, fmt.Errorf("fault exact recovery event missing")
}

func (d *matrixDriver) awaitFaultConnection(ctx context.Context, kind string) error {
	for {
		raw, err := fixtureControl(ctx, d.project, d.fixture, "fault-status", "", "")
		if err != nil {
			return err
		}
		var records []struct {
			Kind      string    `json:"kind"`
			Connected time.Time `json:"connected_at"`
			Cancelled time.Time `json:"cancelled_at"`
		}
		if json.Unmarshal(raw, &records) != nil {
			return fmt.Errorf("fault ledger invalid")
		}
		for _, r := range records {
			if r.Kind == kind && !r.Connected.IsZero() && r.Cancelled.IsZero() {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("natural warmup projection never connected")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (d *matrixDriver) waitFault(ctx context.Context, kind string, original RawEvidence) (FaultProof, error) {
	m, err := measurementFromRaw(original)
	if err != nil {
		return FaultProof{}, err
	}
	for {
		raw, err := fixtureControl(ctx, d.project, d.fixture, "fault-status", "", "")
		if err != nil {
			return FaultProof{}, err
		}
		var records []struct {
			Instance  string    `json:"instance"`
			ID        uint64    `json:"request_id"`
			Kind      string    `json:"kind"`
			Connected time.Time `json:"connected_at"`
			Cancelled time.Time `json:"cancelled_at"`
		}
		if json.Unmarshal(raw, &records) != nil {
			return FaultProof{}, fmt.Errorf("fault ledger decode failed")
		}
		for _, r := range records {
			if r.Kind == kind && !r.Connected.After(m.From) && !r.Cancelled.Before(m.To) {
				return FaultProof{Project: d.project, Kind: kind, ProxyContainerID: d.fixture, Instance: r.Instance, RequestID: fmt.Sprint(r.ID), ConnectedAt: r.Connected, CancelledAt: r.Cancelled}, nil
			}
		}
		select {
		case <-ctx.Done():
			return FaultProof{}, fmt.Errorf("no exact connected-unanswered request covered measurement")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (d *matrixDriver) cleanupRecovery(parent context.Context, p *PhysicalEvidence) (resultErr error) {
	p.CleanupSessionID = d.session
	p.CleanupRuntimeID = d.runtime
	p.CleanupGeneration = d.generation
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	if _, err := fixtureControl(ctx, d.project, d.fixture, "fault", "ingestion", ""); err != nil {
		return err
	}
	nonce := strings.TrimPrefix(d.project, "switchback-obsv-") + "cleanup"
	original, err := d.measure(ctx, nonce, "healthy")
	if err != nil {
		return err
	}
	// All completed windows, including this recovery source, must already
	// have real closing probes before the original Worker can be stopped.
	if err = d.validateCoverage(); err != nil {
		return err
	}
	// Retain the separate restart/cleanup lifecycle events as well as the
	// latency windows. This subscription is never a latency/p95 sample.
	collectionCtx, stopCollection := context.WithCancel(ctx)
	c := startIntervalCollection(collectionCtx, d.project, "healthy", dockerIntervalCollector(d.project))
	defer func() {
		e, collectErr := c.finishAndWait(ctx)
		if collectErr != nil {
			stopCollection()
			<-c.done
			e = c.evidence
		}
		stopCollection()
		var saveErr error
		if len(e.Subscriptions) > 0 {
			saveErr = saveIntervals(d.project, d.output, e)
		}
		resultErr = errors.Join(resultErr, collectErr, saveErr)
	}()
	if err = c.waitReady(ctx); err != nil {
		return err
	}
	for _, s := range original.FragmentProofs {
		if !s.VerifiedAt.IsZero() {
			return fmt.Errorf("cleanup source already passed before registry removal")
		}
	}
	registry := filepath.Join(d.output, "data", "sessions", d.session, "runtimes", fmt.Sprintf("%s-g%d", d.runtime, d.generation), "control-plane", "observation-tokens.json")
	if info, err := os.Lstat(registry); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("original exact runtime registry missing before cleanup")
	}
	p.RegistryPresentAt = time.Now().UTC()
	var workerID string
	var normal bool
	err = d.pool.QueryRow(ctx, `SELECT ri.container_id,NOT EXISTS(SELECT 1 FROM worker_turn_settlements st WHERE st.worker_session_id=ri.worker_session_id AND (st.phase<>'completed' OR st.source_kind='backend_deadline')) AND NOT EXISTS(SELECT 1 FROM runtime_recovery_incidents x WHERE x.worker_session_id=ri.worker_session_id AND x.phase NOT IN ('recovered','exhausted')) FROM runtime_instances ri WHERE ri.runtime_instance_id=$1 AND ri.worker_session_id=$2 AND ri.generation=$3`, d.runtime, d.session, d.generation).Scan(&workerID, &normal)
	if err != nil {
		return err
	}
	if !normal {
		return fmt.Errorf("cleanup route lacks ordinary completed settlement")
	}
	const labels = `{"id":{{json .Id}},"role":{{json (index .Config.Labels "com.switchback.managed")}},"session":{{json (index .Config.Labels "com.switchback.worker-session-id")}},"runtime":{{json (index .Config.Labels "com.switchback.runtime-instance-id")}},"generation":{{json (index .Config.Labels "com.switchback.runtime-generation")}},"running":{{json .State.Running}}}`
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", labels, workerID).Output()
	if err != nil {
		return fmt.Errorf("owned sibling inspect failed")
	}
	var identity struct {
		ID, Role, Session, Runtime, Generation string
		Running                                bool
	}
	if json.Unmarshal(raw, &identity) != nil || identity.ID != workerID || identity.Role != "worker-runtime" || identity.Session != d.session || identity.Runtime != d.runtime || identity.Generation != fmt.Sprint(d.generation) || !identity.Running {
		return fmt.Errorf("owned sibling runtime labels mismatch")
	}
	p.Resources = append(p.Resources, OwnedResource{Project: d.project, Kind: "container", ID: workerID, Ownership: "runtime_db_and_labels", SessionID: d.session, RuntimeID: d.runtime, Generation: d.generation, ObservedAt: time.Now().UTC()})
	siblings, err := d.runtimeSecretInventory(ctx)
	if err != nil {
		return err
	}
	p.Resources = append(p.Resources, siblings...)
	if err = writeEvidence(d.output, "runtime-resource-inventory.json", p.Resources); err != nil {
		return err
	}
	before, err := exactServiceContainer(ctx, d.project, "switchback-backend")
	if err != nil {
		return err
	}
	p.Restart = RestartProof{Project: d.project, Before: before, SourcesBefore: original.FragmentProofs}
	if err = exec.CommandContext(ctx, "docker", "stop", before.ID).Run(); err != nil {
		return fmt.Errorf("owned Backend stop failed")
	}
	stopped, err := exactServiceContainer(ctx, d.project, "switchback-backend")
	if err != nil || stopped.ID != before.ID || stopped.Running {
		return fmt.Errorf("owned Backend did not stop")
	}
	p.Restart.StoppedAt = time.Now().UTC()
	if err = exec.CommandContext(ctx, "docker", "stop", workerID).Run(); err != nil {
		return fmt.Errorf("owned Worker stop failed")
	}
	raw, err = exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", workerID).Output()
	if err != nil || strings.TrimSpace(string(raw)) != "false" {
		return fmt.Errorf("owned Worker stop not proven")
	}
	if err = exec.CommandContext(ctx, "docker", "start", before.ID).Run(); err != nil {
		return fmt.Errorf("owned Backend restart failed")
	}
	for {
		var confirmed bool
		err = d.pool.QueryRow(ctx, `SELECT ri.cleanup_confirmed AND ri.status='terminated' AND s.active_runtime_instance_id IS NULL,ch.fence_revision FROM runtime_instances ri JOIN worker_sessions s USING(worker_session_id) JOIN runtime_control_channels ch ON ch.runtime_instance_id=ri.runtime_instance_id AND ch.runtime_generation=ri.generation WHERE ri.runtime_instance_id=$1 AND ch.status='fenced' AND ch.fence_owner_fence=ch.owner_fence AND ch.fence_revision>0`, d.runtime).Scan(&confirmed, &p.CleanupFenceRevision)
		if err == nil && confirmed {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("production cleanup did not confirm exact runtime")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if _, err = os.Lstat(registry); !os.IsNotExist(err) {
		return fmt.Errorf("original registry absence unproven")
	}
	p.RegistryAbsentAt = time.Now().UTC()
	p.CleanupConfirmedAt = time.Now().UTC()
	left, e := exec.CommandContext(ctx, "docker", "ps", "--all", "--no-trunc", "--filter", "id="+workerID, "--format", "{{.ID}}").Output()
	if e != nil || len(strings.TrimSpace(string(left))) != 0 {
		return fmt.Errorf("production cleanup Worker absence unproven")
	}
	for i := range p.Resources {
		if p.Resources[i].Ownership == "runtime_db_and_labels" {
			p.Resources[i].AbsentAt = time.Now().UTC()
		}
	}
	remaining, err := d.runtimeSecretInventory(ctx)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("production cleanup secret volume/populator residue")
	}
	after, err := exactServiceContainer(ctx, d.project, "switchback-backend")
	if err != nil {
		return err
	}
	p.Restart.After = after
	health, err := (&http.Client{Timeout: 3 * time.Second}).Get(d.backend + "/healthz")
	if err != nil {
		return fmt.Errorf("Backend restart health unavailable")
	}
	health.Body.Close()
	if health.StatusCode != 200 {
		return fmt.Errorf("Backend restart not ready")
	}
	p.Restart.HealthyAt = time.Now().UTC()
	if _, err = fixtureControl(ctx, d.project, d.fixture, "fault", "off", ""); err != nil {
		return err
	}
	recovered, err := d.recoverSample(ctx, original)
	if err != nil {
		return err
	}
	m, err := measurementFromRaw(recovered)
	if err != nil {
		return err
	}
	p.Additional = append(p.Additional, MeasuredWindow{State: "healthy", Measurement: m})
	p.Restart.SourcesAfter = recovered.FragmentProofs
	p.RecoveredSources = recovered.FragmentProofs
	return writeEvidence(d.output, "runtime-cleanup.json", map[string]any{"project": d.project, "session": d.session, "runtime": d.runtime, "generation": d.generation, "registry_relative_path": strings.TrimPrefix(registry, d.output+"/"), "before_sources": original.FragmentProofs, "after_sources": recovered.FragmentProofs, "fence_revision": p.CleanupFenceRevision, "registry_absent_at": p.RegistryAbsentAt})
}

func (d *matrixDriver) runtimeSecretInventory(ctx context.Context) ([]OwnedResource, error) {
	var resources []OwnedResource
	filters := []string{"--filter", "label=com.switchback.runtime-instance-id=" + d.runtime, "--filter", "label=com.switchback.worker-session-id=" + d.session, "--filter", "label=com.switchback.runtime-generation=" + fmt.Sprint(d.generation)}
	for _, kind := range []string{"secret_volume", "secret_populator"} {
		args := []string{"volume", "ls", "--filter", "label=com.switchback.managed=runtime-secret-volume", "--format", "{{.Name}}"}
		if kind == "secret_populator" {
			args = []string{"ps", "--all", "--no-trunc", "--filter", "label=com.switchback.managed=runtime-secret-populator", "--format", "{{.ID}}"}
		}
		args = append(args, filters...)
		raw, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			return nil, fmt.Errorf("exact runtime secret inventory unavailable")
		}
		for _, id := range strings.Fields(string(raw)) {
			resources = append(resources, OwnedResource{Project: d.project, Kind: kind, ID: id, Ownership: "runtime_db_and_labels", SessionID: d.session, RuntimeID: d.runtime, Generation: d.generation, ObservedAt: time.Now().UTC()})
		}
	}
	return resources, nil
}

func (d *matrixDriver) cleanupOwned(ctx context.Context, p *PhysicalEvidence) error {
	// Snapshot each exact Compose-owned resource before removal. Worker
	// siblings have already been removed by the production cleanup path above.
	ids, err := exec.CommandContext(ctx, "docker", "ps", "--all", "--no-trunc", "--filter", "label=com.docker.compose.project="+d.project, "--format", "{{.ID}}").Output()
	if err != nil {
		return fmt.Errorf("owned container inventory failed")
	}
	for _, id := range strings.Fields(string(ids)) {
		raw, e := exec.CommandContext(ctx, "docker", "inspect", "--format", inspectTemplate, id).Output()
		if e != nil {
			return e
		}
		var c CapturedContainer
		if json.Unmarshal(raw, &c) != nil || c.ID != id || c.Project != d.project {
			return fmt.Errorf("cleanup container claim mismatch")
		}
		r := OwnedResource{Project: d.project, Kind: "container", ID: id, Ownership: "compose_label", ObservedAt: time.Now().UTC()}
		if err = exec.CommandContext(ctx, "docker", "rm", "--force", id).Run(); err != nil {
			return fmt.Errorf("owned container removal failed")
		}
		left, e := exec.CommandContext(ctx, "docker", "ps", "--all", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}").Output()
		if e != nil || len(strings.TrimSpace(string(left))) != 0 {
			return fmt.Errorf("owned container absence not proven")
		}
		r.AbsentAt = time.Now().UTC()
		p.Resources = append(p.Resources, r)
	}
	networks, err := exec.CommandContext(ctx, "docker", "network", "ls", "--no-trunc", "--filter", "label=com.docker.compose.project="+d.project, "--format", "{{.ID}}").Output()
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(networks)) {
		label, e := exec.CommandContext(ctx, "docker", "network", "inspect", "--format", `{{index .Labels "com.docker.compose.project"}}`, id).Output()
		if e != nil || strings.TrimSpace(string(label)) != d.project {
			return fmt.Errorf("cleanup network claim mismatch")
		}
		r := OwnedResource{Project: d.project, Kind: "network", ID: id, Ownership: "compose_label", ObservedAt: time.Now().UTC()}
		if e = exec.CommandContext(ctx, "docker", "network", "rm", id).Run(); e != nil {
			return fmt.Errorf("owned network removal failed")
		}
		left, e := exec.CommandContext(ctx, "docker", "network", "ls", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}").Output()
		if e != nil || len(strings.TrimSpace(string(left))) != 0 {
			return fmt.Errorf("owned network absence not proven")
		}
		r.AbsentAt = time.Now().UTC()
		p.Resources = append(p.Resources, r)
	}
	volumes, err := exec.CommandContext(ctx, "docker", "volume", "ls", "--filter", "label=com.docker.compose.project="+d.project, "--format", "{{.Name}}").Output()
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(volumes)) {
		p.Resources = append(p.Resources, OwnedResource{Project: d.project, Kind: "data_volume", ID: id, Ownership: "compose_label", ObservedAt: time.Now().UTC(), Retained: true})
	}
	return writeEvidence(d.output, "resource-cleanup.json", p.Resources)
}
