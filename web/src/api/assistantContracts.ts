import type {
  AssistantConversationData,
  AssistantReadiness,
  AssistantReadinessDimension,
  AssistantRun,
  AssistantTaskSpec,
} from "./contracts";
import { requireTimestamp } from "./contracts";
import {
  isCanonicalIdentity,
  isNonNegativeSafeInteger,
  isPositiveSafeInteger,
} from "./contractPrimitives";

type RecordValue = Record<string, unknown>;

const readinessDimensions: AssistantReadinessDimension[] = [
  "goal",
  "deliverable_acceptance",
  "scope_target",
  "constraints",
  "context_access",
  "risk_approval",
  "execution_policy",
];

export function parseAssistantConversationData(
  value: unknown,
  conversationId: string,
): AssistantConversationData {
  const boundConversationId = canonicalIdentity(conversationId, "conversation id");
  const data = object(value, "Assistant conversation");
  if (data.conversation_id !== boundConversationId) throw new Error("Assistant conversation identity is invalid");
  if (data.status !== "active" && data.status !== "closed") throw new Error("Assistant conversation status is invalid");
  const turns = requiredArray(data.turns, "turns").map((turn) => {
    const item = object(turn, "Assistant turn");
    const role = item.role;
    if (role !== "user" && role !== "assistant") throw new Error("Assistant turn role is invalid");
    return {
      turn_id: canonicalIdentity(item.turn_id, "turn_id"),
      sequence: positiveSafeInteger(item.sequence, "turn sequence"),
      role: role as "user" | "assistant",
      content: string(item.content, "turn content"),
      created_at: requireTimestamp(item.created_at, "turn time"),
    };
  }).sort((left, right) => left.sequence - right.sequence);
  return {
    conversation_id: boundConversationId,
    revision: positiveSafeInteger(data.revision, "conversation revision"),
    generation: positiveSafeInteger(data.generation, "conversation generation"),
    status: data.status,
    workspace_id: optionalNullableCanonicalIdentity(data, "workspace_id", "workspace id"),
    updated_at: requireTimestamp(data.updated_at, "updated_at"),
    turns,
    assistant_run: optionalNullableValue(data, "assistant_run", parseRun),
    readiness: optionalNullableValue(data, "readiness", parseReadiness),
    task_draft: optionalNullableValue(data, "task_draft", parseDraft),
    checkpoint: optionalNullableValue(data, "checkpoint", parseCheckpoint),
    handoffs: requiredArray(data.handoffs, "handoffs").map((handoff) => {
      const item = object(handoff, "Assistant handoff");
      return {
        task_id: canonicalIdentity(item.task_id, "task id"),
        status: string(item.status, "task status"),
        worker_session_id: optionalNullableCanonicalIdentity(
          item,
          "worker_session_id",
          "worker session id",
        ),
      };
    }),
  };
}

function parseRun(value: unknown): AssistantRun {
  const run = object(value, "Assistant run");
  const status = run.status;
  if (!(["queued", "claimed", "failed", "completed", "cancelled"] as const).includes(status as never)) throw new Error("Assistant run status is invalid");
  return {
    assistant_run_id: canonicalIdentity(run.assistant_run_id, "run id"),
    status: status as AssistantRun["status"],
    revision: positiveSafeInteger(run.revision, "run revision"),
    error_code: optionalNullableString(run, "error_code", "run error code"),
    retryable: boolean(run.retryable, "run retryable"),
  };
}

