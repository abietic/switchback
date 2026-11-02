# ADR 0036: Separate Durable Display Checkpoints From Transient Agent Stream

- Status: Accepted
- Date: 2026-07-13

## Context

Coding Agents emit high-frequency text, user-visible reasoning summaries, partial Tool arguments and progress updates. Persisting every chunk as a PostgreSQL ViewProjection version would duplicate the authoritative Trace stream and create unnecessary write/replay pressure. Treating all output as transient would make reconnects display incomplete messages and ambiguous Tool or approval state.

## Decision

Switchback uses two user-display lanes:

- **Durable lane**: DisplayCheckpoint and semantic lifecycle state are committed into ViewProjection and replayed as typed ProjectionDelta. They advance projection version and ViewCursor.
- **Transient lane**: TransientDisplayDelta provides low-latency text/reasoning-summary/partial-argument/progress rendering between checkpoints. It has a stream-local sequence but does not advance ViewCursor or projection version and is not guaranteed to replay exactly.

This is a Frontier delivery distinction, not a Trace retention distinction. Worker continues to tee and upload high-fidelity ACP/Agent frames to authoritative Trace JSONL according to Trace policy. Losing a transient browser chunk does not erase the execution evidence.

Every display stream starts from a durable checkpoint identity and ends with a complete durable terminal checkpoint. Additional checkpoints are required at semantic boundaries: Tool invocation accepted/started/completed, approval/waiting-user transitions, Artifact attachment, error/cancel, and message/run terminal state. A versioned DisplayDurabilityPolicy also bounds elapsed time and accumulated bytes between checkpoints; concrete thresholds are calibrated by a P0 stream/load spike instead of hard-coded into the domain contract.

TransientDisplayDelta references a specific `display_stream_id`, `part_id`, `base_checkpoint_version`, and monotonic `transient_sequence`. Frontend applies it only to the matching checkpoint base. Duplicate sequence is ignored. A sequence gap marks that part as recovering; Frontend fetches or waits for the latest DisplayCheckpoint rather than corrupting the buffer or forcing a whole-application replay.

SSE transient events do not carry a new durable SSE `id`; the browser's `Last-Event-ID` remains the last applied ViewCursor. On reconnect, ProjectionSnapshot/ProjectionDelta restores the latest checkpoint. Missing transient chunks may temporarily reduce animation fidelity, but the next checkpoint and mandatory terminal checkpoint restore exact user-visible content.

Transient backpressure may coalesce adjacent deltas for the same part and drop non-semantic progress pulses. It must never drop or reorder durable checkpoints, Tool/approval lifecycle, Artifact metadata, errors, or terminal state.

Only user-visible reasoning summaries may become DisplayPart content. Hidden chain-of-thought is not a Frontend projection contract. Raw/provider evidence availability follows separate Trace and model-policy rules.

## Consequences

- Real-time text remains fluid without one database write per token.
- Reconnect correctness is defined at durable checkpoints, not at arbitrary token boundaries.
- Final messages, Tool results, approvals, Artifacts and terminal states are exact and replayable.
- Frontend needs a small transient accumulator keyed by stream/part/checkpoint/sequence.
- Operations must measure checkpoint frequency/size, transient drop/coalesce rates, checkpoint lag and terminal convergence.

## Alternatives Rejected

- Persist every chunk: strongest replay precision but excessive write amplification and duplication of Trace evidence.
- Coarse durable updates only: simplest backend, but poor Coding Agent interaction and no natural streaming experience.
- Treat transient sequence as ViewCursor: conflates best-effort rendering with durable projection recovery and can falsely acknowledge unpersisted output.
