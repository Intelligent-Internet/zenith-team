import * as Select from "@radix-ui/react-select";
import * as Popover from "@radix-ui/react-popover";
import { useState, type CSSProperties } from "react";
import { Icon } from "@/shared/ui/icon";
import { ChevronDown, Check } from "@/shared/ui/icons";

const control =
  "min-w-0 rounded-workspace-control border border-workspace-divider bg-workspace-control px-3 py-2 text-sm text-workspace-text focus-visible:outline-2 focus-visible:outline-workspace-accent disabled:opacity-40";
export function InlineSelect({
  p,
  value,
  disabled,
  change,
  style,
}: {
  p: Record<string, any>;
  value: unknown;
  disabled: boolean;
  change: (value: unknown) => void;
  style: CSSProperties;
}) {
  const options = p.options ?? [];
  const index = options.findIndex((o: { value: unknown }) => o.value === value);
  return (
    <Select.Root
      value={index < 0 ? "" : String(index)}
      onValueChange={(next) =>
        change(next === "clear" ? "" : options[Number(next)]?.value)
      }
      disabled={disabled}
      required={p.required}
      name={p.name}
    >
      <Select.Trigger
        id={p.id}
        aria-label={
          p["aria-label"] ?? p.label ?? p.placeholder ?? p.name ?? "Select"
        }
        style={style}
        className={`${control} ${p.block ? "w-full" : "max-w-full"} inline-flex min-h-9 items-center justify-between gap-2`}
      >
        {p.icon && <Icon name={p.icon} size={18} />}
        <Select.Value placeholder={p.placeholder ?? "Choose"} />
        <Select.Icon>
          <ChevronDown size={16} />
        </Select.Icon>
      </Select.Trigger>
      <Select.Content
        position="popper"
        side={p.side ?? "bottom"}
        align={p.align ?? "start"}
        sideOffset={4}
        className="z-50 max-h-64 min-w-[var(--radix-select-trigger-width)] overflow-auto rounded-workspace-overlay border border-workspace-divider bg-workspace-panel p-1 text-workspace-text shadow-lg"
      >
        <Select.Viewport>
          {(p.clearable || index < 0) && (
            <Select.Item
              value="clear"
              disabled={!p.clearable}
              className="relative rounded-workspace-control py-2 pr-8 pl-3 text-sm outline-none data-[highlighted]:bg-workspace-hover data-[disabled]:opacity-40"
            >
              <Select.ItemText>{p.placeholder ?? "None"}</Select.ItemText>
            </Select.Item>
          )}
          {options.map(
            (
              option: { label: string; value: unknown; disabled?: boolean },
              i: number,
            ) => (
              <Select.Item
                key={i}
                value={String(i)}
                disabled={option.disabled}
                className="relative rounded-workspace-control py-2 pr-8 pl-3 text-sm outline-none data-[highlighted]:bg-workspace-hover data-[disabled]:opacity-40"
              >
                <Select.ItemText>{option.label}</Select.ItemText>
                <Select.ItemIndicator className="absolute right-2 top-2">
                  <Check size={16} />
                </Select.ItemIndicator>
              </Select.Item>
            ),
          )}
        </Select.Viewport>
      </Select.Content>
    </Select.Root>
  );
}

