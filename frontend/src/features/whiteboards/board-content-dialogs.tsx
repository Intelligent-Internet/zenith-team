import { useState } from "react";
import type { BoardCommand, BoardEdge } from "./board-api";
import type {
  BoardLineType,
  BoardMarkerType,
  BoardRouteType,
} from "./board-diagram";

export type BoardConnectorChange = Pick<
  BoardCommand,
  | "label"
  | "relation"
  | "line"
  | "startMarker"
  | "endMarker"
  | "route"
  | "anchorY"
  | "sourceLabel"
  | "targetLabel"
>;

type Preset = {
  label: string;
  line: BoardLineType;
  start: BoardMarkerType;
  end: BoardMarkerType;
  relation?: string;
  stereotype?: string;
};

// The connector styles Agents name too (see the server's connector presets).
const presets: Record<string, Preset> = {
  arrow: { label: "Arrow", line: "solid", start: "none", end: "arrow" },
  line: { label: "Line", line: "solid", start: "none", end: "none" },
  both: {
    label: "Arrows both ways",
    line: "solid",
    start: "arrow",
    end: "arrow",
  },
  dashed: {
    label: "Dashed arrow",
    line: "dashed",
    start: "none",
    end: "arrow",
  },
  inherits: {
    label: "Inherits",
    line: "solid",
    start: "none",
    end: "triangle",
    relation: "inherits",
  },
  implements: {
    label: "Implements",
    line: "dashed",
    start: "none",
    end: "triangle",
    relation: "implements",
  },
  composes: {
    label: "Composition",
    line: "solid",
    start: "diamond_filled",
    end: "none",
    relation: "composes",
  },
  aggregates: {
    label: "Aggregation",
    line: "solid",
    start: "diamond",
    end: "none",
    relation: "aggregates",
  },
  depends: {
    label: "Depends on",
    line: "dashed",
    start: "none",
    end: "open",
    relation: "depends_on",
  },
  associates: {
    label: "Association",
    line: "solid",
    start: "none",
    end: "open",
    relation: "associates",
  },
  includes: {
    label: "«include»",
    line: "dashed",
    start: "none",
    end: "open",
    relation: "includes",
    stereotype: "«include»",
  },
  extends: {
    label: "«extend»",
    line: "dashed",
    start: "none",
    end: "open",
    relation: "extends",
    stereotype: "«extend»",
  },
  async: { label: "Async message", line: "solid", start: "none", end: "open" },
  reply: { label: "Reply message", line: "dashed", start: "none", end: "open" },
};

const markers: [BoardMarkerType, string][] = [
  ["none", "None"],
  ["arrow", "Arrow"],
  ["open", "Open arrow"],
  ["triangle", "Hollow triangle"],
  ["diamond", "Hollow diamond"],
  ["diamond_filled", "Filled diamond"],
  ["one", "Exactly one"],
  ["zero_one", "Zero or one"],
  ["many", "One or more"],
  ["zero_many", "Zero or more"],
];

const relations: [string, string][] = [
  ["related", "Related"],
  ["supports", "Supports"],
  ["contradicts", "Contradicts"],
  ["depends_on", "Depends on"],
  ["leads_to", "Leads to"],
  ["inherits", "Inherits"],
  ["implements", "Implements"],
  ["composes", "Composes"],
  ["aggregates", "Aggregates"],
  ["associates", "Associates"],
  ["includes", "Includes"],
  ["extends", "Extends"],
];

