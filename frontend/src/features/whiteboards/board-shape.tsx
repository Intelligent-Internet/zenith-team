import type { CSSProperties, ReactNode } from "react";
import type { components } from "@/shared/api/generated/api.generated";
import {
  Circle,
  Square,
  ShapeActor,
  ShapeClass,
  ShapeCloud,
  ShapeComponent,
  ShapeDatabase,
  ShapeDiamond,
  ShapeDocument,
  ShapeEntity,
  ShapeFragment,
  ShapeLifeline,
  ShapeNote,
  ShapeQueue,
  ShapeRounded,
  ShapeTerminal,
  ShapeTriangle,
} from "@/shared/ui/icons";

export type BoardShapeType = components["schemas"]["BoardShapeType"];
type BoardProperties = components["schemas"]["BoardProperties"];

export const boardShapeGroups = [
  {
    label: "Basic",
    shapes: [
      { value: "rectangle", label: "Rectangle", Icon: Square },
      { value: "rounded", label: "Rounded", Icon: ShapeRounded },
      { value: "ellipse", label: "Ellipse", Icon: Circle },
      { value: "diamond", label: "Diamond", Icon: ShapeDiamond },
      { value: "triangle", label: "Triangle", Icon: ShapeTriangle },
      { value: "terminal", label: "Terminal", Icon: ShapeTerminal },
    ],
  },
  {
    label: "Architecture",
    shapes: [
      { value: "database", label: "Database", Icon: ShapeDatabase },
      { value: "queue", label: "Queue", Icon: ShapeQueue },
      { value: "cloud", label: "Cloud", Icon: ShapeCloud },
      { value: "component", label: "Component", Icon: ShapeComponent },
      { value: "document", label: "Document", Icon: ShapeDocument },
      { value: "note", label: "Note", Icon: ShapeNote },
    ],
  },
  {
    label: "UML",
    shapes: [
      { value: "actor", label: "Actor", Icon: ShapeActor },
      { value: "lifeline", label: "Lifeline", Icon: ShapeLifeline },
      { value: "class", label: "Class", Icon: ShapeClass },
      { value: "entity", label: "Entity", Icon: ShapeEntity },
      { value: "fragment", label: "Fragment", Icon: ShapeFragment },
    ],
  },
] as const satisfies readonly {
  label: string;
  shapes: readonly { value: BoardShapeType; label: string; Icon: unknown }[];
}[];

// Shape kind geometry in Board units, the server's too
// (server/internal/canvas/board_diagram.go).
export const boardActorBand = 36;
const boardLifelineHeader = 48;
// A lifeline headed by an actor's figure over its name.
const boardLifelineActorHeader = 100;

/** The height of a lifeline's head: a box, or an actor's figure. */
export function boardLifelineHead(height: number, head?: string | null) {
  return Math.min(
    height,
    head === "actor" ? boardLifelineActorHeader : boardLifelineHeader,
  );
}
export const boardCompartmentTitle = 30;
export const boardCompartmentLine = 18;
export const boardCompartmentMargin = 12;
export const boardFragmentTag = 22;

/** Kinds whose text lives in compartments, edited in a dialog. */
export function isCompartmented(shape: string | undefined) {
  return shape === "class" || shape === "entity" || shape === "fragment";
}

/** One section's height: its lines and padding. */
export function boardCompartmentHeight(section: string) {
  return (
    (section ? section.split("\n").length : 1) * boardCompartmentLine +
    boardCompartmentMargin
  );
}

let measureContext: CanvasRenderingContext2D | null | undefined;
/** A line's width in the editor's compartment font. */
export function boardTextWidth(text: string, weight = 400) {
  if (measureContext === undefined)
    measureContext = document.createElement("canvas").getContext("2d");
  if (!measureContext) return text.length * 7;
  measureContext.font = `${weight} 13px ${getComputedStyle(document.body).fontFamily}`;
  return measureContext.measureText(text).width;
}

/** A fragment's operator tag width, at most the fragment's. */
export function boardFragmentTagWidth(title: string, width: number) {
  return Math.min(width, boardTextWidth(title, 600) + 20);
}

/**
 * Where a kind keeps its label in a width × height box: below an actor's
 * figure, in a lifeline's header, inside a cylinder's body, beside a
 * component's tabs, inside a cloud's bumps. The original four kinds keep
 * their stylesheet padding.
 */
