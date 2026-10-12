import type { ReactNode } from "react";

import { counted } from "@/features/agents/model/group-change";
import {
  CirclePause,
  EyeOff,
  Folder,
  Users,
  type IconComponent,
} from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";

import { ACCESS_TOOLTIP_CLASS } from "./project-access-permission-icons";

/** What a chip's colour says: a plain count, or something to notice. */
export type AccessChipTone = "count" | "warn";

// One icon and a number (or a short name) in a small pill. The accessible
// name says it in words ("2 runs stop"); the tooltip, when there is one,
// stays at a few words. Chips are never focusable, so a dialog that opens
// on them does not pop a tooltip.
export function AccessChip({
  children,
  icon: Icon,
  label,
  tone = "count",
  tooltip,
}: {
  children?: ReactNode;
  icon: IconComponent;
  /** The chip in words, for screen readers. */
  label: string;
  tone?: AccessChipTone;
  tooltip?: string;
}) {
  const chip = (
    <span
      aria-label={label}
      className="project-access-chip"
      data-tone={tone}
      role="img"
    >
      <Icon aria-hidden="true" size={13} />
      {children !== undefined ? (
        <span aria-hidden="true">{children}</span>
      ) : null}
    </span>
  );
  return tooltip ? (
    <Tooltip contentClassName={ACCESS_TOOLTIP_CLASS} label={tooltip}>
      {chip}
    </Tooltip>
  ) : (
    chip
  );
}

/** "1 run stops", "2 runs stop". */
export function runsStopLabel(count: number): string {
  return count === 1 ? "1 run stops" : `${count} runs stop`;
}

/** A pause icon and how many runs stop. */
export function RunsChip({ count }: { count: number }) {
  return (
    <AccessChip
      icon={CirclePause}
      label={runsStopLabel(count)}
      tone="warn"
      tooltip="Runs stop"
    >
      {count}
    </AccessChip>
  );
}

export type AccessCountValues = {
  people?: number;
  projects?: number;
  runs?: number;
  /** Some Projects the viewer can't see are left out. */
  hidden?: boolean;
};

/** Whether any chip would show. */
export function hasCounts(counts: AccessCountValues | undefined): boolean {
  return Boolean(
    counts &&
    ((counts.projects ?? 0) > 0 ||
      (counts.people ?? 0) > 0 ||
      (counts.runs ?? 0) > 0 ||
      counts.hidden),
  );
}

// What a change touches, as icons with counts: Projects, people, runs that
// stop, and a mark when some Projects are hidden. Zeros are left out.
export function AccessCounts({ counts }: { counts: AccessCountValues }) {
  const { hidden, people = 0, projects = 0, runs = 0 } = counts;
  return (
    <span className="project-access-chips">
      {projects > 0 ? (
        <AccessChip
          icon={Folder}
          label={counted(projects, "project", "projects")}
          tooltip={counted(projects, "project", "projects")}
        >
          {projects}
        </AccessChip>
      ) : null}
      {people > 0 ? (
        <AccessChip
          icon={Users}
          label={counted(people, "person", "people")}
          tooltip={counted(people, "person", "people")}
        >
          {people}
        </AccessChip>
      ) : null}
      {runs > 0 ? <RunsChip count={runs} /> : null}
      {hidden ? (
        <AccessChip
          icon={EyeOff}
          label="Some projects hidden"
          tooltip="Some projects hidden"
        />
      ) : null}
    </span>
  );
}
