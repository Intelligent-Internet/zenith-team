import {
  forwardRef,
  useState,
  type ButtonHTMLAttributes,
  type MouseEvent,
} from "react";
import { cx } from "@/shared/lib/cx";

export type SwitchProps = Omit<
  ButtonHTMLAttributes<HTMLButtonElement>,
  "onChange" | "role"
> & {
  checked?: boolean;
  defaultChecked?: boolean;
  onCheckedChange?: (checked: boolean) => void;
  thumbClassName?: string;
};

/** Shared switch with native track and thumb proportions. */
export const Switch = forwardRef<HTMLButtonElement, SwitchProps>(
  function Switch(
    {
      checked,
      className,
      defaultChecked = false,
      disabled,
      onCheckedChange,
      onClick,
      thumbClassName,
      type,
      ...props
    },
    ref,
  ) {
    const [internalChecked, setInternalChecked] = useState(defaultChecked);
    const isControlled = checked !== undefined;
    const currentChecked = checked ?? internalChecked;

    const handleClick = (event: MouseEvent<HTMLButtonElement>) => {
      onClick?.(event);

      if (event.defaultPrevented || disabled) {
        return;
      }

      const nextChecked = !currentChecked;

      if (!isControlled) {
        setInternalChecked(nextChecked);
      }

      onCheckedChange?.(nextChecked);
    };

    return (
      <button
        ref={ref}
        className={cx(
          "inline-flex h-5 w-8 shrink-0 items-center rounded-full p-0.5 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50",
          currentChecked
            ? "bg-[var(--workspace-accent-fill,#7556b9)]"
            : "bg-[var(--workspace-divider-strong,#c8c8cc)]",
          className,
        )}
        type={type ?? "button"}
        role="switch"
        aria-checked={currentChecked}
        data-state={currentChecked ? "checked" : "unchecked"}
        disabled={disabled}
        onClick={handleClick}
        {...props}
      >
        <span
          className={cx(
            "block size-4 shrink-0 rounded-full shadow-sm transition-transform duration-150 ease-out motion-reduce:transition-none",
            currentChecked
              ? "translate-x-3 bg-[var(--workspace-accent-foreground,#fff)]"
              : "translate-x-0 bg-white",
            thumbClassName,
          )}
        />
      </button>
    );
  },
);
