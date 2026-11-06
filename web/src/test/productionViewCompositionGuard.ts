import { parseAst } from "vite";
import type {
  ProductionViewDefinitionByKind,
  ProductionViewKind,
  ProductionViewType,
} from "../projections/productionViewRegistry";

export function findProductionViewCompositionViolations(
  sources: Readonly<Record<string, string>>,
): readonly ProductionViewCompositionViolation[] {
  const violations = new Map<string, Set<string>>();
  for (const [rawPath, source] of Object.entries(sources)) {
    const path = normalizeSourcePath(rawPath);
    let program: OxcNode;
    try {
      program = parseAst(source, {
        lang: path.endsWith(".tsx") ? "tsx" : "ts",
        astType: "ts",
        range: true,
      }, path) as unknown as OxcNode;
    } catch {
      addBoundaryViolation(violations, path, "source does not parse");
      continue;
    }

    const inspection: SourceInspection = {
      program,
      body: oxcNodes(program.body),
      path,
      violations,
    };
    const internalBindings = new Set<string>(
      path === VIEW_RUNTIME_PATH ? INTERNAL_COMPOSITION_SYMBOLS : [],
    );
    const internalFactoryBindings = new Set<string>();
    inspectViewRuntimeImports(
      inspection,
      internalBindings,
      internalFactoryBindings,
    );
    propagateInternalAliases(inspection.program, internalBindings);
    inspectViewRuntimeExports(inspection, internalBindings);
    inspectDynamicModuleAccess(
      inspection,
      collectStaticModuleStrings(inspection.body),
    );

    if (inspection.path === VIEW_RUNTIME_PATH) {
      inspectRuntimeValueExports(inspection);
    }
    const decoderSpec = DECODER_FACTORY_BY_PATH[inspection.path];
    if (decoderSpec !== undefined) {
      inspectDecoderModule(
        inspection,
        decoderSpec,
        internalFactoryBindings,
        internalBindings,
      );
    } else if (
      INTERNAL_COMPOSITION_IMPORTERS.has(inspection.path)
      && inspection.path !== VIEW_RUNTIME_PATH
    ) {
      for (const statement of inspection.body) {
        if (exportReferencesInternalComposition(statement, internalBindings)) {
          addBoundaryViolation(
            inspection.violations,
            inspection.path,
            "internal composition re-export",
          );
        }
      }
    }
  }

  return [...violations]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([path, reasons]) => ({
      path,
      reason: [...reasons].sort().join("; "),
    }));
}

interface OxcNode {
  readonly type: string;
  readonly [key: string]: unknown;
}

interface SourceInspection {
  readonly program: OxcNode;
  readonly body: readonly OxcNode[];
  readonly path: string;
  readonly violations: Map<string, Set<string>>;
}

export interface ProductionViewCompositionViolation {
  readonly path: string;
  readonly reason: string;
}

interface DecoderFactorySpec {
  readonly modulePath: string;
  readonly factoryName: string;
  readonly dataTypeName: string;
  readonly viewType: ProductionViewType;
  readonly contractKeyPrefix: string;
  readonly dynamicId: boolean;
}

type DecoderFactoryManifest = {
  readonly [K in ProductionViewKind]: DecoderFactorySpec & {
    readonly viewType: ProductionViewDefinitionByKind[K]["viewType"];
  };
};

const VIEW_RUNTIME_PATH = "src/projections/viewRuntime.ts";

const INTERNAL_COMPOSITION_SYMBOLS = new Set([
  "InternalViewContract",
  "InternalViewContractRejectionReason",
  "InternalViewContractState",
  "internalCreateViewProjectionRuntimeDecoder",
  "internalRejectViewContract",
]);

const COMPOSITION_TEST_PATH = "src/projections/productionViewComposition.test.ts";

const RUNTIME_VALUE_EXPORT_KINDS: Readonly<Record<string, string>> = {
  ViewProjectionDecodeError: "ClassDeclaration",
  internalCreateViewProjectionRuntimeDecoder: "FunctionDeclaration",
  internalRejectViewContract: "FunctionDeclaration",
};

