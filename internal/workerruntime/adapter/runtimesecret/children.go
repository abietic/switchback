package runtimesecret

import "fmt"

type ChildProcessSpec struct {
	UID int
	GID int
	Env []string
}

type ProcessSpecs struct {
	LiteLLM ChildProcessSpec
	Codex   ChildProcessSpec
	Tool    ChildProcessSpec
}

// ChildProcessSpecs is called only by the trusted root Shim after it reads the
// root-owned bootstrap and provider credential.
func ChildProcessSpecs(bootstrap Bootstrap, credential ProviderCredential) (ProcessSpecs, error) {
	if bootstrap.LiteLLMUID != LiteLLMUID || bootstrap.AgentUID != AgentUID ||
		!validToken(bootstrap.ProxyToken) || !validToken(bootstrap.ModelSinkToken) ||
		!validToken(bootstrap.WorkerControlToken) || bootstrap.ChannelEpoch == 0 ||
		!canonicalIdentity(bootstrap.WorkerSessionID) || !canonicalIdentity(bootstrap.WorkerRunID) ||
		bootstrap.LiteLLMModel == "" || bootstrap.UpstreamAPIBase == "" || len(credential) == 0 {
		return ProcessSpecs{}, fmt.Errorf("Runtime bootstrap child policy is invalid")
	}
	litellm := ChildProcessSpec{
		UID: LiteLLMUID,
		GID: LiteLLMUID,
		Env: []string{
			"SWITCHBACK_UPSTREAM_MODEL=" + bootstrap.LiteLLMModel,
			"SWITCHBACK_UPSTREAM_API_BASE=" + bootstrap.UpstreamAPIBase,
			"SWITCHBACK_UPSTREAM_API_KEY=" + string(credential),
			"SWITCHBACK_PROXY_TOKEN=" + bootstrap.ProxyToken,
			"SWITCHBACK_MODEL_SINK_TOKEN=" + bootstrap.ModelSinkToken,
		},
	}
	agent := ChildProcessSpec{
		UID: AgentUID,
		GID: AgentUID,
		Env: []string{
			"OPENAI_BASE_URL=http://127.0.0.1:4000/v1",
			"OPENAI_API_KEY=" + bootstrap.ProxyToken,
		},
	}
	return ProcessSpecs{LiteLLM: litellm, Codex: agent, Tool: agent}, nil
}

// POSIXCanRead models the owner/other read bits used by the fixed root-owned
// secret files. Linux container acceptance later verifies the same boundary.
func POSIXCanRead(uid, ownerUID int, mode uint32) bool {
	if uid == 0 {
		return true
	}
	if uid == ownerUID {
		return mode&0o400 != 0
	}
	return mode&0o004 != 0
}
