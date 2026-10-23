import {
  retainInlineCapture,
  type InlineCapture,
  type InlineCaptureReader,
} from "./capture";
import { InlineMarkdown } from "./markdown";
import "./inline-ui.css";
import schema from "./schema.json";
import {
  Fragment,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useTheme } from "next-themes";
import { PreviewContinuationContext } from "@/shared/chat/preview-continuation-context";
import { parseInlineUI, inlineFallback, type InlineNode } from "./parser";
import {
  failureMessage,
  recordDiagnostic,
  repairRequest,
  type InlineDiagnostic,
} from "./recovery";
import { InlineBoundary, InlineStage } from "./render-state";
import {
  Interpreter,
  interactionBudget,
  checkLocalValue,
  isCallable,
  valueText,
  sameDependencies,
  type Environment,
  type RuntimeHost,
  type RuntimeExecution,
} from "./runtime";
import { Primitive, Unavailable } from "./primitives";
import { safeURL } from "./style";
import { repairProperties, validateProperties } from "./contract";
import { InlineAppBlock } from "./app-block";
import { resolveIconName } from "./icon-aliases";
import { hasIcon } from "@/shared/ui/icon";
import {
  InlineReferenceContext,
  InlineInteractionContext,
  type InlineReferenceRenderer,
} from "./reference-context";

const labelledControls = [
  "input",
  "textarea",
  "select",
  "date-picker",
  "slider",
  "segmented-control",
];

function hasProse(nodes: InlineNode[]): boolean {
  return nodes.some((node) =>
    node.type === "text"
      ? !!node.value.trim()
      : node.type === "if"
        ? hasProse(node.children) || hasProse(node.otherwise)
        : node.type === "each" || (node.type === "element" && node.name === "")
          ? hasProse(node.children)
          : false,
  );
}