const DECODER_FACTORY_MANIFEST = {
  home: {
    modulePath: "src/projections/decoders/home.ts",
    factoryName: "createHomeViewDecoder",
    dataTypeName: "HomeDashboardData",
    viewType: "home_dashboard",
    contractKeyPrefix: "home-dashboard-v1:",
    dynamicId: false,
  },
  taskIndex: {
    modulePath: "src/projections/decoders/taskIndex.ts",
    factoryName: "createTaskIndexViewDecoder",
    dataTypeName: "TaskIndexData",
    viewType: "task_summary",
    contractKeyPrefix: "task-index-v1:",
    dynamicId: false,
  },
  assistant: {
    modulePath: "src/projections/decoders/assistant.ts",
    factoryName: "createAssistantViewDecoder",
    dataTypeName: "AssistantConversationData",
    viewType: "assistant_conversation",
    contractKeyPrefix: "assistant-conversation-v1:",
    dynamicId: true,
  },
  worker: {
    modulePath: "src/projections/decoders/worker.ts",
    factoryName: "createWorkerViewDecoder",
    dataTypeName: "WorkerDetailData",
    viewType: "worker_detail",
    contractKeyPrefix: "worker-detail-v1:",
    dynamicId: true,
  },
  artifact: {
    modulePath: "src/projections/decoders/artifact.ts",
    factoryName: "createArtifactViewDecoder",
    dataTypeName: "ArtifactDetailData",
    viewType: "artifact_detail",
    contractKeyPrefix: "artifact-detail-v1:",
    dynamicId: true,
  },
} satisfies DecoderFactoryManifest;

const DECODER_FACTORY_BY_PATH: Readonly<Record<string, DecoderFactorySpec>> = Object.fromEntries(
  Object.values(DECODER_FACTORY_MANIFEST).map((spec) => [spec.modulePath, spec]),
);

const INTERNAL_COMPOSITION_IMPORTERS = new Set([
  VIEW_RUNTIME_PATH,
  "src/projections/viewRuntime.test.ts",
  "src/projections/viewRuntime.typecheck.ts",
  ...Object.values(DECODER_FACTORY_MANIFEST).map(({ modulePath }) => modulePath),
]);

function inspectViewRuntimeImports(
  inspection: SourceInspection,
  internalBindings: Set<string>,
  internalFactoryBindings: Set<string>,
): void {
  const { body, path, violations } = inspection;
  for (const statement of body) {
    if (statement.type !== "ImportDeclaration") continue;
    const moduleSpecifier = oxcString(statement.source);
    if (
      moduleSpecifier === undefined
      || !resolvesToViewRuntime(path, moduleSpecifier)
      || (path === COMPOSITION_TEST_PATH && moduleSpecifier.endsWith("?raw"))
    ) {
      continue;
    }

    for (const specifier of oxcNodes(statement.specifiers)) {
      const localName = oxcName(specifier.local);
      if (specifier.type === "ImportNamespaceSpecifier") {
        if (localName !== undefined) internalBindings.add(localName);
        addBoundaryViolation(violations, path, "namespace import from viewRuntime");
        continue;
      }
      if (specifier.type === "ImportDefaultSpecifier") {
        addBoundaryViolation(violations, path, "default import from viewRuntime");
        continue;
      }
      if (specifier.type !== "ImportSpecifier") continue;
      const importedName = oxcName(specifier.imported);
      if (importedName === undefined || !INTERNAL_COMPOSITION_SYMBOLS.has(importedName)) continue;
      if (localName !== undefined) {
        internalBindings.add(localName);
        if (importedName === "internalCreateViewProjectionRuntimeDecoder") {
          internalFactoryBindings.add(localName);
        }
      }
      if (!INTERNAL_COMPOSITION_IMPORTERS.has(path)) {
        addBoundaryViolation(violations, path, "internal import " + importedName);
      }
    }
  }
}

