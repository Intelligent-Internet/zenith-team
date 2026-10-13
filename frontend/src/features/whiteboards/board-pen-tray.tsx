import type { CSSProperties } from "react";
import PenIllustration from "@/assets/illustrations/drawing-pen.svg?react";
import HighlighterIllustration from "@/assets/illustrations/drawing-highlighter.svg?react";
import { Highlighter, Pencil } from "@/shared/ui/icons";
import { BoardColorPicker } from "./board-color-picker";
import { BoardStrokeWidths } from "./board-stroke-widths";

const brushes = [
  {
    id: "pen",
    label: "Pen",
    shortcut: "P",
    Icon: Pencil,
    Illustration: PenIllustration,
  },
  {
    id: "highlighter",
    label: "Highlighter",
    shortcut: "H",
    Icon: Highlighter,
    Illustration: HighlighterIllustration,
  },
] as const;
const colors = [
  { name: "Graphite", value: "#25252d" },
  { name: "Blue", value: "#4c6fff" },
  { name: "Green", value: "#2d9470" },
  { name: "Yellow", value: "#facc15" },
  { name: "Rose", value: "#e85d75" },
];

export function BoardPenTray({
  tool,
  color,
  brushColors,
  width,
  onToolChange,
  onColorChange,
  onWidthChange,
}: {
  tool: "pen" | "highlighter";
  color: string;
  brushColors: Record<"pen" | "highlighter", string>;
  width: number;
  onToolChange: (tool: "pen" | "highlighter") => void;
  onColorChange: (color: string) => void;
  onWidthChange: (width: number) => void;
}) {
  const widths =
    tool === "highlighter" ? [12, 16, 20, 24, 32] : [1, 2, 3, 5, 8, 12, 20];
  const customColor = !colors.some(
    (preset) => preset.value === color.toLowerCase(),
  );

  return (
    <div className="wb-pen-tray" role="group" aria-label="Drawing tools">
      <div className="wb-pen-tray__main">
        <div
          className="flex gap-[4px] flex-1 min-w-0"
          role="group"
          aria-label="Drawing tool"
        >
          {brushes.map(({ id, label, shortcut, Icon, Illustration }) => (
            <button
              key={id}
              type="button"
              className="wb-pen-tray__brush"
              aria-label={`${label} (${shortcut})`}
              aria-pressed={tool === id}
              title={`${label} (${shortcut})`}
              onClick={() => onToolChange(id)}
            >
              <span className="wb-pen-tray__instrument" aria-hidden="true">
                <Illustration style={{ color: brushColors[id] }} />
              </span>
              <span className="wb-pen-tray__brush-label">
                <Icon size={14} aria-hidden />
                {label}
              </span>
            </button>
          ))}
        </div>
        <div
          className="wb-pen-tray__colors"
          role="group"
          aria-label="Ink color"
        >
          {colors.map((preset) => (
            <button
              key={preset.value}
              type="button"
              className="wb-color-swatch"
              aria-label={`Ink: ${preset.name}`}
              aria-pressed={color.toLowerCase() === preset.value}
              title={preset.name}
              style={{ "--wb-swatch-color": preset.value } as CSSProperties}
              onClick={() => onColorChange(preset.value)}
            />
          ))}
          <BoardColorPicker
            color={color}
            selected={customColor}
            onChange={onColorChange}
          />
        </div>
      </div>
      <div className="flex items-center p-[10px_16px] [border-top:1px_solid_var(--wb-line)]">
        <BoardStrokeWidths
          width={width}
          widths={widths}
          highlighter={tool === "highlighter"}
          onChange={onWidthChange}
        />
      </div>
    </div>
  );
}
