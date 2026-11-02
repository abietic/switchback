# Delivery Can Create TaskSpec Directly

Delivery may create TaskSpec directly from a TaskCreationTriggerPolicy-approved surface, but Task Orchestration still validates readiness, creation intent, identity, permissions, schema, Workspace evidence and approval policy before execution. Per [ADR 0045](0045-require-explicit-task-creation-surface-in-shared-channels.md), shared-channel ordinary messages always enter Assistant even when ready; direct creation is reserved for structured `/task`/form actions or confirmation bound to an exact ready TaskDraft. This preserves the fast path without treating model interpretation as execution authorization.

## Status

Accepted as draft baseline.
