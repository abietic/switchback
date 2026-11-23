import { useEffect, useState } from "react";

interface Receipt {
  reviewer: "argus";
  verdict: "passed" | "rework_required";
  findings: { code: string; summary: string }[];
}

interface CurrentReview {
  task_revision: number;
  review: Receipt | null;
}

export function DeliveryReviewFeedback({ taskId, taskRevision }: { taskId: string; taskRevision: number }) {
  const [attempt, setAttempt] = useState(0);
  const [receipt, setReceipt] = useState<Receipt | null>(null);
  const [message, setMessage] = useState("");

  useEffect(() => {
    if (attempt === 0) return;
    const controller = new AbortController();
    void (async () => {
      try {
        const response = await fetch(`/api/v1/tasks/${encodeURIComponent(taskId)}/delivery-review`, {
          signal: controller.signal,
          cache: "no-store",
        });
        if (!response.ok) throw new Error("Unable to read review");
        const result = await response.json() as CurrentReview;
        if (controller.signal.aborted) return;
        if (result.task_revision !== taskRevision) {
          setMessage("The delivery changed. Refresh its review actions before accepting.");
          return;
        }
        if (result.review !== null && (result.review?.reviewer !== "argus" ||
          !["passed", "rework_required"].includes(result.review.verdict) ||
          !Array.isArray(result.review.findings) ||
          result.review.findings.some((finding) => typeof finding?.code !== "string" || typeof finding.summary !== "string"))) {
          throw new Error("Invalid review receipt");
        }
        setReceipt(result.review);
        setMessage(result.review ? "" : "No Argus review recorded for this report.");
      } catch {
        if (!controller.signal.aborted) setMessage("Unable to read the Argus review.");
      }
    })();
    return () => controller.abort();
  }, [attempt, taskId, taskRevision]);

  return <div>
    <button type="button" onClick={() => {
      setReceipt(null);
      setMessage("Checking Argus review…");
      setAttempt((value) => value + 1);
    }}>Check Argus review</button>
    {message ? <p role="status">{message}</p> : null}
    {receipt ? <>
      <p role="status">{receipt.verdict === "passed" ? "Argus review passed." : "Argus requested rework."}</p>
      {receipt.findings.length ? <ul>
        {receipt.findings.map((finding, index) => <li key={`${finding.code}:${index}`}>{finding.summary}</li>)}
      </ul> : null}
    </> : null}
  </div>;
}
