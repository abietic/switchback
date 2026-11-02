package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/abietic/switchback/test/acceptance/observability/queryevidence"
)

var errCorrelationPending = errors.New("correlation readback pending")

type CorrelationScope struct {
	TenantID      string    `json:"tenant_id"`
	WorkspaceID   string    `json:"workspace_id"`
	SessionID     string    `json:"worker_session_id"`
	RunID         string    `json:"worker_run_id"`
	TurnID        string    `json:"agent_turn_id"`
	StartedAt     time.Time `json:"started_at"`
	TerminalAt    time.Time `json:"terminal_at"`
	RunTerminalAt time.Time `json:"run_terminal_at"`
}

func correlationHTTP() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func correlationHTTPFor(ctx context.Context) *http.Client {
	return queryevidence.HTTPClient(ctx, correlationHTTP())
}

func correlationGET(ctx context.Context, endpoint, user, password string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid correlation endpoint")
	}
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	res, err := correlationHTTPFor(ctx).Do(req)
	if err != nil {
		return errors.New("correlation query unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("correlation query HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return errors.New("correlation response truncated or oversized")
	}
	if json.Unmarshal(raw, dest) != nil {
		return errors.New("malformed correlation response")
	}
	return nil
}

// VerifyCorrelationLoki reads only the Grafana datasource proxy. Run terminals
// deliberately have no Turn ID; crossed-scope checks query Turn terminals only.
func VerifyCorrelationLoki(ctx context.Context, origin, password string, s CorrelationScope, from, to time.Time, crossed bool) (int, error) {
	// /series proves the actual indexed label set. query_range also includes
	// Loki-generated structured metadata, which is not an indexed label.
	const indexedSelector = `{service="switchback-backend",environment="local",component="worker",level="info"}`
	wantLabels := map[string]string{"service": "switchback-backend", "environment": "local", "component": "worker", "level": "info"}
	seriesQuery := url.Values{"match[]": {indexedSelector}, "start": {strconv.FormatInt(from.UnixNano(), 10)}, "end": {strconv.FormatInt(to.UnixNano(), 10)}}
	var series struct {
		Status string              `json:"status"`
		Data   []map[string]string `json:"data"`
	}
	if err := correlationGET(ctx, origin+"/api/datasources/proxy/uid/switchback-loki/loki/api/v1/series?"+seriesQuery.Encode(), "admin", password, &series); err != nil {
		return 0, err
	}
	if series.Status != "success" || series.Data == nil {
		return 0, errors.New("invalid Loki indexed series result")
	}
	for _, labels := range series.Data {
		if !reflect.DeepEqual(labels, wantLabels) {
			return 0, errors.New("unsafe Loki indexed labels")
		}
	}
	if len(series.Data) == 0 && !crossed {
		return 0, errCorrelationPending
	}
	selector := indexedSelector + ` |= ` + strconv.Quote(`"worker_run_id":`+strconv.Quote(s.RunID))
	if crossed {
		selector += ` |= ` + strconv.Quote(`"agent_turn_id":`+strconv.Quote(s.TurnID))
	} else {
		selector += ` |~ "run_terminal|turn_terminal"`
	}
	q := url.Values{"query": {selector}, "start": {strconv.FormatInt(from.UnixNano(), 10)}, "end": {strconv.FormatInt(to.UnixNano(), 10)}, "limit": {"1000"}, "direction": {"forward"}}
	var res struct {
		Status string `json:"status"`
		Data   struct {
			Type   string `json:"resultType"`
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := correlationGET(ctx, origin+"/api/datasources/proxy/uid/switchback-loki/loki/api/v1/query_range?"+q.Encode(), "admin", password, &res); err != nil {
		return 0, err
	}
	if res.Status != "success" || res.Data.Type != "streams" || res.Data.Result == nil {
		return 0, errors.New("invalid Loki result")
	}
	turns, runs, count := 0, 0, 0
	for _, stream := range res.Data.Result {
		if detected, ok := stream.Stream["detected_level"]; ok {
			if detected != "info" || detected != stream.Stream["level"] {
				return 0, errors.New("unexpected Loki detected level")
			}
			delete(stream.Stream, "detected_level")
		}
		if !reflect.DeepEqual(stream.Stream, wantLabels) {
			return 0, errors.New("unsafe Loki labels")
		}
		for _, v := range stream.Values {
			count++
			if len(v) != 2 || len(v[1]) > 4096 {
				return 0, errors.New("invalid Loki value")
			}
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil || ns < from.UnixNano() || ns > to.UnixNano() {
				return 0, errors.New("Loki timestamp outside source window")
			}
			var e telemetry.Event
			dec := json.NewDecoder(bytes.NewBufferString(v[1]))
			dec.DisallowUnknownFields()
			if dec.Decode(&e) != nil || dec.Decode(new(any)) != io.EOF {
				return 0, errors.New("unsafe Loki event schema")
			}
			expected := telemetry.Scope{TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, WorkerSessionID: s.SessionID, WorkerRunID: s.RunID}
			if e.Event == telemetry.EventTurnTerminal {
				expected.AgentTurnID = s.TurnID
				turns++
			} else if e.Event == telemetry.EventRunTerminal {
				runs++
			} else {
				return 0, errors.New("unexpected Loki event")
			}
			if e.Scope != expected || e.Service != telemetry.Service || e.SchemaVersion != telemetry.SchemaVersion || e.Level != telemetry.LevelInfo || e.Component != telemetry.ComponentWorker || e.Code != telemetry.CodeOK || e.TS.Before(from) || e.TS.After(to) || e.TS.UnixNano() != ns || e.Bytes != nil || e.Count != nil || e.OutputSource != "" || e.DurationMillis != nil && *e.DurationMillis < 0 {
				return 0, errors.New("Loki identity or terminal contract mismatch")
			}
			if !s.TerminalAt.IsZero() && e.Event == telemetry.EventTurnTerminal && !e.TS.Equal(s.TerminalAt) {
				return 0, errors.New("Loki Turn source time mismatch")
			}
			if !s.RunTerminalAt.IsZero() && e.Event == telemetry.EventRunTerminal && !e.TS.Equal(s.RunTerminalAt) {
				return 0, errors.New("Loki Run source time mismatch")
			}
		}
	}
	if count >= 1000 {
		return 0, errors.New("Loki result may be truncated")
	}
	if crossed {
		if count != 0 {
			return 0, errors.New("crossed Run/Turn leaked events")
		}
		return 0, nil
	}
	if turns == 0 || runs == 0 {
		return 0, errCorrelationPending
	}
	return count, nil
}

// VerifyCorrelationLangfuse uses the production read client and compiler bodies;
// it has no ingestion capability and cannot repair a failed readback.
func VerifyCorrelationLangfuse(ctx context.Context, client *lf.Client, event lf.IngestionEvent) error {
	body, err := json.Marshal(event.Body)
	if err != nil {
		return errors.New("invalid expected fragment")
	}
	transport, ok := event.Body["metadata"].(map[string]any)
	if !ok {
		return errors.New("invalid expected metadata")
	}
	metadata := map[string]any{}
	for key, value := range transport {
		raw, ok := value.(string)
		if !ok {
			return errors.New("invalid transport metadata")
		}
		var v any
		d := json.NewDecoder(bytes.NewBufferString(raw))
		d.UseNumber()
		if d.Decode(&v) != nil {
			return errors.New("invalid transport metadata")
		}
		metadata[key] = v
	}
	equal := func(a, b any) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return artifactapp.ProjectionJSONEqual(x, y)
	}
	if event.Type == "trace-create" {
		var want lf.Trace
		if json.Unmarshal(body, &want) != nil {
			return errors.New("invalid expected trace")
		}
		got, err := client.GetTraceRoot(ctx, want.ID)
		if lf.IsNotFound(err) {
			return errCorrelationPending
		}
		if err != nil {
			return err
		}
		if got.ID != want.ID || got.Name != want.Name || !got.Timestamp.Equal(want.Timestamp) || !equal(got.ExactMetadata, metadata) || !artifactapp.ProjectionJSONEqual(got.Input, want.Input) || (len(got.Output) != 0 && string(got.Output) != "null" && string(got.Output) != `""`) {
			return errors.New("Langfuse trace collision")
		}
		return nil
	}
	var want lf.Observation
	if json.Unmarshal(body, &want) != nil {
		return errors.New("invalid expected observation")
	}
	want.Type = "SPAN"
	if event.Type == "generation-create" {
		want.Type = "GENERATION"
	} else if event.Type != "span-create" {
		return errors.New("invalid fragment event type")
	}
	got, err := client.GetObservation(ctx, want.ID)
	if lf.IsNotFound(err) {
		return errCorrelationPending
	}
	if err != nil {
		return err
	}
	if got.ID != want.ID || got.TraceID != want.TraceID || !reflect.DeepEqual(got.ParentObservationID, want.ParentObservationID) || got.Name != want.Name || got.Type != want.Type || !got.StartTime.Equal(want.StartTime) || !got.EndTime.Equal(want.EndTime) || got.Model != want.Model || got.Level != want.Level || got.StatusMessage != want.StatusMessage || !equal(got.Metadata, metadata) || !artifactapp.ProjectionJSONEqual(got.Input, want.Input) || !artifactapp.ProjectionJSONEqual(got.Output, want.Output) {
		return errors.New("Langfuse observation collision")
	}
	if want.Type == "GENERATION" {
		usage := got.UsageDetails
		if usage == nil {
			usage = map[string]int64{"input": got.Usage.Input, "output": got.Usage.Output, "total": got.Usage.Total}
		}
		if !reflect.DeepEqual(usage, want.UsageDetails) {
			return errors.New("Langfuse usage mismatch")
		}
	}
	return nil
}

func VerifyCorrelationPrometheus(ctx context.Context, origin string, at, terminal time.Time) (map[string]float64, error) {
	queries := map[string]string{"up": `up{job="switchback-backend"}`, "available": `switchback_telemetry_sample_available{service="switchback-backend",environment="local"}`, "current": `switchback_telemetry_sample_current{service="switchback-backend",environment="local"}`, "last_success": `switchback_telemetry_sample_last_success_timestamp_seconds{service="switchback-backend",environment="local"}`, "runs": `sum(switchback_worker_run_terminal{service="switchback-backend",environment="local",status="completed"})`, "turns": `sum(switchback_agent_turn_terminal{service="switchback-backend",environment="local",status="completed"})`}
	values := map[string]float64{}
	for name, query := range queries {
		var res struct {
			Status string `json:"status"`
			Data   struct {
				Type   string `json:"resultType"`
				Result []struct {
					Value []json.RawMessage `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		q := url.Values{"query": {query}, "time": {strconv.FormatFloat(float64(at.UnixNano())/1e9, 'f', 9, 64)}}
		if err := correlationGET(ctx, origin+"/api/v1/query?"+q.Encode(), "", "", &res); err != nil {
			return nil, err
		}
		if res.Status != "success" || res.Data.Type != "vector" || res.Data.Result == nil {
			return nil, errors.New("invalid Prometheus result")
		}
		if len(res.Data.Result) == 0 {
			return nil, errCorrelationPending
		}
		if len(res.Data.Result) != 1 || len(res.Data.Result[0].Value) != 2 {
			return nil, errors.New("ambiguous Prometheus result")
		}
		var sampledAt float64
		if json.Unmarshal(res.Data.Result[0].Value[0], &sampledAt) != nil || sampledAt < float64(at.Add(-time.Minute).Unix()) || sampledAt > float64(at.Add(time.Second).Unix()) {
			return nil, errors.New("Prometheus sample timestamp outside bounded query")
		}
		var str string
		if json.Unmarshal(res.Data.Result[0].Value[1], &str) != nil {
			return nil, errors.New("invalid Prometheus sample")
		}
		v, err := strconv.ParseFloat(str, 64)
		if err != nil || v != v || v < 0 || v > 1e15 {
			return nil, errors.New("invalid Prometheus value")
		}
		values[name] = v
		if name == "last_success" && v > float64(at.Add(time.Second).Unix()) {
			return nil, errors.New("Prometheus source sample is in the future")
		}
		if (name == "up" || name == "available" || name == "current") && v != 1 || name == "last_success" && v < float64(terminal.Unix()) || (name == "runs" || name == "turns") && v < 2 {
			return nil, errCorrelationPending
		}
	}
	return values, nil
}
