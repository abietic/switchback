# Langfuse Trace Export

Switchback Trace remains the internal full-fidelity JSONL evidence source, and P0 will also export Trace/LLM observations to Langfuse for LLM observability, session debugging, latency, token usage, and cost analysis. Langfuse is an observability projection, not the authoritative trace store; it may use a shorter independent retention and be rebuilt from TraceManifest-managed evidence.

## Content policy

M1 `SingleUserLocalProfile` uses the complete-content policy selected for local debugging:

- a Langfuse Trace and its `agent.turn` span contain the complete user Turn input and finalized stage report
- each `model.generation` contains the complete LiteLLM model request body and serializable provider response, including Tool content and reasoning fields when the provider returns them
- no semantic redaction or application-level truncation is applied to model content
- transport headers, environment variables, provider credentials, proxy credentials and callback internals are not model content and are never copied into the observation payload
- the authenticated loopback receiver enforces a 32 MiB request-body safety limit
- Backend scans the complete ingestion batch for known generated credentials and provider keys before making the Langfuse HTTP request

This choice maximizes local debugging fidelity but makes Langfuse a store of sensitive user and model content. It is acceptable only for the current loopback, single-user profile. A multi-user or remotely accessible deployment must introduce a reviewed retention, access-control and content-governance policy before reusing this profile.

Langfuse rows created before this policy cannot recover exact generation input/output because the earlier LiteLLM callback did not retain those values. Authoritative ObjectStore Trace and Artifact evidence is unaffected.

## Status

Accepted as draft baseline.
