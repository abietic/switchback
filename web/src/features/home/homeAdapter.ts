import type {
  HomeDashboardData,
  HomeDeliveryItem,
  HomeTaskItem,
  HomeWorkerItem,
} from "../../api/homeContracts";
import type { StatusTone } from "../../api/contracts";

export interface HomeAttentionPresentation {
  taskId: string;
  title: string;
  workspace: string;
  status: HomeTaskItem["status"];
  statusLabel: string;
  statusTone: "waiting";
  href: string;
  updatedAt: string;
}

export interface HomeWorkerPresentation {
  workerSessionId: string;
  workspace: string;
  status: HomeWorkerItem["status"];
  statusLabel: string;
  statusTone: StatusTone;
  runtimeGeneration: number;
  taskId?: string;
  taskTitle?: string;
  href: string;
  updatedAt: string;
}

export interface HomeDeliveryPresentation {
  taskId: string;
  title: string;
  workspace: string;
  status: HomeDeliveryItem["status"];
  statusLabel: string;
  statusTone: HomeDeliveryItem["status_tone"];
  acceptanceMode: HomeDeliveryItem["acceptance_mode"];
  workerHref?: string;
  updatedAt: string;
  artifacts: Array<{
    artifactId: string;
    artifactType: HomeDeliveryItem["artifacts"][number]["artifact_type"];
    href: string;
  }>;
}

export interface HomePresentation {
  attention: HomeAttentionPresentation[];
  recentWorkers: HomeWorkerPresentation[];
  deliveries: HomeDeliveryPresentation[];
  capacity: {
    used: number;
    limit: number;
    percent: number;
    fillPercent: number;
    overBy: number;
  };
  updatedAt: string;
}

export function adaptHome(data: HomeDashboardData): HomePresentation {
  const percent = data.capacity.limit > 0
    ? Math.round(data.capacity.used / data.capacity.limit * 100)
    : 0;

  return {
    attention: data.attention_tasks.map((task) => ({
      taskId: task.task_id,
      title: task.title,
      workspace: task.workspace,
      status: task.status,
      statusLabel: formatStatus(task.status),
      statusTone: task.status_tone,
      href: task.worker_session_id ? `/workers/${task.worker_session_id}` : "/tasks",
      updatedAt: formatTimestamp(task.updated_at),
    })),
    recentWorkers: data.recent_workers.map((worker) => ({
      workerSessionId: worker.worker_session_id,
      workspace: worker.workspace,
      status: worker.status,
      statusLabel: formatStatus(worker.status),
      statusTone: worker.status_tone,
      runtimeGeneration: worker.runtime_generation,
      taskId: worker.bound_task_id,
      taskTitle: worker.task_title,
      href: `/workers/${worker.worker_session_id}`,
      updatedAt: formatTimestamp(worker.updated_at),
    })),
    deliveries: data.recent_deliveries.map((delivery) => ({
      taskId: delivery.task_id,
      title: delivery.title,
      workspace: delivery.workspace,
      status: delivery.status,
      statusLabel: formatStatus(delivery.status),
      statusTone: delivery.status_tone,
      acceptanceMode: delivery.acceptance_mode,
      workerHref: delivery.worker_session_id
        ? `/workers/${delivery.worker_session_id}`
        : undefined,
      updatedAt: formatTimestamp(delivery.updated_at),
      artifacts: delivery.artifacts.map((artifact) => ({
        artifactId: artifact.artifact_id,
        artifactType: artifact.artifact_type,
        href: `/artifacts/${artifact.artifact_id}`,
      })),
    })),
    capacity: {
      used: data.capacity.used,
      limit: data.capacity.limit,
      percent,
      fillPercent: Math.min(100, Math.max(0, percent)),
      overBy: Math.max(0, data.capacity.used - data.capacity.limit),
    },
    updatedAt: formatTimestamp(data.updated_at),
  };
}

function formatStatus(status: string): string {
  return status.replaceAll("_", " ");
}

function formatTimestamp(timestamp: string): string {
  return new Intl.DateTimeFormat("en", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "UTC",
  }).format(new Date(timestamp));
}
