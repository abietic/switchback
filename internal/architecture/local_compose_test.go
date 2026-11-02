package architecture_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
)

func TestLocalComposeKeepsM1TopologyAndDockerOwnership(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "compose.yaml"))
	if err != nil {
		t.Fatalf("read local Compose profile: %v", err)
	}
	compose := string(data)
	_, backend, found := strings.Cut(compose, "\n  switchback-backend:\n")
	if !found {
		t.Fatal("Backend Compose service missing")
	}
	backend = strings.Split(backend, "\n  assistant-litellm:\n")[0]
	if strings.Contains(strings.Split(backend, "    environment:")[0], "langfuse-web:") {
		t.Fatal("Backend startup must not depend on Langfuse")
	}
	initScript, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "init-postgres.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(initScript), `PGUSER="$POSTGRES_USER" PGDATABASE=switchback /opt/switchback/migrate-postgres.sh`) {
		t.Fatal("fresh database migrations must use configured PostgreSQL authority")
	}
	for _, service := range []string{
		"  switchback-web:",
		"  switchback-backend:",
		"  assistant-litellm:",
		"  worker-image:",
		"  postgres:",
		"  switchback-migrate:",
		"  langfuse-web:",
		"  langfuse-worker:",
		"  langfuse-clickhouse:",
		"  langfuse-minio:",
		"  langfuse-redis:",
	} {
		if !strings.Contains(compose, service) {
			t.Errorf("local Compose profile lacks %s", strings.TrimSpace(service))
		}
	}
	if strings.Count(compose, "target: /var/run/docker.sock") != 1 {
		t.Fatal("exactly one Docker Socket mount must remain owned by Backend")
	}
	for _, forbidden := range []string{"rocketmq", "kubernetes", "privileged:", "network_mode: host"} {
		if strings.Contains(strings.ToLower(compose), forbidden) {
			t.Errorf("local Compose profile contains deferred or unsafe setting %q", forbidden)
		}
	}
	for _, required := range []string{
		"profiles: [image-build]",
		"SWITCHBACK_WORKER_IMAGE: ${SWITCHBACK_WORKER_IMAGE:?run make local-up}",
		"SWITCHBACK_WORKER_NETWORK: ${SWITCHBACK_WORKER_NETWORK:-switchback-m1-worker-control}",
		"SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED: ${SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED:-0}",
		"SWITCHBACK_M2_ASSISTANT_ENABLED: ${SWITCHBACK_M2_ASSISTANT_ENABLED:-1}",
		"SWITCHBACK_ASSISTANT_LITELLM_BASE_URL: http://assistant-litellm:4000",
		"SWITCHBACK_ASSISTANT_LITELLM_API_KEY: ${SWITCHBACK_ASSISTANT_LITELLM_PROXY_KEY:?run make local-env}",
		"SWITCHBACK_ASSISTANT_MODEL_PROFILE: ${SWITCHBACK_ASSISTANT_MODEL_PROFILE:-kimi-code-assistant-openai-v1}",
		"SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY_FILE: /run/switchback-provider/kimi.key",
		"SWITCHBACK_ASSISTANT_DEEPSEEK_API_KEY_FILE: /run/switchback-provider/deepseek.key",
		"condition: service_completed_successfully",
		`entrypoint: ["/bin/sh", "/opt/switchback/migrate-postgres.sh"]`,
		"assistant-model:",
		"assistant-egress:",
		"127.0.0.1:${SWITCHBACK_WEB_UI_PORT:-4173}:8080",
		"127.0.0.1:${SWITCHBACK_BACKEND_PORT:-8080}:8080",
		"127.0.0.1:${SWITCHBACK_LANGFUSE_PORT:-3000}:3000",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("local Compose profile lacks invariant %q", required)
		}
	}
	if strings.Count(compose, "source: ${SWITCHBACK_KIMI_KEY_FILE:?run make local-env and populate the key file}") != 2 ||
		strings.Count(compose, "source: ${SWITCHBACK_DEEPSEEK_KEY_FILE:?run make local-env and populate the key file}") != 2 {
		t.Fatal("provider credentials must be mounted only into Backend provisioning and Assistant LiteLLM")
	}
	if strings.Contains(compose, "127.0.0.1:${SWITCHBACK_ASSISTANT") ||
		strings.Contains(compose, "target: /var/run/docker.sock\n        read_only: true") {
		t.Fatal("Assistant LiteLLM must not expose a host port or own the Docker Socket")
	}
	if !strings.Contains(compose, "networks: [assistant-model, assistant-egress]") {
		t.Fatal("Assistant LiteLLM must have explicit provider egress without exposing a host port")
	}
	for _, forbidden := range []string{"api." + "moonshot.cn", "KIMI_" + "PLATFORM"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("local Compose retains forbidden Kimi surface %q", forbidden)
		}
	}
	if containsExactToken(compose, "kimi-assistant-openai-v1") {
		t.Error("local Compose retains the retired Kimi Assistant profile")
	}

	acceptance, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	if err != nil {
		t.Fatalf("read real-provider acceptance script: %v", err)
	}
	acceptanceScript := string(acceptance)
	for _, required := range []string{
		"assistant_profile=${SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE:-kimi-code-assistant-openai-v1}",
		"kimi-code-assistant-openai-v1)",
		"deepseek-assistant-openai-v1)",
		"test \"$actual_profile\" = \"$assistant_profile\"",
		"docker inspect --format '{{json .HostConfig.PortBindings}}' \"$assistant_container\"",
	} {
		if !strings.Contains(acceptanceScript, required) {
			t.Errorf("real-provider acceptance script lacks invariant %q", required)
		}
	}
	if containsExactToken(acceptanceScript, "kimi-assistant-openai-v1") {
		t.Error("real-provider acceptance script allows the retired Kimi Assistant profile")
	}
}

