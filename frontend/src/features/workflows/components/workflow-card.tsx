import { useId } from "react";

import type { WorkflowSummary } from "@/shared/api/types";
import { Icon } from "@/shared/ui/icon";
import { Pin, Repeat2 } from "@/shared/ui/icons";

export type WorkflowOpenHandler = (
  workflowKey: string,
  opener: HTMLElement,
) => void;

/**
 * One catalog card. The whole card is one button that opens the launch
 * dialog; the pin toggle is a sibling button laid over its right edge (a
 * button cannot contain another), shown on hover and focus and kept visible
 * while pinned.
 */
export function WorkflowCard({
  onOpen,
  onTogglePin,
  pinned,
  workflow,
}: {
  onOpen: WorkflowOpenHandler;
  onTogglePin: (workflowKey: string) => void;
  pinned: boolean;
  workflow: WorkflowSummary;
}) {
  const id = useId();
  const titleId = `${id}-title`;
  const descriptionId = `${id}-description`;
  const badgeId = `${id}-badge`;
  return (
    <li className="workflow-card" data-pinned={pinned ? "true" : undefined}>
      <button
        type="button"
        className="workflow-card__open"
        data-workflow-card={workflow.key}
        aria-labelledby={titleId}
        aria-describedby={
          workflow.recurring ? `${descriptionId} ${badgeId}` : descriptionId
        }
        onClick={(event) => onOpen(workflow.key, event.currentTarget)}
      >
        <span className="workflow-card__icon" aria-hidden="true">
          <Icon name={workflow.icon} size={20} />
        </span>
        <span className="grid gap-[4px] min-w-0">
          <span className="flex flex-wrap items-center gap-[4px_8px] min-w-0">
            <span
              className="min-w-0 text-workspace-body font-[600] leading-workspace-body wrap-anywhere"
              id={titleId}
            >
              {workflow.name}
            </span>
            {workflow.recurring ? (
              <WorkflowRecurringBadge id={badgeId} />
            ) : null}
          </span>
          <span className="workflow-card__description" id={descriptionId}>
            {workflow.description}
          </span>
        </span>
      </button>
      <button
        type="button"
        className="workflow-card__pin"
        aria-label={`Pin ${workflow.name}`}
        aria-pressed={pinned}
        title={pinned ? "Unpin" : "Pin"}
        onClick={(event) => {
          event.stopPropagation();
          onTogglePin(workflow.key);
        }}
      >
        <Pin aria-hidden size={16} />
      </button>
    </li>
  );
}

export function WorkflowRecurringBadge({ id }: { id?: string }) {
  return (
    <span
      className="inline-flex flex-none items-center gap-[4px] p-[1px_7px] [border:1px_solid_var(--workspace-divider)] rounded-[999px] [background:var(--workspace-control-bg)] text-workspace-muted text-workspace-micro font-[600] leading-[16px] whitespace-nowrap"
      id={id}
    >
      <Repeat2 aria-hidden size={12} />
      Recurring
    </span>
  );
}

export function WorkflowCardGrid({
  label,
  onOpen,
  onTogglePin,
  pinnedKeys,
  workflows,
}: {
  label?: string;
  onOpen: WorkflowOpenHandler;
  onTogglePin: (workflowKey: string) => void;
  pinnedKeys: ReadonlySet<string>;
  workflows: readonly WorkflowSummary[];
}) {
  return (
    <ul className="workflows-grid" aria-label={label}>
      {workflows.map((workflow) => (
        <WorkflowCard
          key={workflow.key}
          workflow={workflow}
          pinned={pinnedKeys.has(workflow.key)}
          onOpen={onOpen}
          onTogglePin={onTogglePin}
        />
      ))}
    </ul>
  );
}
