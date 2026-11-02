import type {
  ArtifactDetailData,
  AssistantConversationData,
  TaskIndexData,
  WorkerDetailData,
} from "../api/contracts";
import type { HomeDashboardData } from "../api/homeContracts";
import { createArtifactViewDecoder } from "./decoders/artifact";
import { createAssistantViewDecoder } from "./decoders/assistant";
import { createHomeViewDecoder } from "./decoders/home";
import { createTaskIndexViewDecoder } from "./decoders/taskIndex";
import { createWorkerViewDecoder } from "./decoders/worker";
import type { BoundViewProjectionRuntimeDecoder } from "./viewRuntime";

const boundProductionViewBrand: unique symbol = Symbol("boundProductionView");

export interface ProductionViewDefinitionByKind {
  readonly home: {
    readonly viewType: "home_dashboard";
    readonly data: HomeDashboardData;
    readonly factoryArguments: readonly [];
  };
  readonly taskIndex: {
    readonly viewType: "task_summary";
    readonly data: TaskIndexData;
    readonly factoryArguments: readonly [];
  };
  readonly assistant: {
    readonly viewType: "assistant_conversation";
    readonly data: AssistantConversationData;
    readonly factoryArguments: readonly [conversationId: string];
  };
  readonly worker: {
    readonly viewType: "worker_detail";
    readonly data: WorkerDetailData;
    readonly factoryArguments: readonly [workerSessionId: string];
  };
  readonly artifact: {
    readonly viewType: "artifact_detail";
    readonly data: ArtifactDetailData;
    readonly factoryArguments: readonly [artifactId: string];
  };
}

export type ProductionViewKind = keyof ProductionViewDefinitionByKind;
export type ProductionViewType = ProductionViewDefinitionByKind[ProductionViewKind]["viewType"];
export type DataOfKind<K extends ProductionViewKind> = ProductionViewDefinitionByKind[K]["data"];

export interface BoundProductionView<K extends ProductionViewKind> {
  readonly [boundProductionViewBrand]: K;
  readonly kind: K;
  readonly runtimeVersion: "v1";
  readonly contractKey: string;
  readonly viewType: ProductionViewDefinitionByKind[K]["viewType"];
  readonly viewId: string;
  readonly decoder: BoundViewProjectionRuntimeDecoder<DataOfKind<K>>;
}

export type AnyBoundProductionView = {
  readonly [K in ProductionViewKind]: BoundProductionView<K>;
}[ProductionViewKind];

type ProductionViewFactories = {
  readonly [K in ProductionViewKind]: (
    ...args: ProductionViewDefinitionByKind[K]["factoryArguments"]
  ) => BoundProductionView<K>;
};

export const productionViews: ProductionViewFactories = Object.freeze({
  home: () => bindProductionView("home", "home_dashboard", createHomeViewDecoder()),
  taskIndex: () => bindProductionView("taskIndex", "task_summary", createTaskIndexViewDecoder()),
  assistant: (conversationId: string) => bindProductionView(
    "assistant",
    "assistant_conversation",
    createAssistantViewDecoder(conversationId),
  ),
  worker: (workerSessionId: string) => bindProductionView(
    "worker",
    "worker_detail",
    createWorkerViewDecoder(workerSessionId),
  ),
  artifact: (artifactId: string) => bindProductionView(
    "artifact",
    "artifact_detail",
    createArtifactViewDecoder(artifactId),
  ),
} satisfies ProductionViewFactories);

function bindProductionView<K extends ProductionViewKind>(
  kind: K,
  viewType: ProductionViewDefinitionByKind[K]["viewType"],
  decoder: BoundViewProjectionRuntimeDecoder<DataOfKind<K>>,
): BoundProductionView<K> {
  if (decoder.viewType !== viewType) {
    throw new Error("Production View factory identity is invalid");
  }
  return Object.freeze({
    [boundProductionViewBrand]: kind,
    kind,
    runtimeVersion: "v1",
    contractKey: decoder.contractKey,
    viewType,
    viewId: decoder.viewId,
    decoder,
  });
}