function dateString(date: Date) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}
export function InlineDatePicker({
  p,
  value,
  disabled,
  change,
  style,
}: {
  p: Record<string, any>;
  value: unknown;
  disabled: boolean;
  change: (value: unknown) => void;
  style: CSSProperties;
}) {
  const [open, setOpen] = useState(false);
  const [month, setMonth] = useState(() => new Date());
  const selected = typeof value === "string" ? value : "";
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  const count = new Date(
    month.getFullYear(),
    month.getMonth() + 1,
    0,
  ).getDate();
  const label = p["aria-label"] ?? p.label ?? p.placeholder ?? "Date";
  return (
    <Popover.Root
      open={open && !disabled}
      onOpenChange={(next) => {
        if (disabled) return;
        if (next) {
          const date = selected ? new Date(`${selected}T12:00:00`) : new Date();
          setMonth(Number.isNaN(date.getTime()) ? new Date() : date);
        }
        setOpen(next);
      }}
    >
      <Popover.Anchor asChild>
        <span
          className={`inline-flex min-w-0 items-center gap-1 ${p.block ? "w-full" : "max-w-full"}`}
        >
          <input
            id={p.id}
            type="date"
            value={selected}
            min={p.min}
            max={p.max}
            step={p.step}
            required={p.required}
            disabled={disabled}
            aria-label={label}
            name={p.name}
            onChange={(event) => change(event.target.value)}
            style={style}
            className={`${control} min-h-9 min-w-0 flex-1`}
          />
          <Popover.Trigger asChild>
            <button
              type="button"
              disabled={disabled}
              aria-label={`Choose ${label}`}
              className={control}
            >
              <Icon name="calendar-2" size={18} />
            </button>
          </Popover.Trigger>
          {p.clearable && (
            <button
              type="button"
              disabled={disabled || !selected}
              onClick={() => change("")}
              className={control}
            >
              Clear
            </button>
          )}
        </span>
      </Popover.Anchor>
      <Popover.Content
        side={p.side ?? "bottom"}
        align={p.align ?? "start"}
        sideOffset={4}
        aria-label={`${label} calendar`}
        className="z-50 w-72 max-w-[90vw] rounded-workspace-overlay border border-workspace-divider bg-workspace-panel p-3 text-workspace-text shadow-lg"
      >
        <div className="mb-3 flex items-center justify-between gap-2">
          <button
            type="button"
            aria-label="Previous month"
            className={control}
            onClick={() =>
              setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))
            }
          >
            Previous
          </button>
          <span aria-live="polite" className="text-center text-sm">
            {first.toLocaleDateString(undefined, {
              month: "long",
              year: "numeric",
            })}
          </span>
          <button
            type="button"
            aria-label="Next month"
            className={control}
            onClick={() =>
              setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))
            }
          >
            Next
          </button>
        </div>
        <div className="grid grid-cols-7 gap-1">
          {["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"].map((day) => (
            <span
              key={day}
              className="text-center text-xs text-workspace-muted"
            >
              {day}
            </span>
          ))}
          {Array.from({ length: first.getDay() }, (_, i) => (
            <span key={`blank-${i}`} />
          ))}
          {Array.from({ length: count }, (_, i) => {
            const date = dateString(
              new Date(month.getFullYear(), month.getMonth(), i + 1),
            );
            return (
              <button
                key={date}
                type="button"
                aria-label={date}
                aria-pressed={selected === date}
                disabled={
                  (!!p.min && date < p.min) || (!!p.max && date > p.max)
                }
                className="min-h-9 rounded-workspace-control text-sm hover:bg-workspace-hover focus-visible:outline-2 focus-visible:outline-workspace-accent disabled:opacity-30 aria-pressed:bg-workspace-accent-fill aria-pressed:text-workspace-accent-foreground"
                onClick={() => {
                  change(date);
                  setOpen(false);
                }}
              >
                {i + 1}
              </button>
            );
          })}
        </div>
      </Popover.Content>
    </Popover.Root>
  );
}

// Zenith masks: # digit, A letter, * alphanumeric; other characters are literals.
// Output is the displayed, formatted value, including separators.
export function maskInput(value: string, mask: string): string {
  let at = 0,
    result = "";
  const patterns: Record<string, RegExp> = {
    "#": /[0-9]/,
    A: /[a-z]/i,
    "*": /[a-z0-9]/i,
  };
  for (const token of mask.slice(0, 256)) {
    if (at >= value.length) break;
    const pattern = patterns[token];
    if (pattern) {
      while (at < value.length && !pattern.test(value[at])) at++;
      if (at >= value.length) break;
      result += value[at++];
    } else {
      result += token;
      if (value[at] === token) at++;
    }
  }
  return result;
}