function InlineDocumentContent({
  source,
  streaming = false,
  readOnly = false,
  renderMarkdown,
  openEntity,
  maxSourceBytes = 262144,
  capture,
  registerCapture,
  renderReference,
}: {
  source: string;
  streaming?: boolean;
  readOnly?: boolean;
  renderMarkdown?: (text: string) => ReactNode;
  openEntity?: (entity: unknown) => Promise<void>;
  maxSourceBytes?: number;
  capture?: InlineCapture;
  registerCapture?: (read: InlineCaptureReader) => () => void;
  renderReference?: InlineReferenceRenderer;
}) {
  const referenceHost = useContext(InlineReferenceContext);
  const hostInteractive = useContext(InlineInteractionContext);
  const host = useContext(PreviewContinuationContext);
  const { resolvedTheme } = useTheme();
  const instanceId = useId();
  const identity = capture?.identity ?? instanceId;
  const state = useRef(
    new Map<string, unknown>(Object.entries(capture?.state ?? {})),
  );
  const execution = useRef<RuntimeExecution>({
    remaining: 50000,
    depth: 0,
    event: false,
    hostActions: false,
  });
  const action = useRef<RuntimeHost["action"]>(() => {
    throw new Error("Host unavailable.");
  });
  const refs = useRef(
    new Map<string, { current: unknown }>(Object.entries(capture?.refs ?? {})),
  );
  const memo = useRef<RuntimeHost["memo"]>(
    new Map(Object.entries(capture?.memo ?? {})),
  );
  const effectDependencies = useRef(new Map<string, unknown[]>());
  const cleanups = useRef(new Map<string, () => void>());
  const [revision, setRevision] = useState(0);
  const renderKey = useMemo(() => ({}), [source, revision]);
  const previousNodes = useRef(new Map<string, ReactNode>());
  const lastDocument = useRef<ReactNode>(null);
  const [renderIssues, setRenderIssues] = useState<{
    key: unknown;
    items: InlineDiagnostic[];
  }>({ key: renderKey, items: [] });
  const [actionError, setActionError] = useState("");
  const [actionStatus, setActionStatus] = useState("");
  useEffect(() => {
    if (!actionStatus) return;
    const timer = setTimeout(() => setActionStatus(""), 2500);
    return () => clearTimeout(timer);
  }, [actionStatus]);
  const [showSource, setShowSource] = useState(false);
  const [requestingRepair, setRequestingRepair] = useState(false);
  const [width, setWidth] = useState(1024);
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const element = root.current;
    if (!element) return;
    const observer = new ResizeObserver((entries) =>
      setWidth(entries[0]?.contentRect.width ?? 1024),
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const parsed = useMemo(() => {
    if (new TextEncoder().encode(source).length > maxSourceBytes) {
      // Bound component interpretation without limiting ordinary Markdown.
      const nodes: InlineNode[] = [{ type: "text", value: source, start: 0 }];
      return { nodes, complete: true, diagnostic: undefined, overLimit: true };
    }
    return { ...parseInlineUI(source, { recover: true }), overLimit: false };
  }, [source, maxSourceBytes]);
  const interactive =
    !parsed.overLimit &&
    !parsed.diagnostic &&
    hostInteractive &&
    !readOnly &&
    !capture;
  const canInteract = useRef(interactive);
  canInteract.current = interactive;
  const effects: RuntimeHost["effects"] = [];
  const diagnostics: InlineDiagnostic[] = [...(parsed.diagnostics ?? [])];
  if (renderIssues.key === renderKey)
    renderIssues.items.forEach((item) => recordDiagnostic(diagnostics, item));
  const reportRender = (item: InlineDiagnostic) =>
    setRenderIssues((previous) => {
      const items = previous.key === renderKey ? [...previous.items] : [];
      const length = items.length;
      recordDiagnostic(items, item);
      return previous.key === renderKey && items.length === length
        ? previous
        : { key: renderKey, items };
    });
  const reportEvaluation = (
    error: unknown,
    start: number,
    component?: string,
  ) =>
    recordDiagnostic(diagnostics, {
      code: "evaluation_error",
      phase: "evaluate",
      start,
      component,
      message: failureMessage(error),
      repair: "omitted",
    });
  action.current = (name, args) => {
    if (!interactive || streaming || !parsed.complete)
      throw new Error(
        "Wait for this response to finish before using this action.",
      );
    if (name === "copy")
      // Confirm only after the clipboard write succeeds.
      return navigator.clipboard
        .writeText(valueText(args[0]))
        .then(() => setActionStatus("Copied to clipboard."));
    if (name === "openUrl") {
      const url = safeURL(args[0]);
      if (!url) throw new Error("This action needs a valid URL.");
      window.open(url, "_blank", "noopener,noreferrer");
      return;
    }
    if (name === "issueNewTurn") {
      if (!host?.submit)
        throw new Error("Sending a follow-up is unavailable in this view.");
      return host.submit(valueText(args[0]));
    }
    if (name === "openEntityDetail" && (openEntity || host?.openEntity))
      return (openEntity ?? host!.openEntity!)(args[0]);
    throw new Error(`${name} is not connected in Zenith Team.`);
  };
  execution.current.remaining = 50000;
  execution.current.depth = 0;
  const runtime = new Interpreter({
    execution: execution.current,
    state: state.current,
    refs: refs.current,
    memo: memo.current,
    effects,
    diagnostic: (error, start) => reportEvaluation(error, start, "declaration"),
    theme: capture?.theme ?? resolvedTheme ?? "light",
    width: capture?.width ?? width,
    change: (key, value) => {
      if (!Object.is(state.current.get(key), value)) {
        state.current.set(key, value);
        setRevision((v) => v + 1);
      }
    },
    action: (name, args) => action.current(name, args),
  });
  const run = (
    callback: unknown,
    args: unknown[] = [],
    allowHostActions = true,
  ) => {
    if (!canInteract.current || !isCallable(callback)) return;
    setActionError("");
    effectRuns.current = 0;
    runtime.remaining = interactionBudget;
    runtime.execution.deadline = performance.now() + 100;
    runtime.event = true;
    runtime.hostActions = allowHostActions;
    const before = new Map(state.current);
    try {
      const result = runtime.call(callback, args);
      if (result instanceof Promise)
        void result.catch((error) =>
          setActionError(
            error instanceof Error ? error.message : "Action failed.",
          ),
        );
    } catch (error) {
      state.current.clear();
      before.forEach((value, key) => state.current.set(key, value));
      setRevision((v) => v + 1);
      setActionError(error instanceof Error ? error.message : "Action failed.");
    } finally {
      runtime.event = false;
      runtime.hostActions = false;
      runtime.execution.deadline = undefined;
    }
  };
  let diagnostic = parsed.diagnostic;
  let content: ReactNode;
  try {
    const env = runtime.environment();
    const render = (
      nodes: InlineNode[],
      env: Environment,
      path: string,
      depth = 0,
      inline = false,
      svg = false,
    ): ReactNode => {
      if (depth > 32) throw new Error("UI nesting exceeds its limit.");
      // `<label>Name</label><input/>` names the control that follows it, as the
      // authoring guide writes forms, without requiring ids.
      const labels = new Map<number, string>();
      nodes.forEach((node, index) => {
        if (
          node.type !== "element" ||
          node.name !== "label" ||
          node.props.htmlFor
        )
          return;
        let next = index + 1;
        while (
          nodes[next]?.type === "text" &&
          !(nodes[next] as { value: string }).value.trim()
        )
          next++;
        const control = nodes[next];
        if (
          control?.type === "element" &&
          labelledControls.includes(control.name) &&
          !control.props.id
        ) {
          const id = `${identity}${path}.${index}`.replace(/[^\w-]/g, "-");
          labels.set(index, id);
          labels.set(next, id);
        }
      });
      return nodes.map((node, index) => {
        const key = `${path}.${index}`;
        try {
          runtime.tick();
          const rendered = (() => {
            if (node.type === "pending")
              return (
                <InlineStage
                  key={key}
                  name={node.name}
                  failed={node.failed}
                  streaming={streaming}
                />
              );
            if (node.type === "text")
              return inline ? (
                node.value
              ) : (
                <Fragment key={key}>
                  {renderMarkdown ? (
                    renderMarkdown(node.value)
                  ) : (
                    <InlineMarkdown>{node.value}</InlineMarkdown>
                  )}
                </Fragment>
              );
            if (node.type === "body" || node.type === "module") {
              runtime.hookScope = `${identity}:body`;
              runtime.module = node.type === "module";
              try {
                runtime.declarations(node.program, env);
              } finally {
                runtime.module = false;
              }
              return null;
            }
            if (node.type === "expression") {
              const value = runtime.evaluate(node.expression, env);
              checkLocalValue(value);
              return svg ? (
                valueText(value)
              ) : (
                <span key={key}>{valueText(value)}</span>
              );
            }
            if (node.type === "if")
              return (
                <Fragment key={key}>
                  {render(
                    runtime.evaluate(node.expression, env)
                      ? node.children
                      : node.otherwise,
                    env,
                    key,
                    depth + 1,
                    inline,
                    svg,
                  )}
                </Fragment>
              );
            if (node.type === "each") {
              const items = runtime.evaluate(node.expression, env);
              if (!Array.isArray(items) || items.length > 1000)
                throw new Error(
                  "Each requires an array of at most 1000 items.",
                );
              return (
                <Fragment key={key}>
                  {items.map((item, itemIndex) => {
                    const local = Object.create(env);
                    local[node.item] = item;
                    if (node.index) local[node.index] = itemIndex;
                    const keyed = node.children.find(
                      (child) =>
                        child.type === "element" &&
                        child.props.key !== undefined,
                    );
                    const authoredKey =
                      keyed?.type === "element" ? keyed.props.key : undefined;
                    const repeatKey =
                      authoredKey === undefined
                        ? item && typeof item === "object" && "id" in item
                          ? String(item.id)
                          : itemIndex
                        : valueText(
                            typeof authoredKey === "object"
                              ? runtime.evaluate(authoredKey, local)
                              : authoredKey,
                          );
                    return (
                      <Fragment key={repeatKey}>
                        {render(
                          node.children,
                          local,
                          `${key}.${repeatKey}`,
                          depth + 1,
                          inline,
                          svg,
                        )}
                      </Fragment>
                    );
                  })}
                </Fragment>
              );
            }
            if (node.type !== "element") return null;
            const supplied: Record<string, unknown> = Object.create(null);
            for (const [name, value] of Object.entries(node.props)) {
              try {
                supplied[name] =
                  typeof value === "object"
                    ? runtime.evaluate(value, env)
                    : value;
              } catch (error) {
                reportEvaluation(error, node.start, `${node.name}.${name}`);
                if (["disabled", "readOnly", "loading"].includes(name))
                  supplied[name] = true;
                if (
                  ["content", "options", "src", "ref", "refs", "bind"].includes(
                    name,
                  )
                )
                  throw error;
              }
            }
            const p: Record<string, any> =
              !svg && node.name !== "svg" && node.name !== ""
                ? repairProperties(node.name, supplied, diagnostics, node.start)
                : supplied;
            if (!svg && node.name !== "svg") validateProperties(node.name, p);
            const association = labels.get(index);
            if (association && node.name === "label") {
              p.id = `${association}-label`;
              p.htmlFor = `${association}-control`;
            } else if (association) {
              p.id = `${association}-control`;
              p.labelledBy = `${association}-label`;
              p.labelled = true;
            } else if (labelledControls.includes(node.name) && p.id)
              p.labelled = true;
            if (
              node.name === "icon" &&
              !hasIcon(resolveIconName(String(p.name)))
            ) {
              // An unknown name never becomes an unrelated fallback glyph.
              recordDiagnostic(diagnostics, {
                code: "unknown_icon",
                phase: "catalog",
                start: node.start,
                component: "icon",
                property: "name",
                message: `Unknown icon ${String(p.name)}; omitted. Use a Lucide name with a Zenith equivalent or an Iconsax name.`,
                repair: "omitted",
              });
              return null;
            }
            if (node.name === "")
              return (
                <Fragment key={key}>
                  {render(node.children, env, key, depth + 1, inline, svg)}
                </Fragment>
              );
            if (["button", "pressable"].includes(node.name)) {
              const icon = node.children.find(
                (child) => child.type === "element" && child.name === "icon",
              );
              if (
                icon?.type === "element" &&
                !node.children.some(
                  (child) => child.type === "text" && child.value.trim(),
                )
              ) {
                p.accessibilityLabel =
                  p.tooltip ??
                  icon.props.label ??
                  (icon.props.name === "plus"
                    ? "Increase"
                    : icon.props.name === "minus"
                      ? "Decrease"
                      : icon.props.name);
              }
            }
            if (node.name === "WritingBlock")
              p.onCopy = runtime.property(env.GenUI, "copy");
            if (node.name === "Entity")
              p.onEntity = runtime.property(env.GenUI, "openEntityDetail");
            if (node.name === "FollowUp") {
              if (!host?.submit)
                return (
                  <span key={key} role="status">
                    FollowUp: Sending a message is unavailable in this view.
                  </span>
                );
              p.onFollowUp = runtime.property(env.GenUI, "issueNewTurn");
            }
            const text = node.children
              .filter((child) => child.type === "text")
              .map((child) => (child as { value: string }).value)
              .join("");
            if (
              node.name === "WritingBlock" &&
              Array.isArray(p.options) &&
              p.options[0].content.trim() !== text.trim()
            ) {
              delete p.options;
              recordDiagnostic(diagnostics, {
                code: "conflicting_drafts",
                phase: "catalog",
                start: node.start,
                component: node.name,
                property: "options",
                message:
                  "WritingBlock's first option must match its primary raw draft. Kept the primary draft.",
                repair: "omitted",
              });
            }
            if (node.name === "AppBlock" && !text.trim())
              throw new Error(
                "AppBlock requires complete inline application source.",
              );
            if (node.name === "AppBlock")
              return (
                <InlineBoundary
                  key={key}
                  resetKey={renderKey}
                  name="AppBlock"
                  start={node.start}
                  onDiagnostic={reportRender}
                >
                  <InlineAppBlock
                    key={key}
                    source={text}
                    title={p.title}
                    bindings={p.bind}
                    interactive={interactive && !streaming && parsed.complete}
                    run={(callback, args) => run(callback, args, false)}
                  />
                </InlineBoundary>
              );
            if (
              ["Cite", "FileCite", "CodeCite", "FileNavList"].includes(
                node.name,
              ) ||
              (node.name === "Link" && p.ref && !p.url)
            ) {
              const renderer = renderReference ?? referenceHost;
              if (renderer)
                return (
                  <InlineBoundary
                    key={key}
                    resetKey={renderKey}
                    name={node.name}
                    start={node.start}
                    onDiagnostic={reportRender}
                  >
                    {renderer(node.name, p, interactive)}
                  </InlineBoundary>
                );
            }
            return (
              <Primitive
                key={String(p.key ?? key)}
                name={node.name}
                recovery={{
                  resetKey: renderKey,
                  start: node.start,
                  onDiagnostic: reportRender,
                }}
                p={p}
                text={text}
                run={run}
                interactive={interactive}
                svg={svg || node.name === "svg"}
              >
                {(() => {
                  const children = render(
                    node.children,
                    env,
                    key,
                    depth + 1,
                    node.name !== "list",
                    svg || node.name === "svg",
                  );
                  if (
                    node.name === "list" ||
                    svg ||
                    node.name === "svg" ||
                    node.name === "code" ||
                    [
                      "AppBlock",
                      "CodeBlock",
                      "WritingBlock",
                      "escape",
                    ].includes(node.name) ||
                    Object.hasOwn(schema.children, node.name) ||
                    schema.emptyChildren.includes(node.name)
                  )
                    return children;
                  const prose = hasProse(node.children);
                  if (!prose && !schema.inlineParents.includes(node.name))
                    return children;
                  return (
                    <InlineMarkdown
                      inline={schema.inlineParents.includes(node.name)}
                    >
                      {children}
                    </InlineMarkdown>
                  );
                })()}
              </Primitive>
            );
          })();
          previousNodes.current.set(key, rendered);
          if (previousNodes.current.size > 5000)
            previousNodes.current.delete(
              previousNodes.current.keys().next().value!,
            );
          return rendered;
        } catch (error) {
          reportEvaluation(
            error,
            node.start,
            node.type === "element" ? node.name : node.type,
          );
          const previous = previousNodes.current.get(key);
          if (svg) return previous ?? null;
          return (
            <Fragment key={key}>
              {previous ?? (
                <InlineStage
                  name={node.type === "element" ? node.name : "content"}
                  failed={!streaming}
                  streaming={streaming}
                />
              )}
            </Fragment>
          );
        }
      });
    };
    content = parsed.nodes.some((node) => node.type !== "text") ? (
      <InlineMarkdown>
        {render(parsed.nodes, env, "root", 0, true)}
      </InlineMarkdown>
    ) : (
      render(parsed.nodes, env, "root")
    );
  } catch (error) {
    diagnostic =
      error instanceof Error
        ? error.message
        : "Unable to render this response.";
    content = lastDocument.current ?? (
      <InlineMarkdown>{inlineFallback(parsed.nodes)}</InlineMarkdown>
    );
  }
  useEffect(() => {
    if (
      !diagnostic &&
      !diagnostics.some(
        (item) => item.phase === "evaluate" || item.phase === "render",
      )
    )
      lastDocument.current = content;
  });
  const captureReader = useRef<InlineCaptureReader>(() => {
    throw new Error("Wait for this component to render.");
  });
  captureReader.current = () => {
    if (
      diagnostic ||
      diagnostics.length ||
      actionError ||
      streaming ||
      !parsed.complete ||
      parsed.overLimit
    )
      throw new Error("Resolve component errors before capturing this Page.");
    return retainInlineCapture({
      rendererVersion: 1,
      identity,
      source,
      theme: resolvedTheme ?? "light",
      width,
      state: Object.fromEntries(state.current),
      refs: Object.fromEntries(refs.current),
      memo: Object.fromEntries(
        [...memo.current].filter(([key]) => !key.endsWith(":useCallback")),
      ),
    });
  };
  useEffect(
    () => registerCapture?.(() => captureReader.current()),
    [registerCapture],
  );
  // Effects can update local state but cannot invoke user-triggered host actions.
  const effectRuns = useRef(0);
  useEffect(() => {
    if (!interactive || streaming || !parsed.complete || diagnostic) return;
    const activeKeys = new Set(effects.map((effect) => effect.key));
    for (const [key, cleanup] of cleanups.current) {
      if (!activeKeys.has(key)) {
        cleanup();
        cleanups.current.delete(key);
        effectDependencies.current.delete(key);
      }
    }
    for (const effect of effects) {
      const previous = effectDependencies.current.get(effect.key);
      if (
        previous &&
        effect.dependencies &&
        sameDependencies(previous, effect.dependencies)
      )
        continue;
      if (++effectRuns.current > 32) {
        setActionError("UI effects exceeded their update limit.");
        break;
      }
      if (effect.dependencies)
        effectDependencies.current.set(effect.key, effect.dependencies);
      cleanups.current.get(effect.key)?.();
      cleanups.current.delete(effect.key);
      runtime.remaining = 50000;
      try {
        runtime.event = true;
        const cleanup = runtime.call(effect.callback, []);
        if (isCallable(cleanup))
          cleanups.current.set(effect.key, () => {
            runtime.remaining = 50000;
            try {
              runtime.call(cleanup, []);
            } catch {
              /* Cleanup cannot interrupt unmount. */
            }
          });
      } catch (error) {
        setActionError(
          error instanceof Error ? error.message : "Effect failed.",
        );
      } finally {
        runtime.event = false;
      }
    }
  });
  useEffect(
    () => () => {
      for (const cleanup of cleanups.current.values()) cleanup();
      cleanups.current.clear();
      effectDependencies.current.clear();
    },
    [],
  );
  return (
    <div
      ref={root}
      className="zenith-inline-ui min-w-0 space-y-3 text-workspace-text"
      data-capture={capture ? "true" : undefined}
      aria-busy={streaming || undefined}
    >
      {content}
      {diagnostic && (
        <Unavailable
          name="Couldn't display this response"
          reason={diagnostic}
        />
      )}
      <p
        role="status"
        aria-live="polite"
        className="text-xs text-workspace-muted empty:hidden"
      >
        {actionStatus}
      </p>
      {actionError && (
        <p role="alert" className="text-sm text-workspace-danger">
          {actionError}
        </p>
      )}
      {(diagnostic ||
        actionError ||
        diagnostics.length > 0 ||
        !parsed.complete) &&
        !streaming && (
          <details className="text-xs text-workspace-muted">
            <summary className="cursor-pointer">Display details</summary>
            <ul className="my-2 list-disc space-y-1 pl-4">
              {diagnostics.map((item, index) => (
                <li key={index}>
                  {item.component ? `${item.component}: ` : ""}
                  {item.message} ({item.code}, {item.repair})
                </li>
              ))}
              {!parsed.complete && (
                <li>
                  The response ended before all components finished. Complete
                  content is preserved.
                </li>
              )}
            </ul>
            <button
              type="button"
              aria-expanded={showSource}
              onClick={() => setShowSource((value) => !value)}
              className="rounded-workspace-control border border-workspace-divider px-3 py-1.5"
            >
              {showSource ? "Hide source" : "Show source"}
            </button>
            {host?.submit && hostInteractive && !readOnly && !capture && (
              <button
                type="button"
                disabled={requestingRepair}
                className="ml-2 rounded-workspace-control border border-workspace-divider px-3 py-1.5 disabled:opacity-40"
                onClick={async () => {
                  setRequestingRepair(true);
                  try {
                    await host.submit!(
                      repairRequest(
                        source,
                        actionError
                          ? [
                              ...diagnostics,
                              {
                                code: "action_error",
                                phase: "evaluate",
                                start: 0,
                                message: actionError,
                                repair: "retained",
                              },
                            ]
                          : diagnostics,
                      ),
                    );
                  } catch (error) {
                    setActionError(failureMessage(error));
                  } finally {
                    setRequestingRepair(false);
                  }
                }}
              >
                {requestingRepair
                  ? "Requesting repair…"
                  : "Ask Agent to repair"}
              </button>
            )}
            {showSource && (
              <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words p-3">
                <code>{source}</code>
              </pre>
            )}
          </details>
        )}
    </div>
  );
}

export function InlineDocument(
  props: Parameters<typeof InlineDocumentContent>[0],
) {
  return (
    <InlineBoundary
      resetKey={props.source}
      name="response"
      start={0}
      fallback={
        <InlineMarkdown>
          {inlineFallback(parseInlineUI(props.source, { recover: true }).nodes)}
        </InlineMarkdown>
      }
    >
      <InlineDocumentContent {...props} />
    </InlineBoundary>
  );
}
