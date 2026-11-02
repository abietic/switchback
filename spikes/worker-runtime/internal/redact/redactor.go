// Package redact removes only the configured exact secret tokens from text.
package redact

import (
	"sort"
	"strings"
)

type tokenKind string

const (
	upstreamModelKey  tokenKind = "upstream_model_key"
	workerToken       tokenKind = "worker_token"
	liteLLMProxyToken tokenKind = "litellm_proxy_token"
	modelSinkToken    tokenKind = "model_sink_token"
	langfusePublicKey tokenKind = "langfuse_public_key"
	langfuseSecretKey tokenKind = "langfuse_secret_key"
)

// Secrets holds the six exact token values known to the trusted backend.
type Secrets struct {
	UpstreamModelKey  string
	WorkerToken       string
	LiteLLMProxyToken string
	ModelSinkToken    string
	LangfusePublicKey string
	LangfuseSecretKey string
}

// Redactor deterministically replaces configured exact tokens before storage.
type Redactor struct {
	replacer *strings.Replacer
}

type token struct {
	kind  tokenKind
	value string
}

// New builds a deterministic redactor. Empty token values are ignored.
func New(secrets Secrets) Redactor {
	tokens := []token{
		{kind: upstreamModelKey, value: secrets.UpstreamModelKey},
		{kind: workerToken, value: secrets.WorkerToken},
		{kind: liteLLMProxyToken, value: secrets.LiteLLMProxyToken},
		{kind: modelSinkToken, value: secrets.ModelSinkToken},
		{kind: langfusePublicKey, value: secrets.LangfusePublicKey},
		{kind: langfuseSecretKey, value: secrets.LangfuseSecretKey},
	}
	tokens = filterTokens(tokens)
	sort.Slice(tokens, func(i, j int) bool {
		if len(tokens[i].value) != len(tokens[j].value) {
			return len(tokens[i].value) > len(tokens[j].value)
		}
		return tokens[i].kind < tokens[j].kind
	})
	pairs := make([]string, 0, len(tokens)*2)
	for _, token := range tokens {
		pairs = append(pairs, token.value, "[REDACTED:"+string(token.kind)+"]")
	}
	if len(pairs) == 0 {
		return Redactor{}
	}
	return Redactor{replacer: strings.NewReplacer(pairs...)}
}

// Redact replaces configured exact tokens and leaves all other content intact.
func (r Redactor) Redact(value string) string {
	if r.replacer == nil {
		return value
	}
	return r.replacer.Replace(value)
}

func filterTokens(tokens []token) []token {
	filtered := make([]token, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if token.value == "" {
			continue
		}
		if _, ok := seen[token.value]; ok {
			continue
		}
		seen[token.value] = struct{}{}
		filtered = append(filtered, token)
	}
	return filtered
}
