package architecture_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMonitoringRejectInvalidEvidence(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct{ name, body string }{
		{"missing", `{"status":"success","data":{"result":[]}}`},
		{"wrong_target", `{"status":"success","data":{"result":[{"metric":{"job":"other","instance":"switchback-backend:9091"},"value":[1,"1"]}]}}`},
		{"malformed", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if monitoringSample([]byte(tc.body), "switchback-backend", "switchback-backend:9091", 1, now) == nil {
				t.Fatal("invalid sample accepted")
			}
		})
	}
	for _, body := range []string{`{}`, `{"results":{"A":{"error":"failed","status":500}}}`, `{"results":{"A":{"frames":[]}}}`} {
		if monitoringFrames([]byte(body), []string{"A"}, false) == nil {
			t.Fatalf("invalid frames accepted: %s", body)
		}
	}
	for _, tc := range []struct {
		state            string
		evaluated, fired time.Time
	}{
		{"Normal", now, time.Time{}}, {"Normal", now.Add(-time.Minute), now}, {"NoData", now.Add(time.Minute), now},
	} {
		if monitoringRecovery(tc.state, tc.evaluated, tc.fired) == nil {
			t.Fatal("unproven recovery accepted")
		}
	}
}

func TestMonitoringHTTPRejectsErrorAndOversize(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusOK} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = w.Write(bytes.Repeat([]byte("x"), (4<<20)+1))
			}
		}))
		_, err := monitoringHTTP(context.Background(), server.URL, nil)
		server.Close()
		if err == nil {
			t.Fatalf("accepted HTTP status=%d", status)
		}
	}
}