func TestM2RealProviderSummaryCarriesDirectProjectionEvidence(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	if err != nil {
		t.Fatalf("read M2 real-provider acceptance script: %v", err)
	}
	script := string(data)
	for _, required := range []string{
		"assistant_litellm_model_alias=",
		"assistant_config_alias_count=",
		`$2 == "model_name:" && $3 == alias`,
		"task_creation_count=$(jq -er",
		"$base_url/api/v1/views/home_dashboard/index",
		"artifact_projection_count=$((artifact_projection_count + 1))",
		"assistant_litellm_model_alias:$assistant_litellm_model_alias",
		"task_creation_count:$task_creation_count",
		"home_projection_verified:(.home_projection_match_count == 1)",
		"artifact_projection_verified:(.artifact_projection_count == 8)",
		"artifact_projection_count:$artifact_projection_count",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("M2 real-provider summary lacks evidence-derived contract %q", required)
		}
	}
	for _, forbidden := range []string{
		"home_projection_verified:true",
		"artifact_projection_verified:true",
		".home_projection_verified = true",
		".artifact_projection_verified = true",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("M2 real-provider summary hard-codes projection evidence %q", forbidden)
		}
	}

	functionStart := strings.Index(script, "assistant_retry_idempotency_key() {\n")
	if functionStart == -1 {
		t.Fatal("M2 real-provider acceptance lacks an exact-target retry key helper")
	}
	functionTail := script[functionStart:]
	functionEnd := strings.Index(functionTail, "\n}\n")
	if functionEnd == -1 {
		t.Fatal("M2 real-provider retry key helper is incomplete")
	}
	function := functionTail[:functionEnd+3]
	harness := function + `
first=$(assistant_retry_idempotency_key "run-tag" "assistant-run-a" "4")
second=$(assistant_retry_idempotency_key "run-tag" "assistant-run-b" "4")
same=$(assistant_retry_idempotency_key "run-tag" "assistant-run-a" "4")
revision=$(assistant_retry_idempotency_key "run-tag" "assistant-run-a" "5")
test "$first" != "$second"
test "$first" = "$same"
test "$first" != "$revision"
`
	command := exec.Command("sh")
	command.Stdin = strings.NewReader(harness)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("M2 real-provider retry keys do not bind exact failed Run identity and revision: %v\n%s", err, output)
	}
}

func TestM2RealProviderAssistantRunsFailClosedWithoutAutomaticRetry(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	if err != nil {
		t.Fatalf("read M2 real-provider acceptance script: %v", err)
	}
	script := string(data)
	flowStart := strings.Index(script, "\nsend_message \"$conversation_revision\" \"m2-real-ambiguous-")
	if flowStart == -1 {
		t.Fatal("M2 real-provider acceptance lacks the Assistant message flow")
	}
	flow := script[flowStart:]
	for _, forbidden := range []string{
		"wait_with_retries ",
		"retry_failed_run ",
		"assistant_retry_idempotency_key ",
	} {
		if strings.Contains(flow, forbidden) {
			t.Errorf("M2 real-provider Assistant verdict path calls forbidden automatic retry seam %q", forbidden)
		}
	}
	if count := strings.Count(flow, `wait_assistant_run "$`); count != 3 {
		t.Errorf("M2 real-provider Assistant flow waits for %d exact Runs, want 3", count)
	}
	for _, required := range []string{
		`current_run=$(jq -r '.data.assistant_run.assistant_run_id // ""' "$snapshot_file")`,
		"test \"$current_run\" = \"$expected_run\"",
		"failed)",
		"sanitized classification: assistant_run_failed",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("M2 real-provider exact Run wait lacks fail-closed invariant %q", required)
		}
	}
}

func TestM2RealProviderCredentialScanNeverExpandsCredentialIntoMatcherArgv(t *testing.T) {
	script := readM2RealProviderAcceptance(t)
	if strings.Contains(script, `$(cat "$credential_file")`) {
		t.Fatal("M2 real-provider credential scan expands credential bytes into matcher argv")
	}
	if !strings.Contains(script, `grep -R -F -f "$credential_file"`) {
		t.Fatal("M2 real-provider credential scan does not use the credential file as a fixed-string pattern source")
	}
}

