package capacitysnapshot_test

import (
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/localproduct"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

func TestQualificationExecutionProfileLoadsRealProductConfiguration(t *testing.T) {
	workerImage := "sha256:" + strings.Repeat("c", 64)
	for _, test := range []struct {
		name    string
		profile []byte
		enabled bool
	}{
		{name: "missing"},
		{name: "explicit_disabled", profile: []byte(`{"schema":"switchback.qualification.execution_profile.v1","mode":"disabled"}`)},
		{name: "worker_delivery", profile: []byte(`{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery"}`), enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, err := capacitysnapshot.QualificationExecutionEnvironmentForTest(test.profile, workerImage)
			if err != nil {
				t.Fatal(err)
			}
			configuration, err := localproduct.LoadConfig(func(key string) (string, bool) {
				value, ok := env[key]
				return value, ok
			})
			if err != nil {
				t.Fatalf("real product rejected qualification environment: %v", err)
			}
			if configuration.ExecutionEnabled != test.enabled || configuration.Assistant.Enabled {
				t.Fatalf("execution=%v Assistant=%v", configuration.ExecutionEnabled, configuration.Assistant.Enabled)
			}
			if !test.enabled {
				if configuration.WorkerImage != "" || configuration.KimiCredentialFile != "" || configuration.DeepSeekCredentialFile != "" {
					t.Fatal("disabled profile added execution material")
				}
				return
			}
			if configuration.WorkerImage != workerImage || configuration.WorkerNetwork != "switchback-m1-worker-control" ||
				configuration.BackendGRPCTarget != "switchback-backend:9090" || configuration.BackendHTTPBaseURL != "http://switchback-backend:8080" ||
				configuration.KimiCredentialFile != "/var/lib/switchback/provider-credentials/kimi.key" || configuration.DeepSeekCredentialFile != "/var/lib/switchback/provider-credentials/deepseek.key" {
				t.Fatal("loaded execution configuration lost exact image/network/credential path bindings")
			}
		})
	}
}
