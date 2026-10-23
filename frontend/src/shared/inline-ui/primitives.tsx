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
import { Icon } from "@/shared/ui/icon";
import { ChevronLeft, ChevronRight } from "@/shared/ui/icons";
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
  if (p.disableAutoSpacing) style.gap = p.gap === undefined ? 0 : style.gap;
  if (p.uniform) {
    style.aspectRatio = "1";
    style.flexShrink = 0;
  }
  if (p.variant === "ghost") {
    style.background = "transparent";
    style.border = "none";
  }
  if (p.variant === "outline") {
    style.background = "transparent";
    style.border = "1px solid var(--workspace-divider)";
  }
  if (["soft", "subtle"].includes(p.variant))
    style.background = "var(--workspace-control-bg)";
  if (p.variant === "solid") {
    style.background = "var(--workspace-accent-fill)";
    style.color = "var(--workspace-accent-foreground)";
  }
  if (p.fontSize !== undefined)
    style.fontSize = layoutStyle({ size: p.fontSize }).fontSize ?? p.fontSize;
  const inputType = p.type ?? p.inputType;
  const label = p["aria-label"] ?? p.label ?? p.placeholder ?? p.name ?? name;
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
      attributes["aria-label"] = p["aria-label"] ?? p.label ?? "Illustration";
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
        className={`min-w-0 ${name === "row" ? "flex flex-row flex-wrap" : "flex flex-col"} ${name === "card" ? "rounded-workspace-panel border border-workspace-divider bg-workspace-panel p-4 gap-3" : ""}`}
      >
        {name === "carousel-media-item" && p.media && (
          <Primitive
            name="AsyncImage"
            p={
              typeof p.media === "string"
                ? {
                    src: p.media,
                    alt: p.label ?? "",
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
          ...style,
          marginBlock: layoutStyle({ padding: p.spacing ?? 2 }).padding,
        }}
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
        onMouseEnter={() => p.onHover && run(p.onHover, [true], false)}
        onMouseLeave={() => p.onHover && run(p.onHover, [false], false)}
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
        aria-label={p["aria-label"] ?? p.label}
        aria-hidden={!p.label && !p["aria-label"]}
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
  if (name === "button" || name === "pressable")
    return (
      <button
        type={p.submit ? "submit" : "button"}
        disabled={disabled || !!p.loading}
        title={p.tooltip}
        id={p.id}
        aria-label={p["aria-label"] ?? p.accessibilityLabel ?? p.tooltip}
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
      "aria-label": String(label),
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
      className: control,
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
        min={p.min}
        max={p.max}
        step={p.step}
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
          aria-label={String(label)}
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
          aria-label={String(label)}
          style={style}
          className={`flex gap-3 ${p.direction === "horizontal" ? "flex-wrap" : "flex-col"}`}
        >
          {Array.isArray(p.options)
            ? p.options.map((option: Props, index: number) => (
                <label
                  key={index}
                  className="inline-flex items-center gap-2 text-sm"
                >
                  <input
                    type="radio"
                    className={`size-4 accent-workspace-accent ${focus}`}
                    name={p.name ?? id}
                    checked={value === option.value}
                    value={String(option.value)}
                    required={!!p.required}
                    disabled={disabled || !!option.disabled}
                    onChange={() => change(option.value)}
                  />
                  {valueText(option.label)}
                </label>
              ))
            : children}
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
        {plain}
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
                children
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
        {children}
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
          {children}
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
  if (name === "Chart") return <InlineChart content={p.content} />;
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
    const href = safeURL(p.href ?? p.url);
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
        onClick={() => run(p.onFollowUp, [p.query ?? p.text ?? p.value])}
      >
        {p.text ?? p.value ?? p.query}
      </button>
    );
  return <Unavailable name={name} />;
}
