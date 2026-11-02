package architecture_test

import (
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"
)

func TestAssistantLiteLLMImagePinsTheProxyAndKeepsProviderCredentialFileScoped(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}
	dockerfile := read("images/assistant-litellm/Dockerfile")
	for _, required := range []string{
		"python@sha256:",
		"litellm[proxy]==1.93.0",
		"USER 10001:10001",
		`ENTRYPOINT ["/usr/local/bin/assistant-litellm"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Assistant LiteLLM image lacks invariant %q", required)
		}
	}
	configuration := read("images/assistant-litellm/config.yaml")
	for _, required := range []string{
		"model_name: switchback-assistant-kimi-code",
		"model: openai/kimi-for-coding",
		"api_base: https://api.kimi.com/coding/v1",
		"api_key: os.environ/SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY",
		"model_name: switchback-assistant-deepseek",
		"model: deepseek/deepseek-v4-pro",
		"api_base: https://api.deepseek.com",
		"master_key: os.environ/SWITCHBACK_ASSISTANT_LITELLM_PROXY_KEY",
	} {
		if !strings.Contains(configuration, required) {
			t.Errorf("Assistant LiteLLM config lacks invariant %q", required)
		}
	}
	for _, forbidden := range []string{"api." + "moonshot.cn", "KIMI_" + "PLATFORM"} {
		if strings.Contains(configuration, forbidden) {
			t.Errorf("Assistant LiteLLM config retains forbidden Kimi surface %q", forbidden)
		}
	}
	if containsExactToken(configuration, "switchback-assistant-kimi") {
		t.Error("Assistant LiteLLM config retains the retired Kimi alias")
	}
	entrypoint := read("images/assistant-litellm/entrypoint.sh")
	compose := read("deploy/local/compose.yaml")
	if err := validateAssistantLiteLLMProductionWiring(compose, dockerfile, entrypoint); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entrypoint, "SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY_FILE") ||
		!strings.Contains(entrypoint, "SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY=$(cat \"$kimi_code_key_file\")") ||
		!strings.Contains(entrypoint, "SWITCHBACK_ASSISTANT_DEEPSEEK_API_KEY_FILE") ||
		!strings.Contains(entrypoint, "exec litellm") {
		t.Fatal("Assistant LiteLLM entrypoint does not load the mounted provider credential")
	}
}

func TestAssistantLiteLLMFailsClosedInsteadOfDroppingStructuredOutput(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	assistantConfiguration := read("images/assistant-litellm/config.yaml")
	if err := validateAssistantLiteLLMFailClosedPolicy(assistantConfiguration); err != nil {
		t.Error(err)
	}

	workerConfiguration := read("images/worker/litellm/config.yaml")
	const workerPolicy = "litellm_settings:\n  drop_params: true\n  callbacks: switchback_callback.switchback_callback"
	if !strings.Contains(workerConfiguration, workerPolicy) ||
		strings.Contains(workerConfiguration, "num_retries: 0") {
		t.Error("Worker LiteLLM policy changed outside the Assistant fix")
	}
}

func TestAssistantLiteLLMFailClosedValidationRejectsWrongWiringAndDuplicatePolicy(t *testing.T) {
	const validCompose = `
services:
  assistant-litellm:
    build:
      context: ../..
      dockerfile: images/assistant-litellm/Dockerfile
`
	const validDockerfile = `
ARG PYTHON_IMAGE=docker.io/library/python@sha256:56249d7a2f93306106f6d8bcdf6423afb73c1b747d874febcc778beee25cb8bb

FROM ${PYTHON_IMAGE}
RUN groupadd --gid 10001 litellm \
    && useradd --uid 10001 --gid litellm --create-home --home-dir /var/lib/switchback-litellm --shell /usr/sbin/nologin litellm \
    && pip install --no-cache-dir 'litellm[proxy]==1.93.0' \
    && python -c "import importlib.metadata as m; assert m.version('litellm') == '1.93.0'" \
    && litellm --version
COPY images/assistant-litellm/config.yaml /opt/switchback/assistant-litellm/config.yaml
COPY images/assistant-litellm/entrypoint.sh /usr/local/bin/assistant-litellm
RUN chmod 0555 /usr/local/bin/assistant-litellm && chown -R litellm:litellm /var/lib/switchback-litellm
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/assistant-litellm"]
`
	const validEntrypoint = `
exec litellm --config /opt/switchback/assistant-litellm/config.yaml --host 0.0.0.0 --port 4000
`
	const validPolicy = `
litellm_settings:
  drop_params: false
  num_retries: 0
`
	t.Run("Dockerfile pure comment inside continued RUN", func(t *testing.T) {
		dockerfile := strings.Replace(
			validDockerfile,
			"    && useradd",
			"    # explanatory comment inside the continued RUN\n    && useradd",
			1,
		)
		if err := validateAssistantLiteLLMProductionWiring(
			validCompose,
			dockerfile,
			validEntrypoint,
		); err != nil {
			t.Fatalf("pure continued-RUN comment was rejected: %v", err)
		}
	})

	for name, dockerfile := range map[string]string{
		"wrong source": strings.Replace(
			validDockerfile,
			"images/assistant-litellm/config.yaml",
			"images/assistant-litellm/alternate.yaml",
			1,
		),
		"wrong entrypoint source": strings.Replace(
			validDockerfile,
			"images/assistant-litellm/entrypoint.sh",
			"images/assistant-litellm/alternate.sh",
			1,
		),
		"digest ARG unused by FROM": strings.Replace(
			validDockerfile,
			"FROM ${PYTHON_IMAGE}",
			"FROM docker.io/library/python:3.12-slim",
			1,
		),
		"PYTHON_IMAGE redefined": strings.Replace(
			validDockerfile,
			"FROM ${PYTHON_IMAGE}",
			"ARG PYTHON_IMAGE=docker.io/library/python:latest\nFROM ${PYTHON_IMAGE}",
			1,
		),
		"PYTHON_IMAGE digest changed": strings.Replace(
			validDockerfile,
			"56249d7a2f93306106f6d8bcdf6423afb73c1b747d874febcc778beee25cb8bb",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			1,
		),
		"PYTHON_IMAGE ARG after FROM": strings.Replace(
			validDockerfile,
			"ARG PYTHON_IMAGE=docker.io/library/python@sha256:56249d7a2f93306106f6d8bcdf6423afb73c1b747d874febcc778beee25cb8bb\n\nFROM ${PYTHON_IMAGE}",
			"FROM ${PYTHON_IMAGE}\nARG PYTHON_IMAGE=docker.io/library/python@sha256:56249d7a2f93306106f6d8bcdf6423afb73c1b747d874febcc778beee25cb8bb",
			1,
		),
		"multiple FROM stages": strings.Replace(
			validDockerfile,
			"FROM ${PYTHON_IMAGE}",
			"FROM scratch AS ignored\nFROM ${PYTHON_IMAGE}",
			1,
		),
		"escape parser directive": "# escape=`\n" + validDockerfile,
		"syntax parser directive": "# syntax=docker/dockerfile:1\n" + validDockerfile,
		"check parser directive":  "# check=skip=JSONArgsRecommended\n" + validDockerfile,
		"overridden destination": validDockerfile +
			"COPY alternate.yaml /opt/switchback/assistant-litellm/config.yaml\n",
		"non-final stage": validDockerfile + `
FROM scratch
ENTRYPOINT ["/bin/false"]
`,
		"final stage entrypoint override": validDockerfile +
			"ENTRYPOINT [\"/bin/false\"]\n",
		"final stage parent copy override": validDockerfile +
			"COPY alternate/config.yaml /opt/switchback/assistant-litellm/\n",
		"final stage run override": validDockerfile +
			"RUN printf 'drop_params: true' > /opt/switchback/assistant-litellm/config.yaml\n",
		"final stage entrypoint run override": validDockerfile +
			"RUN printf '#!/bin/sh\\nexit 0\\n' > /usr/local/bin/assistant-litellm\n",
		"WORKDIR relative config run override": strings.Replace(
			validDockerfile,
			"COPY images/assistant-litellm/config.yaml",
			"WORKDIR /opt/switchback/assistant-litellm\nCOPY images/assistant-litellm/config.yaml",
			1,
		) + "RUN printf 'drop_params: true' > config.yaml\n",
		"WORKDIR relative entrypoint run override": strings.Replace(
			validDockerfile,
			"COPY images/assistant-litellm/config.yaml",
			"WORKDIR /usr/local/bin\nCOPY images/assistant-litellm/config.yaml",
			1,
		) + "RUN printf '#!/bin/sh\\nexit 0\\n' > assistant-litellm\n",
		"litellm binary replacement": strings.Replace(
			validDockerfile,
			"COPY images/assistant-litellm/config.yaml",
			"RUN ln -sf /bin/false /usr/local/bin/litellm\nCOPY images/assistant-litellm/config.yaml",
			1,
		),
		"pre-copy ENV": strings.Replace(
			validDockerfile,
			"COPY images/assistant-litellm/config.yaml",
			"ENV LITELLM_CONFIG=/tmp/alternate.yaml\nCOPY images/assistant-litellm/config.yaml",
			1,
		),
		"extra EXPOSE": validDockerfile + "EXPOSE 4000\n",
	} {
		t.Run("Dockerfile "+name, func(t *testing.T) {
			if err := validateAssistantLiteLLMProductionWiring(validCompose, dockerfile, validEntrypoint); err == nil {
				t.Fatal("invalid Assistant LiteLLM Dockerfile wiring was accepted")
			}
		})
	}
	t.Run("entrypoint alternate config", func(t *testing.T) {
		entrypoint := strings.Replace(
			validEntrypoint,
			"/opt/switchback/assistant-litellm/config.yaml",
			"/tmp/alternate.yaml",
			1,
		)
		if err := validateAssistantLiteLLMProductionWiring(validCompose, validDockerfile, entrypoint); err == nil {
			t.Fatal("invalid Assistant LiteLLM entrypoint wiring was accepted")
		}
	})

	for name, compose := range map[string]string{
		"wrong Dockerfile": strings.Replace(
			validCompose,
			"images/assistant-litellm/Dockerfile",
			"images/assistant-litellm/alternate.Dockerfile",
			1,
		),
		"build target": validCompose +
			"      target: alternate-stage\n",
		"build arguments": validCompose + `
      args:
        PYTHON_IMAGE: docker.io/library/python:latest
`,
		"unrelated build arguments": validCompose + `
      args:
        UNUSED: forbidden
`,
		"entrypoint override": validCompose +
			"    entrypoint: [\"/bin/false\"]\n",
		"command override": validCompose +
			"    command: [\"--config\", \"/tmp/alternate.yaml\"]\n",
		"extends override": validCompose + `
    extends:
      service: alternate-assistant-litellm
`,
		"config mount override": validCompose + `
    volumes:
      - type: bind
        source: ./alternate.yaml
        target: /opt/switchback/assistant-litellm/config.yaml
`,
		"tmpfs directory override": validCompose + `
    tmpfs:
      - /opt/switchback/assistant-litellm
`,
		"scalar tmpfs directory override": validCompose +
			"    tmpfs: /opt/switchback/assistant-litellm\n",
		"secret config override": validCompose + `
    secrets:
      - source: alternate
        target: /opt/switchback/assistant-litellm/config.yaml
`,
	} {
		t.Run("Compose "+name, func(t *testing.T) {
			if err := validateAssistantLiteLLMProductionWiring(compose, validDockerfile, validEntrypoint); err == nil {
				t.Fatal("invalid Assistant LiteLLM Compose wiring was accepted")
			}
		})
	}
	t.Run("Compose scalar tmpfs", func(t *testing.T) {
		compose := validCompose + "    tmpfs: /tmp:size=64m\n"
		if err := validateAssistantLiteLLMProductionWiring(compose, validDockerfile, validEntrypoint); err != nil {
			t.Fatalf("valid scalar Compose tmpfs was rejected: %v", err)
		}
	})

	for name, policy := range map[string]string{
		"duplicate drop_params": validPolicy + "  drop_params: false\n",
		"conflicting drop_params": strings.Replace(
			validPolicy,
			"  num_retries: 0",
			"  drop_params: true\n  num_retries: 0",
			1,
		),
		"duplicate num_retries": validPolicy + "  num_retries: 0\n",
		"conflicting num_retries": strings.Replace(
			validPolicy,
			"  num_retries: 0",
			"  num_retries: 2",
			1,
		),
		"spaced conflicting drop_params":  validPolicy + "  drop_params : true\n",
		"quoted conflicting drop_params":  validPolicy + "  \"drop_params\": true\n",
		"escaped conflicting drop_params": validPolicy + "  \"drop\\u005fparams\": true\n",
		"tagged conflicting drop_params":  validPolicy + "  !!str drop_params: true\n",
		"quoted conflicting num_retries":  validPolicy + "  'num_retries' : 2\n",
		"aliased conflicting drop_params": `
litellm_settings:
  &drop_key drop_params: false
  *drop_key: true
  num_retries: 0
`,
	} {
		t.Run("policy "+name, func(t *testing.T) {
			if err := validateAssistantLiteLLMFailClosedPolicy(policy); err == nil {
				t.Fatal("invalid Assistant LiteLLM fail-closed policy was accepted")
			}
		})
	}
}

