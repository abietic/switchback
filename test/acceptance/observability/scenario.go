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
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runScenario(mode, project, output, backend string) error {
	nonce := strings.TrimPrefix(project, "switchback-obsv-")
	var expected []ExpectedModel
	fixtureBefore, err := fixtureDigest()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(output, "claim.json"))
	if err != nil {
		return err
	}
	var claimed map[string]string
	if err = json.Unmarshal(data, &claimed); err != nil {
		return err
	}
	if !projectPattern.MatchString(project) || claimed["project"] != project || claimed["output"] != output {
		return errors.New("claim mismatch")
	}
	u, err := url.Parse(backend)
	if err != nil || u.Scheme != "http" || u.User != nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return errors.New("Backend must use explicit loopback HTTP address")
	}
	envData, err := os.ReadFile(filepath.Join(output, "acceptance.env"))
	if err != nil {
		return err
	}
	expectedPort := ""
	expectedPGPort := ""
	for _, line := range strings.Split(string(envData), "\n") {
		if strings.HasPrefix(line, "SWITCHBACK_BACKEND_PORT=") {
			expectedPort = strings.TrimPrefix(line, "SWITCHBACK_BACKEND_PORT=")
		}
		if strings.HasPrefix(line, "OBSERVABILITY_POSTGRES_PORT=") {
			expectedPGPort = strings.TrimPrefix(line, "OBSERVABILITY_POSTGRES_PORT=")
		}
	}
	if expectedPort == "" || u.Port() != expectedPort || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Backend URL does not match claimed port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(os.Getenv("OBSERVABILITY_DATABASE_URL"))
	if err != nil {
		return err
	}
	if !stringsSafeDatabase(cfg.ConnConfig.Database, project) {
		return errors.New("database must be named from exact project with hyphens replaced by underscores")
	}
	if !net.ParseIP(cfg.ConnConfig.Host).IsLoopback() {
		return errors.New("database endpoint must be explicit loopback IP")
	}
	if fmt.Sprint(cfg.ConnConfig.Port) != expectedPGPort {
		return errors.New("database endpoint must match claimed PostgreSQL port")
	}
	proofPool, err := openFragmentProofPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer proofPool.Close()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	// SQL is exclusively read-only; all lifecycle commands go through actual Gateway.
	request := func(path string, body any) error {
		encoded, _ := json.Marshal(body)
		req, e := http.NewRequestWithContext(ctx, "POST", backend+path, bytes.NewReader(encoded))
		if e != nil {
			return e
		}
		req.Header.Set("Content-Type", "application/json")
		res, e := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if e != nil {
			return e
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if res.StatusCode != http.StatusAccepted {
			return fmt.Errorf("Gateway %s: %d %s", path, res.StatusCode, raw)
		}
		return nil
	}
	if mode == "smoke" {
		var count int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM tasks`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("smoke requires new empty core database")
		}
		body := map[string]any{"schema_version": "gateway.direct_task_input.v1", "idempotency_key": project, "project_id": "project-local", "workspace_id": "workspace-local", "workspace_revision_id": "workspace-revision-local-v1", "title": "Controlled observation smoke", "goal": "Repair the deterministic counter fixture switchback-obsv-late:" + nonce, "target": map[string]string{"kind": "workspace", "ref": "workspace-revision-local-v1"}, "constraints": []string{"Controlled ACP model Tool fixture only"}, "acceptance_criteria": []string{"Two deterministic Turns settle"}, "agent_profile_id": "deepseek-codex-openai-v1", "acceptance_mode": "manual", "user_visible_types": []string{"report"}}
		if err = request("/api/v1/tasks", body); err != nil {
			return err
		}
		for {
			if failureErr := recordFailedTurn(ctx, pool, output, project); failureErr != nil {
				return failureErr
			}
			var task, status string
			var rev uint64
			err = pool.QueryRow(ctx, `SELECT task_id,status,revision FROM tasks LIMIT 1`).Scan(&task, &status, &rev)
			var settled int
			if err == nil {
				err = pool.QueryRow(ctx, `SELECT count(*) FROM worker_turn_settlements WHERE completed_at IS NOT NULL`).Scan(&settled)
			}
			if err == nil && status == "delivered" && settled == 1 {
				if err = request("/api/v1/tasks/"+task+"/accept", map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": project + "-accept", "expected_task_revision": rev}); err != nil {
					return err
				}
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("Task settlement/accept: %w", ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
		for {
			var session, run, turn, status string
			var sr, rr, tr uint64
			err = pool.QueryRow(ctx, `SELECT s.worker_session_id,r.worker_run_id,t.agent_turn_id,t.status,s.session_revision,r.revision,t.revision FROM agent_turns t JOIN worker_runs r USING(worker_run_id) JOIN worker_sessions s ON s.worker_session_id=t.worker_session_id ORDER BY t.created_at LIMIT 1`).Scan(&session, &run, &turn, &status, &sr, &rr, &tr)
			if err == nil && status == "completed" {
				err = request("/api/v1/worker-sessions/"+session+"/follow-up", map[string]any{"schema_version": "gateway.worker_command.v1", "idempotency_key": project + "-second", "expected_session_revision": sr, "worker_run_id": run, "expected_run_revision": rr, "agent_turn_id": turn, "expected_turn_revision": tr, "content": "Add deterministic zero delta test switchback-obsv-late:" + nonce})
				if err == nil {
					break
				}
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("first Turn/queue: %w", ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		for {
			if failureErr := recordFailedTurn(ctx, pool, output, project); failureErr != nil {
				return failureErr
			}
			var count int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM worker_turn_settlements WHERE completed_at IS NOT NULL`).Scan(&count)
			if err != nil {
				return err
			}
			if count == 2 {
				break
			}
			if count > 2 {
				return errors.New("duplicate execution")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	var successful, sessions, generations int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='completed'),count(DISTINCT worker_session_id) FROM agent_turns`).Scan(&successful, &sessions); err != nil {
		return err
	}
	if err = pool.QueryRow(ctx, `SELECT CASE WHEN count(*)=1 AND min(generation)=1 THEN 1 ELSE 0 END FROM runtime_instances`).Scan(&generations); err != nil {
		return err
	}
	if successful != 2 || sessions != 1 || generations != 1 {
		return errors.New("expected exactly two successful Turns in one original Runtime/Session")
	}
	var turnIDs []string
	if err = pool.QueryRow(ctx, `SELECT array_agg(agent_turn_id ORDER BY created_at) FROM agent_turns`).Scan(&turnIDs); err != nil {
		return err
	}
	if mode == "smoke" {
		container, e := exactServiceContainer(ctx, project, "observation-fake-model")
		if e != nil {
			return e
		}
		expected, err = releaseExpected(ctx, pool, project, output, nonce, container.ID, turnIDs)
		if err != nil {
			return err
		}
	} else {
		raw, e := os.ReadFile(filepath.Join(output, "released-"+nonce+".json"))
		if e != nil {
			return e
		}
		if err = json.Unmarshal(raw, &expected); err != nil {
			return err
		}
	}
	evidence := map[string]any{"project": project, "measurement_id": project + "-smoke", "controlled_acp_fixture": true, "scope": "Langfuse subset; no real Codex/G5/full G4 claim", "captured_at": time.Now().UTC()}
	fixtureAfter, err := fixtureDigest()
	if err != nil {
		return err
	}
	if fixtureBefore != fixtureAfter {
		return errors.New("source fixture changed during execution")
	}
	evidence["fixture_checksum"] = fixtureAfter
	var latencies []byte
	if err = pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('agent_turn_id',s.agent_turn_id,'terminal_message_id',s.terminal_message_id,'terminal_receipt_at',m.created_at,'completed_at',s.completed_at,'terminal_receipt_to_settlement_ms',extract(epoch FROM(s.completed_at-m.created_at))*1000) ORDER BY m.created_at),'[]') FROM worker_turn_settlements s JOIN runtime_control_messages m ON m.runtime_instance_id=s.runtime_instance_id AND m.direction='event' AND m.message_id=s.terminal_message_id`).Scan(&latencies); err != nil {
		return err
	}
	evidence["measured_settlements"] = json.RawMessage(latencies)
	var dispatch []byte
	if err = pool.QueryRow(ctx, `WITH first_terminal AS (SELECT m.created_at FROM worker_turn_settlements s JOIN runtime_control_messages m ON m.runtime_instance_id=s.runtime_instance_id AND m.direction='event' AND m.message_id=s.terminal_message_id ORDER BY m.created_at LIMIT 1), second_turn AS (SELECT agent_turn_id FROM agent_turns ORDER BY created_at OFFSET 1 LIMIT 1) SELECT jsonb_build_object('agent_turn_id',c.envelope->>'agent_turn_id','command_message_id',c.message_id,'acknowledged_at',c.acknowledged_at,'terminal_receipt_at',f.created_at,'terminal_receipt_to_next_dispatch_ms',extract(epoch FROM(c.acknowledged_at-f.created_at))*1000) FROM runtime_control_messages c CROSS JOIN first_terminal f JOIN second_turn t ON t.agent_turn_id=c.envelope->>'agent_turn_id' WHERE c.direction='command' AND c.envelope->>'message_type'='agent_turn.start' AND c.acknowledged_at IS NOT NULL`).Scan(&dispatch); err != nil {
		return fmt.Errorf("exact second Turn dispatch ACK: %w", err)
	}
	evidence["measured_next_dispatch"] = json.RawMessage(dispatch)
	evidence["next_dispatch_timing_semantics"] = "Backend-observed exact second start-command ACK minus first terminal journal receipt; conservative upper bound includes driver polling, Gateway accept and standalone follow-up"
	objects, err := artifactlocal.NewObjectStore(filepath.Join(output, "data"))
	if err != nil {
		return err
	}
	rows, err := pool.Query(ctx, `SELECT trace_stream_id,manifest_checksum FROM trace_manifests ORDER BY visible_at`)
	if err != nil {
		return err
	}
	var refs []artifactapp.ProjectionSourceRef
	for rows.Next() {
		var ref artifactapp.ProjectionSourceRef
		if err = rows.Scan(&ref.TraceStreamID, &ref.ManifestChecksum); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(refs) != 2 {
		return errors.New("expected exactly two finalized observation sources")
	}
	var sources []frozenLegacyEvidence
	for _, ref := range refs {
		source, e := readFrozenLegacy(ctx, artifactpostgres.NewProjectionJobStore(pool), objects, ref)
		if e != nil {
			return fmt.Errorf("verify original source: %w", e)
		}
		sources = append(sources, source)
	}
	evidence["verified_original_sources"] = sources
	fragmentProofs, err := awaitSupplementProofs(ctx, pool, proofPool, objects, turnIDs, expected, mode == "snapshot" || mode == "correlation-snapshot")
	if err != nil {
		return err
	}
	if err = awaitCompletedReaders(ctx, project, expected); err != nil {
		return err
	}
	evidence["fragment_proofs"] = fragmentProofs
	evidence["expected_models"] = expected
	evidence["v2_bytes_digest"] = evidenceDigest(sources)
	for name, query := range map[string]string{
		"turns":              `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.agent_turn_id),'[]') FROM agent_turns t`,
		"settlements":        `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.settlement_id),'[]') FROM worker_turn_settlements t`,
		"jobs":               `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.trace_stream_id),'[]') FROM observability_projection_jobs t`,
		"runtimes":           `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.runtime_instance_id),'[]') FROM runtime_instances t`,
		"commands":           `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.command_id),'[]') FROM worker_user_commands t`,
		"journal_timestamps": `SELECT coalesce(jsonb_agg(jsonb_build_object('runtime_instance_id',runtime_instance_id,'direction',direction,'message_id',message_id,'sequence',sequence,'created_at',created_at,'applied_at',applied_at,'effect_status',effect_status)),'[]') FROM runtime_control_messages`,
	} {
		var raw []byte
		if err = pool.QueryRow(ctx, query).Scan(&raw); err != nil {
			return err
		}
		evidence[name] = json.RawMessage(raw)
	}
	evidence["v2_bytes_digest"] = evidenceDigest(struct {
		Sources           []frozenLegacyEvidence
		Jobs, Settlements any
	}{sources, evidence["jobs"], evidence["settlements"]})
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	if mode == "smoke" {
		if err = writeEvidence(output, "smoke-pending.json", evidence); err != nil {
			return err
		}
	}
	if mode == "snapshot" || mode == "correlation-snapshot" {
		if err = FinalizeScenarioSnapshot(mode, project, output, encoded); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(filepath.Join(output, fmt.Sprintf("%s-%d.json", mode, time.Now().UnixNano())), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(encoded)
	if err == nil {
		fmt.Println(file.Name())
	}
	return err
}

// FinalizeScenarioSnapshot shares the exact-source gate while keeping the
// existing cold-outage acceptance independent from healthy G3 correlation.
func FinalizeScenarioSnapshot(mode, project, output string, encoded []byte) error {
	if mode != "snapshot" && mode != "correlation-snapshot" {
		return errors.New("unsupported snapshot evidence mode")
	}
	beforeBytes, err := os.ReadFile(filepath.Join(output, "smoke-pending.json"))
	if err != nil {
		return err
	}
	var before, after RawEvidence
	if json.Unmarshal(beforeBytes, &before) != nil || json.Unmarshal(encoded, &after) != nil {
		return errors.New("smoke evidence decode failed")
	}
	if before.Project != project || after.Project != project {
		return errors.New("snapshot project mismatch")
	}
	sample, err := supplementEvidenceSample(before, after)
	if err != nil {
		return err
	}
	if mode == "correlation-snapshot" {
		return writeEvidence(output, "correlation-snapshot.json", json.RawMessage(encoded))
	}
	physical := PhysicalEvidence{Project: project, Cold: sample.Measurement}
	for _, item := range []struct {
		name  string
		value any
	}{{"cold-before.json", &physical.ColdBefore}, {"cold-after.json", &physical.ColdAfter}} {
		raw, err := os.ReadFile(filepath.Join(output, item.name))
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, item.value); err != nil {
			return err
		}
	}
	if err = loadIntervals(output, &physical); err != nil {
		return err
	}
	if err = physical.validateCold(); err != nil {
		return err
	}
	return writeEvidence(output, "smoke-passed.json", json.RawMessage(encoded))
}

func recordFailedTurn(ctx context.Context, pool *pgxpool.Pool, output, project string) error {
	var failures []byte
	if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('agent_turn_id',agent_turn_id,'worker_run_id',worker_run_id,'worker_session_id',worker_session_id,'status',status,'terminal_reason',terminal_reason,'terminal_at',terminal_at) ORDER BY agent_turn_id),'[]') FROM agent_turns WHERE status IN ('failed','cancelled','timed_out')`).Scan(&failures); err != nil {
		return err
	}
	if string(failures) == "[]" {
		return nil
	}
	data, err := json.MarshalIndent(map[string]any{"project": project, "failure_code": "controlled_turn_unsuccessful", "captured_at": time.Now().UTC(), "failed_turns": json.RawMessage(failures)}, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(output, fmt.Sprintf("failure-%d.json", time.Now().UnixNano())), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Println(file.Name())
	return errors.New("controlled_turn_unsuccessful: failed/cancelled/timed_out Turn; private failure evidence saved")
}

func fixtureDigest() (string, error) {
	root := "spikes/worker-runtime/fixtures/go-repository"
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("fixture symlink not allowed")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", rel, len(data))
		_, err = hash.Write(data)
		return err
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func stringsSafeDatabase(database, project string) bool {
	expected := []byte(project)
	for i, b := range expected {
		if b == '-' {
			expected[i] = '_'
		}
	}
	return database == string(expected)
}
