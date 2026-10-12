import type { ComponentPropsWithoutRef, Ref } from "react";

import { cx } from "@/shared/lib/cx";

type ContentPanelProps = ComponentPropsWithoutRef<"div"> & {
  contentClassName?: string;
  contentRef?: Ref<HTMLDivElement>;
};

export function ContentPanel({
  children,
  className,
  contentClassName,
  contentRef,
  style,
  ...contentProps
}: ContentPanelProps) {
  return (
    <div
      className={cx("content-panel grid min-h-0 min-w-0", className)}
      style={style}
    >
      <div
        {...contentProps}
        className={cx("relative flex min-h-0 min-w-0", contentClassName)}
        ref={contentRef}
      >
        {children}
      </div>
    </div>
  );
}
