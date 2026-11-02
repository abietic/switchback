## ADDED Requirements

### Requirement: Web IDE starts as an explicit managed SessionProcess
The system SHALL start at most one `web_ide` SessionProcess per live WorkerSession Runtime generation only after an authorized user submits an idempotent exact-Session open command.

#### Scenario: User opens IDE for the first time
- **WHEN** an authorized user opens the IDE for an exact live WorkerSession revision with sufficient Runtime capacity
- **THEN** Worker Runtime commits a managed start intent, starts the pinned IDE through the Shim and projects one stable SessionProcess identity after its declared port becomes healthy

#### Scenario: Open command is retried
- **WHEN** the same actor retries the same idempotency key and request after a timeout
- **THEN** the system returns or resolves the original SessionProcess and does not start another IDE

#### Scenario: Agent attempts to start an arbitrary IDE process
- **WHEN** Agent output or an ordinary Tool process attempts to claim the `web_ide` managed process type
- **THEN** Worker Runtime rejects it unless it arrived through the authorized typed SessionProcess command path

### Requirement: Browser IDE access is proxied and generation-fenced
The system SHALL expose the running IDE only through an authorized same-origin PreviewProxy route bound to the exact WorkerSession, SessionProcess revision, Runtime generation and declared port, and MUST NOT publish a host or public Worker port.

#### Scenario: Authorized iframe connects
- **WHEN** WorkerDetail opens a current opaque PreviewAccessRef
- **THEN** PreviewProxy resolves the internal endpoint from RuntimeSupervisor and proxies HTTP and WebSocket traffic without disclosing container topology to the browser

#### Scenario: Browser supplies a different target
- **WHEN** a client attempts to choose another host, port, process, Session or Runtime generation
- **THEN** Gateway rejects the request and does not reveal whether the target exists

#### Scenario: Switchback headers reach the proxy
- **WHEN** an authorized browser request contains Switchback cookies, authorization or internal headers
- **THEN** PreviewProxy authenticates the request and strips those credentials before forwarding to the IDE

#### Scenario: Runtime generation changes
- **WHEN** the Worker Runtime is replaced while an IDE tunnel is open
- **THEN** the old tunnel closes and no request is forwarded to the stale generation endpoint

### Requirement: Web IDE follows WorkerSession lifecycle
The system SHALL keep a healthy Web IDE running across AgentTurn terminal states and SHALL stop it on explicit stop, WorkerSession terminality, security termination, Runtime replacement or hard resource exhaustion.

#### Scenario: AgentTurn completes while IDE is running
- **WHEN** the source or a later AgentTurn becomes completed, failed, cancelled or timed out while the WorkerSession remains live
- **THEN** the IDE SessionProcess and its access state remain available

#### Scenario: User switches Workbench tabs
- **WHEN** the user hides or unmounts the IDE iframe without issuing Stop
- **THEN** the SessionProcess remains running and can be reattached

#### Scenario: WorkerSession expires
- **WHEN** the WorkerSession reaches idle or absolute terminality
- **THEN** Worker Runtime stops the IDE, closes PreviewProxy tunnels and prevents reopening under the terminal Session identity

#### Scenario: Runtime is replaced
- **WHEN** Runtime recovery creates a new generation
- **THEN** the old IDE process becomes stopped or lost, is not automatically restarted and requires an explicit open action in the new generation

### Requirement: Web IDE shares the WorkingCopy with explicit mixed provenance
The system SHALL allow the IDE user and Agent to access the same WorkerSession WorkingCopy concurrently and MUST mark affected delivery evidence when IDE-origin changes could be included.

#### Scenario: IDE runs during AgentTurn
- **WHEN** a `web_ide` SessionProcess is running at any time from AgentTurn start through Artifact finalization
- **THEN** resulting report and patch metadata carry `mixed_origin_possible` while retaining the exact WorkerRunStartSnapshot and source Run/Turn provenance

#### Scenario: IDE and Agent edit the same file
- **WHEN** concurrent user and Agent writes overlap
- **THEN** the system preserves the resulting WorkingCopy and evidence, does not claim Agent-only authorship and requires normal human review before remote publication

#### Scenario: User runs an IDE terminal command
- **WHEN** the user directly runs a command inside the Web IDE
- **THEN** the operation remains a user IDE action and is not recorded as an Agent Tool Call

### Requirement: IDE startup respects Runtime resource admission
The system SHALL evaluate policy-owned process, memory, CPU and PID headroom before starting the IDE and MUST leave the existing Worker Runtime usable when IDE capacity is unavailable.

#### Scenario: Capacity is sufficient
- **WHEN** the current Runtime satisfies IDE start admission
- **THEN** the system starts the IDE inside the existing Worker resource envelope without creating an AgentTurn or changing its deadline

#### Scenario: Capacity is insufficient
- **WHEN** IDE start would violate required Runtime headroom
- **THEN** Gateway returns `ide_capacity_unavailable`, starts no IDE process and does not stop or replace Codex, LiteLLM or the WorkerSession

#### Scenario: IDE reaches a hard resource limit
- **WHEN** the IDE causes a proven memory, PID or storage hard-limit failure
- **THEN** Worker Runtime stops the IDE, records a safe diagnostic and preserves the Session according to existing resource-exhaustion policy

### Requirement: Web IDE extension policy is server-owned
The system SHALL ship version 1 with only approved image-pinned IDE extensions and MUST prevent runtime extension installation by both Agent and IDE users.

#### Scenario: User browses extensions in version 1
- **WHEN** the user attempts to install a new extension at runtime
- **THEN** the IDE rejects the installation while existing approved extensions remain usable

#### Scenario: Agent invokes extension installation
- **WHEN** an Agent Tool or shell command attempts to write the IDE extension directory
- **THEN** filesystem and process policy deny the write and record a safe diagnostic

#### Scenario: Session restarts the IDE
- **WHEN** the user stops and reopens the IDE in the same readable WorkerSession
- **THEN** persisted IDE settings remain available without granting Agent access to IDE-owned state

### Requirement: Worker Workbench embeds recoverable IDE controls
The system SHALL expose IDE start, readiness, failure, stop, restart and temporary full-screen states in the resizable Worker Workbench without replacing WorkerDetail as the Session navigation owner.

#### Scenario: IDE is starting
- **WHEN** a start intent is committed but the declared port is not healthy
- **THEN** the Workbench displays a pending state and does not mount an iframe against an unready endpoint

#### Scenario: IDE is ready
- **WHEN** the current SessionProcess is healthy and authorized
- **THEN** the Workbench mounts the same-origin iframe and allows resize, tab switching and temporary full-screen use

#### Scenario: IDE fails
- **WHEN** the process exits or health validation fails
- **THEN** the Workbench displays a safe diagnostic and only the actions valid for the latest process revision