function inspectViewRuntimeExports(
  inspection: SourceInspection,
  internalBindings: ReadonlySet<string>,
): void {
  const { body, path, violations } = inspection;
  for (const statement of body) {
    if (statement.type !== "ExportNamedDeclaration" && statement.type !== "ExportAllDeclaration") {
      continue;
    }
    const moduleSpecifier = oxcString(statement.source);
    if (moduleSpecifier !== undefined && resolvesToViewRuntime(path, moduleSpecifier)) {
      addBoundaryViolation(violations, path, "re-export from viewRuntime");
      continue;
    }
    if (
      statement.type === "ExportNamedDeclaration"
      && exportReferencesInternalComposition(statement, internalBindings)
      && path !== VIEW_RUNTIME_PATH
      && DECODER_FACTORY_BY_PATH[path] === undefined
    ) {
      addBoundaryViolation(violations, path, "internal composition re-export");
    }
  }
}

function inspectDynamicModuleAccess(
  inspection: SourceInspection,
  staticModuleStrings: ReadonlyMap<string, string>,
): void {
  const { program, path, violations } = inspection;
  walkOxc(program, (node) => {
    if (node.type === "ImportExpression") {
      const moduleSpecifier = oxcStaticString(node.source, staticModuleStrings);
      if (moduleSpecifier !== undefined && resolvesToViewRuntime(path, moduleSpecifier)) {
        addBoundaryViolation(violations, path, "dynamic import of viewRuntime");
      }
      return;
    }
    if (
      path !== COMPOSITION_TEST_PATH
      && isImportMetaGlob(node)
      && globCallMayCoverViewRuntime(node, path)
    ) {
      addBoundaryViolation(violations, path, "import.meta.glob may expose viewRuntime");
    }
  });
}

function collectStaticModuleStrings(
  body: readonly OxcNode[],
): ReadonlyMap<string, string> {
  const declarations: OxcNode[] = [];
  for (const statement of body) {
    const declaration = statement.type === "ExportNamedDeclaration"
      ? oxcNode(statement.declaration)
      : statement;
    if (declaration?.type !== "VariableDeclaration" || declaration.kind !== "const") continue;
    declarations.push(...oxcNodes(declaration.declarations));
  }

  const values = new Map<string, string>();
  let changed: boolean;
  do {
    changed = false;
    for (const declaration of declarations) {
      const name = oxcName(declaration.id);
      if (name === undefined || values.has(name)) continue;
      const value = evaluateStaticString(declaration.init, values);
      if (value !== undefined) {
        values.set(name, value);
        changed = true;
      }
    }
  } while (changed);
  return values;
}

function evaluateStaticString(
  value: unknown,
  bindings: ReadonlyMap<string, string>,
): string | undefined {
  let node = oxcNode(value);
  while (
    node !== undefined
    && (
      node.type === "ParenthesizedExpression"
      || node.type === "TSAsExpression"
      || node.type === "TSSatisfiesExpression"
      || node.type === "TSNonNullExpression"
      || node.type === "TSInstantiationExpression"
    )
  ) {
    node = oxcNode(node.expression);
  }
  const literal = oxcString(node);
  if (literal !== undefined) return literal;
  if (node?.type === "Identifier" && typeof node.name === "string") {
    return bindings.get(node.name);
  }
  if (node?.type === "BinaryExpression" && node.operator === "+") {
    const left = evaluateStaticString(node.left, bindings);
    const right = evaluateStaticString(node.right, bindings);
    return left === undefined || right === undefined ? undefined : left + right;
  }
  if (node?.type !== "TemplateLiteral") return undefined;
  const expressions = oxcNodes(node.expressions);
  const quasis = oxcNodes(node.quasis);
  if (quasis.length !== expressions.length + 1) return undefined;
  let result = templateElementText(quasis[0] ?? { type: "Missing" });
  if (result === undefined) return undefined;
  for (let index = 0; index < expressions.length; index += 1) {
    const expression = evaluateStaticString(expressions[index], bindings);
    const quasi = templateElementText(quasis[index + 1] ?? { type: "Missing" });
    if (expression === undefined || quasi === undefined) return undefined;
    result += expression + quasi;
  }
  return result;
}