function parseReadiness(value: unknown): AssistantReadiness {
  const readiness = object(value, "Assistant readiness");
  const status = readiness.status;
  if (!(["clarifying", "ready_confirmed", "ready_with_assumptions"] as const).includes(status as never)) throw new Error("Assistant readiness status is invalid");
  const dimensions = object(readiness.dimensions, "readiness dimensions");
  if (Object.keys(dimensions).length !== readinessDimensions.length) throw new Error("readiness dimensions are invalid");
  return {
    assessment_id: canonicalIdentity(readiness.assessment_id, "assessment id"),
    revision: positiveSafeInteger(readiness.revision, "assessment revision"),
    status: status as AssistantReadiness["status"],
    dimensions: readinessDimensions.map((dimension) => {
      const state = object(dimensions[dimension], `readiness dimension ${dimension}`);
      const dimensionStatus = state.status;
      if (!(["known", "assumed", "blocking_unknown", "not_applicable"] as const).includes(dimensionStatus as never)) {
        throw new Error(`readiness dimension ${dimension} status is invalid`);
      }
      return {
        dimension,
        status: dimensionStatus as AssistantReadiness["dimensions"][number]["status"],
        summary: string(state.summary, `readiness dimension ${dimension} summary`),
        evidence_refs: requiredCanonicalIdentities(
          state.evidence_refs,
          `readiness dimension ${dimension} evidence_refs`,
        ),
      };
    }),
    blocking_unknowns: requiredStrings(readiness.blocking_unknowns, "blocking_unknowns"),
    assumptions: requiredStrings(readiness.assumptions, "readiness assumptions"),
    clarification_questions: nonNegativeSafeInteger(readiness.clarification_questions, "clarification questions"),
  };
}

function parseDraft(value: unknown) {
  const draft = object(value, "TaskDraft");
  const status = draft.status;
  if (!(["current", "invalidated", "confirmed"] as const).includes(status as never)) throw new Error("TaskDraft status is invalid");
  return {
    task_draft_id: canonicalIdentity(draft.task_draft_id, "TaskDraft id"),
    revision: positiveSafeInteger(draft.revision, "TaskDraft revision"),
    status: status as "current" | "invalidated" | "confirmed",
    task_spec: parseTaskSpec(draft.task_spec),
    assumptions: requiredStrings(draft.assumptions, "TaskDraft assumptions"),
  };
}

function parseTaskSpec(value: unknown): AssistantTaskSpec {
  const spec = object(value, "TaskDraft spec");
  const resolution = object(spec.workspace_resolution_ref, "workspace resolution");
  const readiness = object(spec.readiness_assessment_ref, "readiness reference");
  const intent = object(spec.creation_intent_ref, "creation intent");
  const target = object(spec.target, "Task target");
  const agent = object(spec.agent_config, "agent config");
  const runtime = object(spec.runtime_config, "runtime config");
  return {
    title: string(spec.title, "Task title"),
    goal: string(spec.goal, "Task goal"),
    workspace_id: canonicalIdentity(spec.workspace_id, "workspace id"),
    workspace_resolution_ref: {
      resolution_id: canonicalIdentity(resolution.resolution_id, "resolution id"),
      revision: positiveSafeInteger(resolution.revision, "resolution revision"),
      workspace_revision_id: canonicalIdentity(resolution.workspace_revision_id, "workspace revision id"),
      membership_revision: positiveSafeInteger(resolution.membership_revision, "membership revision"),
    },
    readiness_assessment_ref: {
      assessment_id: canonicalIdentity(readiness.assessment_id, "readiness assessment id"),
      policy_revision: canonicalIdentity(readiness.policy_revision, "readiness policy revision"),
      content_digest: sha256Digest(readiness.content_digest, "readiness content digest"),
    },
    creation_intent_ref: {
      intent_id: canonicalIdentity(intent.intent_id, "creation intent id"),
      task_spec_digest: sha256Digest(intent.task_spec_digest, "TaskSpec digest"),
      trigger_policy_revision: canonicalIdentity(intent.trigger_policy_revision, "trigger policy revision"),
    },
    target: {
      kind: canonicalIdentity(target.kind, "Task target kind"),
      ref: canonicalIdentity(target.ref, "Task target ref"),
    },
    constraints: requiredStrings(spec.constraints, "Task constraints"),
    acceptance_criteria: requiredStrings(spec.acceptance_criteria, "Task acceptance criteria"),
    assumptions: requiredArray(spec.assumptions, "Task assumptions").map((value) => {
      const assumption = object(value, "Task assumption");
      return string(assumption.description, "Task assumption description");
    }),
    agent_config: {
      profile_id: canonicalIdentity(agent.profile_id, "agent profile id"),
      profile_revision: canonicalIdentity(agent.profile_revision, "agent profile revision"),
      adapter: canonicalIdentity(agent.adapter, "agent adapter"),
    },
    runtime_config: parseRuntimeConfig(runtime),
  };
}

