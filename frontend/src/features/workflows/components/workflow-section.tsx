import { useId, type ReactNode } from "react";

import { Icon, type IconName } from "@/shared/ui/icon";
import { ChevronRight } from "@/shared/ui/icons";

/** A titled Browse section: a heading, an optional "See all", then cards. */
export function WorkflowSection({
  children,
  icon,
  onSeeAll,
  seeAllCount,
  title,
}: {
  children: ReactNode;
  icon?: IconName;
  onSeeAll?: () => void;
  seeAllCount?: number;
  title: string;
}) {
  const headingId = useId();
  return (
    <section className="workflows-section" aria-labelledby={headingId}>
      <header className="flex items-center justify-between gap-[12px] mb-[12px]">
        <h2
          className="flex items-center gap-[8px] min-w-0 m-0 text-workspace-text text-[length:16px] font-[600] leading-[22px]"
          id={headingId}
        >
          {icon ? (
            <span
              className="inline-grid place-items-center text-workspace-accent"
              aria-hidden="true"
            >
              <Icon name={icon} size={16} />
            </span>
          ) : null}
          {title}
        </h2>
        {onSeeAll ? (
          <button
            type="button"
            className="workflows-section__see-all"
            aria-label={
              seeAllCount
                ? `See all ${seeAllCount} ${title} workflows`
                : `See all ${title} workflows`
            }
            onClick={onSeeAll}
          >
            See all
            {seeAllCount ? (
              <span className="text-workspace-muted font-[500]">
                {seeAllCount}
              </span>
            ) : null}
            <ChevronRight aria-hidden size={14} />
          </button>
        ) : null}
      </header>
      {children}
    </section>
  );
}