function inspectRuntimeValueExports(
  inspection: SourceInspection,
): void {
  const { body, path, violations } = inspection;
  for (const statement of body) {
    if (statement.type === "ExportDefaultDeclaration") {
      addBoundaryViolation(violations, path, "default runtime export");
      continue;
    }
    if (statement.type === "ExportAllDeclaration") {
      addBoundaryViolation(violations, path, "runtime re-export");
      continue;
    }
    if (statement.type !== "ExportNamedDeclaration") continue;
    if (statement.source !== null && statement.source !== undefined) {
      addBoundaryViolation(violations, path, "runtime re-export");
      continue;
    }

    const declaration = oxcNode(statement.declaration);
    if (declaration !== undefined && !isTypeDeclaration(declaration)) {
      const names = declaredValueNames(declaration);
      if (
        names.length !== 1
        || RUNTIME_VALUE_EXPORT_KINDS[names[0] ?? ""] !== declaration.type
      ) {
        addBoundaryViolation(violations, path, "runtime value export outside allowlist");
      }
    }
    for (const specifier of oxcNodes(statement.specifiers)) {
      if (statement.exportKind !== "type" && specifier.exportKind !== "type") {
        addBoundaryViolation(violations, path, "source-local runtime export");
      }
    }
  }
}

function inspectDecoderModule(
  inspection: SourceInspection,
  spec: DecoderFactorySpec,
  internalFactoryBindings: ReadonlySet<string>,
  internalBindings: ReadonlySet<string>,
): void {
  const { program, body, path, violations } = inspection;
  const parserForbiddenBindings = new Set([...internalBindings, spec.factoryName]);
  propagateInternalAliases(program, parserForbiddenBindings);
  let factory: OxcNode | undefined;
  for (const statement of body) {
    if (statement.type === "ExportDefaultDeclaration" || statement.type === "ExportAllDeclaration") {
      addBoundaryViolation(violations, path, "decoder default or re-export");
      continue;
    }
    if (statement.type !== "ExportNamedDeclaration") continue;
    if (statement.source !== null && statement.source !== undefined) {
      addBoundaryViolation(violations, path, "decoder re-export");
      continue;
    }

    const declaration = oxcNode(statement.declaration);
    if (declaration !== undefined && !isTypeDeclaration(declaration)) {
      if (
        declaration.type === "FunctionDeclaration"
        && oxcName(declaration.id) === spec.factoryName
      ) {
        if (factory !== undefined) {
          addBoundaryViolation(violations, path, "duplicate decoder factory");
        }
        factory = declaration;
      } else if (!isClosedWorkerTimelineParser(declaration, path, parserForbiddenBindings)) {
        addBoundaryViolation(violations, path, "decoder runtime export outside singleton allowlist");
      }
    }
    for (const specifier of oxcNodes(statement.specifiers)) {
      if (statement.exportKind !== "type" && specifier.exportKind !== "type") {
        addBoundaryViolation(violations, path, "decoder source-local runtime export");
      }
    }
  }

  if (
    factory === undefined
    || !isClosedDecoderFactory(program, factory, spec, internalFactoryBindings)
  ) {
    addBoundaryViolation(violations, path, "decoder factory shape is not closed");
  }
}

// This data-only parser reuses the private Turn decoder for paged history.
// It is not another projection factory and cannot expose composition authority.
function isClosedWorkerTimelineParser(
  declaration: OxcNode,
  path: string,
  forbiddenBindings: ReadonlySet<string>,
): boolean {
  const params = oxcNodes(declaration.params);
  const input = oxcNode(params[0]?.typeAnnotation);
  const result = oxcNode(oxcNode(declaration.returnType)?.typeAnnotation);
  return path === "src/projections/decoders/worker.ts"
    && declaration.type === "FunctionDeclaration"
    && oxcName(declaration.id) === "parseWorkerTimelinePage"
    && declaration.async !== true && declaration.generator !== true && declaration.declare !== true
    && declaration.typeParameters == null
    && params.length === 1 && params[0].type === "Identifier" && params[0].optional !== true
    && input?.type === "TSTypeAnnotation" && oxcNode(input.typeAnnotation)?.type === "TSUnknownKeyword"
    && result?.type === "TSTypeReference" && oxcName(result.typeName) === "WorkerTimelinePage"
    && result.typeArguments == null
    && oxcNode(declaration.body)?.type === "BlockStatement"
    && !nodeContainsBoundIdentifier(declaration, forbiddenBindings);
}