export function BoardConnectorDialog({
  edge,
  onSave,
  onClose,
}: {
  edge: BoardEdge;
  onSave: (change: BoardConnectorChange) => void;
  onClose: () => void;
}) {
  const self = edge.sourceId === edge.targetId;
  const [label, setLabel] = useState(edge.label);
  const [relation, setRelation] = useState(edge.relation || "related");
  const [line, setLine] = useState<BoardLineType>(edge.line);
  const [start, setStart] = useState<BoardMarkerType>(edge.startMarker);
  const [end, setEnd] = useState<BoardMarkerType>(edge.endMarker);
  const [route, setRoute] = useState<BoardRouteType>(edge.route);
  const [row, setRow] = useState(String(Math.round(edge.anchorY ?? 88)));
  const [sourceLabel, setSourceLabel] = useState(edge.sourceLabel);
  const [targetLabel, setTargetLabel] = useState(edge.targetLabel);
  // Several styles draw alike («include», «extend», depends on and a reply
  // are all dashed open arrows), so the one picked last stays shown while
  // its lines and ends do; otherwise the one naming this relationship, then
  // a message style exactly on a message, then any.
  const [picked, setPicked] = useState("");
  const draws = (key: string) =>
    presets[key].line === line &&
    presets[key].start === start &&
    presets[key].end === end;
  const matching = Object.keys(presets).filter(draws);
  const message = (key: string) => key === "async" || key === "reply";
  const preset =
    (picked && draws(picked) ? picked : undefined) ??
    matching.find((key) => presets[key].relation === relation) ??
    matching.find((key) => message(key) === (route === "message")) ??
    matching[0] ??
    "";
  const applyPreset = (key: string) => {
    const chosen = presets[key];
    if (!chosen) return;
    setPicked(key);
    setLine(chosen.line);
    setStart(chosen.start);
    setEnd(chosen.end);
    if (chosen.relation) setRelation(chosen.relation);
    if (chosen.stereotype && !label.trim()) setLabel(chosen.stereotype);
  };
  return (
    <div className="fixed inset-0 flex items-center justify-center [background:#0006] z-[150] p-[24px]">
      <form
        className="wb-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Edit connector"
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            onClose();
          }
        }}
        onSubmit={(event) => {
          event.preventDefault();
          const anchor = Number(row);
          onSave({
            label,
            relation,
            line,
            startMarker: start,
            endMarker: end,
            route,
            ...(route === "message"
              ? { anchorY: Number.isFinite(anchor) ? Math.max(0, anchor) : 88 }
              : {}),
            sourceLabel,
            targetLabel,
          });
        }}
      >
        <h2>Edit connector</h2>
        <label>
          Label
          <input
            autoFocus
            maxLength={500}
            value={label}
            onChange={(event) => setLabel(event.target.value)}
          />
        </label>
        <label>
          Style
          <select
            value={preset}
            onChange={(event) => applyPreset(event.target.value)}
          >
            {preset ? null : <option value="">Custom</option>}
            {Object.entries(presets).map(([key, p]) => (
              <option key={key} value={key}>
                {p.label}
              </option>
            ))}
          </select>
        </label>
        <label>
          Relationship
          <select
            value={relation}
            onChange={(event) => setRelation(event.target.value)}
          >
            {relations.map(([value, name]) => (
              <option key={value} value={value}>
                {name}
              </option>
            ))}
          </select>
        </label>
        <details className="wb-connector-advanced">
          <summary>Advanced</summary>
          <div className="grid grid-cols-[repeat(2,_minmax(0,_1fr))] gap-[12px] mt-[12px]">
            <label>
              Line
              <select
                value={line}
                onChange={(event) =>
                  setLine(event.target.value as BoardLineType)
                }
              >
                <option value="solid">Solid</option>
                <option value="dashed">Dashed</option>
              </select>
            </label>
            <label>
              Path
              <select
                value={route}
                disabled={self}
                onChange={(event) =>
                  setRoute(event.target.value as BoardRouteType)
                }
              >
                <option value="flow">Flow (right to left)</option>
                <option value="straight">Straight</option>
                <option value="elbow">Elbow</option>
                <option value="message">Sequence message</option>
              </select>
            </label>
            <label>
              Start
              <select
                value={start}
                onChange={(event) =>
                  setStart(event.target.value as BoardMarkerType)
                }
              >
                {markers.map(([value, name]) => (
                  <option key={value} value={value}>
                    {name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              End
              <select
                value={end}
                onChange={(event) =>
                  setEnd(event.target.value as BoardMarkerType)
                }
              >
                {markers.map(([value, name]) => (
                  <option key={value} value={value}>
                    {name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Start label
              <input
                maxLength={60}
                value={sourceLabel}
                placeholder="1"
                onChange={(event) => setSourceLabel(event.target.value)}
              />
            </label>
            <label>
              End label
              <input
                maxLength={60}
                value={targetLabel}
                placeholder="0..*"
                onChange={(event) => setTargetLabel(event.target.value)}
              />
            </label>
            {route === "message" ? (
              <label>
                Row below the lifeline's top
                <input
                  type="number"
                  min={0}
                  step={1}
                  value={row}
                  onChange={(event) => setRow(event.target.value)}
                />
              </label>
            ) : null}
          </div>
        </details>
        <p>
          The relationship is explicit Board context; proximity and color do not
          imply a relationship.
        </p>
        <footer>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="wb-primary">Save connector</button>
        </footer>
      </form>
    </div>
  );
}
