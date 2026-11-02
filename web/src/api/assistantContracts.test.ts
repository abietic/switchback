import { describe, expect, it } from "vitest";
import { parseAssistantConversationData } from "./assistantContracts";

function readinessDimensions() {
  return Object.fromEntries([
    "goal",
    "deliverable_acceptance",
    "scope_target",
    "constraints",
    "context_access",
    "risk_approval",
    "execution_policy",
  ].map((dimension) => [dimension, {
    status: "known",
    summary: `${dimension} is known`,
    evidence_refs: [],
  }]));
}

function taskSpec() {
  return {
    title: "Draft",
    goal: "Deliver the draft.",
    workspace_id: "workspace-1",
    workspace_resolution_ref: {
      resolution_id: "resolution-1",
      revision: 1,
      workspace_revision_id: "workspace-revision-1",
      membership_revision: 1,
    },
    readiness_assessment_ref: {
      assessment_id: "assessment-1",
      policy_revision: "assistant-readiness-v1",
      content_digest: `sha256:${"b".repeat(64)}`,
    },
    creation_intent_ref: {
      intent_id: "pending-confirmation",
      task_spec_digest: `sha256:${"a".repeat(64)}`,
      trigger_policy_revision: "assistant-trigger-v1",
    },
    target: { kind: "workspace", ref: "workspace-1" },
    constraints: [],
    acceptance_criteria: ["The draft is delivered."],
    assumptions: [{
      rule_id: "assistant-default/1",
      description: "Use the existing repository conventions.",
    }],
    agent_config: { profile_id: "codex-deepseek-v3.2", profile_revision: "1", adapter: "codex-acp" },
    runtime_config: {
      network_policy: "trusted_full_network_v1",
      network_policy_revision: 1,
      network_policy_digest: `sha256:${"c".repeat(64)}`,
    },
  };
}

function conversationData() {
  return {
    conversation_id: "conversation-1",
    revision: 1,
    generation: 1,
    status: "active",
    workspace_id: "workspace-1",
    updated_at: "2026-07-25T00:00:00Z",
    turns: [],
    handoffs: [],
    assistant_run: { assistant_run_id: "run-1", status: "claimed", revision: 2, retryable: false },
    readiness: {
      assessment_id: "assessment-1",
      revision: 1,
      status: "ready_confirmed",
      clarification_questions: 0,
      blocking_unknowns: [],
      assumptions: [],
      dimensions: readinessDimensions(),
    },
    task_draft: {
      task_draft_id: "draft-1",
      revision: 1,
      status: "current",
      assumptions: [],
      task_spec: taskSpec(),
    },
    checkpoint: null,
  };
}

describe("Assistant conversation data contract", () => {
  it("binds identity, rejects unsafe revisions and normalizes turn order", () => {
    const parsed = parseAssistantConversationData({
      ...conversationData(),
      turns: [
        { turn_id: "turn-2", sequence: 2, role: "assistant", content: "Two", created_at: "2026-07-25T00:00:00Z" },
        { turn_id: "turn-1", sequence: 1, role: "user", content: "One", created_at: "2026-07-25T00:00:00Z" },
      ],
    }, "conversation-1");
    expect(parsed.turns.map((turn) => turn.turn_id)).toEqual(["turn-1", "turn-2"]);
    expect(() => parseAssistantConversationData({
      ...conversationData(),
      revision: Number.MAX_SAFE_INTEGER + 1,
    }, "conversation-1")).toThrow("conversation revision is invalid");
  });

  it("reads immutable legacy network evidence without inventing enforcement", () => {
    const legacy = structuredClone(conversationData());
    const runtime = legacy.task_draft.task_spec.runtime_config as Record<string, unknown>;
    runtime.network_policy = "m1_public_web_restricted_v1";
    delete runtime.network_policy_revision;
    delete runtime.network_policy_digest;
    expect(parseAssistantConversationData(legacy, "conversation-1")
      .task_draft?.task_spec.runtime_config).toEqual({
        network_policy: "m1_public_web_restricted_v1",
      });
  });

  it("rejects incomplete current network and workspace evidence", () => {
    const network = structuredClone(conversationData());
    delete (network.task_draft.task_spec.runtime_config as Record<string, unknown>)
      .network_policy_revision;
    expect(() => parseAssistantConversationData(network, "conversation-1"))
      .toThrow("runtime network policy revision is invalid");

    const resolution = structuredClone(conversationData());
    delete (resolution.task_draft.task_spec.workspace_resolution_ref as Record<string, unknown>)
      .workspace_revision_id;
    expect(() => parseAssistantConversationData(resolution, "conversation-1"))
      .toThrow("workspace revision id is invalid");
  });
});
