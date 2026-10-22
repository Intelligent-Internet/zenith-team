import {
  retainInlineCapture,
  type InlineCapture,
  type InlineCaptureReader,
} from "./capture";
import { Streamdown } from "streamdown";
import {
  Component,
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
import { parseInlineUI, type InlineNode } from "./parser";
import {
  Interpreter,
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

function InlineDocumentContent({
  source,
  streaming = false,
  readOnly = false,
  renderMarkdown,
  openEntity,
  maxSourceBytes = 262144,
  capture,
  registerCapture,
}: {
  source: string;
  streaming?: boolean;
  readOnly?: boolean;
  renderMarkdown?: (text: string) => ReactNode;
  openEntity?: (entity: unknown) => Promise<void>;
  maxSourceBytes?: number;
  capture?: InlineCapture;
  registerCapture?: (read: InlineCaptureReader) => () => void;
}) {
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
  const [, setRevision] = useState(0);
  const [actionError, setActionError] = useState("");
  const [showSource, setShowSource] = useState(false);
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
    return { ...parseInlineUI(source), overLimit: false };
  }, [source, maxSourceBytes]);
  const interactive =
    !streaming &&
    !parsed.overLimit &&
    parsed.complete &&
    !parsed.diagnostic &&
    !readOnly &&
    !capture;
  const effects: RuntimeHost["effects"] = [];
  action.current = (name, args) => {
    if (!interactive)
      throw new Error("Wait for this response to finish before interacting.");
    if (name === "copy")
      return navigator.clipboard.writeText(valueText(args[0]));
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
  const run = (callback: unknown, args: unknown[] = []) => {
    if (!interactive || !isCallable(callback)) return;
    setActionError("");
    effectRuns.current = 0;
    runtime.remaining = 50000;
    runtime.event = true;
    runtime.hostActions = true;
    try {
      const result = runtime.call(callback, args);
      if (result instanceof Promise)
        void result.catch((error) =>
          setActionError(
            error instanceof Error ? error.message : "Action failed.",
          ),
        );
    } catch (error) {
      setActionError(error instanceof Error ? error.message : "Action failed.");
    } finally {
      runtime.event = false;
      runtime.hostActions = false;
    }
  };
  let diagnostic =
    parsed.diagnostic ??
    (!streaming && !parsed.complete
      ? "This response ended before its component or expression was complete."
      : undefined);
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
      return nodes.map((node, index) => {
        runtime.tick();
        const key = `${path}.${index}`;
        if (node.type === "text")
          return (
            <Fragment key={key}>
              {inline ? (
                node.value
              ) : renderMarkdown ? (
                renderMarkdown(node.value)
              ) : (
                <Streamdown
                  controls={false}
                  mode={streaming ? "streaming" : "static"}
                >
                  {node.value}
                </Streamdown>
              )}
            </Fragment>
          );
        if (node.type === "body" || node.type === "module") {
          runtime.hookScope = `${identity}:${path}:${node.start}`;
          runtime.program(node.program, env);
          return null;
        }
        if (node.type === "expression") {
          const value = runtime.evaluate(node.expression, env);
          checkLocalValue(value);
          return <Fragment key={key}>{valueText(value)}</Fragment>;
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
            throw new Error("Each requires an array of at most 1000 items.");
          return (
            <Fragment key={key}>
              {items.map((item, itemIndex) => {
                const local = Object.create(env);
                local[node.item] = item;
                if (node.index) local[node.index] = itemIndex;
                return (
                  <Fragment
                    key={
                      item && typeof item === "object" && "id" in item
                        ? String(item.id)
                        : itemIndex
                    }
                  >
                    {render(
                      node.children,
                      local,
                      `${key}.${item && typeof item === "object" && "id" in item ? String(item.id) : itemIndex}`,
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
        const p: Record<string, any> = Object.create(null);
        for (const [name, value] of Object.entries(node.props))
          p[name] =
            typeof value === "object" ? runtime.evaluate(value, env) : value;
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
              p["aria-label"] ??
              p.tooltip ??
              icon.props.label ??
              (icon.props.name === "plus"
                ? "Increase"
                : icon.props.name === "minus"
                  ? "Decrease"
                  : icon.props.name);
          }
        }
        if (node.name === "Entity")
          p.onEntity = runtime.property(env.GenUI, "openEntityDetail");
        if (node.name === "FollowUp")
          p.onFollowUp = runtime.property(env.GenUI, "issueNewTurn");
        const text = node.children
          .filter((child) => child.type === "text")
          .map((child) => (child as { value: string }).value)
          .join("");
        return (
          <Primitive
            key={String(p.key ?? key)}
            name={node.name}
            p={p}
            text={text}
            run={run}
            interactive={interactive}
            svg={svg || node.name === "svg"}
          >
            {render(
              node.children,
              env,
              key,
              depth + 1,
              true,
              svg || node.name === "svg",
            )}
          </Primitive>
        );
      });
    };
    content = render(parsed.nodes, env, "root");
  } catch (error) {
    diagnostic =
      error instanceof Error
        ? error.message
        : "Unable to render this response.";
    content = undefined;
  }
  const captureReader = useRef<InlineCaptureReader>(() => {
    throw new Error("Wait for this component to render.");
  });
  captureReader.current = () => {
    if (diagnostic || actionError || streaming || parsed.overLimit)
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
    if (!interactive || diagnostic) return;
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
      className="min-w-0 space-y-3 text-workspace-text"
      aria-busy={streaming || undefined}
    >
      {content}
      {diagnostic && (
        <Unavailable
          name="Couldn't display this response"
          reason={diagnostic}
        />
      )}
      {actionError && (
        <p role="alert" className="text-sm text-workspace-danger">
          {actionError}
        </p>
      )}
      {diagnostic && (
        <>
          <button
            type="button"
            aria-expanded={showSource}
            onClick={() => setShowSource((v) => !v)}
            className="rounded-workspace-control border border-workspace-divider px-3 py-1.5 text-sm hover:bg-workspace-hover focus-visible:outline-2 focus-visible:outline-workspace-accent"
          >
            {showSource ? "Hide source" : "Show source"}
          </button>
          {showSource && (
            <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-workspace-control border border-workspace-divider bg-workspace-control p-3 text-xs">
              <code>{source}</code>
            </pre>
          )}
        </>
      )}
    </div>
  );
}

class RenderBoundary extends Component<
  { source: string; children: ReactNode },
  { failed: boolean; showSource: boolean }
> {
  state = { failed: false, showSource: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidUpdate(previous: { source: string }) {
    if (previous.source !== this.props.source && this.state.failed)
      this.setState({ failed: false });
  }
  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="space-y-3">
        <Unavailable
          name="Couldn't display this response"
          reason="A component could not render its supplied values."
        />
        <button
          type="button"
          aria-expanded={this.state.showSource}
          className="rounded-workspace-control border border-workspace-divider px-3 py-1.5 text-sm"
          onClick={() => this.setState({ showSource: !this.state.showSource })}
        >
          {this.state.showSource ? "Hide source" : "Show source"}
        </button>
        {this.state.showSource && (
          <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs">
            <code>{this.props.source}</code>
          </pre>
        )}
      </div>
    );
  }
}

export function InlineDocument(
  props: Parameters<typeof InlineDocumentContent>[0],
) {
  return (
    <RenderBoundary source={props.source}>
      <InlineDocumentContent {...props} />
    </RenderBoundary>
  );
}