func TestM2RealProviderCredentialScanDetectsLiteralCredentialInEvidence(t *testing.T) {
	function := extractM2ShellFunction(
		t,
		readM2RealProviderAcceptance(t),
		"scan_provider_credential",
	)
	root := t.TempDir()
	credentialFile := filepath.Join(root, "provider.key")
	dataRoot := filepath.Join(root, "evidence")
	work := filepath.Join(root, "work")
	for _, directory := range []string{dataRoot, work} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const credential = "synthetic-[literal]*?-credential"
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(work, "projection.json"),
		[]byte(`{"unexpected":"`+credential+`"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	harness := function + `
if scan_provider_credential "$1" "$2" "$3"; then
  exit 93
fi
`
	command := exec.Command(
		"sh", "-eu", "-c", harness, "task25-credential-match",
		credentialFile, dataRoot, work,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("credential match harness failed: %v\n%s", err, output)
	}
	if string(output) != "provider credential reached acceptance evidence\n" {
		t.Fatalf("credential match failure is not sanitized: %q", output)
	}
}

func TestM2RealProviderCredentialScanAcceptsCleanEvidence(t *testing.T) {
	function := extractM2ShellFunction(
		t,
		readM2RealProviderAcceptance(t),
		"scan_provider_credential",
	)
	root := t.TempDir()
	credentialFile := filepath.Join(root, "provider.key")
	dataRoot := filepath.Join(root, "evidence")
	work := filepath.Join(root, "work")
	for _, directory := range []string{dataRoot, work} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		credentialFile,
		[]byte("synthetic-clean-credential\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dataRoot, "trace.json"),
		[]byte(`{"status":"clean"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(work, "projection.json"),
		[]byte(`{"status":"clean"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	harness := function + `
scan_provider_credential "$1" "$2" "$3"
`
	command := exec.Command(
		"sh", "-eu", "-c", harness, "task25-clean-evidence",
		credentialFile, dataRoot, work,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clean credential scan failed: %v\n%s", err, output)
	} else if len(output) != 0 {
		t.Fatalf("clean credential scan emitted output: %q", output)
	}
}

func TestM2RealProviderCredentialScanMatcherArgvContainsOnlyPathsAndFlags(t *testing.T) {
	function := extractM2ShellFunction(
		t,
		readM2RealProviderAcceptance(t),
		"scan_provider_credential",
	)
	root := t.TempDir()
	credentialFile := filepath.Join(root, "provider.key")
	dataRoot := filepath.Join(root, "evidence")
	work := filepath.Join(root, "work")
	binDir := filepath.Join(root, "bin")
	for _, directory := range []string{dataRoot, work, binDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const credential = "synthetic-argv-secret"
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(root, "matcher-argv")
	fakeMatcher := `#!/bin/sh
: > "$TASK25_MATCHER_ARGV_FILE"
for matcher_arg do
  printf '%s\n' "$matcher_arg" >> "$TASK25_MATCHER_ARGV_FILE"
done
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "grep"), []byte(fakeMatcher), 0o700); err != nil {
		t.Fatal(err)
	}
	harness := function + `
scan_provider_credential "$1" "$2" "$3"
`
	command := exec.Command(
		"sh", "-eu", "-c", harness, "task25-matcher-argv",
		credentialFile, dataRoot, work,
	)
	command.Env = append(
		os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"TASK25_MATCHER_ARGV_FILE="+argvFile,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("matcher argv harness failed: %v\n%s", err, output)
	}
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"-R",
		"-F",
		"-f",
		credentialFile,
		dataRoot,
		work,
		"",
	}, "\n")
	if string(argv) != want {
		t.Fatalf("matcher argv = %q, want only flags and paths %q", argv, want)
	}
	if strings.Contains(string(argv), credential) {
		t.Fatal("matcher argv contains synthetic credential bytes")
	}
}

func TestM2RealProviderCredentialScanRejectsMissingOrEmptyFileBeforePublication(t *testing.T) {
	script := readM2RealProviderAcceptance(t)
	scanCall := strings.LastIndex(
		script,
		`scan_provider_credential "$credential_file" "$data_root" "$work"`,
	)
	publicationCall := strings.LastIndex(
		script,
		"\npublish_validated_acceptance_summary\n",
	)
	if scanCall == -1 || publicationCall == -1 || scanCall >= publicationCall {
		t.Fatal("credential scan must complete before acceptance summary publication")
	}
	function := extractM2ShellFunction(t, script, "scan_provider_credential")
	root := t.TempDir()
	dataRoot := filepath.Join(root, "evidence")
	work := filepath.Join(root, "work")
	for _, directory := range []string{dataRoot, work} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	emptyFile := filepath.Join(root, "empty.key")
	if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, credentialFile := range map[string]string{
		"missing": filepath.Join(root, "missing.key"),
		"empty":   emptyFile,
	} {
		t.Run(name, func(t *testing.T) {
			harness := function + `
if scan_provider_credential "$1" "$2" "$3"; then
  exit 94
fi
`
			command := exec.Command(
				"sh", "-eu", "-c", harness, "task25-invalid-credential",
				credentialFile, dataRoot, work,
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("invalid credential harness failed: %v\n%s", err, output)
			}
			if string(output) != "provider credential file is missing or empty\n" {
				t.Fatalf("invalid credential failure is not sanitized: %q", output)
			}
		})
	}
}

