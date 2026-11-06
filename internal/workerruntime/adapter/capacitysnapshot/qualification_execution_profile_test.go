package capacitysnapshot

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestQualificationExecutionProfileClosedDTO(t *testing.T) {
	for _, raw := range []string{
		``, `{}`, `null`, `[]`, `"worker_delivery"`,
		`{"schema":"switchback.qualification.execution_profile.v1"}`,
		`{"mode":"worker_delivery"}`,
		`{"schema":null,"mode":"worker_delivery"}`,
		`{"schema":1,"mode":"worker_delivery"}`,
		`{"schema":"switchback.qualification.execution_profile.v2","mode":"worker_delivery"}`,
		`{"schema":"switchback.qualification.execution_profile.v1","mode":true}`,
		`{"schema":"switchback.qualification.execution_profile.v1","mode":null}`,
		`{"schema":"switchback.qualification.execution_profile.v1","mode":"Worker_Delivery"}`,
		`{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery "}`,
		`{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery","image":"override"}`,
		`{"schema":"switchback.qualification.execution_profile.v1","mode":"disabled","Mode":"worker_delivery"}`,
		`{"schema":"switchback.qualification.execution_profile.v1","mode":"disabled","mode":"worker_delivery"}`,
		`{"schema":"switchback.qualification.execution_profile.v1","schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery"}`,
		`{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery"} {}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := qualificationDecodeExecutionProfile([]byte(raw)); err == nil {
				t.Fatal("invalid existing profile was accepted")
			}
		})
	}
}

func TestQualificationExecutionProfileLegacyEnvironmentUnchanged(t *testing.T) {
	profile := qualificationExecutionProfile{Schema: qualificationExecutionProfileSchema, Mode: "disabled"}
	env, err := qualificationExecutionBackendEnvironment(profile, "ignored-when-disabled")
	if err != nil || !reflect.DeepEqual(env, qualificationBackendEnvironment()) {
		t.Fatal("explicit disabled altered legacy environment", err)
	}
	profile.Mode = "worker_delivery"
	for _, image := range []string{"", "switchback-worker:qualification-candidate", "sha256:short", "sha256:" + strings.Repeat("C", 64)} {
		if _, err := qualificationExecutionBackendEnvironment(profile, image); err == nil {
			t.Fatal("worker mode accepted an image other than an actual canonical image ID", image)
		}
	}
}

func executionProfileBackendRole(t *testing.T, worker bool) (qualificationRole, qualificationStaged, string) {
	t.Helper()
	s := qualificationStaticIntentProjection(qualificationIntentFixture(t))
	profile := qualificationExecutionProfile{Schema: qualificationExecutionProfileSchema, Mode: "disabled"}
	if worker {
		profile.Mode = "worker_delivery"
	}
	env, err := qualificationExecutionBackendEnvironment(profile, s.Binding.WorkerImageDigest)
	if err != nil {
		t.Fatal(err)
	}
	password := strings.Repeat("a", 64)
	env = append(env, qualificationEnv{qualificationDatabaseMarker, Digest([]byte(qualificationDatabaseURL(password)))})
	for i := range s.Source.Inputs.Roles {
		if s.Source.Inputs.Roles[i].Role == "backend" {
			s.Source.Inputs.Roles[i].Environment = env
			return s.Source.Inputs.Roles[i], s, password
		}
	}
	t.Fatal("fixture has no Backend")
	return qualificationRole{}, s, ""
}

func TestQualificationExecutionProfileCreatesFixedNetworks(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		backend, s, password := executionProfileBackendRole(t, enabled)
		for _, role := range s.Source.Inputs.Roles {
			if role.Kind != "container" {
				continue
			}
			if role.Role == "backend" {
				role = backend
			}
			if role.Role == "postgres" {
				role.Environment = []qualificationEnv{{"POSTGRES_USER", "switchback"}, {"POSTGRES_DB", "postgres"}, {"POSTGRES_INITDB_WALDIR", "/var/lib/postgresql/wal"}, {"PGDATA", "/var/lib/postgresql/data/pgdata"}, {qualificationPasswordMarker, Digest([]byte(password))}}
			}
			t.Run(role.Role+"/"+profileTestBool(enabled), func(t *testing.T) {
				options, err := qualificationCoreOptions(role, s, password)
				if err != nil {
					t.Fatal(err)
				}
				wantCount := 1
				if enabled && role.Role == "backend" {
					wantCount = 2
				}
				if options.HostConfig.NetworkMode != "switchback-core1011_core" || len(options.NetworkingConfig.EndpointsConfig) != wantCount {
					t.Fatalf("wrong Create networks: %+v", options.NetworkingConfig)
				}
				wantAlias := map[string]string{"backend": "switchback-backend", "postgres": "postgres", "web": "switchback-web"}[role.Role]
				for name, endpoint := range options.NetworkingConfig.EndpointsConfig {
					if (name != "switchback-core1011_core" && !(enabled && role.Role == "backend" && name == "switchback-m1-worker-control")) ||
						!reflect.DeepEqual(endpoint.Aliases, []string{wantAlias}) {
						t.Fatalf("wrong fixed endpoint: %s %+v", name, endpoint)
					}
				}
			})
		}
	}
}

func profileTestBool(value bool) string {
	if value {
		return "worker_delivery"
	}
	return "disabled"
}

func TestQualificationExecutionProfileFrozenIntentSurvivesSelectorChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "execution-profile.json")
	if err := os.WriteFile(path, []byte(`{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery"}`), 0400); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	profile, err := qualificationExecutionProfileAt(context.Background(), dir, executionProfileFixtureStat)
	if err != nil {
		t.Fatal(err)
	}
	role, staged, password := executionProfileBackendRole(t, true)
	env, err := qualificationExecutionBackendEnvironment(profile, staged.Binding.WorkerImageDigest)
	if err != nil {
		t.Fatal(err)
	}
	role.Environment = append(env, qualificationEnv{qualificationDatabaseMarker, Digest([]byte(qualificationDatabaseURL(password)))})
	for i := range staged.Source.Inputs.Roles {
		if staged.Source.Inputs.Roles[i].Role == "backend" {
			staged.Source.Inputs.Roles[i] = role
		}
	}
	staticDigest := qualificationStaticIntentDigest(staged)
	before, err := qualificationCoreOptions(role, staged, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":"switchback.qualification.execution_profile.v1","mode":"disabled"}`), 0400); err != nil {
		t.Fatal(err)
	}
	after, err := qualificationCoreOptions(role, staged, password)
	if err != nil || !reflect.DeepEqual(before, after) || staticDigest != qualificationStaticIntentDigest(staged) {
		t.Fatal("mutable selector changed already frozen Create intent", err)
	}
	drifted := qualificationClone(staged)
	for i := range drifted.Source.Inputs.Roles {
		if drifted.Source.Inputs.Roles[i].Role == "backend" {
			for j := range drifted.Source.Inputs.Roles[i].Environment {
				if drifted.Source.Inputs.Roles[i].Environment[j].Name == "SWITCHBACK_WORKER_IMAGE" {
					drifted.Source.Inputs.Roles[i].Environment[j].Value = "sha256:" + strings.Repeat("8", 64)
				}
			}
		}
	}
	if staticDigest == qualificationStaticIntentDigest(drifted) {
		t.Fatal("static intent did not bind the complete execution environment")
	}
}

func TestQualificationExecutionProfileCreateRejectsIncompleteFrozenEnvironment(t *testing.T) {
	for _, key := range []string{"SWITCHBACK_WORKER_NETWORK", "SWITCHBACK_BACKEND_GRPC_TARGET", "SWITCHBACK_BACKEND_HTTP_BASE_URL", "SWITCHBACK_KIMI_CREDENTIAL_FILE", "SWITCHBACK_DEEPSEEK_CREDENTIAL_FILE", "SWITCHBACK_M2_ASSISTANT_ENABLED", "SWITCHBACK_M1_WORKER_DELIVERY_ENABLED"} {
		t.Run(key, func(t *testing.T) {
			role, staged, password := executionProfileBackendRole(t, true)
			for i := range role.Environment {
				if role.Environment[i].Name == key {
					role.Environment[i].Value = "foreign"
				}
			}
			if _, err := qualificationCoreOptions(role, staged, password); err == nil {
				t.Fatal("Create accepted changed execution material")
			}
		})
	}
}
