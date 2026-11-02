import { parseHomeDashboardData, type HomeDashboardData } from "../../api/homeContracts";
import {
  internalCreateViewProjectionRuntimeDecoder,
  type BoundViewProjectionRuntimeDecoder,
  type InternalViewContract,
} from "../viewRuntime";

const homeContract: InternalViewContract<HomeDashboardData> = {
  allowedDeltaEventTypes: ["view.entity_upserted.v1"],
  canonicalRelatedRefTypes: ["task_summary", "worker_detail", "artifact_detail"],
  decodeData: parseHomeDashboardData,
  validateState(state): undefined {
    if (state.availableActions.length !== 0) {
      throw new Error("Home actions are not authoritative");
    }

    const expectedRefs = new Set<string>();
    for (const task of state.data.attention_tasks) {
      expectedRefs.add(refKey("task_summary", task.task_id));
    }
    for (const worker of state.data.recent_workers) {
      expectedRefs.add(refKey("worker_detail", worker.worker_session_id));
    }
    for (const delivery of state.data.recent_deliveries) {
      expectedRefs.add(refKey("task_summary", delivery.task_id));
      for (const artifact of delivery.artifacts) {
        expectedRefs.add(refKey("artifact_detail", artifact.artifact_id));
      }
    }

    const actualRefs = new Set(state.relatedRefs.map((ref) => refKey(ref.type, ref.id)));
    if (
      actualRefs.size !== state.relatedRefs.length
      || actualRefs.size !== expectedRefs.size
      || [...expectedRefs].some((key) => !actualRefs.has(key))
    ) {
      throw new Error("Home related refs are not authoritative");
    }
    return undefined;
  },
};

function refKey(type: string, id: string): string {
  return `${type}\0${id}`;
}

export function createHomeViewDecoder(): BoundViewProjectionRuntimeDecoder<HomeDashboardData> {
  return internalCreateViewProjectionRuntimeDecoder({
    contractKey: "home-dashboard-v1:index",
    viewType: "home_dashboard",
    viewId: "index",
    contract: homeContract,
  });
}
