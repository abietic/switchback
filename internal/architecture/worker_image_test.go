package architecture_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type workerImageProvenance struct {
	SchemaVersion   string `json:"schema_version"`
	GoImage         string `json:"go_image"`
	NodeImage       string `json:"node_image"`
	PythonImage     string `json:"python_image"`
	GoVersion       string `json:"go_version"`
	ACPGoSDKVersion string `json:"acp_go_sdk_version"`
	CodexACPVersion string `json:"codex_acp_version"`
	CodexVersion    string `json:"codex_version"`
	LiteLLMVersion  string `json:"litellm_version"`
	RuntimeUIDs     struct {
		LiteLLM   int `json:"litellm"`
		CodexTool int `json:"codex_tool"`
	} `json:"runtime_uids"`
}

func TestWorkerImageProvenanceMatchesBuildAndDependencyLocks(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return data
	}
	var provenance workerImageProvenance
	if err := json.Unmarshal(read("images/worker/provenance.json"), &provenance); err != nil {
		t.Fatalf("decode Worker provenance: %v", err)
	}
	if provenance.SchemaVersion != "switchback.worker-image-provenance.v1" || provenance.GoVersion != "1.26.4" ||
		provenance.ACPGoSDKVersion != "v0.13.5" || provenance.CodexACPVersion != "1.1.2" ||
		provenance.CodexVersion != "0.144.1" || provenance.LiteLLMVersion != "1.93.0" ||
		provenance.RuntimeUIDs.LiteLLM != 10001 || provenance.RuntimeUIDs.CodexTool != 10002 {
		t.Fatalf("unexpected Worker provenance: %+v", provenance)
	}
	dockerfile := string(read("images/worker/Dockerfile"))
	for _, image := range []string{provenance.GoImage, provenance.NodeImage, provenance.PythonImage} {
		if !strings.Contains(dockerfile, image) || !strings.Contains(image, "@sha256:") {
			t.Fatalf("Dockerfile does not use provenance image %q", image)
		}
	}
	for _, pin := range []string{"litellm[proxy]==1.93.0", "-trimpath", "-buildvcs=false", "--omit=dev"} {
		if !strings.Contains(dockerfile, pin) {
			t.Fatalf("Dockerfile lacks reproducibility pin %q", pin)
		}
	}
	packageLock := string(read("images/worker/package-lock.json"))
	for _, pin := range []string{`"@agentclientprotocol/codex-acp": "1.1.2"`, `"@openai/codex": "0.144.1"`} {
		if !strings.Contains(packageLock, pin) {
			t.Fatalf("Worker npm lock lacks %s", pin)
		}
	}
	goMod := string(read("go.mod"))
	if !strings.Contains(goMod, "github.com/coder/acp-go-sdk v0.13.5") {
		t.Fatal("root Go module does not pin ACP Go SDK v0.13.5")
	}
}
