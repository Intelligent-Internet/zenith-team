import * as Popover from "@radix-ui/react-popover";
import { useState } from "react";
import { DateTimeInput } from "@/shared/ui/date-time-input";
import { CalendarDays } from "@/shared/ui/icons";

export function TaskPropertyDate({
  label,
  value,
  disabled,
  onSave,
}: {
  label: string;
  value: string | null;
  disabled: boolean;
  onSave: (value: string | null) => Promise<boolean>;
}) {
  const [open, setOpen] = useState(false);
  const displayValue = value
    ? new Date(`${value}T00:00:00`).toLocaleDateString(undefined, {
        day: "numeric",
        month: "short",
        year: "numeric",
      })
    : "Set date";
  const save = async (next: string | null) => {
    if (next === value || (await onSave(next))) setOpen(false);
  };

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger
        type="button"
        className="task-property-date-trigger"
        aria-label={`${label}, ${displayValue}`}
        disabled={disabled}
      >
        <CalendarDays size={15} /> {displayValue}
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="task-property-date-popover z-[100] w-[250px] p-[14px] [border:1px_solid_var(--workspace-divider)] rounded-[8px] [background:var(--workspace-overlay-bg)] text-workspace-text [box-shadow:var(--workspace-overlay-shadow)] text-workspace-body"
          sideOffset={6}
          collisionPadding={12}
          align="end"
        >
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const date = new FormData(event.currentTarget).get("date");
              void save(typeof date === "string" && date ? date : null);
            }}
          >
            <label>
              {label}
              <DateTimeInput
                type="date"
                name="date"
                defaultValue={value ?? ""}
                disabled={disabled}
              />
            </label>
            <div>
              <button
                type="button"
                disabled={disabled || !value}
                onClick={() => void save(null)}
              >
                Clear
              </button>
              <button type="submit" disabled={disabled}>
                Save
              </button>
            </div>
          </form>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
