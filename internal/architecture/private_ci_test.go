package architecture_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type privateCIWorkflow struct {
	On          map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		If       string            `yaml:"if"`
		Needs    []string          `yaml:"needs"`
		RunsOn   string            `yaml:"runs-on"`
		Env      map[string]string `yaml:"env"`
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
		Steps []struct {
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
			With map[string]any    `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadPrivateCI(t *testing.T) (privateCIWorkflow, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow privateCIWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow, string(data)
}

func TestPrivateCIContract(t *testing.T) {
	workflow, source := loadPrivateCI(t)
	if len(workflow.On) != 2 {
		t.Fatal("CI must have exactly PR and master push triggers")
	}
	if _, ok := workflow.On["pull_request"]; !ok {
		t.Fatal("all PR trigger missing")
	}
	push, ok := workflow.On["push"].(map[string]any)
	if !ok || !reflect.DeepEqual(push["branches"], []any{"master"}) || len(push) != 1 {
		t.Fatal("master push trigger mismatch")
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("CI token is not read-only")
	}
	if len(workflow.Jobs) != 6 {
		t.Fatal("CI job set mismatch")
	}
	required := workflow.Jobs["required"]
	sort.Strings(required.Needs)
	if required.If != "always()" || !reflect.DeepEqual(required.Needs, []string{"browser", "go", "integration", "static", "web"}) {
		t.Fatal("Required dependency contract mismatch")
	}
	pin := regexp.MustCompile(`^actions/(checkout|setup-go|setup-node|upload-artifact)@[0-9a-f]{40}$`)
	for name, job := range workflow.Jobs {
		if job.RunsOn != "ubuntu-24.04" || (name != "required" && job.If != "") {
			t.Fatalf("job %s is conditional or uses wrong runner", name)
		}
		for _, step := range job.Steps {
			if step.Uses != "" && !pin.MatchString(step.Uses) {
				t.Fatalf("unapproved or mutable action: %s", step.Uses)
			}
		}
	}
	if workflow.Jobs["go"].Env["GOFLAGS"] != "-count=1" {
		t.Fatal("unit/race must execute without test-result reuse")
	}
	if workflow.Jobs["integration"].Services["postgres"].Image != "postgres@sha256:dbbeb22a65db2503050cdbbe5e78f017478f10a1002a226463f049dbb017e99b" {
		t.Fatal("PostgreSQL CI must pin the verified linux/amd64 PG17.10 image")
	}
	staticCacheDisabled := false
	for _, step := range workflow.Jobs["static"].Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-go@") {
			staticCacheDisabled = step.With["cache"] == false
		}
	}
	if !staticCacheDisabled {
		t.Fatal("static test cache must not be restored")
	}
	for _, text := range []string{
		"cancel-in-progress: true", "go-version: 1.26.4", "node-version: 24.17.0",
		"make fmt-check migration-check generated-protocol-check worker-image-check vet secret-scan publication-check openspec-check",
		"./scripts/test-integration-json", "make unit", "make race", "--health-cmd", "select 1",
		"python3 -c 'import sys; assert sys.version_info[:2] == (3, 12), sys.version'", "make python-check",
		"set -euo pipefail", "go test -json -count=1 -tags=integration ./...", "./scripts/check-integration-json",
		"SWITCHBACK_TEST_DATABASE_URL:", "working-directory: web", "npm ci", "npm test", "npm run build",
		"npx playwright install --with-deps chromium", "npm run test:browser", "retention-days: 7",
	} {
		if !strings.Contains(source, text) {
			t.Errorf("CI lacks %q", text)
		}
	}
	for _, text := range []string{"pull_request_target", "paths:", "paths-ignore:", "continue-on-error", "test-publication-ready", "secrets."} {
		if strings.Contains(source, text) {
			t.Errorf("CI contains forbidden %q", text)
		}
	}
}

func TestPrivateCIRequiredRejectsIncompleteOrUnsuccessfulResults(t *testing.T) {
	workflow, _ := loadPrivateCI(t)
	steps := workflow.Jobs["required"].Steps
	if len(steps) != 1 || steps[0].Env["JOB_RESULTS"] != "${{ toJSON(needs) }}" {
		t.Fatal("Required must consume real needs")
	}
	const successful = `{"static":{"result":"success"},"go":{"result":"success"},"integration":{"result":"success"},"web":{"result":"success"},"browser":{"result":"success"}}`
	cases := map[string]string{
		"complete":   successful,
		"empty":      `{}`,
		"missing":    `{"static":{"result":"success"}}`,
		"malformed":  `not-json`,
		"unexpected": strings.Replace(successful, `"browser"`, `"other"`, 1),
	}
	for _, job := range []string{"static", "go", "integration", "web", "browser"} {
		for _, status := range []string{"failure", "cancelled", "skipped", ""} {
			cases[job+"/"+status] = strings.Replace(successful, `"`+job+`":{"result":"success"}`, `"`+job+`":{"result":"`+status+`"}`, 1)
		}
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			command := exec.Command("bash", "-euo", "pipefail", "-c", steps[0].Run)
			command.Env = append(os.Environ(), "JOB_RESULTS="+input)
			output, err := command.CombinedOutput()
			if (err == nil) != (name == "complete") {
				t.Fatalf("unexpected Required result: %v; %s", err, output)
			}
		})
	}
}