func TestMonitoringAcceptsProvenEvidence(t *testing.T) {
	now := time.Now()
	valid := fmt.Sprintf(`{"status":"success","data":{"result":[{"metric":{"job":"switchback-backend","instance":"switchback-backend:9091"},"value":[%d,"0"]}]}}`, now.Unix())
	if err := monitoringSample([]byte(valid), "switchback-backend", "switchback-backend:9091", 0, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := monitoringSample([]byte(valid), "wrong-job", "switchback-backend:9091", 0, now.Add(-time.Second)); err == nil {
		t.Fatal("wrong job accepted")
	}
	if err := monitoringSample([]byte(valid), "switchback-backend", "wrong-instance", 0, now.Add(-time.Second)); err == nil {
		t.Fatal("wrong instance accepted")
	}
	if err := monitoringSample([]byte(valid), "switchback-backend", "switchback-backend:9091", 0, now.Add(time.Minute)); err == nil {
		t.Fatal("stale timestamp accepted")
	}
	frame := []byte(`{"results":{"A":{"status":200,"frames":[{"schema":{"fields":[{"type":"time"},{"type":"number"}]},"data":{"values":[[1],[0]]}}]}}}`)
	if err := monitoringFrames(frame, []string{"A"}, false); err != nil {
		t.Fatal(err)
	}
	if err := monitoringFrames([]byte(`{"results":{"A":{"status":200,"frames":[]}}}`), []string{"A"}, true); err != nil {
		t.Fatal(err)
	}
	if err := monitoringRecovery("Normal", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
}

func TestMonitoringRejectsDefinitionDrift(t *testing.T) {
	const dashboard = `{"uid":"test","panels":[{"id":1,"type":"timeseries","datasource":{"uid":"prom","type":"prometheus"},"targets":[{"refId":"A","expr":"up"}]}]}`
	for _, drift := range []string{strings.Replace(dashboard, `"id":1`, `"id":2`, 1), strings.Replace(dashboard, `"up"`, `"vector(0)"`, 1), strings.Replace(dashboard, `"prom"`, `"wrong"`, 1), `{"uid":"test","panels":[]}`} {
		if err := monitoringDashboardMatches([]byte(dashboard), []byte(drift)); err == nil {
			t.Fatal("dashboard drift accepted")
		}
	}
	if err := monitoringDashboardMatches([]byte(dashboard), []byte(dashboard)); err != nil {
		t.Fatal(err)
	}
	const rule = `{"uid":"test","title":"Test","for":"2m","condition":"B","noDataState":"NoData","execErrState":"Error","isPaused":false,"data":[{"refId":"B","datasourceUid":"__expr__","relativeTimeRange":{"from":0,"to":0},"model":{"type":"threshold","conditions":[{"evaluator":{"type":"gt","params":[0]}}],"datasource":{"uid":"__expr__","type":"__expr__"}}}]}`
	for _, drift := range []string{strings.Replace(rule, `"params":[0]`, `"params":[1]`, 1), strings.Replace(rule, `"datasourceUid":"__expr__"`, `"datasourceUid":"wrong"`, 1), strings.Replace(rule, `"isPaused":false`, `"isPaused":true`, 1)} {
		if err := monitoringRuleMatches([]byte(rule), []byte(drift)); err == nil {
			t.Fatal("rule drift accepted")
		}
	}
	if err := monitoringRuleMatches([]byte(rule), []byte(rule)); err != nil {
		t.Fatal(err)
	}
}

func TestMonitoringRejectsMalformedFrames(t *testing.T) {
	for _, body := range []string{
		`{"results":{"A":{}}}`,
		`{"results":{"A":{"frames":[]}}}`,
		`{"results":{"A":{"status":200}}}`,
		`{"results":{"A":{"status":200,"frames":[{}]}}}`,
		`{"results":{"A":{"status":200,"frames":[{"schema":{"fields":[{"type":"number"}]},"data":{"values":[[1]]}}]}}}`,
		`{"results":{"A":{"status":200,"frames":[{"schema":{"fields":[{"type":"time"},{"type":"number"}]},"data":{"values":[[],[1]]}}]}}}`,
		`{"results":{"A":{"status":200,"frames":[{"schema":{"fields":[{"type":"time"},{"type":"number"}]},"data":{"values":[["bad"],[1]]}}]}}}`,
	} {
		for _, unavailable := range []bool{false, true} {
			if err := monitoringFrames([]byte(body), []string{"A"}, unavailable); err == nil {
				t.Fatalf("malformed frame accepted unavailable=%v: %s", unavailable, body)
			}
		}
	}
}

func TestMonitoringNoDataEvaluationHealth(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		state, health, want string
		valid               bool
	}{
		{"NoData", "nodata", "NoData", true}, {"NoData", "ok", "NoData", false}, {"Normal", "nodata", "Normal", false}, {"Error", "error", "NoData", false}, {"Normal", "ok", "Normal", true},
		{"Error", "ok", "Error", false}, {"Error", "nodata", "Error", false}, {"Error", "error", "Error", false},
	} {
		var r monitoringRule
		raw := fmt.Sprintf(`{"uid":"sb-runtime-attention","health":%q,"lastEvaluation":%q,"alerts":[{"state":%q,"labels":{"datasource_uid":"switchback-prometheus","ref_id":"A"}}]}`, tc.health, now.Format(time.RFC3339Nano), tc.state)
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		err := monitoringAlertState(r, tc.want, now.Add(-time.Second))
		if (err == nil) != tc.valid {
			t.Fatalf("state=%s health=%s want=%s valid=%v: %v", tc.state, tc.health, tc.want, tc.valid, err)
		}
		if tc.valid && tc.want == "NoData" {
			r.Alerts[0].Labels["datasource_uid"] = "other"
			if monitoringAlertState(r, tc.want, now.Add(-time.Second)) == nil {
				t.Fatal("wrong NoData source accepted")
			}
		}
	}
}

type monitoringPanel struct {
	ID          int
	Type, Title string
	Datasource  map[string]any
	Targets     []map[string]any
}
type monitoringDashboard struct {
	UID    string
	Panels []monitoringPanel
}

func monitoringDashboardMatches(expected, actual []byte) error {
	var want, got monitoringDashboard
	if err := json.Unmarshal(expected, &want); err != nil {
		return err
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		return err
	}
	if want.UID == "" || got.UID != want.UID || len(want.Panels) == 0 || len(got.Panels) != len(want.Panels) {
		return fmt.Errorf("dashboard identity/panel count drift")
	}
	byID := map[int]monitoringPanel{}
	for _, p := range got.Panels {
		if _, duplicate := byID[p.ID]; duplicate {
			return fmt.Errorf("duplicate panel %d", p.ID)
		}
		byID[p.ID] = p
	}
	for _, p := range want.Panels {
		live, ok := byID[p.ID]
		if !ok || !reflect.DeepEqual(p, live) {
			return fmt.Errorf("dashboard %s panel %d definition drift", want.UID, p.ID)
		}
		delete(byID, p.ID)
	}
	return nil
}

type monitoringRuleDefinition struct {
	UID, Title, For, Condition string
	NoDataState                string `yaml:"noDataState"`
	ExecErrState               string `yaml:"execErrState"`
	IsPaused                   bool   `yaml:"isPaused"`
	Data                       []struct {
		RefID             string                 `yaml:"refId"`
		DatasourceUID     string                 `yaml:"datasourceUid"`
		RelativeTimeRange struct{ From, To int } `yaml:"relativeTimeRange"`
		Model             map[string]any
	}
}

func monitoringRuleMatches(expected, actual []byte) error {
	var want, got monitoringRuleDefinition
	if err := json.Unmarshal(expected, &want); err != nil {
		return err
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		return err
	}
	wantDuration, e1 := time.ParseDuration(want.For)
	gotDuration, e2 := time.ParseDuration(got.For)
	if e1 != nil || e2 != nil || wantDuration != gotDuration {
		return fmt.Errorf("hold duration drift")
	}
	got.For = want.For
	if len(want.Data) != len(got.Data) {
		return fmt.Errorf("rule query count drift")
	}
	for i := range want.Data {
		// Grafana adds these transport defaults to expression queries. Every delivered
		// model field (including threshold conditions and datasource) remains exact.
		for _, key := range []string{"intervalMs", "maxDataPoints"} {
			if _, specified := want.Data[i].Model[key]; !specified {
				delete(got.Data[i].Model, key)
			}
		}
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("rule %s definition drift", want.UID)
	}
	return nil
}

func monitoringSample(body []byte, job, instance string, want float64, after time.Time) error {
	var r struct {
		Status string
		Data   struct {
			Result []struct {
				Metric map[string]string
				Value  []json.RawMessage
			}
		}
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return err
	}
	if r.Status != "success" || len(r.Data.Result) != 1 {
		return fmt.Errorf("expected one successful sample: %s", body)
	}
	v := r.Data.Result[0]
	if v.Metric["job"] != job || v.Metric["instance"] != instance || len(v.Value) != 2 {
		return fmt.Errorf("sample identity mismatch")
	}
	var timestamp float64
	var value string
	if err := json.Unmarshal(v.Value[0], &timestamp); err != nil {
		return err
	}
	if err := json.Unmarshal(v.Value[1], &value); err != nil {
		return err
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || n != want || timestamp < float64(after.Unix()) {
		return fmt.Errorf("unexpected/stale sample %s", body)
	}
	return nil
}

// Empty query data is acceptable only for explicitly classified unavailable panels.
func monitoringFrames(body []byte, refs []string, unavailable bool) error {
	var r struct {
		Results map[string]struct {
			Error  string
			Status int
			Frames []struct {
				Schema *struct{ Fields []struct{ Type string } }
				Data   *struct{ Values [][]any }
			}
		}
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return err
	}
	for _, ref := range refs {
		result, ok := r.Results[ref]
		if !ok || result.Error != "" || result.Status != 200 || result.Frames == nil {
			return fmt.Errorf("query %s failed: %s", ref, body)
		}
		numeric := false
		for _, frame := range result.Frames {
			if frame.Schema == nil || frame.Data == nil || frame.Schema.Fields == nil || frame.Data.Values == nil || len(frame.Schema.Fields) != len(frame.Data.Values) {
				return fmt.Errorf("query %s malformed frame", ref)
			}
			if len(frame.Schema.Fields) == 0 {
				continue
			}
			timeAxis := -1
			numberFields := 0
			rows := len(frame.Data.Values[0])
			for i, field := range frame.Schema.Fields {
				if len(frame.Data.Values[i]) != rows {
					return fmt.Errorf("query %s unaligned frame", ref)
				}
				switch field.Type {
				case "time":
					if timeAxis != -1 {
						return fmt.Errorf("multiple time axes")
					}
					timeAxis = i
				case "number":
					numberFields++
				default:
					return fmt.Errorf("unexpected time-series field %s", field.Type)
				}
			}
			if timeAxis < 0 || numberFields == 0 {
				return fmt.Errorf("query %s missing time/numeric axis", ref)
			}
			previous := float64(0)
			for _, v := range frame.Data.Values[timeAxis] {
				timestamp, ok := v.(float64)
				if !ok || math.IsNaN(timestamp) || math.IsInf(timestamp, 0) || timestamp <= previous {
					return fmt.Errorf("query %s invalid time axis", ref)
				}
				previous = timestamp
			}
			for i, field := range frame.Schema.Fields {
				if field.Type == "number" {
					for _, v := range frame.Data.Values[i] {
						if v == nil {
							continue
						}
						n, ok := v.(float64)
						if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
							return fmt.Errorf("query %s invalid numeric field", ref)
						}
						numeric = true
					}
				}
			}
		}
		if numeric == unavailable {
			return fmt.Errorf("query %s availability mismatch (unavailable=%v): %s", ref, unavailable, body)
		}
	}
	return nil
}

func monitoringRecovery(state string, evaluated, fired time.Time) error {
	if state != "Normal" || fired.IsZero() || !evaluated.After(fired) {
		return fmt.Errorf("recovery needs Normal evaluated after observed firing")
	}
	return nil
}

func monitoringHTTP(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	method := "GET"
	if payload != nil {
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("monitoring", "synthetic-monitoring-only")
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("oversized response")
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, b)
	}
	return b, nil
}

