//go:build observability_native

package architecture_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Root/operator opts in: this test owns only its fresh, uniquely named volume
// and container. It never uses the local deployment's volume or network.
func TestLocalObservabilityAlloyFreshVolumeNative(t *testing.T) {
	if os.Getenv("SWITCHBACK_TEST_ALLOY_NATIVE") != "1" {
		t.Skip("root-owned Docker acceptance requires explicit opt-in")
	}
	compose, err := os.ReadFile("../../deploy/local/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(compose), "\n  alloy:\n")
	if !ok {
		t.Fatal("missing Alloy")
	}
	block = strings.Split(block, "\n  grafana:")[0]
	image := regexp.MustCompile(`image: (\S+)`).FindStringSubmatch(block)
	user := regexp.MustCompile(`user: "([0-9]+:[0-9]+)"`).FindStringSubmatch(block)
	if len(image) != 2 || len(user) != 2 {
		t.Fatal("explicit Alloy image/user required")
	}
	config, err := filepath.Abs("../../deploy/local/observability/alloy.alloy")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("switchback-test-alloy-%d", time.Now().UnixNano())
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v: %s", args[0], err, out)
		}
		return string(out)
	}
	run("volume", "create", "--label", "switchback.test=alloy-fresh-volume", name)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", name).Run() })
	run("create", "--name", name, "--label", "switchback.test=alloy-fresh-volume", "--user", user[1], "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--network", "none", "--mount", "type=volume,src="+name+",dst=/var/lib/alloy", "--mount", "type=bind,src="+config+",dst=/etc/alloy/config.alloy,readonly", image[1], "run", "--server.http.listen-addr=0.0.0.0:12346", "--storage.path=/var/lib/alloy", "--stability.level=experimental", "--disable-reporting", "/etc/alloy/config.alloy")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	run("start", name)
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, err := exec.Command("docker", "exec", name, "sh", "-c", "test -d /var/lib/alloy/remotecfg && test -w /var/lib/alloy/remotecfg && test $(id -u) = 473").CombinedOutput()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fresh volume initialization: %s; logs: %s", out, run("logs", name))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if state := strings.TrimSpace(run("inspect", "--format", "{{.State.Running}}", name)); state != "true" {
		t.Fatal("Alloy stopped after fresh volume initialization")
	}
}
