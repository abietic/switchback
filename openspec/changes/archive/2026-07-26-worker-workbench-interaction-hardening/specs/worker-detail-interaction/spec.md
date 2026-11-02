## ADDED Requirements

### Requirement: Projected Worker actions match command authority
The system SHALL derive WorkerDetail available actions from the exact authorized Task, WorkerSession, WorkerRun and AgentTurn state and MUST NOT advertise a command that the owner would structurally reject at those same revisions.

#### Scenario: User continues a delivered Task from chat
- **WHEN** the Task initiator sends free-form feedback to an exact live WorkerSession whose bound Task is `delivered` and whose latest Run and Turn are terminal
- **THEN** Gateway continues the same Task through a new WorkerRun and AgentTurn, returns the Task to `running` and preserves previous Run, Trace and Artifact evidence

#### Scenario: User requests changes from Artifact review
- **WHEN** the Task initiator submits review feedback for the same exact delivered Task and live WorkerSession
- **THEN** the system uses the same Task continuation lifecycle with interaction kind `rework` rather than creating another Task or standalone Run

#### Scenario: User chats after accepting delivery
- **WHEN** a Task is `completed`, is rendered as `Accepted`, has released its active Session binding and the WorkerSession remains live
- **THEN** WorkerDetail remains interactive and a new message creates a standalone WorkerRun without moving the accepted Task back to `running`

#### Scenario: Delivered Task is still bound
- **WHEN** the latest Run and Turn are terminal but their Task remains `delivered` and bound to the Session
- **THEN** WorkerDetail exposes Task continuation and acceptance actions and does not expose standalone `worker.follow_up`

### Requirement: Worker history materializes user-visible execution
The system SHALL materialize authorized Agent messages, Tool activity, approvals, errors, stage changes and Artifact references into versioned DisplayParts and durable DisplayCheckpoints without requiring the browser to read raw ACP frames or Trace JSONL.

#### Scenario: Agent streams and completes a response
- **WHEN** ACP emits user-visible response chunks followed by a terminal Agent message
- **THEN** Frontier may stream bounded transient text and MUST converge the Turn to one complete replayable text part in a durable checkpoint

#### Scenario: Tool lifecycle changes
- **WHEN** a Tool starts, updates, waits for approval or reaches terminal status
- **THEN** the durable display records stable Tool identity, safe arguments, status, duration, bounded output and authorized evidence references at semantic lifecycle boundaries

#### Scenario: Browser reconnects after missing transient output
- **WHEN** a WorkerDetail client reconnects after a transient sequence gap
- **THEN** it discards the incomplete overlay and restores complete Agent and Tool history from the latest durable checkpoint

#### Scenario: Raw internal content exists
- **WHEN** Trace contains provider payloads, credentials, hidden reasoning or unbounded Tool output
- **THEN** the user-visible display excludes that raw content and exposes only sanitized summaries and separately authorized evidence references

#### Scenario: Worker history is long
- **WHEN** a WorkerSession contains more Run and Turn history than the initial WorkerDetail page
- **THEN** Frontier returns a bounded recent page and allows older authorized history to load through a stable timeline cursor

### Requirement: Artifact Workbench renders canonical delivery results
The system SHALL render checksum-verified report and patch ArtifactRefs in a resizable Worker Workbench while preserving canonical source Run/Turn provenance and safe fallback behavior.

#### Scenario: Report is selected
- **WHEN** an authorized user selects an online report Artifact with a valid checksum
- **THEN** the Workbench renders safe Markdown with source Run/Turn, generation time, checksum and completeness while keeping Runtime and provider diagnostics outside the report body

#### Scenario: Multi-file patch is selected
- **WHEN** an authorized user selects an online parseable patch Artifact with a valid checksum
- **THEN** the Workbench shows changed-file status and statistics, file selection, unified or split review, wrapping and hunk navigation without implying that the patch was applied or published

#### Scenario: Patch contains no changes
- **WHEN** the canonical patch Artifact contains zero patch bytes
- **THEN** the Workbench displays a no-code-changes state and does not render an empty file tree or misleading diff review

#### Scenario: Artifact cannot be safely rendered
- **WHEN** Artifact content is missing, fails checksum, is binary, exceeds render policy or cannot be parsed
- **THEN** the Workbench preserves metadata and safe diagnostics, disables actions that require verified content and offers download only when integrity and authorization permit it

### Requirement: Stale command recovery preserves user intent
The system SHALL preserve an unsent or rejected Worker message across authoritative view refresh and MUST NOT automatically replay it after command target state changes.

#### Scenario: Exact target becomes stale
- **WHEN** Gateway rejects a Worker command because Task, Session, Run or Turn revision changed
- **THEN** the UI preserves the draft, replaces the affected WorkerDetail replica from a current snapshot and recalculates available send modes

#### Scenario: Refreshed state supports another command
- **WHEN** the refreshed snapshot exposes a different valid action for the preserved draft
- **THEN** the UI requires a new user send action against the new exact target and does not silently resubmit the prior command
