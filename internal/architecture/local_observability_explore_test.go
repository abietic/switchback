package architecture_test

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/telemetry"
)

// exploreRunQueries decodes the delivered URLs, retaining their actual query and time contract.
func exploreRunQueries(t *testing.T, run string) []string {
	t.Helper()
	var queries []string
	for _, name := range []string{"platform-health", "worker-runtime", "harness-model", "projection-health"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "observability", "grafana", "dashboards", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var dashboard struct{ Links []struct{ Title, URL string } }
		if err := json.Unmarshal(data, &dashboard); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, link := range dashboard.Links {
			if link.Title != "Explore exact safe run ID" {
				continue
			}
			found = true
			raw := strings.NewReplacer("${run_id}", run, "${__from}", "1000", "${__to}", "2000").Replace(link.URL)
			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			var panes struct {
				Local struct {
					Datasource string
					Queries    []struct{ Expr string }
					Range      struct{ From, To string }
				}
			}
			if err := json.Unmarshal([]byte(parsed.Query().Get("panes")), &panes); err != nil {
				t.Fatal(err)
			}
			if panes.Local.Datasource != "switchback-loki" || panes.Local.Range.From != "1000" || panes.Local.Range.To != "2000" || len(panes.Local.Queries) != 1 {
				t.Fatalf("%s: invalid Explore datasource/time", name)
			}
			expr := panes.Local.Queries[0].Expr
			if strings.Contains(strings.Split(expr, "}")[0], "run_id") {
				t.Fatalf("%s: identity indexed", name)
			}
			queries = append(queries, expr)
		}
		if !found {
			t.Fatalf("%s: exact Run link missing", name)
		}
	}
	return queries
}

func TestLocalObservabilityExploreEnvelopeContract(t *testing.T) {
	event := telemetry.Event{Scope: telemetry.Scope{WorkerRunID: "run-contract"}}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	scope, ok := envelope["scope"].(map[string]any)
	if !ok || scope["worker_run_id"] != "run-contract" {
		t.Fatal("typed Event scope contract changed")
	}
	for _, expr := range exploreRunQueries(t, "run-contract") {
		if !strings.Contains(expr, `| json run_id="scope.worker_run_id" | run_id = "run-contract"`) {
			t.Fatalf("Explore does not extract typed Event scope: %s", expr)
		}
	}
}
