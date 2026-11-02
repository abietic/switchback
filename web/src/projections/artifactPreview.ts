import type { ArtifactDetailData } from "../api/contracts";
import type { BoundProductionView } from "./productionViewRegistry";
import {
  fetchBoundProjectionSnapshot,
  type SnapshotTransportFailure,
  type SnapshotTransportOptions,
} from "./snapshotTransport";
import type { TrustedViewProjectionState } from "./viewRuntime";

export type ArtifactPreview = TrustedViewProjectionState<ArtifactDetailData>;

export type ArtifactPreviewOutcome =
  | {
      readonly kind: "available";
      readonly preview: ArtifactPreview;
    }
  | SnapshotTransportFailure;

export async function loadArtifactPreview(
  view: BoundProductionView<"artifact">,
  options: SnapshotTransportOptions = {},
): Promise<ArtifactPreviewOutcome> {
  const outcome = await fetchBoundProjectionSnapshot(view, [], options);
  if (outcome.kind !== "decoded") return outcome;
  return Object.freeze({ kind: "available", preview: outcome.snapshot.effective });
}
