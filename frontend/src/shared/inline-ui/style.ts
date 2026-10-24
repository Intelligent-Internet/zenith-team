import type { CSSProperties } from "react";
const colors: Record<string, string> = {
  default: "var(--workspace-native-text)",
  primary: "var(--workspace-native-accent)",
  secondary: "var(--workspace-native-text-muted)",
  subtle: "var(--workspace-native-text-faint)",
  neutral: "var(--workspace-native-text)",
  info: "var(--workspace-native-info)",
  success: "var(--workspace-native-success)",
  danger: "var(--workspace-native-danger)",
  warning: "var(--workspace-native-approval)",
  accent: "var(--workspace-native-accent)",
};
export const uiColor = (value: unknown) =>
  colors[String(value)] ?? colors.default;
const surfaces: Record<string, string> = {
  background: "var(--workspace-panel-bg)",
  surface: "var(--workspace-panel-bg)",
  "surface-secondary": "var(--workspace-control-bg)",
  transparent: "transparent",
  blue: "var(--workspace-info-soft)",
  purple:
    "color-mix(in srgb, var(--color-purple-500, #a855f7) 16%, var(--workspace-panel-bg))",
  yellow: "var(--workspace-approval-soft)",
  green: "var(--workspace-success-soft)",
  red: "var(--workspace-danger-soft)",
  info: "var(--workspace-info-soft)",
  success: "var(--workspace-success-soft)",
  warning: "var(--workspace-approval-soft)",
  danger: "var(--workspace-danger-soft)",
  accent: "var(--workspace-accent-soft)",
};
const sizes: Record<string, number> = {
  xs: 11,
  sm: 13,
  md: 15,
  lg: 18,
  xl: 24,
  "2xl": 30,
  "3xl": 36,
};
function dimension(value: unknown): string | number | undefined {
  if (typeof value === "number" && Number.isFinite(value))
    return Math.max(0, Math.min(2000, value));
  if (
    typeof value === "string" &&
    /^(?:auto|fit-content|100%|\d{1,4}(?:\.\d{1,2})?(?:px|pt|rem|em|%|vh|vw))$/.test(
      value,
    )
  )
    return value;
}
function spacing(value: unknown): string | number | undefined {
  if (
    typeof value === "string" &&
    /^(?:\d{1,4}(?:\.\d{1,2})?)(?:px|pt|rem|em|%)$/.test(value)
  )
    return value;
  if (value === "0") return 0;
  const n = Number(value);
  return Number.isFinite(n)
    ? `${Math.max(0, Math.min(24, n)) * 4}pt`
    : undefined;
}
// Authored dimensions are runtime data; only these bounded layout properties
// become inline styles. Component defaults and interaction states use Tailwind.
export function layoutStyle(props: Record<string, unknown>): CSSProperties {
  const theme =
    props.theme === "light" || props.theme === "dark" ? props.theme : undefined;
  const style: CSSProperties = theme
    ? {
        colorScheme: theme,
        color: "var(--workspace-native-text)",
        background: "var(--workspace-panel-bg)",
      }
    : {};
  for (const key of [
    "width",
    "height",
    "minWidth",
    "maxWidth",
    "minHeight",
    "maxHeight",
  ] as const)
    style[key] = dimension(props[key]);
  for (const [source, targets] of Object.entries({
    minSize: ["minWidth", "minHeight"],
    maxSize: ["maxWidth", "maxHeight"],
  })) {
    if (props[source] === undefined) continue;
    for (const target of targets)
      (style as Record<string, unknown>)[target] = dimension(props[source]);
  }
  if (props.size && !sizes[String(props.size)])
    style.width = style.height = dimension(props.size);
  style.padding = props.flush ? 0 : spacing(props.padding);
  style.gap = spacing(props.gap);
  if (props.rowGap !== undefined) style.rowGap = spacing(props.rowGap);
  if (props.columnGap !== undefined) style.columnGap = spacing(props.columnGap);
  if (props.flex !== undefined)
    style.flex =
      props.flex === true
        ? 1
        : Number.isFinite(Number(props.flex))
          ? Math.max(0, Math.min(100, Number(props.flex)))
          : dimension(props.flex);
  if (props.background !== undefined)
    style.background = surfaces[String(props.background)] ?? surfaces.surface;
  if (props.color !== undefined) style.color = uiColor(props.color);
  if (props.radius !== undefined)
    style.borderRadius = (
      { none: 0, sm: 4, md: 6, lg: 8, xl: 12, "2xl": 16, full: 9999 } as Record<
        string,
        number
      >
    )[String(props.radius)];
  if (props.border) style.border = "1px solid var(--workspace-divider)";
  const align: Record<string, string> = {
    start: "flex-start",
    end: "flex-end",
    center: "center",
    stretch: "stretch",
    baseline: "baseline",
  };
  const justify: Record<string, string> = {
    ...align,
    between: "space-between",
    around: "space-around",
    evenly: "space-evenly",
  };
  if (props.align) style.alignItems = align[String(props.align)];
  if (props.justify)
    style.justifyContent =
      justify[String(props.justify).replace(/^space-/, "")];
  if (props.direction)
    style.flexDirection = ["horizontal", "row"].includes(
      String(props.direction),
    )
      ? "row"
      : "column";
  if (props.wrap !== undefined) style.flexWrap = props.wrap ? "wrap" : "nowrap";
  if (props.clip) style.overflow = "hidden";
  if (props.scrollable) style.overflow = "auto";
  // Accepts 16/9, 16:9 or a single number.
  if (
    props.aspectRatio &&
    /^[0-9.]+(?:\s*[/:]\s*[0-9.]+)?$/.test(String(props.aspectRatio))
  )
    style.aspectRatio = String(props.aspectRatio).replace(":", "/");
  if (
    ["left", "right", "center", "justify", "start", "end"].includes(
      String(props.textAlign),
    )
  )
    style.textAlign = props.textAlign as CSSProperties["textAlign"];
  if (props.size && sizes[String(props.size)])
    style.fontSize = sizes[String(props.size)];
  if (props.weight)
    style.fontWeight =
      typeof props.weight === "number"
        ? props.weight
        : (
            { normal: 400, medium: 500, semibold: 600, bold: 700 } as Record<
              string,
              number
            >
          )[String(props.weight)];
  if (props.strong) style.fontWeight = 600;
  if (props.italic) style.fontStyle = "italic";
  if (props.tabularNums) style.fontVariantNumeric = "tabular-nums";
  const decorations = [
    props.underline && "underline",
    props.lineThrough && "line-through",
  ].filter(Boolean);
  if (decorations.length) style.textDecoration = decorations.join(" ");
  if (props.maxLines) {
    style.display = "-webkit-box";
    style.WebkitBoxOrient = "vertical";
    style.WebkitLineClamp = Math.max(1, Math.min(50, Number(props.maxLines)));
    style.overflow = "hidden";
  }
  if (props.minLines)
    style.minHeight = `${Math.max(1, Math.min(50, Number(props.minLines)))}lh`;
  if (props.highlight) style.backgroundColor = surfaces.yellow;
  if (props.pill) style.borderRadius = 9999;
  if (props.shadow) style.boxShadow = "0 4px 16px rgb(0 0 0 / 0.12)";
  if (props.preserveWhitespace) style.whiteSpace = "pre-wrap";
  return style;
}
export function safeURL(value: unknown, image = false): string | undefined {
  if (typeof value !== "string" || value.length > 2048) return;
  try {
    const url = new URL(value);
    if (
      (url.protocol === "https:" ||
        (!image && ["http:", "mailto:"].includes(url.protocol))) &&
      !url.username &&
      !url.password
    )
      return value;
  } catch {
    /* Invalid links stay unavailable. */
  }
}