func validateAssistantLiteLLMProductionWiring(compose, dockerfile, entrypoint string) error {
	const (
		configurationPath = "/opt/switchback/assistant-litellm/config.yaml"
		expectedExec      = "exec litellm --config " + configurationPath +
			" --host 0.0.0.0 --port 4000"
	)
	if err := validateAssistantLiteLLMCompose(compose); err != nil {
		return err
	}
	if err := validateAssistantLiteLLMDockerfileRecipe(dockerfile); err != nil {
		return err
	}
	var liteLLMExecs []string
	for _, line := range strings.Split(entrypoint, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "exec litellm") {
			liteLLMExecs = append(liteLLMExecs, line)
		}
	}
	if len(liteLLMExecs) != 1 || liteLLMExecs[0] != expectedExec {
		return fmt.Errorf(
			"Assistant LiteLLM entrypoint must contain exactly one production config exec %q; got %q",
			expectedExec,
			liteLLMExecs,
		)
	}
	return nil
}

func validateAssistantLiteLLMFailClosedPolicy(configuration string) error {
	root, err := parseSingleYAMLMapping("Assistant LiteLLM config", configuration)
	if err != nil {
		return err
	}
	if err := rejectYAMLMergeKeys(root, "Assistant LiteLLM config"); err != nil {
		return err
	}
	settings, err := requiredYAMLMappingValue(root, "litellm_settings")
	if err != nil {
		return fmt.Errorf("Assistant LiteLLM config: %w", err)
	}
	if settings.Kind != yaml.MappingNode {
		return fmt.Errorf("Assistant LiteLLM litellm_settings must be a mapping")
	}
	if err := rejectYAMLMergeKeys(settings, "Assistant LiteLLM litellm_settings"); err != nil {
		return err
	}
	dropParams, err := requiredYAMLMappingValue(settings, "drop_params")
	if err != nil {
		return fmt.Errorf("Assistant LiteLLM litellm_settings: %w", err)
	}
	var drop bool
	if dropParams.Kind != yaml.ScalarNode || dropParams.Decode(&drop) != nil || drop {
		return fmt.Errorf("Assistant LiteLLM drop_params must be boolean false")
	}
	numRetries, err := requiredYAMLMappingValue(settings, "num_retries")
	if err != nil {
		return fmt.Errorf("Assistant LiteLLM litellm_settings: %w", err)
	}
	var retries int
	if numRetries.Kind != yaml.ScalarNode || numRetries.Decode(&retries) != nil || retries != 0 {
		return fmt.Errorf("Assistant LiteLLM num_retries must be integer 0")
	}
	return nil
}