function isClosedDecoderFactory(
  program: OxcNode,
  factory: OxcNode,
  spec: DecoderFactorySpec,
  internalFactoryBindings: ReadonlySet<string>,
): boolean {
  if (
    factory.type !== "FunctionDeclaration"
    || factory.async === true
    || factory.generator === true
    || factory.declare === true
    || (factory.typeParameters !== null && factory.typeParameters !== undefined)
  ) {
    return false;
  }

  const params = oxcNodes(factory.params);
  let idParameter: string | undefined;
  if (spec.dynamicId) {
    if (params.length !== 1) return false;
    const parameter = params[0];
    idParameter = oxcName(parameter);
    if (
      parameter.type !== "Identifier"
      || idParameter === undefined
      || parameter.optional === true
      || !isStringTypeAnnotation(parameter.typeAnnotation)
    ) {
      return false;
    }
  } else if (params.length !== 0) {
    return false;
  }

  if (!isExactDecoderReturnType(factory.returnType, spec.dataTypeName)) return false;
  const body = oxcNode(factory.body);
  if (body?.type !== "BlockStatement") return false;
  const statements = oxcNodes(body.body);
  const returnStatements = statements.filter((statement) => statement.type === "ReturnStatement");
  if (
    returnStatements.length !== 1
    || statements[statements.length - 1] !== returnStatements[0]
  ) {
    return false;
  }

  const call = oxcNode(returnStatements[0].argument);
  if (
    call?.type !== "CallExpression"
    || call.optional === true
    || (call.typeArguments !== null && call.typeArguments !== undefined)
  ) {
    return false;
  }
  const calleeName = oxcName(call.callee);
  if (calleeName === undefined || !internalFactoryBindings.has(calleeName)) return false;
  const args = oxcNodes(call.arguments);
  if (args.length !== 1 || args[0].type !== "ObjectExpression") return false;
  const properties = oxcNodes(args[0].properties);
  if (properties.length !== 4 || properties.some((property) => property.type !== "Property")) {
    return false;
  }

  const propertyValues = new Map<string, OxcNode>();
  for (const property of properties) {
    if (
      property.kind !== "init"
      || property.computed === true
      || property.method === true
    ) {
      return false;
    }
    const key = oxcName(property.key);
    const value = oxcNode(property.value);
    if (
      key === undefined
      || value === undefined
      || propertyValues.has(key)
      || !["contractKey", "viewType", "viewId", "contract"].includes(key)
    ) {
      return false;
    }
    propertyValues.set(key, value);
  }
  if (propertyValues.size !== 4) return false;

  if (oxcString(propertyValues.get("viewType")) !== spec.viewType) return false;
  const contractName = oxcName(propertyValues.get("contract"));
  if (
    contractName === undefined
    || contractName === idParameter
    || !hasExactLocalContractDeclaration(
      program,
      factory,
      contractName,
      spec.dataTypeName,
    )
  ) {
    return false;
  }

  if (spec.dynamicId) {
    if (idParameter === undefined) return false;
    if (oxcName(propertyValues.get("viewId")) !== idParameter) return false;
    return isDynamicContractKey(
      propertyValues.get("contractKey"),
      spec.contractKeyPrefix,
      idParameter,
    );
  }
  if (oxcString(propertyValues.get("viewId")) !== "index") return false;
  const contractKey = oxcString(propertyValues.get("contractKey"));
  return contractKey === spec.contractKeyPrefix + "index";
}

function isExactDecoderReturnType(value: unknown, dataTypeName: string): boolean {
  const annotation = oxcNode(value);
  const reference = annotation?.type === "TSTypeAnnotation"
    ? oxcNode(annotation.typeAnnotation)
    : undefined;
  if (
    reference?.type !== "TSTypeReference"
    || oxcName(reference.typeName) !== "BoundViewProjectionRuntimeDecoder"
  ) {
    return false;
  }
  const typeArguments = oxcNode(reference.typeArguments);
  const params = oxcNodes(typeArguments?.params);
  return params.length === 1
    && params[0].type === "TSTypeReference"
    && oxcName(params[0].typeName) === dataTypeName
    && (params[0].typeArguments === null || params[0].typeArguments === undefined);
}

