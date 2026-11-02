package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	"github.com/abietic/switchback/test/acceptance/observability/queryevidence"
	"github.com/jackc/pgx/v5/pgxpool"
)

func correlationEnvironment(project, output string) (map[string]string, error) {
	if _, err := readClaim(project, output); err != nil {
		return nil, err
	}
	path := filepath.Join(output, "acceptance.env")
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, errors.New("correlation env is not claim-owned")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("invalid correlation env")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || values[k] != "" {
			return nil, errors.New("invalid duplicate env field")
		}
		values[k] = v
	}
	if values["COMPOSE_PROJECT_NAME"] != project || values["SWITCHBACK_HOST_DATA_ROOT"] != filepath.Join(output, "data") || !stringsSafeDatabase(values["OBSERVABILITY_DATABASE_NAME"], project) {
		return nil, errors.New("correlation environment claim mismatch")
	}
	ports := map[int]bool{}
	for _, key := range []string{"SWITCHBACK_BACKEND_PORT", "SWITCHBACK_GRAFANA_PORT", "SWITCHBACK_PROMETHEUS_PORT", "SWITCHBACK_LANGFUSE_PORT", "OBSERVABILITY_POSTGRES_PORT"} {
		p, e := strconv.Atoi(values[key])
		if e != nil || p < 1 || p > 65535 || ports[p] || strconv.Itoa(p) != values[key] {
			return nil, errors.New("missing or invalid distinct claimed port")
		}
		ports[p] = true
	}
	for _, key := range []string{"SWITCHBACK_GRAFANA_PASSWORD", "LANGFUSE_INIT_PROJECT_PUBLIC_KEY", "LANGFUSE_INIT_PROJECT_SECRET_KEY", "SWITCHBACK_POSTGRES_PASSWORD"} {
		if values[key] == "" {
			return nil, errors.New("missing claimed credential")
		}
	}
	return values, nil
}

