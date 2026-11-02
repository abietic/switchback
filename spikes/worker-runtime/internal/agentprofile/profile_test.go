package agentprofile

import "testing"

func TestAgentProfileExactValuesAndValidation(t *testing.T) {
	if Codex != "codex" || IntegrationFake != "integration-fake" {
		t.Fatalf("profiles = %q/%q, want exact protocol values", Codex, IntegrationFake)
	}
	for _, value := range []string{"codex", "integration-fake"} {
		got, err := Parse(value)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", value, err)
		}
		if string(got) != value {
			t.Fatalf("Parse(%q) = %q", value, got)
		}
	}
	for _, value := range []string{"", " codex", "codex ", "unknown", "/opt/custom-agent"} {
		if got, err := Parse(value); err == nil {
			t.Fatalf("Parse(%q) = %q, want rejection", value, got)
		}
	}
}
