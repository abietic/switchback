# Local observability stack contract

## ADDED Requirements

### Requirement: Derived bounded metrics
The Backend SHALL expose snapshot-only Prometheus metrics with family-specific bounded labels. Tenant/user/workspace/session/run/turn IDs and arbitrary text MUST NOT become metric labels. Committed terminal rows SHALL be counted once regardless of polling, replay or exporter retry. Missing model TTFT/usage/cost MUST NOT be reported as measured zero.

#### Scenario: Slow or unavailable sample source
- **WHEN** a background sample exceeds its deadline or fails
- **THEN** scraping SHALL remain independent of database/network I/O, retain the last good data and expose sample unavailability and last-success time

#### Scenario: Replay and unbounded identity inputs
- **WHEN** the same committed records are sampled repeatedly with many distinct scoped IDs
- **THEN** terminal counts SHALL remain unchanged and metric series SHALL remain bounded by the documented enums

### Requirement: Safe asynchronous logs
The Backend SHALL emit a versioned allowlisted JSON envelope through a bounded non-blocking queue to Alloy/Loki. Raw prompt, source, tool payload, headers, environment, credentials and arbitrary exception messages MUST NOT be exported. Scope IDs SHALL be JSON metadata rather than indexed labels. Full/invalid/remote-failed/shutdown drops SHALL be visible through finite-reason counters and loss timestamps.

#### Scenario: Downstream stall and recovery
- **WHEN** Alloy is slow, unavailable or rejects records, then recovers
- **THEN** execution SHALL not wait for logging, memory SHALL remain bounded, and metrics plus a recovery gap event SHALL disclose dropped observations without claiming backfill

### Requirement: Backend owned Worker log boundary
Only Backend SHALL collect Docker runtime output for an exact server-verified runtime identity and generation. Unknown raw output SHALL be suppressed and represented only by bounded byte/source metadata. Alloy, Grafana and Worker MUST NOT receive Docker Socket access.

#### Scenario: Runtime replacement and sensitive output
- **WHEN** a Worker emits credentials or its runtime generation is replaced
- **THEN** no raw content SHALL reach Loki and the old generation collector SHALL stop

### Requirement: Real local operational views
The deployment SHALL provision real Prometheus and Loki datasources, four Grafana dashboards, scoped diagnostic links and local alert rules. Metrics and logs SHALL have seven-day retention. Operator host ports SHALL be loopback-only. Diagnostic links MUST NOT bypass target tenant/user/workspace authorization.

#### Scenario: Physical collection and alerts
- **WHEN** the controlled acceptance workload creates scoped Run/Turn observations and a sustained monitored failure
- **THEN** PromQL, exact Run/Turn LogQL, the existing Langfuse query, and Grafana panel/rule APIs SHALL return corresponding real results, including firing and resolved transitions

#### Scenario: Retention expiry
- **WHEN** timestamped isolated records are outside the configured retention period and compaction completes
- **THEN** acceptance SHALL prove query exclusion and actual storage expiry rather than relying only on configuration text

### Requirement: Observation outages do not gate execution
Backend cold start, readiness, terminal settlement and next-turn dispatch SHALL remain independent of Prometheus, Alloy, Loki, Grafana and Langfuse availability. Acceptance SHALL preserve core PostgreSQL and authoritative storage during fault injection.

#### Scenario: All observation services absent
- **WHEN** Backend cold starts with observation services unavailable and executes two controlled Turns
- **THEN** both Turns SHALL settle and the second SHALL dispatch without observation readiness dependencies

#### Scenario: Paired fault matrix
- **WHEN** at least 20 controlled healthy/outage pairs are executed
- **THEN** each settlement and next dispatch SHALL take at most 5 seconds and outage p95 increase SHALL be at most 1 second, with a retained continuous evidence record

### Requirement: Historical diagnostic identity preserves its proof boundary
Diagnostics SHALL authorize the exact current server-owned Tenant/User/Workspace and Session/Run/optional Turn before reading the ArtifactTrace-owned identity bridge. Only an exact historical source/version/digest proof with the selected Turn's passed evidence SHALL produce `historically_verified`, a public loopback `/project/{project}/traces/{trace}` URL and a safe identity summary. The public browser origin and canonical project segment SHALL be separate from ingest keys; Compose SHALL share the public and init project value. The DTO SHALL return canonical authorized target and finite statuses without raw proof, secrets, exception text or fabricated verification time. The UI SHALL state `身份曾核验；链接使用当前项目配置，未实时核对`; historical proof MUST NOT imply current project or health verification.

#### Scenario: Identity dependency fails after authorization
- **WHEN** the identity reader fails for an authorized target
- **THEN** diagnostics SHALL report safe identity-read `unavailable`, retain legal Grafana/Logs links, and make no remote service request or remote-offline claim

#### Scenario: Optional browser configuration or identity is absent
- **WHEN** browser configuration is empty or invalid, or an exact Run/Turn lacks usable proof
- **THEN** the response SHALL distinguish disabled, invalid_config, identity_unavailable, pending or blocked and omit Langfuse identity/link; Session-only requests SHALL NOT infer the latest Run