func validateAssistantLiteLLMCompose(compose string) error {
	root, err := parseSingleYAMLMapping("local Compose", compose)
	if err != nil {
		return err
	}
	services, err := requiredYAMLMappingValue(root, "services")
	if err != nil {
		return fmt.Errorf("local Compose: %w", err)
	}
	service, err := requiredYAMLMappingValue(services, "assistant-litellm")
	if err != nil {
		return fmt.Errorf("local Compose services: %w", err)
	}
	if service.Kind != yaml.MappingNode {
		return fmt.Errorf("local Compose assistant-litellm service must be a mapping")
	}
	if err := rejectYAMLMergeKeys(service, "local Compose assistant-litellm service"); err != nil {
		return err
	}
	build, err := requiredYAMLMappingValue(service, "build")
	if err != nil {
		return fmt.Errorf("local Compose assistant-litellm service: %w", err)
	}
	if build.Kind != yaml.MappingNode {
		return fmt.Errorf("local Compose assistant-litellm build must be a mapping")
	}
	if err := rejectYAMLMergeKeys(build, "local Compose assistant-litellm build"); err != nil {
		return err
	}
	for key, want := range map[string]string{
		"context":    "../..",
		"dockerfile": "images/assistant-litellm/Dockerfile",
	} {
		node, valueErr := requiredYAMLMappingValue(build, key)
		if valueErr != nil {
			return fmt.Errorf("local Compose assistant-litellm build: %w", valueErr)
		}
		var got string
		if node.Kind != yaml.ScalarNode || node.Decode(&got) != nil || got != want {
			return fmt.Errorf("local Compose assistant-litellm build %s must be %q", key, want)
		}
	}
	targets, err := yamlMappingValues(build, "target")
	if err != nil {
		return err
	}
	if len(targets) != 0 {
		return fmt.Errorf("local Compose assistant-litellm build must not select a target stage")
	}
	arguments, err := yamlMappingValues(build, "args")
	if err != nil {
		return err
	}
	if len(arguments) != 0 {
		return fmt.Errorf("local Compose assistant-litellm build must not override build arguments")
	}
	for _, forbidden := range []string{"entrypoint", "command", "configs", "extends"} {
		values, valueErr := yamlMappingValues(service, forbidden)
		if valueErr != nil {
			return valueErr
		}
		if len(values) != 0 {
			return fmt.Errorf("local Compose assistant-litellm service must not override %s", forbidden)
		}
	}
	volumes, err := optionalYAMLMappingValue(service, "volumes")
	if err != nil {
		return err
	}
	if volumes != nil {
		if volumes.Kind != yaml.SequenceNode {
			return fmt.Errorf("local Compose assistant-litellm volumes must be a sequence")
		}
		for _, volume := range volumes.Content {
			target, targetErr := composeVolumeTarget(volume)
			if targetErr != nil {
				return targetErr
			}
			if mountCoversAssistantLiteLLMConfig(target) {
				return fmt.Errorf(
					"local Compose assistant-litellm volume must not override production config: %q",
					target,
				)
			}
		}
	}
	tmpfs, err := optionalYAMLMappingValue(service, "tmpfs")
	if err != nil {
		return err
	}
	if tmpfs != nil {
		switch tmpfs.Kind {
		case yaml.ScalarNode:
			if err := validateAssistantLiteLLMTmpfsMount(tmpfs); err != nil {
				return err
			}
		case yaml.SequenceNode:
			for _, mount := range tmpfs.Content {
				if err := validateAssistantLiteLLMTmpfsMount(mount); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("local Compose assistant-litellm tmpfs must be a string or sequence")
		}
	}
	secrets, err := optionalYAMLMappingValue(service, "secrets")
	if err != nil {
		return err
	}
	if secrets != nil {
		if secrets.Kind != yaml.SequenceNode {
			return fmt.Errorf("local Compose assistant-litellm secrets must be a sequence")
		}
		for _, secret := range secrets.Content {
			if secret.Kind == yaml.ScalarNode {
				continue
			}
			if secret.Kind != yaml.MappingNode {
				return fmt.Errorf("local Compose assistant-litellm secret must be scalar or mapping")
			}
			target, targetErr := optionalYAMLMappingValue(secret, "target")
			if targetErr != nil {
				return targetErr
			}
			if target == nil {
				continue
			}
			var value string
			if target.Kind != yaml.ScalarNode || target.Decode(&value) != nil {
				return fmt.Errorf("local Compose assistant-litellm secret target must be a string")
			}
			if mountCoversAssistantLiteLLMConfig(value) {
				return fmt.Errorf(
					"local Compose assistant-litellm secret must not override production config: %q",
					value,
				)
			}
		}
	}
	return nil
}

func validateAssistantLiteLLMDockerfileRecipe(dockerfile string) error {
	const (
		pythonImageArgument = "PYTHON_IMAGE=docker.io/library/python@sha256:" +
			"56249d7a2f93306106f6d8bcdf6423afb73c1b747d874febcc778beee25cb8bb"
		proxyInstallation = "groupadd --gid 10001 litellm && " +
			"useradd --uid 10001 --gid litellm --create-home --home-dir " +
			"/var/lib/switchback-litellm --shell /usr/sbin/nologin litellm && " +
			"pip install --no-cache-dir 'litellm[proxy]==1.93.0' && " +
			`python -c "import importlib.metadata as m; assert m.version('litellm') == '1.93.0'" && ` +
			"litellm --version"
		entrypointSetup = "chmod 0555 /usr/local/bin/assistant-litellm && " +
			"chown -R litellm:litellm /var/lib/switchback-litellm"
	)
	instructions, err := dockerInstructions(dockerfile)
	if err != nil {
		return err
	}
	expected := []dockerInstruction{
		{operation: "ARG", arguments: pythonImageArgument},
		{operation: "FROM", arguments: "${PYTHON_IMAGE}"},
		{operation: "RUN", arguments: proxyInstallation},
		{
			operation: "COPY",
			arguments: "images/assistant-litellm/config.yaml " +
				"/opt/switchback/assistant-litellm/config.yaml",
		},
		{
			operation: "COPY",
			arguments: "images/assistant-litellm/entrypoint.sh " +
				"/usr/local/bin/assistant-litellm",
		},
		{operation: "RUN", arguments: entrypointSetup},
		{operation: "USER", arguments: "10001:10001"},
		{operation: "ENTRYPOINT", arguments: `["/usr/local/bin/assistant-litellm"]`},
	}
	if len(instructions) != len(expected) {
		return fmt.Errorf(
			"Assistant LiteLLM Dockerfile must contain exactly %d production recipe instructions; got %d",
			len(expected),
			len(instructions),
		)
	}
	for index, want := range expected {
		if got := instructions[index]; got != want {
			return fmt.Errorf(
				"Assistant LiteLLM Dockerfile instruction %d must be %s %s; got %s %s",
				index+1,
				want.operation,
				want.arguments,
				got.operation,
				got.arguments,
			)
		}
	}
	return nil
}

func parseSingleYAMLMapping(label, content string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse %s YAML: %w", label, err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("parse trailing %s YAML: %w", label, err)
		}
		return nil, fmt.Errorf("%s must contain exactly one YAML document", label)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 ||
		document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must contain one YAML mapping document", label)
	}
	return document.Content[0], nil
}