function isStringTypeAnnotation(value: unknown): boolean {
  const annotation = oxcNode(value);
  return annotation?.type === "TSTypeAnnotation"
    && oxcNode(annotation.typeAnnotation)?.type === "TSStringKeyword";
}

function isDynamicContractKey(
  value: OxcNode | undefined,
  expectedPrefix: string,
  idParameter: string,
): boolean {
  if (value?.type !== "TemplateLiteral") return false;
  const expressions = oxcNodes(value.expressions);
  const quasis = oxcNodes(value.quasis);
  if (
    expressions.length !== 1
    || oxcName(expressions[0]) !== idParameter
    || quasis.length !== 2
  ) {
    return false;
  }
  const prefix = templateElementText(quasis[0]);
  const suffix = templateElementText(quasis[1]);
  return prefix === expectedPrefix
    && suffix === "";
}

function templateElementText(value: OxcNode): string | undefined {
  const templateValue = value.value;
  return templateValue === null || typeof templateValue !== "object"
    ? undefined
    : typeof (templateValue as { readonly cooked?: unknown }).cooked === "string"
      ? (templateValue as { readonly cooked: string }).cooked
      : typeof (templateValue as { readonly raw?: unknown }).raw === "string"
        ? (templateValue as { readonly raw: string }).raw
        : undefined;
}

function exportReferencesInternalComposition(
  statement: OxcNode,
  internalBindings: ReadonlySet<string>,
): boolean {
  if (statement.type === "ExportDefaultDeclaration") {
    return nodeContainsBoundIdentifier(statement.declaration, internalBindings);
  }
  if (statement.type !== "ExportNamedDeclaration") return false;
  for (const specifier of oxcNodes(statement.specifiers)) {
    const localName = oxcName(specifier.local);
    if (localName !== undefined && internalBindings.has(localName)) return true;
  }
  const declaration = oxcNode(statement.declaration);
  return declaration !== undefined
    && nodeContainsBoundIdentifier(declaration, internalBindings);
}

function propagateInternalAliases(
  program: OxcNode,
  internalBindings: Set<string>,
): void {
  let changed: boolean;
  do {
    changed = false;
    walkOxc(program, (node) => {
      if (node.type !== "VariableDeclarator") return;
      const name = oxcName(node.id);
      const alias = unwrappedIdentifier(node.init);
      if (
        name !== undefined
        && alias !== undefined
        && internalBindings.has(alias)
        && !internalBindings.has(name)
      ) {
        internalBindings.add(name);
        changed = true;
      }
    });
  } while (changed);
}

function unwrappedIdentifier(value: unknown): string | undefined {
  let node = oxcNode(value);
  while (
    node !== undefined
    && (
      node.type === "ParenthesizedExpression"
      || node.type === "TSAsExpression"
      || node.type === "TSSatisfiesExpression"
      || node.type === "TSNonNullExpression"
      || node.type === "TSInstantiationExpression"
    )
  ) {
    node = oxcNode(node.expression);
  }
  return oxcName(node);
}

function hasExactLocalContractDeclaration(
  program: OxcNode,
  factory: OxcNode,
  contractName: string,
  dataTypeName: string,
): boolean {
  const factoryBody = oxcNode(factory.body);
  const localCandidates = factoryBody?.type === "BlockStatement"
    ? directVariableDeclarators(oxcNodes(factoryBody.body), contractName)
    : [];
  const candidates = localCandidates.length > 0
    ? localCandidates
    : directVariableDeclarators(oxcNodes(program.body), contractName);
  return candidates.length === 1
    && candidates[0].kind === "const"
    && isExactInternalContractType(candidates[0].declarator.id, dataTypeName)
    && isExactContractObject(candidates[0].declarator.init);
}

