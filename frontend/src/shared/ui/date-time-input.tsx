import type { ComponentProps } from "react";

import { cx } from "@/shared/lib/cx";
import "./date-time-input.css";

type DateTimeInputProps = Omit<ComponentProps<"input">, "type"> & {
  type: "date" | "datetime-local" | "time";
};

export function DateTimeInput({ className, ...props }: DateTimeInputProps) {
  return (
    <input
      {...props}
      className={cx(
        "workspace-date-time-input [color-scheme:inherit]",
        className,
      )}
    />
  );
}
