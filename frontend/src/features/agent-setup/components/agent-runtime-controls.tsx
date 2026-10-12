import { ChevronDown } from "@/shared/ui/icons";
import type { SelectHTMLAttributes } from "react";
import "@/features/agent-setup/styles/agent-runtime-controls.css";

export function AgentRuntimeSelect({
  children,
  ...props
}: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="agent-runtime-select">
      <select {...props}>{children}</select>
      <ChevronDown aria-hidden="true" className="agent-runtime-select__icon" />
    </span>
  );
}
