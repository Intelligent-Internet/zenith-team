import type { CSSProperties, ReactNode } from "react";
import type { BoardItem } from "./board-api";
import type { BoardTool } from "./board-toolbar";
import { boardShapeGroups, type BoardShapeType } from "./board-shape";
import { BoardPenTray } from "./board-pen-tray";
import { BoardColorPicker } from "./board-color-picker";
import { BoardStrokeWidths } from "./board-stroke-widths";
import { BoardTextOptions } from "./board-text";

const toolLabels: Record<BoardTool, string> = {
  select: "Selection",
  move: "Selection",
  text: "Text",
  shape: "Shape",
  frame: "Frame",
  pen: "Pen",
  highlighter: "Highlighter",
};
const inkColors = [
  { name: "Graphite", value: "#25252d" },
  { name: "Blue", value: "#4c6fff" },
  { name: "Green", value: "#2d9470" },
  { name: "Yellow", value: "#facc15" },
  { name: "Rose", value: "#e85d75" },
];
const fillColors = [
  { name: "None", value: "#00000000" },
  { name: "Yellow", value: "#fff2a8" },
  { name: "Blue", value: "#dff0fc" },
  { name: "Pink", value: "#fce1eb" },
];

export function BoardStyleToolbar({
  tool,
  selectedItemCount,
  selectedConnectorCount,
  singleTextSelected,
  selectedItemType,
  styleDisabled,
  textBackground,
  onTextBackgroundChange,
  textBackgroundDisabled,
  color,
  brushColors,
  fill,
  width,
  shape,
  onColorChange,
  onFillChange,
  onColorPreview,
  onFillPreview,
  onWidthChange,
  onShapeChange,
  onToolChange,
  children,
}: {
  tool: BoardTool;
  selectedItemCount: number;
  selectedConnectorCount: number;
  singleTextSelected: boolean;
  selectedItemType?: BoardItem["type"];
  styleDisabled: boolean;
  textBackground: string;
  onTextBackgroundChange: (value: string) => void;
  textBackgroundDisabled: boolean;
  color: string;
  brushColors: Record<"pen" | "highlighter", string>;
  fill: string;
  width: number;
  shape: string;
  onColorChange: (value: string) => void;
  onFillChange: (value: string) => void;
  onColorPreview?: (value: string) => void;
  onFillPreview?: (value: string) => void;
  onWidthChange: (value: number) => void;
  onShapeChange: (value: BoardShapeType) => void;
  onToolChange: (value: BoardTool) => void;
  children?: ReactNode;
}) {
  const selection =
    (tool === "select" || tool === "move") &&
    selectedItemCount + selectedConnectorCount > 0;
  if (tool === "text" || (selection && singleTextSelected))
    return (
      <BoardTextOptions
        background={textBackground}
        onBackgroundChange={onTextBackgroundChange}
        disabled={textBackgroundDisabled}
        actions={tool === "text" ? undefined : children}
      />
    );
  if (tool === "pen" || tool === "highlighter")
    return (
      <BoardPenTray
        tool={tool}
        color={color}
        brushColors={brushColors}
        width={width}
        onToolChange={onToolChange}
        onColorChange={onColorChange}
        onWidthChange={onWidthChange}
      />
    );
  const hasStyle = ["shape", "frame"].includes(tool);
  if (!selection && !hasStyle) return null;

  const selectedItems = selection && selectedItemCount > 0;
  const showColor = hasStyle || selectedItems;
  const showFill = hasStyle || selectedItems;
  const colorOnlySelection =
    selection &&
    selectedItemCount === 1 &&
    selectedConnectorCount === 0 &&
    (selectedItemType === "shape" || selectedItemType === "frame");
  const showWidth = selectedItems && !colorOnlySelection;
  const label = toolLabels[tool];
  const colorLabel = hasStyle || colorOnlySelection ? "Stroke" : "Color";
  const fillLabel = hasStyle || colorOnlySelection ? "Background" : "Fill";
  const widthOptions = [...new Set([1, 2, 3, 5, 8, 12, 20, width])].sort(
    (a, b) => a - b,
  );

  return (
    <div
      className="wb-style-toolbar"
      role="toolbar"
      aria-label={`${label} options`}
    >
      <div className="wb-style-context">
        {selection
          ? `${selectedItemCount + selectedConnectorCount} selected`
          : label}
      </div>
      {(showColor || showFill) && (
        <div className="wb-style-controls">
          {tool === "shape" && (
            <div
              className="wb-shape-picker"
              role="group"
              aria-label="Shape type"
            >
              {boardShapeGroups.map((group) => (
                <div
                  key={group.label}
                  className="wb-shape-group"
                  role="group"
                  aria-label={group.label}
                >
                  <span className="wb-shape-group__label">{group.label}</span>
                  <div className="wb-shape-group__grid">
                    {group.shapes.map(({ value, label, Icon }) => (
                      <button
                        key={value}
                        type="button"
                        aria-label={label}
                        aria-pressed={shape === value}
                        title={label}
                        onClick={() => onShapeChange(value)}
                      >
                        <Icon size={24} aria-hidden />
                      </button>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}
          {showColor && (
            <ColorControl
              label={colorLabel}
              accessibleLabel="Text and stroke color"
              value={color}
              presets={inkColors}
              onChange={onColorChange}
              onPreview={onColorPreview}
              disabled={styleDisabled}
            />
          )}
          {showFill && (
            <ColorControl
              label={fillLabel}
              accessibleLabel="Fill color"
              value={fill}
              presets={fillColors}
              onChange={onFillChange}
              onPreview={onFillPreview}
              disabled={styleDisabled}
            />
          )}
        </div>
      )}
      {showWidth && (
        <div className="wb-style-width">
          <BoardStrokeWidths
            width={width}
            widths={widthOptions}
            onChange={onWidthChange}
            disabled={styleDisabled}
          />
        </div>
      )}
      {/* A single shape or frame hides only the width control; lock and
          delete stay available for it. */}
      {selection && <div className="wb-selection-actions">{children}</div>}
    </div>
  );
}

function ColorControl({
  label,
  accessibleLabel,
  value,
  presets,
  onChange,
  onPreview,
  disabled,
}: {
  label: string;
  accessibleLabel: string;
  value: string;
  presets: typeof inkColors;
  onChange: (value: string) => void;
  onPreview?: (value: string) => void;
  disabled: boolean;
}) {
  return (
    <div className="wb-color-control" role="group" aria-label={accessibleLabel}>
      <span>{label}</span>
      <div className="wb-color-control__swatches">
        {presets.map((preset) => (
          <button
            key={preset.value}
            type="button"
            className="wb-color-swatch"
            data-transparent={preset.value === "#00000000" || undefined}
            aria-label={`${label}: ${preset.name}`}
            aria-pressed={value.toLowerCase() === preset.value}
            title={`${label}: ${preset.name}`}
            disabled={disabled}
            style={
              {
                "--wb-swatch-color": preset.value,
              } as CSSProperties
            }
            onClick={() => onChange(preset.value)}
          />
        ))}
        <BoardColorPicker
          label={label === "Color" ? "Color" : `${label} color`}
          color={value.slice(0, 7)}
          selected={
            !presets.some((preset) => preset.value === value.toLowerCase())
          }
          onChange={onPreview ?? onChange}
          onCommit={onPreview ? onChange : undefined}
          disabled={disabled}
        />
      </div>
    </div>
  );
}