func TestM2RealProviderSummaryPublishesOnlyAfterFinalContractValidation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	if err != nil {
		t.Fatalf("read M2 real-provider acceptance script: %v", err)
	}
	script := string(data)
	for _, required := range []string{
		`run-$(date -u +%Y%m%dT%H%M%SZ)-$$`,
		"\nclaim_acceptance_output\n",
		`command link "$summary_final" "$summary"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("M2 real-provider acceptance lacks atomic publication invariant %q", required)
		}
	}
	markers := []struct {
		name  string
		value string
	}{
		{name: "output claim", value: "\nclaim_acceptance_output\n"},
		{name: "stale-summary collision guard", value: `if test -e "$summary"; then`},
		{name: "AssistantRun profile gate", value: `assistant_run_count=$(jq -er 'length' "$work/assistant-runs.json")`},
		{name: "candidate declaration", value: `summary_candidate="$work/summary-candidate.json"`},
		{name: "candidate construction", value: `> "$summary_candidate"`},
		{name: "credential-absence gate", value: "for credential_name in SWITCHBACK_KIMI_KEY_FILE SWITCHBACK_DEEPSEEK_KEY_FILE; do"},
		{name: "browser-response privacy gate", value: `find "$work" -type f -name '*.json' ! -name '*-request.json'`},
		{name: "final construction", value: `> "$summary_final"`},
		{name: "final-summary privacy gate", value: `"$summary_final" >/dev/null 2>&1; then`},
		{name: "validated no-replace publication", value: "\npublish_validated_acceptance_summary\n"},
	}
	positions := make(map[string]int, len(markers))
	previous := -1
	for _, marker := range markers {
		position := strings.Index(script, marker.value)
		if position == -1 {
			t.Fatalf("M2 real-provider acceptance lacks %s marker %q", marker.name, marker.value)
		}
		if position <= previous {
			t.Fatalf("M2 real-provider acceptance orders %s before its prerequisite", marker.name)
		}
		positions[marker.name] = position
		previous = position
	}
	beforePublication := script[:positions["validated no-replace publication"]]
	if strings.Contains(beforePublication, `> "$summary"`) {
		t.Fatal("M2 real-provider acceptance writes persistent summary before final validation")
	}
	if strings.Contains(script, `rm -f "$summary"`) {
		t.Fatal("M2 real-provider acceptance deletes previously published evidence")
	}
	if strings.Contains(script, `mv "$summary_final" "$summary"`) {
		t.Fatal("M2 real-provider acceptance uses overwriting mv for final publication")
	}
	if strings.Contains(script, `rm -rf "$data_root"`) {
		t.Fatal("M2 real-provider acceptance recursively deletes its output root")
	}
	if !strings.Contains(script, "acceptance summary already exists; use a fresh output directory") {
		t.Fatal("M2 real-provider stale-summary collision lacks a sanitized failure classification")
	}
	candidate := script[positions["candidate construction"]:positions["credential-absence gate"]]
	for _, forbidden := range []string{
		`outcome:"passed"`,
		"home_projection_verified:true",
		"artifact_projection_verified:true",
		"worker_detail_safe_projection:true",
		"known_token_absence:true",
	} {
		if strings.Contains(candidate, forbidden) {
			t.Errorf("M2 real-provider candidate claims success before direct gates with %q", forbidden)
		}
	}
	if !strings.Contains(script, `summary_final=$(mktemp "$data_root/`) {
		t.Fatal("M2 real-provider final summary is not built as a unique destination-filesystem temporary")
	}
}

func TestM2RealProviderOutputClaimIsExclusive(t *testing.T) {
	script := readM2RealProviderAcceptance(t)
	library := writeM2OutputProtocolLibrary(t, script)
	dataRoot := t.TempDir()
	command := exec.Command("sh", "-eu", "-c", `
. "$1"
data_root=$2
summary="$data_root/m2-real-provider-summary.json"
claim_lock="$data_root/.m2-real-provider-acceptance.lock"
output_claim_acquired=0
summary_final=
summary_final_created=0
claim_acceptance_output
output_claim_acquired=0
if claim_acceptance_output; then
  exit 91
fi
output_claim_acquired=1
cleanup_acceptance_output
`, "task23-claim", library, dataRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("exclusive output claim harness failed: %v\n%s", err, output)
	}
	if string(output) != "acceptance output is already claimed; use a fresh output directory\n" {
		t.Fatalf("second output claim failure is not sanitized: %q", output)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, ".m2-real-provider-acceptance.lock")); !os.IsNotExist(err) {
		t.Fatalf("owned output claim was not cleaned exactly: %v", err)
	}
}

