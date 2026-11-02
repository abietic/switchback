package redact

import (
	"strings"
	"testing"
)

func TestRedactorReplacesAllKnownSecretKinds(t *testing.T) {
	secrets := Secrets{
		UpstreamModelKey:  "upstream-key",
		WorkerToken:       "worker-token",
		LiteLLMProxyToken: "litellm-token",
		ModelSinkToken:    "sink-token",
		LangfusePublicKey: "langfuse-public",
		LangfuseSecretKey: "langfuse-secret",
	}
	redactor := New(secrets)
	input := strings.Join([]string{
		secrets.UpstreamModelKey,
		secrets.WorkerToken,
		secrets.LiteLLMProxyToken,
		secrets.ModelSinkToken,
		secrets.LangfusePublicKey,
		secrets.LangfuseSecretKey,
	}, "|")
	want := strings.Join([]string{
		"[REDACTED:upstream_model_key]",
		"[REDACTED:worker_token]",
		"[REDACTED:litellm_proxy_token]",
		"[REDACTED:model_sink_token]",
		"[REDACTED:langfuse_public_key]",
		"[REDACTED:langfuse_secret_key]",
	}, "|")
	if got := redactor.Redact(input); got != want {
		t.Fatalf("Redact() = %q, want %q", got, want)
	}
}

func TestRedactorHandlesOverlappingTokensDeterministicallyWithoutLeaks(t *testing.T) {
	redactor := New(Secrets{
		UpstreamModelKey:  "secret-value",
		LiteLLMProxyToken: "secret-value-extended",
	})
	input := "secret-value-extended then secret-value then secret-value-extended"
	got := redactor.Redact(input)
	want := "[REDACTED:litellm_proxy_token] then [REDACTED:upstream_model_key] then [REDACTED:litellm_proxy_token]"
	if got != want {
		t.Fatalf("Redact() = %q, want %q", got, want)
	}
	for _, token := range []string{"secret-value", "secret-value-extended"} {
		if strings.Contains(got, token) {
			t.Fatalf("redacted output leaked token %q: %q", token, got)
		}
	}
}