func monitoringPoll(t *testing.T, ctx context.Context, label string, check func() error) {
	t.Helper()
	var last error
	lastLog := time.Now().Add(-time.Minute)
	for {
		last = check()
		if last == nil {
			return
		}
		if time.Since(lastLog) >= time.Minute {
			t.Logf("waiting %s: %v", label, last)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v: %v", label, ctx.Err(), last)
		case <-time.After(3 * time.Second):
		}
	}
}

type monitoringRule struct {
	UID, Name, State, Health, LastError string
	LastEvaluation                      time.Time
	Labels                              map[string]string
	Alerts                              []struct {
		State  string
		Labels map[string]string
	}
}

func monitoringAlertState(r monitoringRule, want string, after time.Time) error {
	state := ""
	if len(r.Alerts) == 1 {
		state = r.Alerts[0].State
	}
	health := "ok"
	if want == "NoData" {
		health = "nodata"
	}
	if (want != "Normal" && want != "Alerting" && want != "NoData") || state != want || !r.LastEvaluation.After(after) || r.LastError != "" || r.Health != health {
		return fmt.Errorf("%s state=%s health=%s evaluation=%s error=%s", r.UID, state, r.Health, r.LastEvaluation, r.LastError)
	}
	if want == "NoData" && (r.Alerts[0].Labels["datasource_uid"] != "switchback-prometheus" || r.Alerts[0].Labels["ref_id"] != "A") {
		return fmt.Errorf("NoData source binding mismatch")
	}
	return nil
}

func monitoringRules(ctx context.Context, base string) ([]monitoringRule, error) {
	b, err := monitoringHTTP(ctx, base+"/api/prometheus/grafana/api/v1/rules", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Status string
		Data   struct {
			Groups []struct {
				Interval int
				Rules    []monitoringRule
			}
		}
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("rules unsuccessful")
	}
	var rules []monitoringRule
	for _, g := range r.Data.Groups {
		if g.Interval != 60 {
			return nil, fmt.Errorf("unexpected live evaluation interval: %d", g.Interval)
		}
		rules = append(rules, g.Rules...)
	}
	return rules, nil
}

func number(value float64) *float64 { return &value }
