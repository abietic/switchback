## ADDED Requirements

### Requirement: Trusted local Runtime has full outbound reachability
The system SHALL resolve `trusted_full_network_v1` only for SingleUserLocalProfile and SHALL allow its Worker Runtime unrestricted outbound destination, protocol and port access through the server-configured Docker network.

#### Scenario: Worker reaches public development dependencies
- **WHEN** a Worker using `trusted_full_network_v1` connects to a publicly routable package registry, source host, API or documentation endpoint
- **THEN** the Runtime permits the connection without a destination allowlist or per-target ApprovalGrant

#### Scenario: Worker reaches private development dependencies
- **WHEN** the host Docker environment routes an RFC 1918, LAN, VPN or host development-service destination
- **THEN** the Worker may connect to that destination and port without private-range filtering

#### Scenario: Client requests trusted full network authority
- **WHEN** TaskSpec, Assistant output, Agent, Skill or MCP input attempts to select or expand `trusted_full_network_v1`
- **THEN** the system ignores or rejects the client-controlled authority and uses only the server-selected deployment profile

### Requirement: Full reachability does not expand other Runtime authority
The system SHALL keep container privilege, mounts, credentials and inbound exposure independent from `trusted_full_network_v1`.

#### Scenario: Trusted full-network Worker is inspected
- **WHEN** acceptance inspects the Worker container and its Docker configuration
- **THEN** the Worker has no Docker Socket, privileged mode, host network mode, arbitrary client host mount, ambient infrastructure credential or published host port

#### Scenario: Reachable private service requires credentials
- **WHEN** the Worker can route to a private repository, database, cluster or service but has no separately authorized credential
- **THEN** network reachability does not cause Switchback to inject or infer that credential

### Requirement: Legacy network snapshots remain truthful and readable
The system SHALL preserve immutable TaskSpec snapshots containing `m1_public_web_restricted_v1`, SHALL stop using that value for new admission, and SHALL NOT claim that historical or compatibility execution enforced private-network filtering.

#### Scenario: New Task is admitted in SingleUserLocalProfile
- **WHEN** direct or Assistant Task creation resolves current Runtime configuration
- **THEN** the immutable TaskSpec and WorkerRunStartSnapshot reference `trusted_full_network_v1` with its server-owned revision and digest

#### Scenario: Historical TaskSpec is read
- **WHEN** a read model or audit query loads an immutable TaskSpec containing `m1_public_web_restricted_v1`
- **THEN** the system preserves the stored value and renders its enforcement status as legacy or unknown rather than rewriting the snapshot

#### Scenario: Nonterminal legacy Task executes during migration
- **WHEN** an already-admitted nonterminal Task references `m1_public_web_restricted_v1`
- **THEN** compatibility execution uses the existing named-network behavior and records `legacy_unenforced` in the new Run evidence