func TestM2RealProviderPublicationDoesNotReplaceLateDestination(t *testing.T) {
	script := readM2RealProviderAcceptance(t)
	library := writeM2OutputProtocolLibrary(t, script)
	dataRoot := t.TempDir()
	candidate := filepath.Join(dataRoot, ".m2-real-provider-summary.candidate")
	if err := os.WriteFile(candidate, []byte(validM2AcceptanceSummary), 0o600); err != nil {
		t.Fatalf("write valid summary candidate: %v", err)
	}
	command := exec.Command("sh", "-eu", "-c", `
. "$1"
data_root=$2
summary="$data_root/m2-real-provider-summary.json"
claim_lock="$data_root/.m2-real-provider-acceptance.lock"
output_claim_acquired=0
summary_final=$3
summary_final_created=1
assistant_profile=kimi-code-assistant-openai-v1
expected_assistant_profile_revision=2
expected_assistant_profile_digest=sha256:04e2bd0fb1ab379aaaffaf0584da50c1997b6807bde537e606954ef4f0f34b2c
assistant_litellm_model_alias=switchback-assistant-kimi-code
claim_acceptance_output
printf '{"historical_evidence":true}\n' > "$summary"
if publish_validated_acceptance_summary; then
  exit 92
fi
cleanup_acceptance_output
`, "task23-late-destination", library, dataRoot, candidate)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("late destination harness failed: %v\n%s", err, output)
	}
	if string(output) != "acceptance summary publication conflict; use a fresh output directory\n" {
		t.Fatalf("late destination failure is not sanitized: %q", output)
	}
	published, err := os.ReadFile(filepath.Join(dataRoot, "m2-real-provider-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "{\"historical_evidence\":true}\n" {
		t.Fatalf("late destination was overwritten: %s", published)
	}
	for _, path := range []string{
		candidate,
		filepath.Join(dataRoot, ".m2-real-provider-acceptance.lock"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("process-owned output artifact was not cleaned exactly: path=%s err=%v", path, err)
		}
	}
}

func TestM2RealProviderPublicationDoesNotTreatLateDirectoryAsDestination(t *testing.T) {
	script := readM2RealProviderAcceptance(t)
	library := writeM2OutputProtocolLibrary(t, script)
	dataRoot := t.TempDir()
	candidate := filepath.Join(dataRoot, ".m2-real-provider-summary.candidate")
	if err := os.WriteFile(candidate, []byte(validM2AcceptanceSummary), 0o600); err != nil {
		t.Fatalf("write valid summary candidate: %v", err)
	}
	command := exec.Command("sh", "-eu", "-c", `
. "$1"
data_root=$2
summary="$data_root/m2-real-provider-summary.json"
claim_lock="$data_root/.m2-real-provider-acceptance.lock"
output_claim_acquired=0
summary_final=$3
summary_final_created=1
assistant_profile=kimi-code-assistant-openai-v1
expected_assistant_profile_revision=2
expected_assistant_profile_digest=sha256:04e2bd0fb1ab379aaaffaf0584da50c1997b6807bde537e606954ef4f0f34b2c
assistant_litellm_model_alias=switchback-assistant-kimi-code
claim_acceptance_output
mkdir "$summary"
if publish_validated_acceptance_summary; then
  exit 96
fi
cleanup_acceptance_output
`, "task23-late-directory", library, dataRoot, candidate)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("late destination directory harness failed: %v\n%s", err, output)
	}
	if string(output) != "acceptance summary publication conflict; use a fresh output directory\n" {
		t.Fatalf("late destination directory failure is not sanitized: %q", output)
	}
	entries, err := os.ReadDir(filepath.Join(dataRoot, "m2-real-provider-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("publication wrote inside a late destination directory: %+v", entries)
	}
	for _, path := range []string{
		candidate,
		filepath.Join(dataRoot, ".m2-real-provider-acceptance.lock"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("process-owned output artifact was not cleaned exactly: path=%s err=%v", path, err)
		}
	}
}

func TestM2RealProviderValidClaimPublishesOnceAndCleansOwnedFiles(t *testing.T) {
	script := readM2RealProviderAcceptance(t)
	library := writeM2OutputProtocolLibrary(t, script)
	dataRoot := t.TempDir()
	candidate := filepath.Join(dataRoot, ".m2-real-provider-summary.candidate")
	if err := os.WriteFile(candidate, []byte(validM2AcceptanceSummary), 0o600); err != nil {
		t.Fatalf("write valid summary candidate: %v", err)
	}
	command := exec.Command("sh", "-eu", "-c", `
. "$1"
data_root=$2
summary="$data_root/m2-real-provider-summary.json"
claim_lock="$data_root/.m2-real-provider-acceptance.lock"
output_claim_acquired=0
summary_final=$3
summary_final_created=1
assistant_profile=kimi-code-assistant-openai-v1
expected_assistant_profile_revision=2
expected_assistant_profile_digest=sha256:04e2bd0fb1ab379aaaffaf0584da50c1997b6807bde537e606954ef4f0f34b2c
assistant_litellm_model_alias=switchback-assistant-kimi-code
claim_acceptance_output
publish_validated_acceptance_summary
cleanup_acceptance_output
`, "task23-valid-publication", library, dataRoot, candidate)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("valid publication harness failed: %v\n%s", err, output)
	}
	published, err := os.ReadFile(filepath.Join(dataRoot, "m2-real-provider-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != validM2AcceptanceSummary {
		t.Fatal("valid publication did not preserve the validated summary bytes")
	}
	for _, path := range []string{
		candidate,
		filepath.Join(dataRoot, ".m2-real-provider-acceptance.lock"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("process-owned output artifact was not cleaned exactly: path=%s err=%v", path, err)
		}
	}
}

func TestM2RealProviderFinalValidationPrecedesPublication(t *testing.T) {
	script := readM2RealProviderAcceptance(t)
	library := writeM2OutputProtocolLibrary(t, script)
	dataRoot := t.TempDir()
	candidate := filepath.Join(dataRoot, ".m2-real-provider-summary.invalid")
	if err := os.WriteFile(candidate, []byte("{\"outcome\":\"passed\"}\n"), 0o600); err != nil {
		t.Fatalf("write invalid summary candidate: %v", err)
	}
	command := exec.Command("sh", "-eu", "-c", `
. "$1"
data_root=$2
summary="$data_root/m2-real-provider-summary.json"
claim_lock="$data_root/.m2-real-provider-acceptance.lock"
output_claim_acquired=0
summary_final=$3
summary_final_created=1
assistant_profile=kimi-code-assistant-openai-v1
expected_assistant_profile_revision=2
expected_assistant_profile_digest=sha256:04e2bd0fb1ab379aaaffaf0584da50c1997b6807bde537e606954ef4f0f34b2c
assistant_litellm_model_alias=switchback-assistant-kimi-code
claim_acceptance_output
if publish_validated_acceptance_summary; then
  exit 93
fi
cleanup_acceptance_output
`, "task23-invalid-publication", library, dataRoot, candidate)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("invalid publication harness failed: %v\n%s", err, output)
	}
	if string(output) != "acceptance summary failed final contract validation\n" {
		t.Fatalf("invalid summary failure is not sanitized: %q", output)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "m2-real-provider-summary.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid summary was published: %v", err)
	}
	for _, path := range []string{
		candidate,
		filepath.Join(dataRoot, ".m2-real-provider-acceptance.lock"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("process-owned output artifact was not cleaned exactly: path=%s err=%v", path, err)
		}
	}
}

