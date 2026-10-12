import { useId, useRef } from "react";
import * as DropdownMenuPrimitive from "@radix-ui/react-dropdown-menu";
import { Check, ChevronDown } from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";

export function ArtifactAccessMenu({
  value,
  options,
  label,
  disabled,
  onChange,
  onRemove,
  removeLabel,
  side,
  align = "end",
}: {
  value: string;
  options: ReadonlyArray<{
    value: string;
    label: string;
    description?: string;
  }>;
  label: string;
  disabled?: boolean;
  onChange: (value: string) => void;
  onRemove?: (trigger: HTMLButtonElement | null) => void;
  removeLabel?: string;
  side?: "top" | "bottom";
  align?: "start" | "end";
}) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  const openingConfirmation = useRef(false);
  const descriptionId = useId();

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          ref={triggerRef}
          type="button"
          className="artifact-access-menu-trigger"
          aria-label={label}
          disabled={disabled}
        >
          <span>{options.find((option) => option.value === value)?.label}</span>
          <ChevronDown size={14} aria-hidden />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        className="artifact-access-menu z-[160]"
        align={align}
        side={side}
        collisionPadding={12}
        onCloseAutoFocus={(event) => {
          if (openingConfirmation.current) {
            event.preventDefault();
            openingConfirmation.current = false;
          }
        }}
      >
        <DropdownMenuRadioGroup value={value} onValueChange={onChange}>
          {options.map((option) => (
            <DropdownMenuPrimitive.RadioItem
              key={option.value}
              value={option.value}
              className="artifact-access-menu-option"
              aria-label={option.label}
              aria-describedby={
                option.description
                  ? `${descriptionId}-${option.value}`
                  : undefined
              }
            >
              <span>
                <span>{option.label}</span>
                {option.description ? (
                  <small id={`${descriptionId}-${option.value}`}>
                    {option.description}
                  </small>
                ) : null}
              </span>
              <DropdownMenuPrimitive.ItemIndicator>
                <Check size={14} aria-hidden />
              </DropdownMenuPrimitive.ItemIndicator>
            </DropdownMenuPrimitive.RadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {onRemove ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className="artifact-access-menu-remove"
              variant="destructive"
              onSelect={() => {
                openingConfirmation.current = true;
                onRemove(triggerRef.current);
              }}
            >
              {removeLabel}
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
