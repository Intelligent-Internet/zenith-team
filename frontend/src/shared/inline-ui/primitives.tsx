import { InlineCodeBlock } from "./code-block";
import {
  Children,
  isValidElement,
  createContext,
  createElement,
  useContext,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import katex from "katex";
import "katex/dist/katex.min.css";
import { Icon } from "@/shared/ui/icon";
import { ChevronLeft, ChevronRight } from "@/shared/ui/icons";
import { InlineChart } from "./chart";
import { layoutStyle, safeURL, uiColor } from "./style";
import { valueText } from "./runtime";

type Props = Record<string, any>;
export const InputGroup = createContext<{
  value: unknown;
  change: (value: unknown) => void;
  name: string;
  disabled: boolean;
} | null>(null);
const Popover = createContext<{ id: string } | null>(null);
const control =
  "min-w-0 w-full rounded-workspace-control border border-workspace-divider bg-workspace-control px-3 py-2.5 text-sm text-workspace-text placeholder:text-workspace-faint focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-workspace-accent disabled:cursor-not-allowed disabled:opacity-50";
const focus =
  "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-workspace-accent";
export function Unavailable({
  name,
  reason,
}: {
  name: string;
  reason?: string;
}) {
  return (
    <div
      role="status"
      className="rounded-workspace-control border border-workspace-divider bg-workspace-control p-3 text-sm text-workspace-muted"
    >
      {name}: {reason ?? "This capability is not connected in Zenith Team."}
    </div>
  );
}
export function Primitive({
  name,
  p,
  children,
  text,
  run,
  interactive,
  svg = false,
}: {
  name: string;
  p: Props;
  children: ReactNode;
  text: string;
  run: (callback: unknown, args?: unknown[]) => void;
  interactive: boolean;
  svg?: boolean;
}) {
  const id = useId();
  const group = useContext(InputGroup);
  const popover = useContext(Popover);
  const viewport = useRef<HTMLDivElement>(null);
  const [local, setLocal] = useState(p.defaultValue ?? p.defaultChecked ?? "");
  const [imageFailed, setImageFailed] = useState(false);
  const value = p.value ?? p.checked ?? local;
  const disabled = !interactive || !!p.disabled;
  const change = (next: unknown) => {
    setLocal(next);
    if (p.onChange) run(p.onChange, [next]);
  };
  const style = layoutStyle(p);
  const label = p.label ?? p.placeholder ?? p.name ?? name;
  const plain = p.label ?? children;
  const props = { style, className: "min-w-0" };
  const click = () => {
    if (p.onClick) run(p.onClick);
  };
  if (svg) {
    const allowed = new Set([
      "svg",
      "g",
      "path",
      "circle",
      "ellipse",
      "rect",
      "line",
      "polyline",
      "polygon",
      "defs",
      "linearGradient",
      "radialGradient",
      "stop",
      "clipPath",
      "mask",
      "text",
      "tspan",
      "title",
    ]);
    if (!allowed.has(name))
      return <Unavailable name={name} reason="Not supported inside SVG." />;
    const attributes: Props = {};
    for (const key of [
      "viewBox",
      "width",
      "height",
      "d",
      "points",
      "x",
      "y",
      "x1",
      "x2",
      "y1",
      "y2",
      "cx",
      "cy",
      "r",
      "rx",
      "ry",
      "fill",
      "stroke",
      "strokeWidth",
      "strokeLinecap",
      "strokeLinejoin",
      "opacity",
      "fillOpacity",
      "strokeOpacity",
      "transform",
      "textAnchor",
      "dominantBaseline",
      "fontSize",
      "fontWeight",
      "offset",
      "stopColor",
      "stopOpacity",
    ]) {
      const value = p[key];
      if (
        (typeof value === "string" || typeof value === "number") &&
        !String(value).toLowerCase().includes("url(")
      )
        attributes[key] = value;
    }
    if (name === "svg") {
      attributes.role = "img";
      attributes["aria-label"] = p.label ?? "Illustration";
      attributes.className = "max-w-full";
    }
    return createElement(name, attributes, children);
  }
  if (
    [
      "box",
      "row",
      "col",
      "card",
      "carousel-item",
      "carousel-media-item",
    ].includes(name)
  )
    return (
      <div
        {...props}
        className={`min-w-0 ${name === "row" ? "flex flex-row flex-wrap" : "flex flex-col"} ${name === "card" ? "rounded-workspace-panel border border-workspace-divider bg-workspace-panel p-4 gap-3" : ""}`}
      >
        {children}
      </div>
    );
  if (["grid", "flow"].includes(name)) {
    const minWidth = Math.max(
      120,
      Math.floor(640 / Math.max(1, Math.min(12, Number(p.columns) || 2))),
    );
    return (
      <div
        style={{
          ...style,
          gridTemplateColumns: `repeat(auto-fit, minmax(min(100%, ${minWidth}px), 1fr))`,
        }}
        className="grid min-w-0 gap-3"
      >
        {children}
      </div>
    );
  }
  if (["grid-item", "flow-item"].includes(name))
    return (
      <div
        style={{
          ...style,
          gridColumn: `span ${Math.max(1, Math.min(12, Number(p.span) || 1))}`,
          gridRow: `span ${Math.max(1, Math.min(12, Number(p.rowSpan) || 1))}`,
        }}
        className="min-w-0"
      >
        {children}
      </div>
    );
  if (name === "spacer")
    return <span style={style} className="min-h-2 min-w-2 flex-1" />;
  if (name === "divider")
    return (
      <hr
        style={{ ...style, marginBlock: Number(p.spacing ?? 2) * 4 }}
        className="w-full border-workspace-divider"
      />
    );
  if (
    [
      "text",
      "title",
      "caption",
      "label",
      "bold",
      "italic",
      "code",
      "strikethrough",
      "underline",
      "blockquote",
    ].includes(name)
  ) {
    const tag = (
      {
        title: "h3",
        caption: "p",
        label: "label",
        bold: "strong",
        italic: "em",
        strikethrough: "s",
        underline: "u",
        blockquote: "blockquote",
        code: "code",
        text: p.inline ? "span" : "p",
      } as Record<string, string>
    )[name];
    return createElement(
      tag,
      {
        style,
        htmlFor: p.htmlFor,
        className: `min-w-0 break-words ${name === "title" ? "text-xl font-semibold tracking-tight text-balance" : name === "caption" ? "text-xs text-workspace-muted" : name === "label" ? "text-sm font-medium" : name === "blockquote" ? "border-l-2 border-workspace-divider pl-4 text-workspace-muted" : "text-sm leading-relaxed"} ${p.truncate ? "truncate" : ""}`,
      },
      children,
    );
  }
  if (name === "badge")
    return (
      <span
        style={style}
        onMouseEnter={() => p.onHover && run(p.onHover, [true])}
        onMouseLeave={() => p.onHover && run(p.onHover, [false])}
        className="inline-flex w-fit max-w-full rounded-full border border-workspace-divider bg-workspace-control px-2.5 py-0.5 text-xs font-medium"
      >
        {plain}
      </span>
    );
  if (name === "icon")
    return (
      <Icon
        name={String(
          ({ plus: "add", "sliders-horizontal": "setting-4" } as Props)[
            p.name
          ] ?? p.name,
        )}
        aria-label={p.label}
        aria-hidden={!p.label}
        size={Number(
          ({ xs: 12, sm: 16, md: 20, lg: 24, xl: 32, "2xl": 40 } as Props)[
            p.size
          ] ?? 20,
        )}
        style={{ color: uiColor(p.color) }}
      />
    );
  if (name === "math") {
    try {
      return (
        <span
          style={style}
          className="max-w-full overflow-auto"
          dangerouslySetInnerHTML={{
            __html: katex.renderToString(String(p.value ?? text), {
              throwOnError: false,
              trust: false,
              strict: "error",
              maxExpand: 100,
              maxSize: 10,
              displayMode: !!p.block,
            }),
          }}
        />
      );
    } catch {
      return (
        <Unavailable name="Math" reason="Invalid mathematical expression." />
      );
    }
  }
  if (
    name === "popover-trigger" &&
    Children.toArray(children).some(
      (child) =>
        isValidElement<{ name?: string }>(child) &&
        ["button", "pressable"].includes(child.props.name ?? ""),
    )
  ) {
    return (
      <span
        className="inline-flex"
        onClick={() => {
          if (!disabled && popover)
            document.getElementById(popover.id)?.togglePopover();
        }}
      >
        {children}
      </span>
    );
  }
  if (name === "button" || name === "pressable" || name === "popover-trigger")
    return (
      <button
        type={p.submit ? "submit" : "button"}
        disabled={disabled || !!p.loading}
        title={p.tooltip}
        aria-label={p.accessibilityLabel ?? p.tooltip}
        popoverTarget={name === "popover-trigger" ? popover?.id : undefined}
        onClick={click}
        style={style}
        className={`${focus} ${p.block ? "w-full" : ""} ${name === "button" ? `inline-flex min-h-9 items-center justify-center gap-2 rounded-workspace-control px-3 py-2 text-sm font-medium ${p.variant === "outline" || p.variant === "ghost" ? "border border-workspace-divider bg-workspace-control text-workspace-text hover:bg-workspace-hover" : "bg-workspace-accent-fill text-workspace-accent-foreground hover:opacity-90"}` : "inline-flex min-w-0 flex-col text-left"} disabled:cursor-not-allowed disabled:opacity-40`}
      >
        {p.loading ? "Working…" : plain}
      </button>
    );
  if (name === "form")
    return (
      <form
        style={style}
        className="flex min-w-0 flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          if (!disabled && p.onSubmit) run(p.onSubmit);
        }}
      >
        {children}
      </form>
    );
  if (name === "input" || name === "textarea" || name === "date-picker") {
    const common = {
      id: p.id ?? id,
      "aria-label": String(label),
      disabled,
      required: !!p.required,
      placeholder: p.placeholder,
      value: valueText(value),
      onChange: (
        event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>,
      ) =>
        change(
          p.inputType === "number"
            ? (event.target as HTMLInputElement).valueAsNumber
            : event.target.value,
        ),
      style,
      className: control,
      "aria-invalid": p.invalid || undefined,
    };
    if (name === "textarea")
      return (
        <textarea
          {...common}
          rows={Math.max(1, Math.min(30, Number(p.rows) || 3))}
        />
      );
    return (
      <input
        {...common}
        type={
          name === "date-picker"
            ? "date"
            : [
                  "text",
                  "number",
                  "email",
                  "password",
                  "search",
                  "url",
                  "tel",
                ].includes(p.inputType)
              ? p.inputType
              : "text"
        }
        min={p.min}
        max={p.max}
      />
    );
  }
  if (name === "checkbox")
    return (
      <label style={style} className="inline-flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          className={`size-4 accent-workspace-accent ${focus}`}
          checked={!!value}
          disabled={disabled}
          onChange={(event) => change(event.target.checked)}
        />
        <span className={p.lineThrough && value ? "line-through" : ""}>
          {plain}
        </span>
      </label>
    );
  if (name === "slider")
    return (
      <div style={style} className="min-w-0">
        <input
          aria-label={String(label)}
          className={`min-h-8 w-full accent-workspace-accent ${focus}`}
          type="range"
          disabled={disabled}
          min={p.min ?? 0}
          max={p.max ?? 100}
          step={p.step ?? 1}
          value={Number(value) || 0}
          onChange={(event) => change(event.target.valueAsNumber)}
        />
        {Array.isArray(p.marks) && (
          <div className="flex justify-between gap-2 text-xs text-workspace-muted">
            {p.marks.slice(0, 20).map((mark: Props, index: number) => (
              <span key={index}>{valueText(mark.label ?? mark.value)}</span>
            ))}
          </div>
        )}
      </div>
    );
  if (name === "radio-group")
    return (
      <InputGroup.Provider value={{ value, change, name: id, disabled }}>
        <fieldset
          aria-label={String(label)}
          style={style}
          className={`flex gap-3 ${p.direction === "horizontal" ? "flex-wrap" : "flex-col"}`}
        >
          {children}
        </fieldset>
      </InputGroup.Provider>
    );
  if (name === "radio")
    return (
      <label style={style} className="inline-flex items-center gap-2 text-sm">
        <input
          type="radio"
          className={`size-4 accent-workspace-accent ${focus}`}
          name={group?.name ?? id}
          checked={group?.value === p.value}
          disabled={disabled || group?.disabled}
          onChange={() => group?.change(p.value)}
        />
        {plain}
      </label>
    );
  if (name === "select" || name === "segmented-control") {
    const options = (Array.isArray(p.options) ? p.options : [])
      .slice(0, 1000)
      .map((option: any) =>
        typeof option === "object" ? option : { label: option, value: option },
      );
    if (name === "select")
      return (
        <select
          style={style}
          className={control}
          aria-label={String(label)}
          disabled={disabled}
          value={options.findIndex((o: Props) => o.value === value)}
          onChange={(event) =>
            change(options[Number(event.target.value)]?.value)
          }
        >
          {options.map((option: Props, index: number) => (
            <option key={index} value={index}>
              {valueText(option.label)}
            </option>
          ))}
        </select>
      );
    return (
      <div
        role="radiogroup"
        aria-label={String(label)}
        style={style}
        className="flex flex-wrap gap-1 rounded-workspace-control border border-workspace-divider bg-workspace-control p-1"
      >
        {options.map((option: Props, index: number) => (
          <label
            key={index}
            className={`flex flex-1 cursor-pointer items-center justify-center rounded-workspace-control px-3 py-2 text-sm font-medium has-focus-visible:outline-2 has-focus-visible:outline-workspace-accent has-disabled:opacity-40 ${option.value === value ? "bg-workspace-accent-fill text-workspace-accent-foreground" : "hover:bg-workspace-hover"}`}
          >
            <input
              type="radio"
              className="sr-only"
              name={id}
              checked={option.value === value}
              disabled={disabled || option.disabled}
              onChange={() => change(option.value)}
            />
            {valueText(option.label)}
          </label>
        ))}
      </div>
    );
  }
  if (name === "popover")
    return (
      <Popover.Provider value={{ id }}>
        <div style={style}>{children}</div>
      </Popover.Provider>
    );
  if (name === "popover-content")
    return (
      <div
        id={popover?.id}
        popover="auto"
        style={style}
        className="m-auto max-h-[80vh] max-w-[min(90vw,32rem)] overflow-auto rounded-workspace-overlay border border-workspace-divider bg-workspace-panel p-4 text-workspace-text"
      >
        {children}
        {p.showCloseButton && (
          <button
            type="button"
            popoverTarget={popover?.id}
            popoverTargetAction="hide"
            className={`mt-3 text-sm underline ${focus}`}
          >
            Close
          </button>
        )}
      </div>
    );
  if (name === "carousel")
    return (
      <section style={style} aria-roledescription="carousel">
        <div
          ref={viewport}
          className="flex snap-x snap-mandatory gap-3 overflow-x-auto [&>*]:min-w-0 [&>*]:shrink-0 [&>*]:basis-full [&>*]:snap-start"
        >
          {children}
        </div>
        {p.showArrows !== false && (
          <nav aria-label="Slides" className="mt-2 flex justify-end gap-2">
            {[-1, 1].map((direction) => (
              <button
                key={direction}
                type="button"
                aria-label={direction < 0 ? "Previous slide" : "Next slide"}
                className={`rounded-full border border-workspace-divider p-2 hover:bg-workspace-hover ${focus}`}
                onClick={() => {
                  const el = viewport.current;
                  if (el) {
                    el.scrollBy({ left: direction * el.clientWidth });
                    if (p.onChange)
                      run(p.onChange, [
                        Math.max(
                          0,
                          Math.round(el.scrollLeft / el.clientWidth) +
                            direction,
                        ),
                      ]);
                  }
                }}
              >
                {direction < 0 ? (
                  <ChevronLeft size={16} />
                ) : (
                  <ChevronRight size={16} />
                )}
              </button>
            ))}
          </nav>
        )}
      </section>
    );
  if (name === "list")
    return createElement(
      p.marker === "number" ? "ol" : "ul",
      {
        style,
        className: `space-y-2 pl-5 text-sm ${p.marker === "number" ? "list-decimal" : "list-disc"}`,
        start: p.start,
      },
      children,
    );
  if (name === "list-item")
    return (
      <li style={style}>
        {plain}
        {p.description && (
          <p className="text-xs text-workspace-muted">
            {valueText(p.description)}
          </p>
        )}
      </li>
    );
  if (name === "table")
    return (
      <div className="min-w-0 overflow-x-auto rounded-workspace-control border border-workspace-divider">
        <table style={style} className="w-full text-left text-sm">
          <tbody>{children}</tbody>
        </table>
      </div>
    );
  if (name === "table-section")
    return (
      <>
        {p.header && (
          <tr>
            <th className="bg-workspace-control p-3" colSpan={100}>
              {valueText(p.header)}
            </th>
          </tr>
        )}
        {children}
      </>
    );
  if (name === "table-row")
    return (
      <tr
        style={style}
        className="border-b border-workspace-divider last:border-0"
      >
        {p.label && (
          <th className="px-3 py-2" colSpan={p.labelColSpan ?? 1}>
            {valueText(p.label)}
          </th>
        )}
        {children}
      </tr>
    );
  if (name === "table-cell")
    return createElement(
      p.header ? "th" : "td",
      {
        style: { ...style, textAlign: p.align ?? style.textAlign },
        className: `px-3 py-2 ${p.header ? "bg-workspace-control font-medium" : ""}`,
        colSpan: p.colSpan,
        rowSpan: p.rowSpan,
      },
      children,
    );
  if (name === "Chart") return <InlineChart content={p.content} />;
  if (name === "favicon" || name === "AsyncImage") {
    const url = safeURL(p.src ?? p.url, true);
    if (!url || imageFailed)
      return (
        <Unavailable
          name={name}
          reason={
            url
              ? "Image could not be loaded."
              : "A resolved HTTPS image source is required."
          }
        />
      );
    return (
      <img
        src={url}
        alt={p.alt ?? ""}
        loading="lazy"
        referrerPolicy="no-referrer"
        style={style}
        className={
          name === "favicon"
            ? "size-5 object-contain"
            : "max-h-96 max-w-full rounded-workspace-control object-contain"
        }
        onError={() => setImageFailed(true)}
      />
    );
  }
  if (name === "Link") {
    const href = safeURL(p.url);
    return href ? (
      <a
        style={style}
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        className={`text-workspace-accent underline underline-offset-2 ${focus}`}
      >
        {children ?? p.title ?? href}
      </a>
    ) : (
      <Unavailable name="Link" reason="A resolved URL is required." />
    );
  }
  if (name === "CodeBlock")
    return <InlineCodeBlock source={text} language={p.language} />;
  if (name === "WritingBlock")
    return (
      <textarea
        aria-label={p.subject ?? "Draft"}
        className={`${control} min-h-40`}
        defaultValue={text}
        readOnly={!interactive}
      />
    );
  if (name === "escape") return <span>{text}</span>;
  if (name === "Entity" && ["task", "page"].includes(p.category))
    return (
      <button
        type="button"
        disabled={disabled}
        className={`rounded-workspace-control border border-workspace-divider px-3 py-2 text-left text-sm text-workspace-accent hover:bg-workspace-hover ${focus}`}
        onClick={() =>
          run(p.onEntity, [{ category: p.category, value: p.value }])
        }
      >
        {p.label ?? `Open ${p.category}`}
      </button>
    );
  if (name === "FollowUp")
    return (
      <button
        type="button"
        disabled={disabled}
        className={`rounded-workspace-control border border-workspace-divider px-3 py-2 text-left text-sm hover:bg-workspace-hover ${focus}`}
        onClick={() => run(p.onFollowUp, [p.query ?? p.value])}
      >
        {p.value ?? p.query}
      </button>
    );
  return <Unavailable name={name} />;
}