func TestM2RealProviderWaitsOnlyForMissingArtifactDetailProjection(t *testing.T) {
	function := extractM2ShellFunction(t, readM2RealProviderAcceptance(t), "wait_artifact_projection")
	harness := function + `
base_url=http://127.0.0.1:8080
curl_count_file=$2
curl() {
  curl_calls=$(cat "$curl_count_file")
  curl_calls=$((curl_calls + 1))
  printf '%s\n' "$curl_calls" > "$curl_count_file"
  printf '{}\n' > "$3"
  if test "$curl_calls" -lt 3; then
    printf '404'
  else
    printf '200'
  fi
}
sleep() {
  :
}
wait_artifact_projection artifact-1 "$1"
test "$(cat "$curl_count_file")" = 3
`
	successDir := t.TempDir()
	successCount := filepath.Join(successDir, "curl-count")
	if err := os.WriteFile(successCount, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		"sh", "-eu", "-c", harness, "task23-artifact-wait",
		filepath.Join(successDir, "artifact.json"), successCount,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ArtifactDetail bounded 404 wait failed: %v\n%s", err, output)
	}

	failFastHarness := function + `
base_url=http://127.0.0.1:8080
curl_count_file=$2
curl() {
  curl_calls=$(cat "$curl_count_file")
  curl_calls=$((curl_calls + 1))
  printf '%s\n' "$curl_calls" > "$curl_count_file"
  printf '{}\n' > "$3"
  printf '500'
}
sleep() {
  exit 94
}
if wait_artifact_projection artifact-1 "$1"; then
  exit 95
fi
test "$(cat "$curl_count_file")" = 1
`
	failFastDir := t.TempDir()
	failFastCount := filepath.Join(failFastDir, "curl-count")
	if err := os.WriteFile(failFastCount, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command(
		"sh", "-eu", "-c", failFastHarness, "task23-artifact-fail-fast",
		filepath.Join(failFastDir, "artifact.json"), failFastCount,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ArtifactDetail non-404 fail-fast harness failed: %v\n%s", err, output)
	}
	if string(output) != "Artifact projection returned an unexpected HTTP status\n" {
		t.Fatalf("ArtifactDetail non-404 failure is not sanitized: %q", output)
	}
}

func TestM2RealProviderAcceptancePreservesExistingPublishedSummary(t *testing.T) {
	dataRoot := t.TempDir()
	summaryPath := filepath.Join(dataRoot, "m2-real-provider-summary.json")
	const sentinel = "{\"historical_evidence\":true}\n"
	if err := os.WriteFile(summaryPath, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write historical acceptance summary: %v", err)
	}
	command := exec.Command("sh", filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	command.Env = append(os.Environ(), "SWITCHBACK_M2_REAL_PROVIDER_DATA_ROOT="+dataRoot)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("M2 real-provider acceptance overwrote an existing published summary")
	}
	if string(output) != "acceptance summary already exists; use a fresh output directory\n" {
		t.Fatalf("M2 real-provider stale-summary failure is not sanitized: %q", output)
	}
	after, readErr := os.ReadFile(summaryPath)
	if readErr != nil {
		t.Fatalf("read historical acceptance summary: %v", readErr)
	}
	if string(after) != sentinel {
		t.Fatal("M2 real-provider acceptance changed historical published evidence")
	}
}

func TestM2RealProviderExpectedAssistantProfileRefsTrackProductionRegistry(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	if err != nil {
		t.Fatalf("read M2 real-provider acceptance script: %v", err)
	}
	script := string(data)
	registry := config.ProductionProfileRegistry()
	for _, profileID := range []string{
		config.KimiCodeAssistantOpenAIV1,
		config.DeepSeekAssistantOpenAIV1,
	} {
		profile, ok := registry.Assistant(profileID)
		if !ok {
			t.Fatalf("production Assistant profile %q is missing", profileID)
		}
		branchStart := strings.Index(script, "\n  "+profileID+")\n")
		if branchStart == -1 {
			t.Fatalf("M2 real-provider selector lacks profile branch %q", profileID)
		}
		branchTail := script[branchStart:]
		branchEnd := strings.Index(branchTail, "\n    ;;")
		if branchEnd == -1 {
			t.Fatalf("M2 real-provider selector profile branch %q is incomplete", profileID)
		}
		branch := branchTail[:branchEnd]
		for _, expected := range []string{
			"assistant_litellm_model_alias=" + profile.LiteLLMModel,
			"expected_assistant_profile_revision=" + strconv.FormatUint(profile.Revision, 10),
			"expected_assistant_profile_digest=" + profile.Digest,
		} {
			if !strings.Contains(branch, expected) {
				t.Errorf("M2 real-provider selector profile %q lacks registry ref %q", profileID, expected)
			}
		}
	}
}

func TestM2RealProviderSummaryBindsEveryAssistantRunToExpectedProfile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	if err != nil {
		t.Fatalf("read M2 real-provider acceptance script: %v", err)
	}
	script := string(data)
	for _, required := range []string{
		"postgres_container=$(docker compose --env-file \"$env_file\" -f \"$compose_file\" ps -q postgres)",
		"FROM assistant_runs",
		"FROM assistant_context_packs",
		"WHERE run.conversation_id = :'conversation_id'",
		`> "$work/assistant-runs.json"`,
		"'status', run.status",
		"'attempt_count', run.attempt_count",
		"'policy_revision', run.policy_revision",
		"'context_pack_count', context_evidence.context_pack_count",
		"'context_pack_policy_revision', context_evidence.policy_revision",
		"pack.compiled_payload->>'policy_revision'",
		"assistant_profile_revision:$expected_assistant_profile_revision",
		"assistant_profile_digest:$expected_assistant_profile_digest",
		"assistant_runs:$assistant_runs[0]",
		"assistant_run_count:($assistant_runs[0] | length)",
		"assistant_profile_runs_verified:true",
		"all(.assistant_runs[];",
		`.status == "completed"`,
		".attempt_count == 1",
		"(.policy_revision | length) > 0",
		".context_pack_count == 1",
		".context_pack_policy_revision == .policy_revision",
		".profile_revision == $expected_profile_revision",
		".profile_digest == $expected_profile_digest",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("M2 real-provider canonical AssistantRun evidence lacks %q", required)
		}
	}
}