func yamlMappingValues(mapping *yaml.Node, key string) ([]*yaml.Node, error) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("YAML %q owner must be a mapping", key)
	}
	var values []*yaml.Node
	for index := 0; index < len(mapping.Content); index += 2 {
		keyNode := mapping.Content[index]
		if keyNode.Kind == yaml.AliasNode {
			keyNode = keyNode.Alias
		}
		if keyNode.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("YAML mapping keys must be scalar")
		}
		var decoded string
		if err := keyNode.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("decode YAML mapping key: %w", err)
		}
		if decoded == key {
			values = append(values, mapping.Content[index+1])
		}
	}
	return values, nil
}

func requiredYAMLMappingValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	values, err := yamlMappingValues(mapping, key)
	if err != nil {
		return nil, err
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("YAML mapping must contain exactly one %q key; got %d", key, len(values))
	}
	return values[0], nil
}

func optionalYAMLMappingValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	values, err := yamlMappingValues(mapping, key)
	if err != nil {
		return nil, err
	}
	if len(values) > 1 {
		return nil, fmt.Errorf("YAML mapping must contain at most one %q key; got %d", key, len(values))
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values[0], nil
}

func rejectYAMLMergeKeys(mapping *yaml.Node, label string) error {
	values, err := yamlMappingValues(mapping, "<<")
	if err != nil {
		return err
	}
	if len(values) != 0 {
		return fmt.Errorf("%s must not use YAML merge keys", label)
	}
	return nil
}