export function boardShapeLabelBox(
  shape: string | undefined,
  width: number,
  height: number,
  head?: string | null,
): CSSProperties | undefined {
  const inset = (left: number, top: number, right: number, bottom: number) => ({
    padding: 0,
    left,
    top,
    width: Math.max(0, width - left - right),
    height: Math.max(0, height - top - bottom),
  });
  switch (shape) {
    case "actor": {
      const band = Math.min(boardActorBand, height * 0.35);
      return { ...inset(0, height - band, 0, 0), alignItems: "flex-start" };
    }
    case "lifeline": {
      const header = boardLifelineHead(height, head);
      if (head === "actor") {
        const band = Math.min(boardActorBand, header * 0.35);
        return {
          ...inset(0, header - band, 0, height - header),
          alignItems: "flex-start",
        };
      }
      return inset(6, 6, 6, height - header + 4);
    }
    case "database": {
      const ry = Math.min(0.1 * height, 12);
      return inset(10, 2 * ry + 4, 10, ry + 4);
    }
    case "component":
      return inset(24, 8, 8, 8);
    case "cloud":
      return inset(0.2 * width, 0.3 * height, 0.15 * width, 0.12 * height);
    case "rounded":
    case "terminal":
    case "queue":
    case "document":
    case "note":
      return inset(12, 12, 12, 12);
  }
  return undefined;
}

// The cloud's outline in a 100×100 box and its curve's bounds there.
const cloudPath =
  "M25 80 C8 80 2 62 14 52 C8 36 24 24 38 30 C44 14 68 12 74 30 C90 28 100 44 92 56 C102 70 90 82 76 80 Z";
const cloudBounds = [7.7117, 17.239, 95.6903, 80.2189];

/** An actor's stick figure in a w × h box, over the band its name takes. */
function actorFigure(y: number, w: number, h: number, cx: number) {
  const band = Math.min(boardActorBand, h * 0.35);
  const figure = h - band;
  const r = Math.max(0, Math.min(figure * 0.14, w * 0.18));
  const headY = y + r + 1;
  const neck = headY + r,
    hip = y + figure * 0.62,
    arms = y + figure * 0.36;
  const reach = w * 0.32,
    stride = w * 0.25;
  return (
    <g fill="none" strokeLinecap="round">
      <circle cx={cx} cy={headY} r={r} />
      <path
        d={`M${cx},${neck} L${cx},${hip} M${cx - reach},${arms} L${cx + reach},${arms} M${cx - stride},${y + figure} L${cx},${hip} L${cx + stride},${y + figure}`}
      />
    </g>
  );
}

function cloud(x: number, y: number, w: number, h: number) {
  const [minX, minY, maxX, maxY] = cloudBounds;
  let axis = 0;
  return cloudPath.replace(/-?\d+(\.\d+)?/g, (value) => {
    const n = Number(value);
    const out =
      axis === 0
        ? x + ((n - minX) / (maxX - minX)) * w
        : y + ((n - minY) / (maxY - minY)) * h;
    axis = 1 - axis;
    return String(Math.round(out * 100) / 100);
  });
}

const points = (list: [number, number][]) =>
  list.map(([x, y]) => `${x},${y}`).join(" ");

/**
 * Canvas geometry shared by the placement preview and the saved object. Each
 * kind draws its outline in Board units: its body (filled and outlined),
 * filled parts over it (headers, tabs, tags), open lines (rims, folds, a
 * stick figure, compartment dividers) and dashed lines.
 */