func readM2RealProviderAcceptance(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	if err != nil {
		t.Fatalf("read M2 real-provider acceptance script: %v", err)
	}
	return string(data)
}

func writeM2OutputProtocolLibrary(t *testing.T, script string) string {
	t.Helper()
	var library strings.Builder
	library.WriteString("set -eu\n")
	for _, name := range []string{
		"claim_acceptance_output",
		"cleanup_acceptance_output",
		"publish_validated_acceptance_summary",
	} {
		library.WriteString(extractM2ShellFunction(t, script, name))
		library.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "output-protocol.sh")
	if err := os.WriteFile(path, []byte(library.String()), 0o600); err != nil {
		t.Fatalf("write output protocol library: %v", err)
	}
	return path
}

func extractM2ShellFunction(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, name+"() {\n")
	if start == -1 {
		t.Fatalf("M2 real-provider acceptance lacks shell function %s", name)
	}
	tail := script[start:]
	end := strings.Index(tail, "\n}\n")
	if end == -1 {
		t.Fatalf("M2 real-provider shell function %s is incomplete", name)
	}
	return tail[:end+3]
}

const validM2AcceptanceSummary = `{
  "schema_version": "switchback.m2-real-provider-acceptance.v1",
  "generated_at": "2026-07-30T00:00:00Z",
  "outcome": "passed",
  "assistant_profile": "kimi-code-assistant-openai-v1",
  "assistant_profile_revision": 2,
  "assistant_profile_digest": "sha256:04e2bd0fb1ab379aaaffaf0584da50c1997b6807bde537e606954ef4f0f34b2c",
  "assistant_litellm_model_alias": "switchback-assistant-kimi-code",
  "assistant_profile_runs_verified": true,
  "assistant_run_count": 1,
  "assistant_runs": [{
    "assistant_run_id": "assistant-run-1",
    "status": "completed",
    "attempt_count": 1,
    "policy_revision": "assistant-readiness-v1",
    "context_pack_count": 1,
    "context_pack_policy_revision": "assistant-readiness-v1",
    "profile_id": "kimi-code-assistant-openai-v1",
    "profile_revision": 2,
    "profile_digest": "sha256:04e2bd0fb1ab379aaaffaf0584da50c1997b6807bde537e606954ef4f0f34b2c"
  }],
  "conversation_id": "assistant-conversation-1",
  "task_id": "task-1",
  "worker_session_id": "worker-session-1",
  "stages": {
    "initial_delivery": {"worker_run_id": "worker-run-1", "agent_turn_id": "agent-turn-1"},
    "direct_task_chat": {"worker_run_id": "worker-run-2", "agent_turn_id": "agent-turn-2"},
    "same_task_rework": {"worker_run_id": "worker-run-3", "agent_turn_id": "agent-turn-3"},
    "standalone_follow_up": {"worker_run_id": "worker-run-4", "agent_turn_id": "agent-turn-4"}
  },
  "task_creation_count": 1,
  "assistant_clarification_observed": true,
  "assistant_ready_observed": true,
  "direct_task_chat_observed": true,
  "same_task_rework_observed": true,
  "task_accept_observed": true,
  "standalone_follow_up_observed": true,
  "task_identity_preserved": true,
  "worker_session_identity_preserved": true,
  "display_timeline_verified": true,
  "artifact_provenance_verified": true,
  "home_projection_match_count": 1,
  "home_projection_verified": true,
  "artifact_projection_count": 8,
  "artifact_projection_verified": true,
  "worker_detail_safe_projection": true,
  "task_status": "completed",
  "worker_run_status": "completed",
  "agent_turn_status": "completed",
  "artifact_types": ["patch", "report"],
  "source_fixture_immutable": true,
  "known_token_absence": true
}
`

