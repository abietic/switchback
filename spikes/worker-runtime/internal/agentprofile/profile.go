// Package agentprofile defines the server-owned Worker agent profiles.
package agentprofile

import "fmt"

type Profile string

const (
	Codex           Profile = "codex"
	IntegrationFake Profile = "integration-fake"
)

// Parse accepts only the two server-owned protocol values.
func Parse(value string) (Profile, error) {
	profile := Profile(value)
	switch profile {
	case Codex, IntegrationFake:
		return profile, nil
	default:
		return "", fmt.Errorf("agent profile %q is unsupported", value)
	}
}

func (p Profile) String() string {
	return string(p)
}
