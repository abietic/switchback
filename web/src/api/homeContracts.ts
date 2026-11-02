import {
  type StatusTone,
  requireArray,
  requireNonNegativeInteger,
  requireRecord,
  requireString,
  requireTimestamp,
} from "./contracts";
import {
  isCanonicalIdentity,
  isNonNegativeSafeInteger,
} from "./contractPrimitives";

export interface HomeTaskItem {
  task_id: string;
  title: string;
  workspace: string;
  status: "waiting_user" | "delivered";
  status_tone: "waiting";
  worker_session_id?: string;
  updated_at: string;
}

export interface HomeWorkerItem {
  worker_session_id: string;
  workspace: string;
  status: "allocating" | "interactive" | "running" | "recovering" | "waiting_user" | "idle" | "lost" | "expired" | "closed";
  status_tone: StatusTone;
  runtime_generation: number;
  bound_task_id?: string;
  task_title?: string;
  updated_at: string;
}

export interface HomeDeliveryArtifact {
  artifact_id: string;
  artifact_type: "report" | "patch";
  href: string;
}

export interface HomeDeliveryItem {
  task_id: string;
  title: string;
  workspace: string;
  status: "delivered" | "completed";
  status_tone: "waiting" | "success";
  acceptance_mode: "auto" | "manual";
  worker_session_id?: string;
  updated_at: string;
  artifacts: HomeDeliveryArtifact[];
}

export interface HomeDashboardData {
  attention_tasks: HomeTaskItem[];
  recent_workers: HomeWorkerItem[];
  recent_deliveries: HomeDeliveryItem[];
  capacity: { used: number; limit: number };
  updated_at: string;
}

export function parseHomeDashboardData(value: unknown): HomeDashboardData {
  const data = requireRecord(value, "Home data");
  const capacity = requireRecord(data.capacity, "capacity");
  const used = requireNonNegativeSafeInteger(capacity.used, "capacity used");
  const limit = requireNonNegativeSafeInteger(capacity.limit, "capacity limit");
  return {
    attention_tasks: parseHomeList(data.attention_tasks, "attention_tasks", parseAttentionTask),
    recent_workers: parseHomeList(data.recent_workers, "recent_workers", parseWorker),
    recent_deliveries: parseHomeList(data.recent_deliveries, "recent_deliveries", parseDelivery),
    capacity: { used, limit },
    updated_at: requireTimestamp(data.updated_at, "Home updated_at"),
  };
}

function parseHomeList<T>(value: unknown, label: string, parseItem: (item: unknown) => T): T[] {
  const items = requireArray(value, label);
  if (items.length > 6) throw new Error(`${label} is invalid`);
  return items.map(parseItem);
}

function parseAttentionTask(value: unknown): HomeTaskItem {
  const item = requireRecord(value, "attention task");
  const status = item.status;
  if (status !== "waiting_user" && status !== "delivered") throw new Error("attention task status is invalid");
  if (item.status_tone !== "waiting") throw new Error("attention task status_tone is invalid");
  return {
    task_id: requireCanonicalIdentity(item.task_id, "attention task id"),
    title: requireString(item.title, "attention task title"),
    workspace: requireCanonicalIdentity(item.workspace, "attention task workspace"),
    status,
    status_tone: "waiting",
    worker_session_id: optionalCanonicalIdentity(item, "worker_session_id", "attention task worker_session_id"),
    updated_at: requireTimestamp(item.updated_at, "attention task updated_at"),
  };
}

function parseWorker(value: unknown): HomeWorkerItem {
  const item = requireRecord(value, "recent worker");
  const status = item.status;
  if (!isWorkerStatus(status)) throw new Error("recent worker status is invalid");
  const statusTone = expectedWorkerStatusTone(status);
  if (item.status_tone !== statusTone) throw new Error("recent worker status_tone is invalid");
  return {
    worker_session_id: requireCanonicalIdentity(item.worker_session_id, "recent worker id"),
    workspace: requireCanonicalIdentity(item.workspace, "recent worker workspace"),
    status,
    status_tone: statusTone,
    runtime_generation: requireNonNegativeSafeInteger(item.runtime_generation, "recent worker runtime_generation"),
    bound_task_id: optionalCanonicalIdentity(item, "bound_task_id", "recent worker bound_task_id"),
    task_title: optionalOmittedString(item, "task_title", "recent worker task_title"),
    updated_at: requireTimestamp(item.updated_at, "recent worker updated_at"),
  };
}

function parseDelivery(value: unknown): HomeDeliveryItem {
  const item = requireRecord(value, "recent delivery");
  const status = item.status;
  if (status !== "delivered" && status !== "completed") throw new Error("recent delivery status is invalid");
  const statusTone = status === "delivered" ? "waiting" : "success";
  if (item.status_tone !== statusTone) throw new Error("recent delivery status_tone is invalid");
  const acceptanceMode = item.acceptance_mode;
  if (acceptanceMode !== "auto" && acceptanceMode !== "manual") {
    throw new Error("recent delivery acceptance_mode is invalid");
  }
  return {
    task_id: requireCanonicalIdentity(item.task_id, "recent delivery task id"),
    title: requireString(item.title, "recent delivery title"),
    workspace: requireCanonicalIdentity(item.workspace, "recent delivery workspace"),
    status,
    status_tone: statusTone,
    acceptance_mode: acceptanceMode,
    worker_session_id: optionalCanonicalIdentity(item, "worker_session_id", "recent delivery worker_session_id"),
    updated_at: requireTimestamp(item.updated_at, "recent delivery updated_at"),
    artifacts: requireArray(item.artifacts, "recent delivery artifacts").map(parseArtifact),
  };
}

function parseArtifact(value: unknown): HomeDeliveryArtifact {
  const artifact = requireRecord(value, "delivery artifact");
  const artifactID = requireCanonicalIdentity(artifact.artifact_id, "delivery artifact id");
  const artifactType = artifact.artifact_type;
  if (artifactType !== "report" && artifactType !== "patch") throw new Error("delivery artifact type is invalid");
  const href = requireString(artifact.href, "delivery artifact href");
  if (href !== `/api/v1/views/artifact_detail/${artifactID}`) throw new Error("delivery artifact href is invalid");
  return { artifact_id: artifactID, artifact_type: artifactType, href };
}

function optionalCanonicalIdentity(
  record: Record<string, unknown>,
  key: string,
  label: string,
): string | undefined {
  if (!Object.hasOwn(record, key)) return undefined;
  return requireCanonicalIdentity(record[key], label);
}

function optionalOmittedString(
  record: Record<string, unknown>,
  key: string,
  label: string,
): string | undefined {
  if (!Object.hasOwn(record, key)) return undefined;
  return requireString(record[key], label);
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

function isWorkerStatus(value: unknown): value is HomeWorkerItem["status"] {
  return value === "allocating" || value === "interactive" || value === "running" || value === "recovering" ||
    value === "waiting_user" || value === "idle" || value === "lost" || value === "expired" || value === "closed";
}

function expectedWorkerStatusTone(status: HomeWorkerItem["status"]): StatusTone {
  if (status === "interactive" || status === "running") return "active";
  if (status === "allocating" || status === "waiting_user" || status === "idle") return "waiting";
  if (status === "lost" || status === "expired" || status === "closed") return "danger";
  return "neutral";
}
