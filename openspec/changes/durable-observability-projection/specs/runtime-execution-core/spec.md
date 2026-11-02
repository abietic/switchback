## MODIFIED Requirements

### Requirement: Langfuse is a derived observability projection
The system SHALL asynchronously export exact Tenant/User/Workspace-scoped WorkerSession, WorkerRun, AgentTurn, Runtime and model-call identities to Langfuse while keeping PostgreSQL execution state and ObjectStore Trace JSONL as authoritative evidence. In the M1 single-user local profile, the immutable Run root SHALL contain its original input and run-start identity; each AgentTurn span SHALL contain complete Turn input and finalized stage-report output, and each model generation SHALL contain complete model request body and serializable provider response without semantic redaction or application-level truncation. The Run root MUST NOT be rewritten with latest output, runtime generation or Turn collection. V3 SHALL accept authenticated late model facts through one transport-independent application owner and separately publish immutable basis/model fragments with original-scope scan receipts, without changing legacy frozen evidence or waiting for future callbacks.

#### Scenario: Langfuse projection succeeds
- **WHEN** finalized Trace and model observations are exported by the background worker
- **THEN** the verifier queries the exact source's Turn spans and generations with matching runtime metadata and semantically equal non-missing input/output values, while allowing separately projected Turns on the same Run root

#### Scenario: Model content is exported
- **WHEN** LiteLLM completes a model call inside an active AgentTurn
- **THEN** the authenticated loopback callback sends the complete request body and serializable response through WorkerControl for durable source capture, while transport headers, environment variables, provider credentials, proxy credentials and callback internals remain outside the observation payload

#### Scenario: Known credential appears in projected content
- **WHEN** the complete source snapshot or Langfuse ingestion batch matches a server-known generated credential or provider key
- **THEN** Backend blocks the observation before persisting unsafe source content or issuing the HTTP request, records safe observability degradation and does not echo the credential in the error

#### Scenario: Historical projection predates content capture
- **WHEN** an operator opens a model generation created before complete input/output capture was enabled
- **THEN** its missing content is reported as non-backfillable because the earlier callback discarded those bytes, while authoritative Trace and Artifact evidence remains available independently

#### Scenario: Langfuse is unavailable
- **WHEN** Codex and Artifact finalization complete but Langfuse export or exact verification fails
- **THEN** the system preserves `execution_outcome=completed`, independently retries observation work and neither blocks terminal/next-turn execution nor claims the observability acceptance gate passed

#### Scenario: Earlier Turn callback arrives during idle or another Turn
- **WHEN** a model fact has valid current Runtime/channel authority and exact historical start correlation for its original Turn
- **THEN** the single application receiver records that fact with its original identity and atomic Apply/scan evidence without requiring a Turn waiter
- **AND** it does not grant new execution authority, change Task/Run/Turn state, refresh idle time or block later heartbeats indefinitely

#### Scenario: Historical or rejected evidence is selected
- **WHEN** old source bytes lack original-scope safety proof, or the original event outcome was ignored
- **THEN** the system does not fabricate content, replace the original generation scanner or upgrade the event to accepted
- **AND** v3 historical projection requires an explicit exact actor/source-bound request rather than automatic backfill

#### Scenario: Runtime secret cleanup races observation publication
- **WHEN** the exact Runtime has a proven durable fence and original-scope receipts are committed
- **THEN** cleanup may remove its registry without waiting for ObjectStore or Langfuse, and those receipts remain usable
- **AND** missing basis proof after forced-stop/recovery is reported as unavailable without borrowing replacement credentials