func TestM2RealProviderPrivacyGateScansEveryBrowserResponseJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "run-m2-real-provider-acceptance"))
	if err != nil {
		t.Fatalf("read M2 real-provider acceptance script: %v", err)
	}
	script := string(data)
	gateStart := strings.Index(script, `find "$work" -type f -name '*.json' ! -name '*-request.json'`)
	if gateStart == -1 {
		t.Fatal("M2 real-provider privacy gate does not scan every JSON except local request fixtures")
	}
	gateTail := script[gateStart:]
	gateEnd := strings.Index(gateTail, "\nfi")
	if gateEnd == -1 {
		t.Fatal("M2 real-provider privacy gate is incomplete")
	}
	gate := gateTail[:gateEnd]
	if strings.Count(gate, "! -name") != 1 || strings.Contains(gate, "-o -name") {
		t.Fatal("M2 real-provider privacy gate excludes response or projection JSON")
	}
	for _, forbiddenSurface := range []string{
		"proxy_server_request",
		"litellm_params",
		"reasoning_content",
		"chain.?of.?thought",
		`"(authorization|api[_-]?key|raw_(request|response))"`,
	} {
		if !strings.Contains(gate, forbiddenSurface) {
			t.Errorf("M2 real-provider privacy gate does not forbid %q", forbiddenSurface)
		}
	}
}

func TestLocalMigrationRunnerTracksFreshAndLegacySchemas(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "migrate-postgres.sh"))
	if err != nil {
		t.Fatalf("read local migration runner: %v", err)
	}
	runner := string(data)
	for _, required := range []string{
		"switchback_schema_migrations",
		"SWITCHBACK_MIGRATIONS_DIR:-/opt/switchback/migrations",
		`"${migrations_dir}"/[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql`,
		"no PostgreSQL migrations found",
		"legacy_applied",
		"public.worker_sessions",
		"public.assistant_conversations",
		"BEGIN;",
		"ON_ERROR_STOP=1",
	} {
		if !strings.Contains(runner, required) {
			t.Errorf("local migration runner lacks invariant %q", required)
		}
	}
}

func TestManualAcceptanceCommandsUseTheCanonicalProfileSelector(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "architecture", "m2-assistant-task-dispatch-manual-acceptance.md"))
	if err != nil {
		t.Fatalf("read M2 manual acceptance runbook: %v", err)
	}
	_, activeCommands, found := strings.Cut(string(data), "## Automated Real-Provider Gate")
	if !found {
		t.Fatal("M2 manual acceptance runbook lacks the automated real-provider gate")
	}
	activeCommands, _, found = strings.Cut(activeCommands, "## Browser Product Check")
	if !found {
		t.Fatal("M2 manual acceptance runbook lacks the browser-check boundary")
	}
	for _, required := range []string{
		"SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=deepseek-assistant-openai-v1",
		"SWITCHBACK_M2_REAL_PROVIDER_ASSISTANT_PROFILE=kimi-code-assistant-openai-v1",
		"SWITCHBACK_M2_REAL_PROVIDER_SKIP_LOCAL_UP=1",
	} {
		if !strings.Contains(activeCommands, required) {
			t.Errorf("active manual acceptance commands lack canonical setting %q", required)
		}
	}
	if strings.Contains(activeCommands, "SWITCHBACK_ASSISTANT_MODEL_PROFILE=") {
		t.Error("active manual acceptance commands retain the obsolete profile selector")
	}
}
