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
      <header className="workflows-section__header">
        <h2 className="workflows-section__title" id={headingId}>
          {icon ? (
            <span className="workflows-section__icon" aria-hidden="true">
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
              <span className="workflows-section__count">{seeAllCount}</span>
            ) : null}
            <ChevronRight aria-hidden size={14} />
          </button>
        ) : null}
      </header>
      {children}
    </section>
  );
}
