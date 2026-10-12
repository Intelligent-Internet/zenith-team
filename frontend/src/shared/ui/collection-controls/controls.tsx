import { useEffect, useId, useState } from "react";

import { cx } from "@/shared/lib/cx";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { Icon } from "@/shared/ui/icon";
import { LayoutGrid, List } from "@/shared/ui/icons";

import { GlassButton } from "./glass-primitives";
import "./controls.css";

export type CollectionViewMode = "grid" | "list";

const collectionDropdownOpenEvent = "boardly:collection-dropdown-open";

export function SearchControl({
  ariaLabel,
  onChange,
  placeholder,
  value,
  wide,
}: {
  ariaLabel: string;
  onChange?: (value: string) => void;
  placeholder: string;
  value?: string;
  wide?: boolean;
}) {
  const inputId = useId();

  return (
    <label
      htmlFor={inputId}
      className={cx(
        "collection-search-control flex h-12 items-center gap-3 rounded-xl bg-white/10 px-4 max-[980px]:min-w-[240px] max-[980px]:flex-1 max-[640px]:w-full",
        wide ? "w-[360px]" : "w-[248px]",
      )}
    >
      <span className="sr-only">{ariaLabel}</span>
      <Icon name="search" className="size-5 text-white/50" />
      <input
        id={inputId}
        className="min-w-0 flex-1 bg-transparent text-sm text-white outline-none placeholder:text-white/30"
        placeholder={placeholder}
        type="search"
        value={value}
        onChange={(event) => onChange?.(event.target.value)}
      />
    </label>
  );
}

export function ViewSwitch({
  value,
  onChange,
  ariaLabel = "View",
}: {
  value: CollectionViewMode;
  onChange: (value: CollectionViewMode) => void;
  ariaLabel?: string;
}) {
  const options = [
    { icon: LayoutGrid, label: "Grid view", value: "grid" },
    { icon: List, label: "List view", value: "list" },
  ] as const;

  return (
    <div className="collection-view-switch" role="group" aria-label={ariaLabel}>
      {options.map(({ icon: ViewIcon, ...option }) => (
        <button
          key={option.value}
          type="button"
          aria-label={option.label}
          title={option.label}
          aria-pressed={value === option.value}
          onClick={() => onChange(option.value)}
        >
          <ViewIcon size={18} aria-hidden />
        </button>
      ))}
    </div>
  );
}

export function DropdownSelectButton<TValue extends string>({
  ariaLabel,
  className,
  onChange,
  options,
  prefix,
  value,
}: {
  ariaLabel: string;
  className?: string;
  onChange: (value: TValue) => void;
  options: ReadonlyArray<{ label: string; value: TValue }>;
  prefix?: string;
  value: TValue;
}) {
  const dropdownId = useId();
  const [open, setOpen] = useState(false);
  const selectedOption =
    options.find((option) => option.value === value) ?? options[0];

  useEffect(() => {
    const handleOtherDropdownOpen = (event: Event) => {
      const openedDropdownId = (event as CustomEvent<string>).detail;

      if (openedDropdownId !== dropdownId) {
        setOpen(false);
      }
    };

    window.addEventListener(
      collectionDropdownOpenEvent,
      handleOtherDropdownOpen,
    );

    return () => {
      window.removeEventListener(
        collectionDropdownOpenEvent,
        handleOtherDropdownOpen,
      );
    };
  }, [dropdownId]);

  const handleOpenChange = (nextOpen: boolean) => {
    if (nextOpen) {
      window.dispatchEvent(
        new CustomEvent(collectionDropdownOpenEvent, {
          detail: dropdownId,
        }),
      );
    }

    setOpen(nextOpen);
  };
  const selectOption = (nextValue: string) => {
    const option = options.find((candidate) => candidate.value === nextValue);
    if (!option) return;
    onChange(option.value);
    setOpen(false);
  };

  return (
    <DropdownMenu open={open} onOpenChange={handleOpenChange}>
      <DropdownMenuTrigger asChild>
        <GlassButton
          aria-label={`${ariaLabel}: ${selectedOption.label}`}
          className={cx(
            "min-w-[132px] justify-between max-[980px]:min-w-[150px] max-[980px]:flex-1",
            className,
          )}
        >
          <span>
            {prefix ? `${prefix} ` : null}
            {selectedOption.label}
          </span>
          <Icon name="arrow-down" className="size-4 shrink-0" />
        </GlassButton>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-44" sideOffset={6}>
        <DropdownMenuRadioGroup
          aria-label={ariaLabel}
          value={selectedOption.value}
          onValueChange={selectOption}
        >
          {options.map((option) => (
            <DropdownMenuRadioItem
              key={option.value}
              className="min-h-8 gap-3 rounded-md px-2.5 text-[13px] font-medium text-[var(--workspace-native-text,var(--text-primary))] focus:bg-[var(--workspace-hover-bg,var(--state-hover-bg))] focus:text-[var(--workspace-native-text,var(--text-primary))] [&>span:first-child]:hidden"
              value={option.value}
            >
              <span className="min-w-0 flex-1">{option.label}</span>
              {option.value === selectedOption.value ? (
                <Icon
                  name="tick"
                  className="size-4 text-[var(--workspace-native-accent)]"
                />
              ) : null}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
