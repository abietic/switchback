import type { ArtifactDetailData } from "../../api/contracts";
import {
  parseArtifactDetailData,
  validateArtifactProjectionAuthority,
} from "../artifactAuthority";
import {
  internalCreateViewProjectionRuntimeDecoder,
  type BoundViewProjectionRuntimeDecoder,
  type InternalViewContract,
} from "../viewRuntime";

export function createArtifactViewDecoder(
  artifactId: string,
): BoundViewProjectionRuntimeDecoder<ArtifactDetailData> {
  const contract: InternalViewContract<ArtifactDetailData> = {
    allowedDeltaEventTypes: ["view.entity_upserted.v1", "view.artifact_ready.v1"],
    canonicalRelatedRefTypes: [],
    decodeData(value) {
      return parseArtifactDetailData(value, artifactId);
    },
    validateState: validateArtifactProjectionAuthority,
  };
  return internalCreateViewProjectionRuntimeDecoder({
    contractKey: `artifact-detail-v1:${artifactId}`,
    viewType: "artifact_detail",
    viewId: artifactId,
    contract,
  });
}
