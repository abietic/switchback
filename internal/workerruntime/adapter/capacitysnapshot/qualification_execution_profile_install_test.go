package capacitysnapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQualificationExecutionProfileInstallCandidateNames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		leaves  []string
		unknown string
	}{
		{name: "legacy empty"},
		{name: "legacy material", leaves: []string{"static-intent.json", "candidate.json", "prepared-trust.json", "claimed-install.json"}},
		{name: "fixed private profile", leaves: []string{"execution-profile.json"}},
		{name: "profile with legacy material", leaves: []string{"execution-profile.json", "static-intent.json", "candidate.json", "claimed-install.json"}},
		{name: "profile with unknown", leaves: []string{"execution-profile.json", "unexpected.json"}, unknown: "unexpected.json"},
		{name: "lookalike suffix", leaves: []string{"execution-profile.json.next"}, unknown: "execution-profile.json.next"},
		{name: "lookalike case", leaves: []string{"Execution-profile.json"}, unknown: "Execution-profile.json"},
		{name: "unknown", leaves: []string{"unexpected.json"}, unknown: "unexpected.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise real directory enumeration without touching the fixed guest
			// path. The fixture has private directory/read-only leaf permissions;
			// ownership stays with the unprivileged test user at this name seam.
			root := filepath.Join(t.TempDir(), "qualification-candidate")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.leaves {
				raw := []byte(`{}`)
				if name == "execution-profile.json" {
					raw = []byte(`{"schema":"switchback.qualification.execution_profile.v1","mode":"worker_delivery"}`)
				}
				if err := os.WriteFile(filepath.Join(root, name), raw, 0400); err != nil {
					t.Fatal(err)
				}
			}
			file, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			err = qualificationInstallCandidateNames(qualificationCandidateDirectory{file: file, uid: uint32(os.Getuid())})
			if tc.unknown == "" {
				if err != nil {
					t.Fatalf("approved candidate rejected: %v", err)
				}
			} else if want := "unknown qualification candidate material: " + tc.unknown; err == nil || err.Error() != want {
				t.Fatalf("unknown leaf rejection = %v, want %q", err, want)
			}
		})
	}
}