function directVariableDeclarators(
  statements: readonly OxcNode[],
  name: string,
): readonly { readonly kind: unknown; readonly declarator: OxcNode }[] {
  const matches: Array<{ readonly kind: unknown; readonly declarator: OxcNode }> = [];
  for (const statement of statements) {
    const declaration = statement.type === "ExportNamedDeclaration"
      ? oxcNode(statement.declaration)
      : statement;
    if (declaration?.type !== "VariableDeclaration") continue;
    for (const declarator of oxcNodes(declaration.declarations)) {
      if (oxcName(declarator.id) === name) {
        matches.push({ kind: declaration.kind, declarator });
      }
    }
  }
  return matches;
}

function isExactInternalContractType(value: unknown, dataTypeName: string): boolean {
  const identifier = oxcNode(value);
  const annotation = oxcNode(identifier?.typeAnnotation);
  const reference = annotation?.type === "TSTypeAnnotation"
    ? oxcNode(annotation.typeAnnotation)
    : undefined;
  if (
    reference?.type !== "TSTypeReference"
    || oxcName(reference.typeName) !== "InternalViewContract"
  ) {
    return false;
  }
  const argumentsNode = oxcNode(reference.typeArguments);
  const params = oxcNodes(argumentsNode?.params);
  return params.length === 1
    && params[0].type === "TSTypeReference"
    && oxcName(params[0].typeName) === dataTypeName
    && (params[0].typeArguments === null || params[0].typeArguments === undefined);
}

function isExactContractObject(value: unknown): boolean {
  const object = oxcNode(value);
  if (object?.type !== "ObjectExpression") return false;
  const properties = oxcNodes(object.properties);
  if (properties.length !== 4 || properties.some((property) => property.type !== "Property")) {
    return false;
  }
  const names = properties.map((property) => (
    property.computed === true ? undefined : oxcName(property.key)
  ));
  return names.every((name): name is string => name !== undefined)
    && new Set(names).size === 4
    && [
      "allowedDeltaEventTypes",
      "canonicalRelatedRefTypes",
      "decodeData",
      "validateState",
    ].every((name) => names.includes(name));
}

function nodeContainsBoundIdentifier(
  value: unknown,
  bindings: ReadonlySet<string>,
): boolean {
  let found = false;
  walkOxc(value, (node) => {
    if (node.type === "Identifier" && typeof node.name === "string" && bindings.has(node.name)) {
      found = true;
    }
  });
  return found;
}

function isImportMetaGlob(node: OxcNode): boolean {
  if (node.type !== "CallExpression") return false;
  const callee = oxcNode(node.callee);
  if (callee?.type !== "MemberExpression") return false;
  const object = oxcNode(callee.object);
  return object?.type === "MetaProperty"
    && oxcName(object.meta) === "import"
    && oxcName(object.property) === "meta"
    && (oxcName(callee.property) === "glob" || oxcName(callee.property) === "globEager");
}

function globCallMayCoverViewRuntime(call: OxcNode, importerPath: string): boolean {
  const patterns: string[] = [];
  const firstArgument = oxcNodes(call.arguments)[0];
  if (firstArgument?.type === "ArrayExpression") {
    for (const element of oxcNodes(firstArgument.elements)) {
      const pattern = oxcStaticString(element);
      if (pattern !== undefined) patterns.push(pattern);
    }
  } else {
    const pattern = oxcStaticString(firstArgument);
    if (pattern !== undefined) patterns.push(pattern);
  }
  return patterns.some((pattern) => {
    const resolvedPattern = resolveSourcePattern(importerPath, pattern);
    return globPatternMatches(resolvedPattern, VIEW_RUNTIME_PATH);
  });
}

function resolveSourcePattern(importerPath: string, pattern: string): string {
  if (!pattern.startsWith(".")) return pattern;
  const segments = importerPath.split("/").slice(0, -1);
  for (const segment of pattern.split("/")) {
    if (segment === "." || segment.length === 0) continue;
    if (segment === "..") {
      segments.pop();
    } else {
      segments.push(segment);
    }
  }
  return segments.join("/");
}