function parseRuntimeConfig(runtime: RecordValue): AssistantTaskSpec["runtime_config"] {
  const policy = string(runtime.network_policy, "runtime network policy");
  if (policy === "trusted_full_network_v1") {
    return {
      network_policy: policy,
      network_policy_revision: positiveSafeInteger(runtime.network_policy_revision, "runtime network policy revision"),
      network_policy_digest: sha256Digest(runtime.network_policy_digest, "runtime network policy digest"),
    };
  }
  if (policy === "m1_public_web_restricted_v1") {
    if (Object.hasOwn(runtime, "network_policy_revision") || Object.hasOwn(runtime, "network_policy_digest")) {
      throw new Error("legacy runtime network evidence is invalid");
    }
    return { network_policy: policy };
  }
  throw new Error("runtime network policy is invalid");
}

function parseCheckpoint(value: unknown) {
  const checkpoint = object(value, "checkpoint");
  const actions = requiredStrings(checkpoint.allowed_actions, "checkpoint allowed_actions").map((action) => {
    if (action !== "continue" && action !== "narrow_scope" && action !== "cancel") throw new Error("checkpoint action is invalid");
    return action;
  });
  if (new Set(actions).size !== actions.length) throw new Error("checkpoint action is invalid");
  const rawStatus = checkpoint.status;
  if (rawStatus !== "active" && rawStatus !== "resolved" && rawStatus !== "cancelled") throw new Error("checkpoint status is invalid");
  const status: "active" | "resolved" | "cancelled" = rawStatus;
  return {
    checkpoint_id: canonicalIdentity(checkpoint.checkpoint_id, "checkpoint id"),
    revision: positiveSafeInteger(checkpoint.revision, "checkpoint revision"),
    status,
    working_draft: object(checkpoint.working_draft, "working draft"),
    allowed_actions: actions,
  };
}

function object(value: unknown, label: string): RecordValue { if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} is invalid`); return value as RecordValue; }
function requiredArray(value: unknown, label: string): unknown[] { if (!Array.isArray(value)) throw new Error(`${label} is invalid`); return value; }
function requiredStrings(value: unknown, label: string): string[] {
  const values = requiredArray(value, label);
  if (!values.every((item) => typeof item === "string" && item.length > 0)) throw new Error(`${label} is invalid`);
  return values as string[];
}
function requiredCanonicalIdentities(value: unknown, label: string): string[] {
  return requiredArray(value, label).map((item) => canonicalIdentity(item, label));
}
function string(value: unknown, label: string): string { if (typeof value !== "string" || !value) throw new Error(`${label} is invalid`); return value; }
function sha256Digest(value: unknown, label: string): string { const parsed = string(value, label); if (!/^sha256:[a-f0-9]{64}$/.test(parsed)) throw new Error(`${label} is invalid`); return parsed; }
function boolean(value: unknown, label: string): boolean { if (typeof value !== "boolean") throw new Error(`${label} is invalid`); return value; }
function number(value: unknown, label: string): number { if (typeof value !== "number" || !Number.isFinite(value)) throw new Error(`${label} is invalid`); return value; }
function nonNegative(value: unknown, label: string): number { const parsed = number(value, label); if (parsed < 0) throw new Error(`${label} is invalid`); return parsed; }
function positive(value: unknown, label: string): number { const parsed = number(value, label); if (parsed <= 0) throw new Error(`${label} is invalid`); return parsed; }

function positiveSafeInteger(value: unknown, label: string): number {
  const parsed = positive(value, label);
  if (!isPositiveSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function nonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = nonNegative(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function canonicalIdentity(value: unknown, label: string): string {
  const identity = string(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

function optionalNullableValue<T>(
  record: RecordValue,
  key: string,
  parser: (value: unknown) => T,
): T | undefined {
  if (!Object.hasOwn(record, key) || record[key] === null) return undefined;
  return parser(record[key]);
}

function optionalNullableCanonicalIdentity(
  record: RecordValue,
  key: string,
  label: string,
): string | undefined {
  if (!Object.hasOwn(record, key) || record[key] === null) return undefined;
  return canonicalIdentity(record[key], label);
}

function optionalNullableString(
  record: RecordValue,
  key: string,
  label: string,
): string | undefined {
  if (!Object.hasOwn(record, key) || record[key] === null) return undefined;
  return string(record[key], label);
}
