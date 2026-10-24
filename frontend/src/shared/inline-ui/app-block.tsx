import { useContext, useEffect, useMemo, useRef, useState } from "react";
import { InlineThemeContext } from "./reference-context";
import { InlineStage } from "./render-state";
import { checkLocalValue, isCallable } from "./runtime";

// Opaque-origin iframe: authored applications never share the product DOM,
// cookies, storage, navigation authority, or host actions. Only JSON values and
// named local callbacks cross this boundary.
export function InlineAppBlock({
  source,
  title,
  bindings = {},
  interactive,
  run,
}: {
  source: string;
  title?: string;
  bindings?: Record<string, unknown>;
  interactive: boolean;
  run: (callback: unknown, args?: unknown[]) => void;
}) {
  useContext(InlineThemeContext); // Re-send inherited colors when workspace palettes change.
  const frame = useRef<HTMLIFrameElement>(null);
  const current = useRef({ bindings, run, interactive });
  current.current = { bindings, run, interactive };
  const lastBindings = useRef("");
  const rate = useRef({ since: 0, calls: 0 });
  const [height, setHeight] = useState(300);
  const [error, setError] = useState("");
  const [loadedChannel, setLoadedChannel] = useState("");
  const channel = useMemo(() => crypto.randomUUID(), [source, interactive]);
  const document = useMemo(
    () => appDocument(source, channel, interactive),
    [source, channel, interactive],
  );
  useEffect(() => setError(""), [channel]);
  const send = (force = false) => {
    const values: Record<string, unknown> = Object.create(null);
    const setters: string[] = [];
    for (const [key, value] of Object.entries(current.current.bindings)) {
      if (["__proto__", "constructor", "prototype"].includes(key)) continue;
      if (isCallable(value)) setters.push(key);
      else {
        checkLocalValue(value);
        values[key] = value;
      }
    }
    const computed = frame.current
      ? getComputedStyle(frame.current)
      : undefined;
    const theme: Record<string, string> = {};
    for (const [target, source] of Object.entries({
      text: "--workspace-native-text",
      muted: "--workspace-native-secondary",
      panel: "--workspace-panel-bg",
      card: "--workspace-control-bg",
      border: "--workspace-divider",
      accent: "--workspace-accent-fill",
      "accent-foreground": "--workspace-accent-foreground",
    })) {
      const value = computed?.getPropertyValue(source).trim();
      if (value) theme[`--viz-${target}`] = value;
    }
    const snapshot = JSON.stringify({ values, setters, theme });
    if (!force && snapshot === lastBindings.current) return;
    lastBindings.current = snapshot;
    frame.current?.contentWindow?.postMessage(
      { channel, type: "bindings", values, setters, theme },
      "*",
    );
  };
  useEffect(() => {
    if (!interactive) return;
    const receive = (event: MessageEvent) => {
      if (
        !current.current.interactive ||
        event.source !== frame.current?.contentWindow ||
        event.data?.channel !== channel
      )
        return;
      const message = event.data;
      try {
        if (message.type === "ready") {
          setLoadedChannel(channel);
          send(true);
        }
        if (message.type === "height" && Number.isFinite(message.value))
          setHeight(Math.max(120, Math.min(1600, message.value)));
        if (message.type === "error")
          setError(String(message.value).slice(0, 500));
        if (
          message.type === "set" &&
          typeof message.key === "string" &&
          Object.hasOwn(current.current.bindings, message.key)
        ) {
          const now = performance.now();
          if (now - rate.current.since > 1000)
            rate.current = { since: now, calls: 0 };
          if (++rate.current.calls > 60)
            throw new Error("Application binding update limit exceeded.");
          checkLocalValue(message.value);
          current.current.run(current.current.bindings[message.key], [
            message.value,
          ]);
        }
      } catch (cause) {
        setError(
          cause instanceof Error
            ? cause.message
            : "Application binding failed.",
        );
      }
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [channel, interactive]);
  useEffect(() => {
    if (interactive) send();
  });
  return (
    <div className="min-w-0 space-y-2" aria-busy={loadedChannel !== channel}>
      {loadedChannel !== channel && (
        <InlineStage name={title || "application"} streaming />
      )}
      <iframe
        key={channel}
        ref={frame}
        title={title || "Interactive visualization"}
        sandbox={interactive ? "allow-scripts" : ""}
        referrerPolicy="no-referrer"
        inert={!interactive}
        tabIndex={interactive ? 0 : -1}
        srcDoc={document}
        onLoad={() => {
          setLoadedChannel(channel);
          if (interactive) send(true);
        }}
        className="w-full rounded-workspace-control border-0"
        style={{ height }}
      />
      {!interactive && (
        <p className="text-xs text-workspace-muted">
          Static application preview. Scripts are disabled in this view.
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-workspace-danger">
          {error}
        </p>
      )}
    </div>
  );
}

export function appDocument(
  source: string,
  channel: string,
  interactive: boolean,
): string {
  const policy = `default-src 'none'; script-src ${interactive ? "'unsafe-inline'" : "'none'"}; style-src 'unsafe-inline'; img-src data:; font-src 'none'; connect-src 'none'; media-src 'none'; object-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'`;
  const bridge = `(() => {
    const channel = ${JSON.stringify(channel)};
    const post = (type, extra = {}) => parent.postMessage({channel, type, ...extra}, '*');
    const effects = new Set();
    let interaction, effectRunning = false;
    const runEffect = fn => { effectRunning = true; try { fn(); } finally { effectRunning = false; } };
    for (const kind of ['click','input','change','keydown','pointerdown']) addEventListener(kind, event => {
      if (event.isTrusted) interaction = event;
    }, true);
    globalThis.__bindings = Object.create(null);
    globalThis.__effect = (fn) => { effects.add(fn); runEffect(fn); return () => effects.delete(fn); };
    addEventListener('message', event => {
      const data = event.data;
      if (event.source !== parent || data?.channel !== channel || data.type !== 'bindings') return;
      for (const [key,value] of Object.entries(data.theme ?? {})) {
        if (/^--viz-[a-z-]+$/.test(key) && typeof value === 'string' && value.length < 300) document.documentElement.style.setProperty(key,value);
      }
      const bindings = Object.assign(Object.create(null), data.values);
      for (const key of data.setters) bindings[key] = value => {
        if (effectRunning || !interaction?.isTrusted || interaction.eventPhase === 0) throw new Error('Native state setters require a direct interaction.');
        post('set', {key, value});
      };
      globalThis.__bindings = bindings;
      for (const effect of effects) { try { runEffect(effect); } catch (error) { post('error', {value:String(error)}); } }
    });
    addEventListener('error', event => post('error', {value:event.message}));
    addEventListener('unhandledrejection', event => post('error', {value:String(event.reason)}));
    addEventListener('DOMContentLoaded', () => {
      new ResizeObserver(() => post('height', {value:Math.max(document.body.scrollHeight,document.body.offsetHeight)})).observe(document.body);
      post('ready');
    });
  })();`;
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${policy}"><meta name="viewport" content="width=device-width, initial-scale=1"><style>:root{color-scheme:light dark;--viz-text:CanvasText;--viz-muted:GrayText;--viz-panel:Canvas;--viz-card:Canvas;--viz-border:GrayText;--viz-accent:Highlight;--viz-accent-foreground:HighlightText}html,body{margin:0;max-width:100%;overflow-wrap:anywhere;font-family:system-ui,sans-serif;line-height:1.5;color:var(--viz-text);background:var(--viz-panel)}button,input,select,textarea{font:inherit;color:inherit}button{cursor:pointer}button:disabled{cursor:default}button:focus-visible,input:focus-visible{outline:2px solid var(--viz-accent);outline-offset:2px}img,svg,video{max-width:100%}${!interactive ? "*{animation:none!important;transition:none!important}" : ""}*,*::before,*::after{box-sizing:border-box}@media(prefers-reduced-motion:reduce){*,*::before,*::after{animation-duration:0.01ms!important;animation-iteration-count:1!important;transition-duration:0.01ms!important}}</style>${interactive ? "<script>" + bridge + "</script>" : ""}</head><body>${source}</body></html>`;
}