export function BoardShapeGraphic({
  shape,
  width,
  height,
  color,
  fill,
  strokeWidth,
  properties,
}: {
  shape?: string;
  width: number;
  height: number;
  color: string;
  fill: string;
  strokeWidth: number;
  properties?: Pick<
    BoardProperties,
    "title" | "sections" | "dividers" | "head"
  >;
}) {
  const inset = Math.min(strokeWidth / 2, width / 2, height / 2);
  const x = inset,
    y = inset,
    w = Math.max(0, width - inset * 2),
    h = Math.max(0, height - inset * 2);
  const right = x + w,
    bottom = y + h;
  const open = { fill: "none" };
  const dashed = { fill: "none", strokeDasharray: "8 6" };
  let graphic: ReactNode;
  switch (shape) {
    case "ellipse":
      graphic = (
        <ellipse cx={width / 2} cy={height / 2} rx={w / 2} ry={h / 2} />
      );
      break;
    case "triangle":
      graphic = (
        <polygon
          points={points([
            [width / 2, y],
            [right, bottom],
            [x, bottom],
          ])}
        />
      );
      break;
    case "diamond":
      graphic = (
        <polygon
          points={points([
            [width / 2, y],
            [right, height / 2],
            [width / 2, bottom],
            [x, height / 2],
          ])}
        />
      );
      break;
    case "rounded":
    case "terminal": {
      const r =
        shape === "terminal"
          ? Math.min(w, h) / 2
          : Math.min(12, Math.min(w, h) / 4);
      graphic = <rect x={x} y={y} width={w} height={h} rx={r} ry={r} />;
      break;
    }
    case "actor":
      graphic = actorFigure(y, w, h, width / 2);
      break;
    case "database": {
      const rx = w / 2,
        ry = Math.min(0.1 * h, 12);
      graphic = (
        <>
          <path
            d={`M${x},${y + ry} A${rx},${ry} 0 0 1 ${right},${y + ry} L${right},${bottom - ry} A${rx},${ry} 0 0 1 ${x},${bottom - ry} Z`}
          />
          <path
            {...open}
            d={`M${right},${y + ry} A${rx},${ry} 0 0 1 ${x},${y + ry}`}
          />
        </>
      );
      break;
    }
    case "queue": {
      const rx = Math.min(0.08 * w, 12),
        ry = h / 2;
      graphic = (
        <>
          <path
            d={`M${right - rx},${y} A${rx},${ry} 0 0 1 ${right - rx},${bottom} L${x + rx},${bottom} A${rx},${ry} 0 0 1 ${x + rx},${y} Z`}
          />
          <path
            {...open}
            d={`M${right - rx},${bottom} A${rx},${ry} 0 0 1 ${right - rx},${y}`}
          />
        </>
      );
      break;
    }
    case "cloud":
      graphic = <path d={cloud(x, y, w, h)} />;
      break;
    case "component": {
      const tab = 10;
      graphic = (
        <>
          <rect x={x + tab} y={y} width={Math.max(0, w - tab)} height={h} />
          <rect x={x} y={y + h * 0.25 - tab / 2} width={2 * tab} height={tab} />
          <rect x={x} y={y + h * 0.6 - tab / 2} width={2 * tab} height={tab} />
        </>
      );
      break;
    }
    case "document": {
      const a = Math.min(0.1 * h, 10);
      const wave: [number, number][] = [];
      for (let i = 0; i <= 48; i++) {
        const t = i / 48;
        wave.push([right - t * w, bottom - a + a * Math.sin(2 * Math.PI * t)]);
      }
      graphic = <polygon points={points([[x, y], [right, y], ...wave])} />;
      break;
    }
    case "note": {
      const f = Math.min(16, Math.min(w, h) / 4);
      graphic = (
        <>
          <polygon
            points={points([
              [x, y],
              [right - f, y],
              [right, y + f],
              [right, bottom],
              [x, bottom],
            ])}
          />
          <polyline
            {...open}
            points={points([
              [right - f, y],
              [right - f, y + f],
              [right, y + f],
            ])}
          />
        </>
      );
      break;
    }
    case "lifeline": {
      const header = boardLifelineHead(h, properties?.head);
      graphic = (
        <>
          {properties?.head === "actor" ? (
            actorFigure(y, w, header, width / 2)
          ) : (
            <rect x={x} y={y} width={w} height={header} />
          )}
          <line
            {...dashed}
            x1={width / 2}
            y1={y + header}
            x2={width / 2}
            y2={bottom}
          />
        </>
      );
      break;
    }
    case "class":
    case "entity": {
      const title = Math.min(h, boardCompartmentTitle);
      const sections = properties?.sections ?? [];
      const dividers: number[] = [];
      let at = y + title;
      for (const section of sections.slice(0, -1)) {
        at += boardCompartmentHeight(section);
        if (at < bottom) dividers.push(at);
      }
      graphic = (
        <>
          <rect
            x={x}
            y={y}
            width={w}
            height={h}
            fill={shape === "entity" ? "#ffffff" : undefined}
          />
          {shape === "entity" && <rect x={x} y={y} width={w} height={title} />}
          <path
            {...open}
            d={[y + title, ...dividers]
              .map((dy) => `M${x},${dy} L${right},${dy}`)
              .join(" ")}
          />
        </>
      );
      break;
    }
    case "fragment": {
      const tag = boardFragmentTagWidth(properties?.title ?? "", w);
      const tagHeight = Math.min(h, boardFragmentTag);
      graphic = (
        <>
          <rect {...open} x={x} y={y} width={w} height={h} />
          <polygon
            points={points([
              [x, y],
              [x + tag, y],
              [x + tag, y + tagHeight - 6],
              [x + tag - 8, y + tagHeight],
              [x, y + tagHeight],
            ])}
          />
          {(properties?.dividers ?? [])
            .filter((divider) => y + divider < bottom)
            .map((divider, index) => (
              <line
                key={index}
                {...dashed}
                x1={x}
                y1={y + divider}
                x2={right}
                y2={y + divider}
              />
            ))}
        </>
      );
      break;
    }
    default:
      graphic = <rect x={x} y={y} width={w} height={h} />;
  }
  return (
    <svg
      className="absolute inset-0 w-full h-full pointer-events-none overflow-visible"
      viewBox={`0 0 ${Math.max(width, 0.1)} ${Math.max(height, 0.1)}`}
      preserveAspectRatio="none"
      fill={fill}
      stroke={color}
      strokeWidth={strokeWidth}
      strokeLinejoin="round"
      aria-hidden
    >
      {graphic}
    </svg>
  );
}

