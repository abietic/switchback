import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchWorkerDiagnostics } from "./workerDiagnostics";

afterEach(() => vi.unstubAllGlobals());
const target = { sessionId: "session-1", runId: "old-run", turnId: "old-turn" };
const verified = {
  target: { worker_session_id: "session-1", worker_run_id: "old-run", agent_turn_id: "old-turn" },
  links: [{ kind: "langfuse", url: "http://localhost:3000/project/switchback-m1/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }],
  grafana_status: "disabled", langfuse_status: "historically_verified",
  identity: { version: "langfuse.projection.v3", trace_id: "a".repeat(32), selected_observation_id: "b".repeat(32), sources: [
    { kind: "run_basis", source_id: "run-source", frame_digest: `sha256:${"c".repeat(64)}` },
    { kind: "turn_basis", source_id: "turn-source", frame_digest: `sha256:${"d".repeat(64)}` },
  ] },
};
function reply(body: unknown) { vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => body })); }
describe("worker diagnostics public contract", () => {
  it("rejects a coercible array Grafana status at the exact target", async () => {
    reply({ target: verified.target, links: [], grafana_status: ["disabled"], langfuse_status: "pending" });
    await expect(fetchWorkerDiagnostics(target, new AbortController().signal)).rejects.toThrow("Invalid diagnostic links");
  });
  it("accepts a historically verified exact identity without inventing verification time", async () => {
    reply(verified);
    expect(await fetchWorkerDiagnostics(target, new AbortController().signal)).toEqual(verified);
  });
  it.each([
    ["grafana_status", "object", {}],
    ["grafana_status", "null", null],
    ["grafana_status", "number", 1],
    ["grafana_status", "boolean", false],
    ["langfuse_status", "array", ["pending"]],
    ["langfuse_status", "object", {}],
    ["langfuse_status", "null", null],
    ["langfuse_status", "number", 1],
    ["langfuse_status", "boolean", false],
  ] as const)("rejects %s with a non-string %s value", async (field, _type, value) => {
    reply({ target: verified.target, links: [], grafana_status: "disabled", langfuse_status: "pending", [field]: value });
    await expect(fetchWorkerDiagnostics(target, new AbortController().signal)).rejects.toThrow("Invalid diagnostic links");
  });
  it.each(["disabled", "invalid_config", "identity_unavailable", "pending", "blocked", "unavailable"])(
    "accepts the valid string Langfuse status %s without identity", async (status) => {
      const body = { target: verified.target, links: [], grafana_status: "disabled", langfuse_status: status };
      reply(body);
      expect(await fetchWorkerDiagnostics(target, new AbortController().signal)).toEqual(body);
    },
  );
  it.each(["configured", "disabled", "invalid_config"])("accepts the valid string Grafana status %s", async (status) => {
    const body = { target: verified.target, grafana_status: status, langfuse_status: "pending", links: status === "configured" ? [
      { kind: "grafana", url: "http://localhost:3000/d/sb-worker-runtime/worker-runtime" },
      { kind: "logs", url: "http://localhost:3000/explore" },
    ] : [] };
    reply(body);
    expect(await fetchWorkerDiagnostics(target, new AbortController().signal)).toEqual(body);
  });
  it.each([
    ["wrong target", { ...verified, target: { ...verified.target, agent_turn_id: "other" } }],
    ["duplicate kinds", { ...verified, links: [...verified.links, ...verified.links] }],
    ["unknown status", { ...verified, langfuse_status: "verified" }],
    ["missing identity", { ...verified, identity: undefined }],
    ["missing link", { ...verified, links: [] }],
    ["nonverified identity", { ...verified, langfuse_status: "pending", links: [] }],
    ["wrong trace URL", { ...verified, links: [{ kind: "langfuse", url: "http://localhost:3000/project/switchback-m1/traces/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }] }],
    ["noncanonical project", { ...verified, links: [{ kind: "langfuse", url: "http://localhost:3000/project/../traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }] }],
    ["malformed sources", { ...verified, identity: { ...verified.identity, sources: [] } }],
    ["invalid version", { ...verified, identity: { ...verified.identity, version: "old" } }],
    ["unsafe link", { ...verified, links: [{ kind: "langfuse", url: "http://secret@localhost:3000/project/switchback-m1/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }] }],
  ])("rejects %s", async (_label, body) => {
    reply(body);
    await expect(fetchWorkerDiagnostics(target, new AbortController().signal)).rejects.toThrow("Invalid diagnostic links");
  });
});
