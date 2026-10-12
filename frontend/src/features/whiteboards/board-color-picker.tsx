import * as Popover from "@radix-ui/react-popover";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type PointerEvent,
} from "react";
import { X } from "@/shared/ui/icons";
import "./board-color-picker.css";

type HSV = { hue: number; saturation: number; brightness: number };

function hexToHSV(hex: string, previousHue = 0): HSV {
  const [red, green, blue] = [1, 3, 5].map(
    (offset) => Number.parseInt(hex.slice(offset, offset + 2), 16) / 255,
  );
  const max = Math.max(red, green, blue);
  const delta = max - Math.min(red, green, blue);
  const sector =
    delta === 0
      ? previousHue / 60
      : max === red
        ? (green - blue) / delta
        : max === green
          ? (blue - red) / delta + 2
          : (red - green) / delta + 4;
  return {
    hue: (sector * 60 + 360) % 360,
    saturation: max === 0 ? 0 : delta / max,
    brightness: max,
  };
}

function hsvToHex({ hue, saturation, brightness }: HSV): string {
  const channel = (offset: number) => {
    const sector = (offset + hue / 60) % 6;
    const value =
      brightness *
      (1 - saturation * Math.max(0, Math.min(sector, 4 - sector, 1)));
    return Math.round(value * 255)
      .toString(16)
      .padStart(2, "0");
  };
  return `#${channel(5)}${channel(3)}${channel(1)}`;
}