func composeVolumeTarget(volume *yaml.Node) (string, error) {
	switch volume.Kind {
	case yaml.MappingNode:
		target, err := requiredYAMLMappingValue(volume, "target")
		if err != nil {
			return "", fmt.Errorf("local Compose assistant-litellm volume: %w", err)
		}
		var value string
		if target.Kind != yaml.ScalarNode || target.Decode(&value) != nil {
			return "", fmt.Errorf("local Compose assistant-litellm volume target must be a string")
		}
		return value, nil
	case yaml.ScalarNode:
		var value string
		if err := volume.Decode(&value); err != nil {
			return "", fmt.Errorf("decode local Compose assistant-litellm volume: %w", err)
		}
		parts := strings.Split(value, ":")
		switch len(parts) {
		case 1:
			return parts[0], nil
		case 2:
			return parts[1], nil
		default:
			return parts[len(parts)-2], nil
		}
	default:
		return "", fmt.Errorf("local Compose assistant-litellm volume must be scalar or mapping")
	}
}

func mountCoversAssistantLiteLLMConfig(target string) bool {
	const configurationPath = "/opt/switchback/assistant-litellm/config.yaml"
	clean := pathpkg.Clean(target)
	return clean == configurationPath ||
		strings.HasPrefix(configurationPath, strings.TrimSuffix(clean, "/")+"/")
}

