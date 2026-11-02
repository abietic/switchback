package localproduct

import (
	"fmt"
	"testing"
)

func TestObservabilityConstructsOnlyFragmentWorkers(t *testing.T) {
	c := Config{LangfuseBaseURL: "http://127.0.0.1:1", LangfusePublicKey: "public", LangfuseSecretKey: "secret"}
	got := newObservabilityRunner(c, nil, nil)
	if fmt.Sprintf("%T", got) != "localproduct.fragmentBackgroundRunners" {
		t.Fatalf("expected v3 background workers, got %T", got)
	}
}

func TestObservabilityDisabledOrPartialIsNonfatal(t *testing.T) {
	for _, c := range []Config{{}, {LangfuseBaseURL: "http://127.0.0.1:1"}, {LangfusePublicKey: "public", LangfuseSecretKey: "secret"}} {
		if got := newObservabilityRunner(c, nil, nil); got != nil {
			t.Fatalf("incomplete configuration enabled %T", got)
		}
	}
}
