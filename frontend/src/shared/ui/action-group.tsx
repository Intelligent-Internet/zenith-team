import type { ReactNode } from "react";
import { cx } from "@/shared/lib/cx";

type ActionGroupProps = {
  children: ReactNode;
  className?: string;
};

export function ActionGroup({ children, className }: ActionGroupProps) {
  return <div className={cx("flex flex-wrap gap-2", className)}>{children}</div>;
}