func validateAssistantLiteLLMTmpfsMount(mount *yaml.Node) error {
	var value string
	if mount.Kind != yaml.ScalarNode || mount.Decode(&value) != nil {
		return fmt.Errorf("local Compose assistant-litellm tmpfs target must be a string")
	}
	target := strings.SplitN(value, ":", 2)[0]
	if mountCoversAssistantLiteLLMConfig(target) {
		return fmt.Errorf(
			"local Compose assistant-litellm tmpfs must not override production config: %q",
			target,
		)
	}
	return nil
}

type dockerInstruction struct {
	operation string
	arguments string
}

func dockerInstructions(dockerfile string) ([]dockerInstruction, error) {
	lines, err := dockerLogicalLines(dockerfile)
	if err != nil {
		return nil, err
	}
	instructions := make([]dockerInstruction, 0, len(lines))
	for _, line := range lines {
		separator := strings.IndexFunc(line, unicode.IsSpace)
		if separator < 0 {
			separator = len(line)
		}
		instructions = append(instructions, dockerInstruction{
			operation: strings.ToUpper(line[:separator]),
			arguments: strings.TrimSpace(line[separator:]),
		})
	}
	return instructions, nil
}

func dockerLogicalLines(dockerfile string) ([]string, error) {
	var lines []string
	var continued strings.Builder
	for _, raw := range strings.Split(dockerfile, "\n") {
		line := strings.TrimSpace(raw)
		if continued.Len() == 0 {
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "#") {
				if isDockerParserDirective(line) {
					return nil, fmt.Errorf(
						"Assistant LiteLLM Dockerfile must not use parser directive %q",
						line,
					)
				}
				continue
			}
		} else if strings.HasPrefix(line, "#") {
			// Docker ignores a pure comment line inside a continued
			// instruction; it does not terminate that logical instruction.
			continue
		}
		hasContinuation := strings.HasSuffix(line, "\\")
		if hasContinuation {
			line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		}
		if continued.Len() != 0 {
			continued.WriteByte(' ')
		}
		continued.WriteString(line)
		if hasContinuation {
			continue
		}
		lines = append(lines, strings.TrimSpace(continued.String()))
		continued.Reset()
	}
	if continued.Len() != 0 {
		return nil, fmt.Errorf("Assistant LiteLLM Dockerfile ends with an incomplete continuation")
	}
	return lines, nil
}

func isDockerParserDirective(line string) bool {
	comment := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
	key, _, found := strings.Cut(comment, "=")
	if !found {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "syntax", "escape", "check":
		return true
	default:
		return false
	}
}

func containsExactToken(content, token string) bool {
	pattern := `(^|[^[:alnum:]_-])` + regexp.QuoteMeta(token) + `($|[^[:alnum:]_-])`
	return regexp.MustCompile(pattern).MatchString(content)
}
