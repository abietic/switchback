package litellm

import (
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
)

func TestAssistantRouteProjectionDetachesFromMutableProfileAuthority(t *testing.T) {
	profile, ok := config.ProductionProfileRegistry().Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("Kimi Code Assistant profile missing")
	}
	wantRef := profile.Ref()

	route, err := projectAssistantRoute(profile)
	if err != nil {
		t.Fatal(err)
	}
	profile.ID = "caller-profile"
	profile.LiteLLMModel = "caller-model"
	profile.Digest = "sha256:caller"
	*profile.RequestPolicy.Temperature = 2
	profile.RequestPolicy.StructuredOutput.Mode = "caller-mode"

	if route.profile != wantRef || route.model != "switchback-assistant-kimi-code" ||
		route.temperature == nil || *route.temperature != 1 || route.responseFormat == nil {
		t.Fatalf("route projection drifted with caller profile: %#v", route)
	}
	request := modelRequest(config.AssistantModelProfile{
		ID: wantRef.ID, Revision: wantRef.Revision, Digest: wantRef.Digest,
	})
	if !route.matches(request) {
		t.Fatalf("route rejected its exact profile ref: route=%#v request=%#v", route, request)
	}
	request.ProfileDigest = "sha256:stale"
	if route.matches(request) {
		t.Fatal("route accepted a stale profile ref")
	}
}

func TestAssistantRouteProjectionOmitsUndeclaredDeepSeekPolicy(t *testing.T) {
	profile, ok := config.ProductionProfileRegistry().Assistant(config.DeepSeekAssistantOpenAIV1)
	if !ok {
		t.Fatal("DeepSeek Assistant profile missing")
	}
	route, err := projectAssistantRoute(profile)
	if err != nil {
		t.Fatal(err)
	}
	if route.profile != profile.Ref() || route.model != "switchback-assistant-deepseek" ||
		route.temperature != nil || route.responseFormat != nil {
		t.Fatalf("unexpected DeepSeek route projection: %#v", route)
	}
}
