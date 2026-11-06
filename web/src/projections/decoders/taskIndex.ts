import {
  type StatusTone,
  type RuntimeDispatchWait,
  type TaskIndexData,
  type TaskListItem,
  requireArray,
  requireNonNegativeInteger,
  requireRecord,
  requireString,
  requireTimestamp,
} from "../../api/contracts";
import {
  isCanonicalIdentity,
  isNonNegativeSafeInteger,
} from "../../api/contractPrimitives";
import {
  internalCreateViewProjectionRuntimeDecoder,
  type BoundViewProjectionRuntimeDecoder,
  type InternalViewContract,
} from "../viewRuntime";

const taskStatusTones = {
  queued: "neutral",
  running: "active",
  waiting_user: "waiting",
  delivered: "waiting",
  completed: "success",
  failed: "danger",
  cancelled: "danger",
} as const satisfies Record<string, StatusTone>;

const taskIndexContract: InternalViewContract<TaskIndexData> = {
  allowedDeltaEventTypes: ["view.entity_upserted.v1"],
  canonicalRelatedRefTypes: [],
  decodeData: parseTaskIndexData,
  validateState(state): undefined {
    if (state.availableActions.length !== 0 || state.relatedRefs.length !== 0) {
      throw new Error("Task index authority must be empty");
    }
    return undefined;
  },
};

export function createTaskIndexViewDecoder(): BoundViewProjectionRuntimeDecoder<TaskIndexData> {
  return internalCreateViewProjectionRuntimeDecoder({
    contractKey: "task-index-v1:index",
    viewType: "task_summary",
    viewId: "index",
    contract: taskIndexContract,
  });
}

function parseTaskIndexData(value: unknown): TaskIndexData {
  const data = requireRecord(value, "Task index data");
  const capacity = requireRecord(data.capacity, "Task index capacity");
  return {
    tasks: requireArray(data.tasks, "Task index tasks").map(parseTask),
    capacity: {
      used: requireNonNegativeSafeInteger(capacity.used, "Task index capacity used"),
      limit: requireNonNegativeSafeInteger(capacity.limit, "Task index capacity limit"),
    },
  };
}

function parseTask(value: unknown): TaskListItem {
  const task = requireRecord(value, "Task index item");
  const status = requireString(task.status, "Task index item status");
  const statusTone = taskStatusTones[status as keyof typeof taskStatusTones];
  if (statusTone === undefined || task.status_tone !== statusTone) {
    throw new Error("Task index item status authority is invalid");
  }
  return {
    task_id: requireCanonicalIdentity(task.task_id, "Task index item id"),
    title: requireString(task.title, "Task index item title"),
    workspace: requireCanonicalIdentity(task.workspace, "Task index item workspace"),
    status,
    status_tone: statusTone,
    worker_session_id: optionalCanonicalIdentity(
      task,
      "worker_session_id",
      "Task index item Worker identity",
    ),
    updated_at: requireTimestamp(task.updated_at, "Task index item updated_at"),
    ...(Object.hasOwn(task, "runtime_dispatch_wait")
      ? {runtime_dispatch_wait: parseRuntimeDispatchWait(task.runtime_dispatch_wait, status)} : {}),
  };
}

function optionalCanonicalIdentity(
  record: Record<string, unknown>,
  key: string,
  label: string,
): string | undefined {
  if (!Object.hasOwn(record, key)) return undefined;
  return requireCanonicalIdentity(record[key], label);
}

function requireCanonicalIdentity(value: unknown, label: string): string {
  const identity = requireString(value, label);
  if (!isCanonicalIdentity(identity)) throw new Error(`${label} is invalid`);
  return identity;
}

function requireNonNegativeSafeInteger(value: unknown, label: string): number {
  const parsed = requireNonNegativeInteger(value, label);
  if (!isNonNegativeSafeInteger(parsed)) throw new Error(`${label} is invalid`);
  return parsed;
}

function parseRuntimeDispatchWait(value: unknown, status: string): RuntimeDispatchWait {
  if (status !== "queued") throw new Error("Runtime dispatch wait requires queued Task");
  const wait = requireRecord(value, "Runtime dispatch wait");
  const reason = requireString(wait.reason, "Runtime dispatch wait reason");
  switch (reason) {
    case "capacity_uninitialized": case "capacity_evidence_invalid": case "capacity_evidence_stale":
    case "capacity_identity_drift": case "capacity_inventory_unknown": case "capacity_exhausted":
    case "capacity_path_not_integrated":
      return {reason, observed_at: requireTimestamp(wait.observed_at, "Runtime dispatch wait observed_at")};
    default: throw new Error("Unknown runtime dispatch wait reason");
  }
}
