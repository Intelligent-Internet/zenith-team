import type { CSSProperties } from "react";
import { InlineBoundary, InlineStage } from "./render-state";
import type { InlineDiagnostic } from "./recovery";
import { InlineDatePicker, InlineSelect, maskInput } from "./controls";
import { InlineWritingBlock } from "./writing-block";
import { InlineCodeBlock } from "./code-block";
import {
  InlinePopover,
  InlinePopoverTrigger,
  InlinePopoverContent,
} from "./popover";
import {
  Children,
  Fragment,
  cloneElement,
  isValidElement,
  createContext,
  createElement,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import katex from "katex";
import "katex/dist/katex.min.css";
import {
  ChevronLeft,
  ChevronRight,
  iconsaxIcons,
  type IconsaxIconName,
} from "@/shared/ui/icons";
import { InlineChart } from "./chart";
import { layoutStyle, safeURL, uiColor } from "./style";
import { InlineThemeContext } from "./reference-context";
import { valueText } from "./runtime";

type Props = Record<string, any>;
export const InputGroup = createContext<{
  value: unknown;
  change: (value: unknown) => void;
  name: string;
  disabled: boolean;
  required: boolean;
} | null>(null);
const TableContext = createContext<Props>({});
const ListContext = createContext<Props>({});
const FlowContext = createContext(false);
const AspectContext = createContext<string | undefined>(undefined);
const SVGScope = createContext("");
const control =
  "min-w-0 w-full rounded-workspace-control border border-workspace-divider bg-workspace-control px-3 py-2.5 text-sm text-workspace-text placeholder:text-workspace-faint focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-workspace-accent disabled:cursor-not-allowed disabled:opacity-50";
const focus =
  "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-workspace-accent";
// Branches and loops are transparent to component child contracts.
function contentChildren(children: ReactNode, path = ""): ReactNode[] {
  return Children.toArray(children).flatMap((child, index) => {
    const key = `${path}.${isValidElement(child) ? (child.key ?? index) : index}`;
    if (
      isValidElement<{ children?: ReactNode }>(child) &&
      child.type === Fragment
    )
      return contentChildren(child.props.children, key);
    if (typeof child === "string" && !child.trim()) return [];
    return [isValidElement(child) ? cloneElement(child, { key }) : child];
  });
}
function hasTableRows(children: ReactNode): boolean {
  return contentChildren(children).some((child) => {
    if (
      !isValidElement<{ name?: string; children?: ReactNode; p?: Props }>(child)
    )
      return false;
    return (
      child.props.name === "table-row" ||
      (child.props.name === "table-section" &&
        (!!child.props.p?.header || hasTableRows(child.props.children)))
    );
  });
}
// Semantic color x variant for buttons and badges. Colors are workspace tokens,
// so light/dark themes and contrast follow the host palette.
function toneStyle(color: unknown, variant: unknown): CSSProperties {
  const tone = (
    {
      primary: "var(--workspace-native-accent)",
      accent: "var(--workspace-native-accent)",
      default: "var(--workspace-native-accent)",
      info: "var(--workspace-native-info)",
      success: "var(--workspace-native-success)",
      danger: "var(--workspace-native-danger)",
      warning: "var(--workspace-native-approval)",
    } as Record<string, string>
  )[String(color)];
  const neutral = !tone;
  const ink = tone ?? "var(--workspace-native-text)";
  switch (variant) {
    case "solid":
      return neutral
        ? {
            background: "var(--workspace-control-bg)",
            color: ink,
            borderColor: "var(--workspace-divider)",
          }
        : {
            background:
              color === "primary" || color === "accent" || color === "default"
                ? "var(--workspace-accent-fill)"
                : ink,
            color:
              color === "primary" || color === "accent" || color === "default"
                ? "var(--workspace-accent-foreground)"
                : "var(--workspace-panel-bg)",
            borderColor: "transparent",
          };
    case "outline":
      return {
        background: "transparent",
        color: ink,
        borderColor: neutral
          ? "var(--workspace-divider)"
          : `color-mix(in srgb, ${ink} 45%, transparent)`,
      };
    case "ghost":
      return {
        background: "transparent",
        color: ink,
        borderColor: "transparent",
      };
    default:
      return {
        background: neutral
          ? "var(--workspace-control-bg)"
          : `color-mix(in srgb, ${ink} 14%, transparent)`,
        color: ink,
        borderColor: "transparent",
      };
  }
}
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
export function Primitive(
  props: Parameters<typeof PrimitiveContent>[0] & {
    recovery?: {
      resetKey: unknown;
      start: number;
      onDiagnostic: (diagnostic: InlineDiagnostic) => void;
    };
  },
) {
  if (!props.recovery || props.svg) return <PrimitiveContent {...props} />;
  const issue = <InlineStage name={props.name} failed />;
  const fallback =
    props.name === "table-cell" ? (
      <td>{issue}</td>
    ) : props.name === "table-row" ? (
      <tr>
        <td colSpan={99}>{issue}</td>
      </tr>
    ) : props.name === "table-section" ? (
      <tbody>
        <tr>
          <td colSpan={99}>{issue}</td>
        </tr>
      </tbody>
    ) : undefined;
  return (
    <InlineBoundary {...props.recovery} name={props.name} fallback={fallback}>
      <PrimitiveContent {...props} />
    </InlineBoundary>
  );
}
function PrimitiveContent({
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
  run: (
    callback: unknown,
    args?: unknown[],
    allowHostActions?: boolean,
  ) => void;
  interactive: boolean;
  svg?: boolean;
}) {
  const id = useId();
  const group = useContext(InputGroup);
  const parentSVGScope = useContext(SVGScope);
  const table = useContext(TableContext);
  const list = useContext(ListContext);
  const masonry = useContext(FlowContext);
  const aspect = useContext(AspectContext);
  const palettes = useContext(InlineThemeContext);
  const viewport = useRef<HTMLDivElement>(null);
  const [local, setLocal] = useState(p.defaultValue ?? p.defaultChecked ?? "");
  const [imageFailed, setImageFailed] = useState(false);
  useEffect(() => {
    setImageFailed(false);
  }, [p.src, p.url]);
  useEffect(() => {
    if (name !== "carousel" || !viewport.current || p.activeIndex === undefined)
      return;
    const element = viewport.current;
    const index = Math.max(
      0,
      Math.min(element.children.length - 1, Number(p.activeIndex) || 0),
    );
    const child = element.children[index] as HTMLElement | undefined;
    element.scrollTo({
      left: child
        ? child.offsetLeft - (element.children[0] as HTMLElement).offsetLeft
        : 0,
    });
  }, [name, p.activeIndex]);
  const value = name === "checkbox" ? (p.checked ?? local) : (p.value ?? local);
  const disabled = !interactive || !!p.disabled;
  const change = (next: unknown) => {
    setLocal(next);
    if (p.onChange) run(p.onChange, [next]);
  };
  const style = {
    ...(p.theme === "light" || p.theme === "dark"
      ? palettes[p.theme as "light" | "dark"]
      : {}),
    ...layoutStyle(p),
  };
  // Layout containers space their children unless the author opts out.
  if (
    style.gap === undefined &&
    !p.disableAutoSpacing &&
    [
      "box",
      "row",
      "col",
      "form",
      "carousel-item",
      "carousel-media-item",
      "flow-item",
      "grid-item",
      "pressable",
    ].includes(name)
  )
    style.gap = "8pt";
  if (p.disableAutoSpacing && p.gap === undefined) style.gap = 0;
  if (p.uniform) {
    style.aspectRatio = "1";
    style.flexShrink = 0;
  }
  const surface = ![
    "button",
    "badge",
    "input",
    "textarea",
    "select",
    "date-picker",
    "segmented-control",
  ].includes(name);
  if (surface && p.variant === "ghost") {
    style.background = "transparent";
    style.border = "none";
  }
  if (surface && p.variant === "outline") {
    style.background = "transparent";
    style.border = "1px solid var(--workspace-divider)";
  }
  if (surface && ["soft", "subtle"].includes(p.variant))
    style.background = "var(--workspace-control-bg)";
  if (surface && p.variant === "solid") {
    style.background = "var(--workspace-accent-fill)";
    style.color = "var(--workspace-accent-foreground)";
  }
  if (p.fontSize !== undefined)
    style.fontSize = layoutStyle({ size: p.fontSize }).fontSize ?? p.fontSize;
  const inputType = p.inputType;
  // A visible associated <label> names the control; otherwise fall back to the
  // control's own label, placeholder or name.
  const label = p.labelled
    ? undefined
    : (p.label ?? p.placeholder ?? p.name ?? name);
  const plain = p.label ?? children;
  const props = { style, className: "min-w-0", role: p.role, id: p.id };
  const click = () => {
    if (p.onClick) run(p.onClick);
  };
  if (svg) {
    const scope =
      name === "svg"
        ? `ui-svg-${id.replace(/[^a-zA-Z0-9_-]/g, "")}`
        : parentSVGScope;
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
      "preserveAspectRatio",
      "color",
      "xmlns",
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
      "strokeDasharray",
      "strokeDashoffset",
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
      "clipPath",
      "mask",
      "gradientUnits",
      "gradientTransform",
      "spreadMethod",
      "fx",
      "fy",
      "fr",
      "clipPathUnits",
      "maskUnits",
      "maskContentUnits",
      "fillRule",
      "clipRule",
      "pathLength",
    ]) {
      const value =
        p[key] ??
        p[key.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)];
      if (typeof value !== "string" && typeof value !== "number") continue;
      if (String(value).toLowerCase().includes("url(")) {
        const local = /^url\(#([a-zA-Z_][\w:.-]*)\)$/.exec(String(value));
        if (local && ["fill", "stroke", "clipPath", "mask"].includes(key))
          attributes[key] = `url(#${scope}-${local[1]})`;
      } else attributes[key] = value;
    }
    if (typeof p.id === "string" && /^[a-zA-Z_][\w:.-]*$/.test(p.id))
      attributes.id = `${scope}-${p.id}`;
    if (name === "svg") {
      attributes.width = p.width ?? p.size;
      attributes.height = p.height ?? p.size;
      attributes.role = "img";
      attributes["aria-label"] = p["aria-label"] ?? "Illustration";
      attributes.className = "max-w-full";
    }
    const element = createElement(name, attributes, children);
    return name === "svg" ? (
      <SVGScope.Provider value={scope}>{element}</SVGScope.Provider>
    ) : (
      element
    );
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
        className={`min-w-0 ${name === "row" || (name !== "card" && p.direction && ["horizontal", "row"].includes(p.direction)) ? "flex flex-row" : "flex flex-col"} ${name === "row" && p.wrap === undefined ? "flex-wrap" : ""} ${name === "card" ? `rounded-workspace-panel ${p.variant === "ghost" ? "" : "border border-workspace-divider"} ${p.background === undefined && !["soft", "subtle", "solid", "ghost", "outline"].includes(p.variant) ? "bg-workspace-panel" : ""} ${p.padding === undefined ? "p-4" : ""} ${p.gap === undefined && !p.disableAutoSpacing ? "gap-3" : ""}` : ""}`}
      >
        {name === "carousel-media-item" && p.media && (
          <Primitive
            name="AsyncImage"
            p={
              typeof p.media === "string"
                ? {
                    src: p.media,
                    alt: "",
                    aspectRatio: p.aspectRatio,
                  }
                : p.media
            }
            text=""
            run={run}
            interactive={interactive}
          >
            {null}
          </Primitive>
        )}
        {children}
      </div>
    );
  if (["grid", "flow"].includes(name)) {
    const minWidth = Math.max(
      120,
      Number(p.minChildWidth) ||
        Math.floor(640 / Math.max(1, Math.min(12, Number(p.columns) || 2))),
    );
    if (name === "flow" && p.layout === "masonry")
      return (
        <FlowContext.Provider value={true}>
          <div
            style={{
              ...style,
              columns: Math.max(1, Math.min(12, Number(p.columns) || 2)),
              columnGap: style.gap,
            }}
          >
            {children}
          </div>
        </FlowContext.Provider>
      );
    return (
      <div
        style={{
          ...style,
          gridTemplateColumns:
            p.columns && !p.minChildWidth
              ? `repeat(${Math.max(1, Math.min(12, Number(p.columns)))}, minmax(0,1fr))`
              : `repeat(auto-fit, minmax(min(100%, ${minWidth}px), 1fr))`,
          gridTemplateRows: p.rows
            ? `repeat(${Math.max(1, Math.min(100, Number(p.rows)))}, auto)`
            : undefined,
          gridAutoFlow: p.layout === "column" ? "column" : "row",
        }}
        className="grid min-w-0 gap-3"
      >
        {children}
      </div>
    );
  }
  if (["grid-item", "flow-item"].includes(name))
    return (
      <AspectContext.Provider value={p.aspectMode ?? aspect}>
        <div
          style={{
            ...style,
            gridColumn: `span ${Math.max(1, Math.min(12, Number(p.span) || 1))}`,
            gridRow: `span ${Math.max(1, Math.min(12, Number(p.rowSpan) || 1))}`,
            breakInside: masonry ? "avoid" : undefined,
            columnSpan: masonry && Number(p.span) > 1 ? "all" : undefined,
            marginBottom: masonry ? "12pt" : undefined,
            overflow: p.aspectMode === "fill" ? "hidden" : undefined,
          }}
          data-aspect-mode={p.aspectMode}
          data-inline-item=""
          className="min-w-0 data-[aspect-mode=fit]:[&_img]:object-contain data-[aspect-mode=fill]:[&_img]:h-full data-[aspect-mode=fill]:[&_img]:w-full data-[aspect-mode=fill]:[&_img]:object-cover"
        >
          {children}
        </div>
      </AspectContext.Provider>
    );
  if (name === "spacer")
    return <span style={style} className="min-h-2 min-w-2 flex-1" />;
  if (name === "divider")
    return (
      <hr
        style={{
          marginBlock: layoutStyle({ padding: p.spacing ?? 2 }).padding,
          borderTopWidth:
            ({ xs: 1, sm: 1, md: 2, lg: 3, xl: 4 } as Record<string, number>)[
              p.size
            ] ??
            (typeof p.size === "number" ? Math.max(1, Math.min(8, p.size)) : 1),
          borderColor: p.color ? uiColor(p.color) : undefined,
          marginInline: p.flush ? "-12pt" : undefined,
        }}
        className="border-0 border-t border-workspace-divider"
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
        id: name === "label" ? p.id : undefined,
        className: `min-w-0 break-words ${name === "title" ? "text-xl font-semibold tracking-tight text-balance" : name === "caption" ? "text-xs text-workspace-muted" : name === "label" ? "text-sm font-medium" : name === "blockquote" ? "border-l-2 border-workspace-divider pl-4 text-workspace-muted" : "text-sm leading-relaxed"} ${p.truncate ? "truncate" : ""}`,
      },
      children,
    );
  }
  if (name === "badge")
    return (
      <span
        style={{ ...style, ...toneStyle(p.color, p.variant ?? "soft") }}
        onMouseEnter={() => p.onHover && run(p.onHover, [true], false)}
        onMouseLeave={() => p.onHover && run(p.onHover, [false], false)}
        className={`inline-flex w-fit max-w-full items-center gap-1 truncate border px-2 py-0.5 text-xs font-medium ${p.pill === false ? "rounded-workspace-control" : "rounded-full"}`}
        data-color={p.color ?? "neutral"}
      >
        {plain}
      </span>
    );
  if (name === "icon") {
    // Inline UI draws Iconsax Outline glyphs by their own names only; unknown
    // names were already diagnosed and omitted by the document renderer.
    const Glyph = iconsaxIcons[String(p.name) as IconsaxIconName];
    if (!Glyph) return null;
    return (
      <Glyph
        aria-hidden
        size={Number(
          ({ xs: 12, sm: 16, md: 20, lg: 24, xl: 32, "2xl": 40 } as Props)[
            p.size
          ] ?? (typeof p.size === "number" ? p.size : 20),
        )}
        style={{
          color: uiColor(p.color),
          display: p.inline ? "inline-block" : "block",
          verticalAlign: p.inline ? "middle" : undefined,
        }}
      />
    );
  }
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
  if (name === "popover-trigger") {
    const compound = contentChildren(children).some(
      (child) =>
        isValidElement<{ name?: string }>(child) &&
        ["button", "pressable"].includes(child.props.name ?? ""),
    );
    return (
      <InlinePopoverTrigger
        interactive={interactive}
        block={p.block}
        compound={compound}
        onClick={click}
      >
        {children}
      </InlinePopoverTrigger>
    );
  }
  if (name === "pressable")
    return (
      <button
        type="button"
        disabled={disabled}
        title={p.tooltip}
        aria-label={p.accessibilityLabel ?? p.tooltip}
        onClick={click}
        style={style}
        // Buttons shrink to their content even as flex boxes; a block pressable
        // fills its container like any layout surface (grid squares, cards).
        className={`${focus} ${p.inline ? "inline-flex" : "flex w-full"} min-w-0 flex-col rounded-workspace-control text-left hover:bg-workspace-hover disabled:cursor-not-allowed disabled:opacity-40`}
      >
        {children}
      </button>
    );
  if (name === "button") {
    const size =
      (
        {
          xs: "min-h-7 px-2 text-xs",
          sm: "min-h-8 px-2.5 text-xs",
          md: "min-h-9 px-3 text-sm",
          lg: "min-h-11 px-4 text-base",
          xl: "min-h-12 px-5 text-base",
        } as Record<string, string>
      )[p.size] ?? "min-h-9 px-3 text-sm";
    const { fontSize: _size, ...buttonStyle } = style;
    return (
      <button
        type={p.submit ? "submit" : "button"}
        disabled={disabled || !!p.loading}
        aria-busy={p.loading || undefined}
        aria-label={p.accessibilityLabel}
        onClick={click}
        style={{
          ...buttonStyle,
          ...toneStyle(p.color ?? "primary", p.variant ?? "solid"),
          ...(p.uniform ? { paddingInline: 0, aspectRatio: "1" } : {}),
        }}
        className={`${focus} ${size} ${p.block ? "w-full" : ""} ${p.pill ? "rounded-full" : "rounded-workspace-control"} inline-flex items-center justify-center gap-2 border py-1.5 font-medium transition-[filter] hover:brightness-95 disabled:cursor-not-allowed disabled:opacity-40`}
      >
        {p.loading && (
          <span
            aria-hidden
            className="size-3.5 shrink-0 animate-spin rounded-full border-2 border-current border-t-transparent motion-reduce:animate-none"
          />
        )}
        {plain}
      </button>
    );
  }
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
  if (name === "date-picker")
    return (
      <InlineDatePicker
        p={p}
        value={value}
        disabled={disabled}
        change={change}
        style={style}
      />
    );
  if (name === "input" || name === "textarea") {
    const common = {
      id: p.id ?? id,
      "aria-label": label === undefined ? undefined : String(label),
      "aria-labelledby": p.labelledBy,
      disabled,
      required: !!p.required,
      name: p.name,
      onFocus: p.autoSelect
        ? (event: React.FocusEvent<HTMLInputElement | HTMLTextAreaElement>) =>
            event.target.select()
        : undefined,
      placeholder: p.placeholder,
      value: valueText(value),
      onChange: (
        event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>,
      ) =>
        change(
          inputType === "number"
            ? event.target.value === ""
              ? ""
              : (event.target as HTMLInputElement).valueAsNumber
            : p.mask
              ? maskInput(event.target.value, p.mask)
              : event.target.value,
        ),
      style,
      className: `${control} ${p.pill ? "rounded-full" : ""} ${p.invalid ? "border-workspace-danger outline-workspace-danger" : ""} ${p.variant === "ghost" ? "border-transparent bg-transparent" : ""} ${({ sm: "min-h-8 text-xs", lg: "min-h-11 text-base" } as Props)[p.size] ?? "min-h-9"}`,
      "aria-invalid": p.invalid || undefined,
    };
    if (name === "textarea")
      return (
        <textarea
          {...common}
          rows={Math.max(
            1,
            Math.min(Number(p.maxRows) || 30, Number(p.rows) || 3),
          )}
          onInput={
            p.autoResize
              ? (event) => {
                  const el = event.currentTarget;
                  el.style.height = "auto";
                  el.style.height = `${Math.min(el.scrollHeight, (Number(p.maxRows) || 30) * 24)}px`;
                }
              : undefined
          }
        />
      );
    return (
      <input
        {...common}
        type={
          [
            "text",
            "number",
            "email",
            "password",
            "search",
            "url",
            "tel",
          ].includes(inputType)
            ? inputType
            : "text"
        }
      />
    );
  }
  if (name === "checkbox")
    return (
      <label style={style} className="inline-flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          className={`size-4 accent-workspace-accent ${p.shape === "round" ? "[clip-path:circle(50%)]" : ""} ${focus}`}
          checked={!!value}
          id={p.id}
          value={p.value}
          required={!!p.required}
          name={p.name}
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
          aria-label={label === undefined ? undefined : String(label)}
          aria-labelledby={p.labelledBy}
          className={`min-h-8 w-full accent-workspace-accent ${focus}`}
          type="range"
          id={p.id}
          name={p.name}
          required={!!p.required}
          disabled={disabled}
          min={p.min ?? 0}
          max={p.max ?? 100}
          step={p.step ?? 1}
          style={
            p.sliderStyle === "filled"
              ? {
                  background: `linear-gradient(to right, var(--workspace-native-accent) ${Math.max(0, Math.min(100, (((Number(value) || 0) - (p.min ?? 0)) / ((p.max ?? 100) - (p.min ?? 0) || 1)) * 100))}%, var(--workspace-control-bg) 0)`,
                }
              : undefined
          }
          value={Number(value) || 0}
          onChange={(event) => change(event.target.valueAsNumber)}
        />
        {Array.isArray(p.marks) && (
          <div className="relative h-4 text-xs text-workspace-muted">
            {p.marks.slice(0, 20).map((mark: Props, index: number) => {
              const min = Number(p.min ?? 0),
                max = Number(p.max ?? 100);
              const at = Math.max(
                0,
                Math.min(
                  100,
                  ((Number(mark.value) - min) / (max - min || 1)) * 100,
                ),
              );
              return (
                <span
                  key={index}
                  className="absolute -translate-x-1/2 whitespace-nowrap first:translate-x-0 last:-translate-x-full"
                  style={{ left: `${at}%` }}
                >
                  {valueText(mark.label ?? mark.value)}
                </span>
              );
            })}
          </div>
        )}
      </div>
    );
  if (name === "radio-group")
    return (
      <InputGroup.Provider
        value={{
          value,
          change,
          name: p.name ?? id,
          disabled,
          required: !!p.required,
        }}
      >
        <fieldset
          id={p.id}
          style={style}
          className={`flex gap-3 ${p.direction === "horizontal" ? "flex-row flex-wrap" : "flex-col"}`}
        >
          {p.label !== undefined && (
            <legend className="mb-1 text-sm font-medium">
              {valueText(p.label)}
            </legend>
          )}
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
          id={p.id}
          required={!!p.required || group?.required}
          value={p.value}
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
        <InlineSelect
          p={p}
          value={value}
          disabled={disabled}
          change={change}
          style={style}
        />
      );
    return (
      <div
        role="radiogroup"
        id={p.id}
        aria-label={label === undefined ? undefined : String(label)}
        aria-labelledby={p.labelledBy}
        style={style}
        className={`flex flex-wrap gap-1 border border-workspace-divider bg-workspace-control p-1 ${p.pill ? "rounded-full [&>label]:rounded-full" : "rounded-workspace-control"} ${({ sm: "text-xs", lg: "text-base" } as Props)[p.size] ?? ""}`}
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
      <InlinePopover
        open={p.open}
        showOnHover={p.showOnHover}
        hoverOpenDelay={p.hoverOpenDelay}
        interactive={interactive}
        onOpenChange={
          p.onOpenChange
            ? (open) => run(p.onOpenChange, [open], false)
            : undefined
        }
      >
        {children}
      </InlinePopover>
    );
  if (name === "popover-content")
    return (
      <InlinePopoverContent
        side={p.side}
        align={p.align}
        sideOffset={p.sideOffset}
        showCloseButton={p.showCloseButton}
        interactive={interactive}
      >
        {children}
      </InlinePopoverContent>
    );
  if (name === "carousel")
    return (
      <section
        style={{ ...style, padding: p.flush ? 0 : style.padding }}
        aria-roledescription="carousel"
      >
        <div
          ref={viewport}
          onScroll={() => {
            const el = viewport.current;
            if (!el || !p.onChange || !interactive) return;
            const slides = Array.from(el.children) as HTMLElement[];
            const start = slides[0]?.offsetLeft ?? 0;
            const nearest = slides.reduce(
              (best, slide, i) =>
                Math.abs(slide.offsetLeft - start - el.scrollLeft) <
                Math.abs(slides[best].offsetLeft - start - el.scrollLeft)
                  ? i
                  : best,
              0,
            );
            if (nearest !== local) {
              setLocal(nearest);
              run(p.onChange, [nearest], false);
            }
          }}
          style={{
            gap: p.flush ? 0 : style.gap,
            scrollSnapType: p.snap === false ? "none" : "x mandatory",
          }}
          className="flex snap-x snap-mandatory gap-3 overflow-x-auto [&>*]:min-w-0 [&>*]:shrink-0 [&>*]:basis-full [&>*]:snap-start"
        >
          {contentChildren(children).map((child, index) => (
            <div
              key={isValidElement(child) ? child.key : index}
              style={{
                flexBasis: `calc((100% - ${Math.max(0, Math.min(12, Number(p.visibleItems) || 1) - 1)} * ${p.flush ? "0px" : (style.gap ?? "12px")}) / ${Math.max(1, Math.min(12, Number(p.visibleItems) || 1))})`,
                scrollSnapAlign: ["start", "center", "end"].includes(
                  p.snapAlign,
                )
                  ? p.snapAlign
                  : "start",
              }}
            >
              {child}
            </div>
          ))}
        </div>
        {p.showArrows !== false && (
          <nav aria-label="Slides" className="mt-2 flex justify-end gap-2">
            {[-1, 1].map((direction) => (
              <button
                key={direction}
                type="button"
                disabled={!interactive}
                aria-label={direction < 0 ? "Previous slide" : "Next slide"}
                className={`rounded-full border border-workspace-divider p-2 hover:bg-workspace-hover ${focus}`}
                onClick={() => {
                  const el = viewport.current;
                  if (el) {
                    const slides = Array.from(el.children) as HTMLElement[];
                    const step =
                      slides.length > 1
                        ? slides[1].offsetLeft - slides[0].offsetLeft
                        : el.clientWidth;
                    el.scrollBy({ left: direction * step });
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
    return (
      <ListContext.Provider value={p}>
        {createElement(
          p.marker === "number" ? "ol" : "ul",
          {
            style: {
              ...style,
              display: "flex",
              flexDirection: "column",
              gap: style.gap ?? "8pt",
              listStyleType:
                p.marker === "none" || p.connector
                  ? "none"
                  : p.marker === "number"
                    ? "decimal"
                    : "disc",
            },
            className: "pl-5 text-sm",
            start: p.start,
          },
          contentChildren(children).map((child, index) =>
            isValidElement<{ name?: string }>(child) &&
            child.props.name === "list-item" ? (
              child
            ) : (
              <li key={index}>{child}</li>
            ),
          ),
        )}
      </ListContext.Provider>
    );
  if (name === "list-item")
    return (
      <li
        style={style}
        inert={!!p.disabled}
        aria-disabled={p.disabled || undefined}
        className={`relative ${p.disabled ? "opacity-40" : ""} ${list.connector ? "border-l border-workspace-divider pl-4 pb-3 last:border-transparent" : ""}`}
      >
        {p.marker !== undefined && (
          <span
            style={{ fontSize: p.markerSize, maxWidth: list.maxMarkerSize }}
            className="mr-2 inline-block align-middle"
          >
            {valueText(p.marker)}
          </span>
        )}
        {p.label !== undefined && (
          <span className="font-medium">{valueText(p.label)}</span>
        )}
        {p.label !== undefined && contentChildren(children).length > 0 ? (
          <div>{children}</div>
        ) : (
          p.label === undefined && children
        )}
        {p.description && (
          <p className="text-xs text-workspace-muted">
            {valueText(p.description)}
          </p>
        )}
      </li>
    );
  if (name === "table") {
    const sizing =
      p.columnSizing && typeof p.columnSizing === "object"
        ? p.columnSizing
        : undefined;
    const columns = sizing
      ? Math.min(
          100,
          Array.isArray(sizing)
            ? sizing.length
            : Math.max(
                -1,
                ...Object.keys(sizing).map(Number).filter(Number.isFinite),
              ) + 1,
        )
      : 0;
    return (
      <TableContext.Provider value={{ ...p, header: false }}>
        <div
          className={`min-w-0 overflow-auto rounded-workspace-control border border-workspace-divider ${p.stickyHeader ? "max-h-96" : ""}`}
        >
          <table
            style={{
              ...style,
              tableLayout:
                p.columnSizing === "fixed" || sizing ? "fixed" : "auto",
            }}
            className="w-full text-left text-sm"
          >
            {!!columns && (
              <colgroup>
                {Array.from({ length: columns }, (_, i) => (
                  <col key={i} style={layoutStyle({ width: sizing[i] })} />
                ))}
              </colgroup>
            )}
            <tbody>
              {hasTableRows(children) ? (
                contentChildren(children)
              ) : (
                <tr>
                  <td className="p-3">{p.emptyLabel ?? "No rows"}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </TableContext.Provider>
    );
  }
  if (name === "table-section")
    return (
      <TableContext.Provider
        value={{
          ...table,
          header: !!p.header,
          rowDivider: p.rowDivider ?? table.rowDivider,
        }}
      >
        {typeof p.header === "string" && (
          <tr>
            <th colSpan={100} className="bg-workspace-control p-3">
              {p.header}
            </th>
          </tr>
        )}
        {contentChildren(children)}
      </TableContext.Provider>
    );
  if (name === "table-row")
    return (
      <TableContext.Provider
        value={{ ...table, header: p.header ?? table.header }}
      >
        <tr
          style={style}
          className={
            table.rowDivider !== false &&
            !["none", "columns"].includes(table.dividers)
              ? "border-b border-workspace-divider last:border-0"
              : ""
          }
        >
          {p.label && (
            <th scope="row" className="px-3 py-2" colSpan={p.labelColSpan ?? 1}>
              {valueText(p.label)}
            </th>
          )}
          {contentChildren(children)}
        </tr>
      </TableContext.Provider>
    );
  if (name === "table-cell") {
    const header = p.header ?? table.header;
    return createElement(
      header ? "th" : "td",
      {
        style: {
          ...style,
          textAlign: p.align ?? style.textAlign,
          verticalAlign: p.vAlign,
        },
        scope: header ? "col" : undefined,
        className: `px-3 py-2 ${header ? "bg-workspace-control font-medium" : ""} ${header && table.stickyHeader ? "sticky top-0 z-10" : ""} ${["columns", "all"].includes(table.dividers) ? "border-r border-workspace-divider last:border-r-0" : ""}`,
        colSpan: p.colSpan,
        rowSpan: p.rowSpan,
      },
      children,
    );
  }
  if (name === "Chart")
    return <InlineChart content={p.content} animate={interactive} />;
  if (name === "favicon" || name === "AsyncImage") {
    const url = safeURL(p.src ?? p.url, true);
    if ((!url || imageFailed) && p.fallback)
      return <span role="status">{valueText(p.fallback)}</span>;
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
        style={{
          ...style,
          objectFit: p.fit ?? (aspect === "fill" ? "cover" : "contain"),
          display: p.inline ? "inline-block" : "block",
          verticalAlign: p.inline ? "middle" : undefined,
          padding: p.frame ? "4pt" : style.padding,
          border: p.frame ? "1px solid var(--workspace-divider)" : style.border,
          height: aspect === "fill" ? "100%" : style.height,
          width:
            name === "favicon"
              ? typeof p.size === "number"
                ? p.size
                : (({ xs: 12, sm: 16, md: 20, lg: 24, xl: 32 } as Props)[
                    p.size
                  ] ?? 20)
              : aspect === "fill"
                ? "100%"
                : style.width,
        }}
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
        {contentChildren(children).length ? children : (p.title ?? href)}
      </a>
    ) : (
      <Unavailable name="Link" reason="A resolved URL is required." />
    );
  }
  if (name === "CodeBlock")
    return (
      <InlineCodeBlock
        source={text}
        language={p.language}
        editable={p.editable && interactive && p.interactive !== false}
        interactive={interactive && !!p.interactive}
        preview={p.model_previewable ?? p.interactive ?? true}
      />
    );
  if (name === "WritingBlock")
    return (
      <InlineWritingBlock
        source={text}
        p={p}
        interactive={interactive}
        copy={(draft) => run(p.onCopy, [draft])}
      />
    );
  if (name === "escape") return <span>{text}</span>;
  if (name === "Entity" && ["task", "page"].includes(p.category) && p.ref)
    return (
      <button
        type="button"
        disabled={disabled}
        className={`rounded-workspace-control border border-workspace-divider px-3 py-2 text-left text-sm text-workspace-accent hover:bg-workspace-hover ${focus}`}
        onClick={() =>
          run(p.onEntity, [{ category: p.category, refId: p.ref }])
        }
      >
        {contentChildren(children).length
          ? children
          : valueText(p.value ?? `Open ${p.category}`)}
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
