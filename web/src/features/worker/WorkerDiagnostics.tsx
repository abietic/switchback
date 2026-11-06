import { useEffect, useState } from "react";
import type { WorkerDiagnosticLinks } from "../../api/contracts";
import { fetchWorkerDiagnostics, type WorkerDiagnosticTarget } from "../../api/workerDiagnostics";

export function WorkerDiagnostics(target: WorkerDiagnosticTarget) {
  // Changing any target discards old links synchronously, including in-flight replies.
  return <DiagnosticTarget key={JSON.stringify(target)} {...target} />;
}

function DiagnosticTarget({ sessionId, runId, turnId }: WorkerDiagnosticTarget) {
  const [result, setResult] = useState<WorkerDiagnosticLinks>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    fetchWorkerDiagnostics({ sessionId, runId, turnId }, controller.signal).then(
      (next) => { if (current) setResult(next); },
      () => { if (current) setFailed(true); },
    );
    return () => { current = false; controller.abort(); };
  }, [sessionId, runId, turnId]);
  return <section className="side-section metadata-section" aria-label="Diagnostics">
    <h2>Diagnostics</h2>
    {failed ? <p role="status">Diagnostics unavailable.</p> : !result ? <p role="status">Loading diagnostics…</p> : <>
      {result.links.map((link) => <p key={link.kind}><a href={link.url} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
        {link.kind === "grafana" ? "Grafana · Worker overview" : link.kind === "logs" ? "Logs · selected scope" : "Langfuse"}
      </a></p>)}
      {result.grafana_status === "disabled" ? <p>{result.langfuse_status === "historically_verified" ? "Grafana and Logs links disabled." : "Diagnostic links disabled."}</p> : result.grafana_status === "invalid_config" ? <p>Grafana and Logs links unavailable: invalid local browser configuration.</p> : <small>Aggregate metrics; logs use the selected Session, Run and Turn. Service availability is not checked.</small>}
      <p><small>{result.langfuse_status === "historically_verified" ? "身份曾核验；链接使用当前项目配置，未实时核对" :
        result.langfuse_status === "disabled" ? "Langfuse links disabled." :
          result.langfuse_status === "invalid_config" ? "Langfuse link unavailable: invalid local browser configuration." :
            result.langfuse_status === "unavailable" ? "Langfuse identity read unavailable. Remote service availability is not checked." :
              result.langfuse_status === "pending" ? "Langfuse identity verification pending." :
                result.langfuse_status === "blocked" ? "Langfuse identity verification blocked." :
                  "Langfuse link unavailable: verified projection identity is not available."}</small></p>
      {result.identity ? <details><summary>Historical identity</summary>
        <p style={{ overflowWrap: "anywhere" }}>Trace {result.identity.trace_id}</p>
        {result.identity.selected_observation_id ? <p style={{ overflowWrap: "anywhere" }}>Observation {result.identity.selected_observation_id}</p> : null}
        <small>{result.identity.version}</small>
        {result.identity.sources.map((source) => <p key={source.kind} style={{ overflowWrap: "anywhere" }}><small>{source.kind} · {source.source_id} · {source.frame_digest}</small></p>)}
      </details> : null}
    </>}
  </section>;
}
