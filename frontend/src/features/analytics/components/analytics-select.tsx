import type { ComponentProps } from "react";
import { ChevronDown } from "@/shared/ui/icons";

export function AnalyticsSelect(props: ComponentProps<"select">) {
  return (
    <span className="analytics-select">
      <select {...props} />
      <ChevronDown size={16} aria-hidden="true" />
    </span>
  );
}
