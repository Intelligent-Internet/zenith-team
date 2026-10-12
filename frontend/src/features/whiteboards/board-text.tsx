import type { CSSProperties, ReactNode } from "react";
import type { BoardItem } from "./board-api";

const backgrounds = [
  { name: "None", value: "#00000000" },
  { name: "Yellow", value: "#fff2a8" },
  { name: "Blue", value: "#dff0fc" },
  { name: "Pink", value: "#fce1eb" },
  { name: "Green", value: "#dff3e3" },
];

export function textBackground(item: BoardItem): string {
  return (
    item.properties.fill ?? (item.type === "sticky" ? "#fff2a8" : "#00000000")
  );
}

export function hasTextBackground(fill: string): boolean {
  return fill !== "transparent" && !/^#[\da-f]{6}00$/i.test(fill);
}

export function BoardTextOptions({
  background,
  onBackgroundChange,
  disabled = false,
  actions,
}: {
  background: string;
  onBackgroundChange: (value: string) => void;
  disabled?: boolean;
  /** Selection actions (lock, delete) for a selected note. */
  actions?: ReactNode;
}) {
  const colors = backgrounds.some(
    ({ value }) => value === background.toLowerCase(),
  )
    ? backgrounds
    : [...backgrounds, { name: "Current", value: background.toLowerCase() }];
  return (
    <div
      className="wb-text-options"
      role="toolbar"
      aria-label="Text background"
    >
      <span>Background</span>
      <div
        className="wb-color-control__swatches"
        role="group"
        aria-label="Background color"
      >
        {colors.map(({ name, value }) => (
          <button
            key={value}
            type="button"
            className="wb-color-swatch"
            style={{ "--wb-swatch-color": value } as CSSProperties}
            data-transparent={!hasTextBackground(value) || undefined}
            aria-label={`Background: ${name}`}
            title={name === "None" ? "No background" : name}
            aria-pressed={background.toLowerCase() === value}
            disabled={disabled}
            onClick={() => onBackgroundChange(value)}
          />
        ))}
      </div>
      {actions && <div className="wb-selection-actions">{actions}</div>}
    </div>
  );
}
