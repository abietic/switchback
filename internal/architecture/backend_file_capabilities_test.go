package architecture

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBackendFileCapabilitiesAreBoundedInDeploymentTemplates(t *testing.T) {
	for _, template := range []string{"local", "experience"} {
		t.Run(template, func(t *testing.T) {
			name := "compose.yaml"
			if template == "experience" {
				name = "core.compose.yaml"
			}
			data, err := os.ReadFile(filepath.Join("..", "..", "deploy", template, name))
			if err != nil {
				t.Fatal(err)
			}
			var compose struct {
				Services map[string]struct {
					User        string   `yaml:"user"`
					CapDrop     []string `yaml:"cap_drop"`
					CapAdd      []string `yaml:"cap_add"`
					ReadOnly    bool     `yaml:"read_only"`
					SecurityOpt []string `yaml:"security_opt"`
					Privileged  bool     `yaml:"privileged"`
					NetworkMode string   `yaml:"network_mode"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal(data, &compose); err != nil {
				t.Fatal(err)
			}
			for service, profile := range compose.Services {
				if service != "switchback-backend" && len(profile.CapAdd) != 0 {
					t.Errorf("unexpected cap_add on %s: %v", service, profile.CapAdd)
				}
			}
			backend, ok := compose.Services["switchback-backend"]
			if !ok {
				t.Fatal("deployment template lacks switchback-backend")
			}
			// Exact count also rejects duplicate capabilities and ALL.
			want := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}
			sort.Strings(backend.CapAdd)
			if !reflect.DeepEqual(backend.CapAdd, want) {
				t.Fatalf("Backend cap_add = %v, want exactly %v", backend.CapAdd, want)
			}
			if backend.User != "0:0" || !reflect.DeepEqual(backend.CapDrop, []string{"ALL"}) ||
				!backend.ReadOnly || !reflect.DeepEqual(backend.SecurityOpt, []string{"no-new-privileges:true"}) ||
				backend.Privileged || backend.NetworkMode == "host" {
				t.Fatalf("Backend isolation profile drifted: %+v", backend)
			}
		})
	}
}