func runCorrelation(project, output string) error {
	values, err := correlationEnvironment(project, output)
	if err != nil {
		return err
	}
	path := filepath.Join(output, "correlation-snapshot.json")
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return errors.New("claim correlation snapshot missing")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16<<20 {
		return errors.New("invalid correlation snapshot")
	}
	var smoke RawEvidence
	if json.Unmarshal(raw, &smoke) != nil || smoke.Project != project || !smoke.Controlled {
		return errors.New("smoke claim mismatch")
	}
	measurement, err := measurementFromRaw(smoke)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(os.Getenv("OBSERVABILITY_DATABASE_URL"))
	if err != nil {
		return errors.New("invalid correlation database URL")
	}
	if !net.ParseIP(cfg.ConnConfig.Host).IsLoopback() || fmt.Sprint(cfg.ConnConfig.Port) != values["OBSERVABILITY_POSTGRES_PORT"] || cfg.ConnConfig.Database != values["OBSERVABILITY_DATABASE_NAME"] || cfg.ConnConfig.User != "switchback" || cfg.ConnConfig.Password != values["SWITCHBACK_POSTGRES_PASSWORD"] || len(cfg.ConnConfig.Fallbacks) != 0 {
		return errors.New("correlation database does not match exact claim")
	}
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
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
	objects, err := artifactlocal.NewObjectStore(filepath.Join(output, "data"))
	if err != nil {
		return err
	}
	proofs, ready, err := readSupplementProofs(ctx, pool, proofPool, objects, measurement.TurnIDs, smoke.Expected, true)
	if err != nil {
		return err
	}
	if !ready || !reflect.DeepEqual(proofs, smoke.FragmentProofs) {
		return errors.New("source proofs changed since smoke snapshot")
	}
	rows, err := pool.Query(ctx, `SELECT s.tenant_id,s.workspace_id,s.worker_session_id,r.worker_run_id,t.agent_turn_id,t.started_at,t.terminal_at,r.terminal_at FROM agent_turns t JOIN worker_runs r USING(worker_run_id) JOIN worker_sessions s ON s.worker_session_id=t.worker_session_id WHERE t.status='completed' AND r.status='completed' ORDER BY t.created_at`)
	if err != nil {
		return err
	}
	var scopes []CorrelationScope
	for rows.Next() {
		var s CorrelationScope
		if err = rows.Scan(&s.TenantID, &s.WorkspaceID, &s.SessionID, &s.RunID, &s.TurnID, &s.StartedAt, &s.TerminalAt, &s.RunTerminalAt); err != nil {
			rows.Close()
			return err
		}
		scopes = append(scopes, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(scopes) != 2 || scopes[0].RunID == scopes[1].RunID {
		return errors.New("correlation requires exact two independent completed Runs")
	}
	from, to := scopes[0].StartedAt, scopes[0].TerminalAt
	for i, s := range scopes {
		if s.TurnID != measurement.TurnIDs[i] || s.SessionID != measurement.SessionID || s.StartedAt.IsZero() || s.TerminalAt.Before(s.StartedAt) || s.RunTerminalAt.Before(s.StartedAt) {
			return errors.New("source scope mismatch")
		}
		if s.StartedAt.Before(from) {
			from = s.StartedAt
		}
		if s.TerminalAt.After(to) {
			to = s.TerminalAt
		}
		if s.RunTerminalAt.After(to) {
			to = s.RunTerminalAt
		}
	}
	from = from.Add(-time.Second)
	to = to.Add(time.Second)
	if to.Sub(from) > 30*time.Minute {
		return errors.New("source correlation window exceeds controlled bound")
	}
	loader := artifactapp.FragmentLoader{Metadata: artifactpostgres.NewObservationLedger(proofPool), Objects: objects}
	events := map[string]lf.IngestionEvent{}
	for _, p := range proofs {
		loaded, e := loader.Load(ctx, artifactapp.FragmentJob{Source: artifactapp.FragmentRef{Kind: p.Kind, SourceID: p.SourceID, FrameDigest: p.Digest}, Version: fragmentVersion})
		if e != nil {
			return e
		}
		for i, frame := range loaded.Frames {
			receipt, e := artifactapp.DecodeObservationScanReceipt(loaded.Receipts[i], frame)
			if e != nil {
				return e
			}
			wire, e := (lf.FragmentCompiler{}).CompileObservationFragment(ctx, frame)
			if e != nil {
				return e
			}
			if artifactdomain.Checksum(wire) != receipt.WireSHA256 {
				return errors.New("compiled wire differs from scan proof")
			}
			var batch lf.IngestionBatch
			d := json.NewDecoder(bytes.NewReader(wire))
			d.UseNumber()
			if d.Decode(&batch) != nil || len(batch.Batch) != 1 {
				return errors.New("invalid compiled fragment")
			}
			event := batch.Batch[0]
			if previous, ok := events[event.ID]; ok && !reflect.DeepEqual(previous, event) {
				return errors.New("conflicting compiled identities")
			}
			events[event.ID] = event
		}
	}
	recorder, err := queryevidence.NewRecorder(filepath.Join(output, "correlation-queries"), queryevidence.Binding{
		Project: project, SnapshotSHA256: artifactdomain.Checksum(raw),
		Origins: map[string]string{
			"grafana":    "http://127.0.0.1:" + values["SWITCHBACK_GRAFANA_PORT"],
			"langfuse":   "http://127.0.0.1:" + values["SWITCHBACK_LANGFUSE_PORT"],
			"prometheus": "http://127.0.0.1:" + values["SWITCHBACK_PROMETHEUS_PORT"],
		},
	}, http.DefaultTransport)
	if err != nil {
		return err
	}
	ctx = queryevidence.WithTransport(ctx, recorder)
	client, err := lf.NewClient(lf.ClientOptions{BaseURL: "http://127.0.0.1:" + values["SWITCHBACK_LANGFUSE_PORT"], PublicKey: values["LANGFUSE_INIT_PROJECT_PUBLIC_KEY"], SecretKey: values["LANGFUSE_INIT_PROJECT_SECRET_KEY"], HTTPClient: correlationHTTPFor(ctx)})
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(events))
	for id := range events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var metrics map[string]float64
	var counts []int
	var queriedAt time.Time
	for {
		if err := recorder.BeginAttempt(); err != nil {
			return err
		}
		pending := false
		counts = nil
		for _, s := range scopes {
			n, e := VerifyCorrelationLoki(ctx, "http://127.0.0.1:"+values["SWITCHBACK_GRAFANA_PORT"], values["SWITCHBACK_GRAFANA_PASSWORD"], s, from, to, false)
			if errors.Is(e, errCorrelationPending) {
				pending = true
			} else if e != nil {
				return e
			}
			counts = append(counts, n)
		}
		for i, s := range scopes {
			s.TurnID = scopes[1-i].TurnID
			if _, e := VerifyCorrelationLoki(ctx, "http://127.0.0.1:"+values["SWITCHBACK_GRAFANA_PORT"], values["SWITCHBACK_GRAFANA_PASSWORD"], s, from, to, true); e != nil {
				return e
			}
		}
		for _, id := range ids {
			e := VerifyCorrelationLangfuse(ctx, client, events[id])
			if errors.Is(e, errCorrelationPending) {
				pending = true
			} else if e != nil {
				return e
			}
		}
		queriedAt = time.Now().UTC()
		metrics, err = VerifyCorrelationPrometheus(ctx, "http://127.0.0.1:"+values["SWITCHBACK_PROMETHEUS_PORT"], queriedAt, to.Add(-time.Second))
		if errors.Is(err, errCorrelationPending) {
			pending = true
		} else if err != nil {
			return err
		}
		if !pending {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("correlation availability deadline exceeded")
		case <-time.After(2 * time.Second):
		}
	}
	if err := recorder.Seal(); err != nil {
		return err
	}
	identities := []map[string]any{}
	for _, id := range ids {
		e := events[id]
		identities = append(identities, map[string]any{"event_type": e.Type, "id": e.Body["id"], "trace_id": e.Body["traceId"], "parent_observation_id": e.Body["parentObservationId"]})
	}
	return writeEvidence(output, "correlation-passed.json", map[string]any{"project": project, "controlled_acp_fixture": true, "boundary": "G3 controlled cross-backend correlation; not G4/G5 or real provider", "scopes": scopes, "source_proofs_digest": evidenceDigest(proofs), "correlation_snapshot_sha256": artifactdomain.Checksum(raw), "window_from": from, "window_to": to, "queried_at": queriedAt, "loki_event_counts": counts, "crossed_scope_counts": []int{0, 0}, "loki_query": "fixed source window; exact Run and Turn literal JSON filters; original four labels checked", "langfuse_readbacks": identities, "prometheus_values": metrics, "metrics_semantics": "aggregate completed counts >= two source Runs/Turns; not exact-ID metrics"})
}
