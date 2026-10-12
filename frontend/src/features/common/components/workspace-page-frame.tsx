import type { ReactNode } from "react";
import { cx } from "@/shared/lib/cx";
import "@/features/common/styles/workspace-page-frame.css";
import "@/shared/ui/collection-list.css";

type WorkspacePageFrameProps = {
  actions?: ReactNode;
  ariaLabel: string;
  breadcrumb?: ReactNode;
  children: ReactNode;
  className?: string;
  contentClassName?: string;
  contentWidth?: "full" | "contained";
  description?: ReactNode;
  eyebrow?: string;
  title: ReactNode;
  toolbar?: ReactNode;
  tourId?: string;
};

export function WorkspacePageFrame({
  actions,
  ariaLabel,
  breadcrumb,
  children,
  className,
  contentClassName,
  contentWidth = "full",
  description,
  eyebrow,
  title,
  toolbar,
  tourId,
}: WorkspacePageFrameProps) {
  return (
    <main
      className={cx(
        "workspace-page-frame",
        contentWidth === "contained" && "workspace-page-frame--contained",
        className,
      )}
      aria-label={ariaLabel}
      data-tour-id={tourId}
    >
      {breadcrumb ? (
        <div className="workspace-page-frame__breadcrumb">{breadcrumb}</div>
      ) : null}
      <header className="workspace-page-frame__header">
        <div
          className="workspace-page-frame__heading"
          data-tour-id={tourId ? `${tourId}-heading` : undefined}
        >
          <div className="min-w-0">
            {eyebrow ? (
              <p className="workspace-page-frame__eyebrow">{eyebrow}</p>
            ) : null}
            <h1 className="workspace-page-frame__title">{title}</h1>
            {description ? (
              <div className="workspace-page-frame__description">
                {description}
              </div>
            ) : null}
          </div>
        </div>
        {actions ? (
          <div className="workspace-page-frame__actions">{actions}</div>
        ) : null}
      </header>
      <div className={cx("workspace-page-frame__content", contentClassName)}>
        {toolbar ? (
          <div className="workspace-page-frame__toolbar">{toolbar}</div>
        ) : null}
        {children}
      </div>
    </main>
  );
}

type WorkspaceAccessStateProps = {
  action?: ReactNode;
  description: string;
  eyebrow?: string;
  title: string;
};

export function WorkspaceAccessState({
  action,
  description,
  eyebrow = "Workspace access",
  title,
}: WorkspaceAccessStateProps) {
  return (
    <WorkspacePageFrame
      ariaLabel={title}
      description={description}
      eyebrow={eyebrow}
      title={title}
      contentClassName="grid place-items-center"
    >
      <section className="workspace-access-state workspace-matte-panel">
        <span className="workspace-access-state__mark" aria-hidden="true">
          B
        </span>
        <h2>{title}</h2>
        <p>{description}</p>
        {action ? <div className="mt-5">{action}</div> : null}
      </section>
    </WorkspacePageFrame>
  );
}