/**
 * A class's, entity's or fragment's text: the name in its band and each
 * section's lines; a fragment's operator in its tag, the first operand's
 * guard beside it and each later guard below its divider.
 */
export function BoardCompartments({
  shape,
  properties,
  width,
}: {
  shape: string | undefined;
  properties: Pick<BoardProperties, "title" | "sections" | "dividers">;
  width: number;
}) {
  const sections = properties.sections ?? [];
  if (shape === "fragment") {
    const tag = boardFragmentTagWidth(properties.title ?? "", width);
    const dividers = properties.dividers ?? [];
    return (
      <div className="absolute inset-0 overflow-hidden text-[length:13px] leading-[18px] pointer-events-none wb-compartments-fragment">
        <span
          className="absolute top-0 left-0 h-[22px] p-[0_12px_0_8px] leading-[22px] font-[600] whitespace-nowrap overflow-hidden"
          style={{ width: tag }}
        >
          {properties.title}
        </span>
        {sections.map((section, index) =>
          index > 0 && index - 1 >= dividers.length ? null : (
            <span
              key={index}
              className="absolute right-[8px] whitespace-pre overflow-hidden text-ellipsis"
              style={
                index === 0
                  ? { left: tag + 8, top: 4 }
                  : { left: 8, top: dividers[index - 1] + 4 }
              }
            >
              {section}
            </span>
          ),
        )}
      </div>
    );
  }
  let at = boardCompartmentTitle;
  return (
    <div className="absolute inset-0 overflow-hidden text-[length:13px] leading-[18px] pointer-events-none">
      <strong
        className="absolute top-0 left-[8px] right-[8px] leading-[30px] font-[600] text-center whitespace-nowrap overflow-hidden text-ellipsis"
        style={{ height: boardCompartmentTitle }}
      >
        {properties.title}
      </strong>
      {sections.map((section, index) => {
        const top = at + boardCompartmentMargin / 2;
        at += boardCompartmentHeight(section);
        return (
          <span
            key={index}
            className="absolute right-[8px] whitespace-pre overflow-hidden text-ellipsis"
            style={{ left: 8, top }}
          >
            {section}
          </span>
        );
      })}
    </div>
  );
}

/**
 * The properties a kind starts with when placed: a class's two empty
 * sections, an entity's tinted header over a white body, a fragment's
 * operator and guard.
 */
export function boardShapeDefaults(
  shape: BoardShapeType,
  fill: string,
): Partial<BoardProperties> {
  const clear = fill === "#00000000";
  switch (shape) {
    case "class":
      return {
        title: "Class",
        sections: ["", ""],
        ...(clear ? { fill: "#ffffff" } : {}),
      };
    case "entity":
      return {
        title: "table",
        sections: [""],
        ...(clear ? { fill: "#dff0fc" } : {}),
      };
    case "fragment":
      return { title: "alt", sections: ["[condition]"], dividers: [] };
  }
  return {};
}

/** The height a class's or entity's name and sections need. */
export function boardCompartmentsHeight(sections: string[]) {
  return sections.reduce(
    (height, section) => height + boardCompartmentHeight(section),
    boardCompartmentTitle,
  );
}

/** The width a class's or entity's widest line needs, 160 to 360. */
export function boardCompartmentsWidth(title: string, sections: string[]) {
  let width = boardTextWidth(title, 600);
  for (const section of sections)
    for (const line of section.split("\n"))
      width = Math.max(width, boardTextWidth(line));
  return Math.ceil(Math.min(360, Math.max(160, width + 20)));
}

/** Whether a kind's interior lets clicks through to what is behind it. */
export function isHollowShape(shape: string | undefined) {
  return shape === "lifeline" || shape === "fragment";
}
