package capacitysnapshot

import "strings"

// Test-only adapter lets the external composition test exercise LoadConfig
// without adding a production API or introducing a package import cycle.
func QualificationExecutionEnvironmentForTest(raw []byte, workerImage string) (map[string]string, error) {
	profile := qualificationExecutionProfile{Schema: qualificationExecutionProfileSchema, Mode: "disabled"}
	if raw != nil {
		var err error
		profile, err = qualificationDecodeExecutionProfile(raw)
		if err != nil {
			return nil, err
		}
	}
	env, err := qualificationExecutionBackendEnvironment(profile, workerImage)
	if err != nil {
		return nil, err
	}
	values := map[string]string{"SWITCHBACK_DATABASE_URL": qualificationDatabaseURL(strings.Repeat("a", 64))}
	for _, entry := range env {
		values[entry.Name] = entry.Value
	}
	return values, nil
}