export function BoardColorPicker({
  color,
  selected,
  onChange,
  onCommit,
  disabled = false,
  label = "Ink color",
}: {
  color: string;
  selected: boolean;
  onChange: (color: string) => void;
  onCommit?: (color: string) => void;
  disabled?: boolean;
  label?: string;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  const [container, setContainer] = useState<HTMLElement>();
  return (
    <Popover.Root
      onOpenChange={(open) => {
        if (open)
          setContainer(
            trigger.current?.closest<HTMLElement>(".wb-editor") ?? undefined,
          );
      }}
    >
      <Popover.Trigger asChild>
        <button
          ref={trigger}
          type="button"
          className="wb-custom-color"
          data-selected={selected || undefined}
          disabled={disabled}
          aria-label={`Custom ${label.toLowerCase()}`}
          title={`Choose custom ${label.toLowerCase()}`}
        />
      </Popover.Trigger>
      <Popover.Portal container={container}>
        <Popover.Content
          className="wb-color-picker nodrag nopan nowheel"
          aria-label={`Custom ${label.toLowerCase()}`}
          side="top"
          align="end"
          sideOffset={14}
          collisionPadding={16}
          collisionBoundary={container}
          onEscapeKeyDown={(event) => event.stopPropagation()}
        >
          <ColorPickerControls
            color={color}
            onChange={onChange}
            onCommit={onCommit}
            disabled={disabled}
            label={label}
          />
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function ColorPickerControls({
  color,
  onChange,
  onCommit,
  disabled,
  label,
}: {
  color: string;
  onChange: (color: string) => void;
  onCommit?: (color: string) => void;
  disabled: boolean;
  label: string;
}) {
  // Keep hue and saturation while crossing black or gray; hex alone loses them.
  const [hsv, setHSV] = useState(() => hexToHSV(color));
  // Preview continuously, but create one scene edit when the gesture ends.
  const pendingColor = useRef<string | undefined>(undefined);
  const commit = useRef(onCommit);
  commit.current = onCommit;
  const finishChange = useCallback(() => {
    const next = pendingColor.current;
    pendingColor.current = undefined;
    if (next !== undefined) commit.current?.(next);
  }, []);
  useEffect(() => finishChange, [finishChange]);
  const change = (next: string) => {
    pendingColor.current = next;
    onChange(next);
  };
  const updateHSV = (next: HSV) => {
    setHSV(next);
    change(hsvToHex(next));
  };
  const updateHex = (next: string) => {
    setHSV(hexToHSV(next, hsv.hue));
    change(next);
    finishChange();
  };
  const updatePosition = (event: PointerEvent<HTMLDivElement>) => {
    const bounds = event.currentTarget.getBoundingClientRect();
    updateHSV({
      ...hsv,
      saturation: Math.max(
        0,
        Math.min(1, (event.clientX - bounds.left) / bounds.width),
      ),
      brightness:
        1 -
        Math.max(0, Math.min(1, (event.clientY - bounds.top) / bounds.height)),
    });
  };
  const rgb = [1, 3, 5].map((offset) =>
    Number.parseInt(color.slice(offset, offset + 2), 16),
  );

  return (
    <>
      <div className="wb-color-picker__header">
        <span>{label}</span>
        <Popover.Close asChild>
          <button type="button" aria-label="Close color picker">
            <X size={16} aria-hidden />
          </button>
        </Popover.Close>
      </div>
      <div
        className="wb-color-picker__spectrum"
        role="slider"
        tabIndex={disabled ? -1 : 0}
        aria-disabled={disabled}
        aria-label="Saturation and brightness"
        aria-description="Use left and right arrows for saturation, up and down for brightness. Hold Shift for larger steps."
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(hsv.saturation * 100)}
        aria-valuetext={`${Math.round(hsv.saturation * 100)}% saturation, ${Math.round(hsv.brightness * 100)}% brightness`}
        style={{ backgroundColor: `hsl(${hsv.hue} 100% 50%)` }}
        onPointerDown={(event) => {
          if (disabled || event.button !== 0) return;
          event.preventDefault();
          event.currentTarget.focus();
          event.currentTarget.setPointerCapture(event.pointerId);
          updatePosition(event);
        }}
        onPointerMove={(event) => {
          if (
            !disabled &&
            event.currentTarget.hasPointerCapture(event.pointerId)
          )
            updatePosition(event);
        }}
        onPointerUp={(event) => {
          if (!event.currentTarget.hasPointerCapture(event.pointerId)) return;
          updatePosition(event);
          event.currentTarget.releasePointerCapture(event.pointerId);
          finishChange();
        }}
        onLostPointerCapture={finishChange}
        onKeyUp={finishChange}
        onBlur={finishChange}
        onKeyDown={(event) => {
          if (disabled) return;
          const step = event.shiftKey ? 0.1 : 0.01;
          const offsets: Record<string, [number, number]> = {
            ArrowLeft: [-step, 0],
            ArrowRight: [step, 0],
            ArrowDown: [0, -step],
            ArrowUp: [0, step],
          };
          const offset = offsets[event.key];
          if (!offset) return;
          event.preventDefault();
          event.stopPropagation();
          updateHSV({
            ...hsv,
            saturation: Math.max(0, Math.min(1, hsv.saturation + offset[0])),
            brightness: Math.max(0, Math.min(1, hsv.brightness + offset[1])),
          });
        }}
      >
        <span
          className="wb-color-picker__cursor"
          aria-hidden="true"
          style={{
            left: `${hsv.saturation * 100}%`,
            top: `${(1 - hsv.brightness) * 100}%`,
            background: color,
          }}
        />
      </div>
      <input
        className="wb-color-picker__hue"
        type="range"
        aria-label="Hue"
        aria-valuetext={`${Math.round(hsv.hue)} degrees`}
        min={0}
        max={360}
        step={1}
        value={hsv.hue}
        disabled={disabled}
        onPointerUp={finishChange}
        onLostPointerCapture={finishChange}
        onKeyUp={finishChange}
        onBlur={finishChange}
        onChange={(event) =>
          updateHSV({ ...hsv, hue: Number(event.target.value) })
        }
      />
      <div className="wb-color-picker__hex-row">
        <span
          className="wb-color-picker__preview"
          style={{ background: color }}
          aria-hidden="true"
        />
        <ColorField
          label="Hex"
          value={color.toUpperCase()}
          disabled={disabled}
          onCommit={(draft) => {
            const match = /^#?([\da-f]{6}|[\da-f]{3})$/i.exec(draft.trim());
            if (!match) return;
            const hex =
              match[1].length === 3
                ? [...match[1]].map((digit) => digit.repeat(2)).join("")
                : match[1];
            updateHex(`#${hex.toLowerCase()}`);
          }}
        />
      </div>
      <div className="wb-color-picker__rgb">
        {["Red", "Green", "Blue"].map((label, index) => (
          <ColorField
            key={label}
            label={label}
            value={String(rgb[index])}
            numeric
            disabled={disabled}
            onCommit={(draft) => {
              if (!/^\d{1,3}$/.test(draft.trim())) return;
              const next = [...rgb];
              next[index] = Math.min(255, Number(draft));
              updateHex(
                `#${next.map((channel) => channel.toString(16).padStart(2, "0")).join("")}`,
              );
            }}
          />
        ))}
      </div>
    </>
  );
}

function ColorField({
  label,
  value,
  numeric,
  onCommit,
  disabled,
}: {
  label: string;
  value: string;
  numeric?: boolean;
  onCommit: (value: string) => void;
  disabled: boolean;
}) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return (
    <label className="wb-color-picker__field">
      <span>{numeric ? label[0] : label}</span>
      <input
        type="text"
        inputMode={numeric ? "numeric" : "text"}
        spellCheck={false}
        autoComplete="off"
        aria-label={numeric ? `${label} channel` : "Hex color"}
        value={draft}
        disabled={disabled}
        maxLength={numeric ? 3 : 7}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => {
          setDraft(value);
          onCommit(draft);
        }}
        onKeyDown={(event) => {
          if (event.key !== "Enter") return;
          event.preventDefault();
          event.currentTarget.blur();
        }}
      />
    </label>
  );
}
