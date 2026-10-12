import { useId } from "react";

import {
  WorkflowRecurringBadge,
  type WorkflowOpenHandler,
} from "@/features/workflows/components/workflow-card";
import type { WorkflowSummary } from "@/shared/api/types";
import { Icon } from "@/shared/ui/icon";

/** A larger card for the Featured section at the top of Browse. */
export function WorkflowFeaturedCard({
  onOpen,
  workflow,
}: {
  onOpen: WorkflowOpenHandler;
  workflow: WorkflowSummary;
}) {
  const id = useId();
  const titleId = `${id}-title`;
  const descriptionId = `${id}-description`;
  const badgeId = `${id}-badge`;
  return (
    <li className="workflow-featured-card">
      <button
        type="button"
        className="workflow-featured-card__open"
        data-workflow-card={workflow.key}
        aria-labelledby={titleId}
        aria-describedby={
          workflow.recurring ? `${descriptionId} ${badgeId}` : descriptionId
        }
        onClick={(event) => onOpen(workflow.key, event.currentTarget)}
      >
        <span className="workflow-featured-card__top">
          <span className="workflow-featured-card__icon" aria-hidden="true">
            <Icon name={workflow.icon} size={22} />
          </span>
          {workflow.recurring ? <WorkflowRecurringBadge id={badgeId} /> : null}
        </span>
        <span className="workflow-featured-card__title" id={titleId}>
          {workflow.name}
        </span>
        <span
          className="workflow-featured-card__description"
          id={descriptionId}
        >
          {workflow.description}
        </span>
      </button>
    </li>
  );
}

export function WorkflowFeaturedGrid({
  onOpen,
  workflows,
}: {
  onOpen: WorkflowOpenHandler;
  workflows: readonly WorkflowSummary[];
}) {
  return (
    <ul className="workflows-featured-grid">
      {workflows.map((workflow) => (
        <WorkflowFeaturedCard
          key={workflow.key}
          workflow={workflow}
          onOpen={onOpen}
        />
      ))}
    </ul>
  );
}
