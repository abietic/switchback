import type { ArtifactDetailData, WorkerDetailData } from "../api/contracts";
import { loadArtifactPreview } from "./artifactPreview";
import {
  productionViews,
  type BoundProductionView,
  type DataOfKind,
} from "./productionViewRegistry";
import type { BoundViewProjectionRuntimeDecoder } from "./viewRuntime";

const worker = productionViews.worker("session-1");
const artifact = productionViews.artifact("artifact-1");
const workerData: DataOfKind<"worker"> = {} as WorkerDetailData;
void workerData;

// @ts-expect-error Bound views require the projections-private brand.
const forged: BoundProductionView<"worker"> = {
  kind: "worker",
  runtimeVersion: "v1",
  contractKey: "worker-detail-v1:session-1",
  viewType: "worker_detail",
  viewId: "session-1",
  decoder: worker.decoder,
};
void forged;

// @ts-expect-error A Worker binding cannot be assigned to the Artifact kind.
const wrongKind: BoundProductionView<"artifact"> = worker;
void wrongKind;

// @ts-expect-error Cross-kind decoders preserve their exact data contract.
const wrongDecoder: BoundViewProjectionRuntimeDecoder<ArtifactDetailData> = worker.decoder;
void wrongDecoder;

// @ts-expect-error The registry has no arbitrary kind lookup or fallback factory.
const arbitrary = productionViews.future("id");
void arbitrary;

// @ts-expect-error Artifact data is not Worker data.
const wrongData: DataOfKind<"worker"> = {} as ArtifactDetailData;
void wrongData;

// @ts-expect-error One-shot Artifact decoding accepts only an Artifact binding.
loadArtifactPreview(worker);
void artifact;