function globPatternMatches(pattern: string, candidate: string): boolean {
  let expression = "";
  for (let index = 0; index < pattern.length; index += 1) {
    const character = pattern.charAt(index);
    if (character === "*" && pattern[index + 1] === "*") {
      expression += ".*";
      index += 1;
    } else if (character === "*") {
      expression += "[^/]*";
    } else if (character === "?") {
      expression += "[^/]";
    } else if (character === "{") {
      const end = pattern.indexOf("}", index + 1);
      if (end < 0) {
        expression += "\\{";
      } else {
        expression += "(" + pattern.slice(index + 1, end)
          .split(",")
          .map(escapeRegularExpression)
          .join("|") + ")";
        index = end;
      }
    } else {
      expression += escapeRegularExpression(character);
    }
  }
  return new RegExp("^" + expression + "$").test(candidate);
}

function escapeRegularExpression(value: string): string {
  return value.replace(/[.*+?^$(){}|[\]\\]/g, "\\$&");
}

function isTypeDeclaration(node: OxcNode): boolean {
  return node.type === "TSInterfaceDeclaration"
    || node.type === "TSTypeAliasDeclaration"
    || node.type === "TSDeclareFunction"
    || node.type === "TSModuleDeclaration";
}

function declaredValueNames(declaration: OxcNode): readonly string[] {
  if (
    declaration.type === "FunctionDeclaration"
    || declaration.type === "ClassDeclaration"
    || declaration.type === "TSEnumDeclaration"
  ) {
    const name = oxcName(declaration.id);
    return name === undefined ? [] : [name];
  }
  if (declaration.type === "VariableDeclaration") {
    return oxcNodes(declaration.declarations)
      .map((item) => oxcName(item.id))
      .filter((name): name is string => name !== undefined);
  }
  return [];
}

function oxcNode(value: unknown): OxcNode | undefined {
  return value !== null
    && typeof value === "object"
    && typeof (value as { readonly type?: unknown }).type === "string"
    ? value as OxcNode
    : undefined;
}

function oxcNodes(value: unknown): readonly OxcNode[] {
  return Array.isArray(value)
    ? value.map(oxcNode).filter((node): node is OxcNode => node !== undefined)
    : [];
}

function oxcName(value: unknown): string | undefined {
  const node = oxcNode(value);
  if (node?.type === "Identifier" && typeof node.name === "string") return node.name;
  return oxcString(node);
}

function oxcString(value: unknown): string | undefined {
  const node = oxcNode(value);
  return node?.type === "Literal" && typeof node.value === "string"
    ? node.value
    : undefined;
}

function oxcStaticString(
  value: unknown,
  bindings: ReadonlyMap<string, string> = new Map(),
): string | undefined {
  return evaluateStaticString(value, bindings);
}

function walkOxc(value: unknown, visit: (node: OxcNode) => void): void {
  const node = oxcNode(value);
  if (node !== undefined) {
    visit(node);
    for (const [key, child] of Object.entries(node)) {
      if (key !== "range") walkOxc(child, visit);
    }
    return;
  }
  if (Array.isArray(value)) {
    for (const child of value) walkOxc(child, visit);
  }
}

function addBoundaryViolation(
  violations: Map<string, Set<string>>,
  path: string,
  reason: string,
): void {
  const reasons = violations.get(path) ?? new Set<string>();
  reasons.add(reason);
  violations.set(path, reasons);
}

function resolvesToViewRuntime(importerPath: string, specifier: string): boolean {
  const cleanSpecifier = specifier.split("?", 1)[0].replace(/\.[cm]?[jt]sx?$/, "");
  if (!cleanSpecifier.startsWith(".")) {
    return cleanSpecifier === "src/projections/viewRuntime"
      || cleanSpecifier.endsWith("/projections/viewRuntime");
  }
  const segments = importerPath.split("/").slice(0, -1);
  for (const segment of cleanSpecifier.split("/")) {
    if (segment === "." || segment.length === 0) continue;
    if (segment === "..") {
      segments.pop();
    } else {
      segments.push(segment);
    }
  }
  return segments.join("/") === "src/projections/viewRuntime";
}

function normalizeSourcePath(path: string): string {
  if (path.startsWith("./")) return "src/projections/" + path.slice(2);
  if (path.startsWith("../")) return "src/" + path.slice(3);
  return path;
}
