import {
  parseAssistantConversationData,
} from "../../api/assistantContracts";
import type { AssistantConversationData } from "../../api/contracts";
import {
  internalCreateViewProjectionRuntimeDecoder,
  type BoundViewProjectionRuntimeDecoder,
  type InternalViewContract,
  type InternalViewContractState,
} from "../viewRuntime";

export function createAssistantViewDecoder(
  conversationId: string,
): BoundViewProjectionRuntimeDecoder<AssistantConversationData> {
  const contract: InternalViewContract<AssistantConversationData> = {
    allowedDeltaEventTypes: ["view.entity_upserted.v1"],
    canonicalRelatedRefTypes: ["task_summary", "worker_detail"],
    decodeData(value) {
      return parseAssistantConversationData(value, conversationId);
    },
    validateState,
  };
  return internalCreateViewProjectionRuntimeDecoder({
    contractKey: `assistant-conversation-v1:${conversationId}`,
    viewType: "assistant_conversation",
    viewId: conversationId,
    contract,
  });
}

function validateState(
  state: InternalViewContractState<AssistantConversationData>,
): undefined {
  const seenActions = new Set<string>();
  for (const action of state.availableActions) {
    if (Object.hasOwn(action, "metadata") || seenActions.has(action.action)) {
      throw new Error("Assistant action authority is invalid");
    }
    seenActions.add(action.action);
    switch (action.action) {
      case "assistant.send_message":
        if (
          state.data.status !== "active"
          || action.target_id !== state.data.conversation_id
          || action.expected_revision !== state.data.revision
        ) {
          throw new Error("Assistant send authority is invalid");
        }
        break;
      case "assistant.confirm_task": {
        const draft = state.data.task_draft;
        const readiness = state.data.readiness;
        if (
          draft === undefined
          || readiness === undefined
          || draft.status !== "current"
          || (readiness.status !== "ready_confirmed" && readiness.status !== "ready_with_assumptions")
          || action.target_id !== draft.task_draft_id
          || action.expected_revision !== draft.revision
        ) {
          throw new Error("Assistant confirmation authority is invalid");
        }
        break;
      }
      case "assistant.checkpoint": {
        const checkpoint = state.data.checkpoint;
        if (
          checkpoint === undefined
          || checkpoint.status !== "active"
          || action.target_id !== checkpoint.checkpoint_id
          || action.expected_revision !== checkpoint.revision
        ) {
          throw new Error("Assistant checkpoint authority is invalid");
        }
        break;
      }
      case "assistant.retry_run": {
        const run = state.data.assistant_run;
        if (
          run === undefined
          || run.status !== "failed"
          || !run.retryable
          || action.target_id !== run.assistant_run_id
          || action.expected_revision !== run.revision
        ) {
          throw new Error("Assistant retry authority is invalid");
        }
        break;
      }
      default:
        throw new Error("Assistant action is unknown");
    }
  }

  const remainingRefs = new Map<string, number>();
  for (const handoff of state.data.handoffs) {
    increment(remainingRefs, refKey("task_summary", handoff.task_id));
    if (handoff.worker_session_id !== undefined) {
      increment(remainingRefs, refKey("worker_detail", handoff.worker_session_id));
    }
  }
  for (const ref of state.relatedRefs) {
    const key = refKey(ref.type, ref.id);
    const remaining = remainingRefs.get(key) ?? 0;
    if (remaining === 0) throw new Error("Assistant related ref authority is invalid");
    remainingRefs.set(key, remaining - 1);
  }
  return undefined;
}

function increment(counts: Map<string, number>, key: string): void {
  counts.set(key, (counts.get(key) ?? 0) + 1);
}

function refKey(type: string, id: string): string {
  return `${type}\0${id}`;
}
