// A Schedule seed is how another page hands the Schedule editor a prefilled
// draft: the Workflows launch dialog renders a recurring Workflow's Schedule
// template and navigates to /scheduled/new with the seed as route state. The
// editor applies it once, on first render, and the person still reviews every
// field — including the Agent and the Connector sources — before saving.
export type ScheduleSeed = {
  kind: "workflow";
  /** The Workflow key, kept as provenance in the editor. */
  workflowKey: string;
  /** The Workflow's display name, shown in the editor's provenance note. */
  workflowName: string;
  name: string;
  /** Rendered, self-contained instructions (Schedule Runs are connector-only). */
  instructions: string;
  cronExpression: string;
  agentId?: string;
  /**
   * Connector ids to preselect as sources, found ready for `agentId`. The
   * editor keeps them only while that Agent stays chosen; the server still
   * resolves and reviews every source on save.
   */
  sourceIds?: string[];
};

export type ScheduleSeedRouteState = { scheduleSeed: ScheduleSeed };

/** A Workflow names at most six apps, so a seed preselects at most six. */
export const SCHEDULE_SEED_MAX_SOURCES = 6;

function isSourceIdList(value: unknown): value is string[] {
  return (
    Array.isArray(value) &&
    value.length <= SCHEDULE_SEED_MAX_SOURCES &&
    value.every((sourceId) => typeof sourceId === "string" && sourceId !== "")
  );
}

export function readScheduleSeed(state: unknown): ScheduleSeed | null {
  if (!state || typeof state !== "object" || !("scheduleSeed" in state)) {
    return null;
  }
  const seed = (state as { scheduleSeed: unknown }).scheduleSeed;
  if (!seed || typeof seed !== "object") return null;
  const candidate = seed as Partial<Record<keyof ScheduleSeed, unknown>>;
  if (
    candidate.kind !== "workflow" ||
    typeof candidate.workflowKey !== "string" ||
    typeof candidate.workflowName !== "string" ||
    typeof candidate.name !== "string" ||
    typeof candidate.instructions !== "string" ||
    typeof candidate.cronExpression !== "string" ||
    (candidate.agentId !== undefined &&
      typeof candidate.agentId !== "string") ||
    (candidate.sourceIds !== undefined && !isSourceIdList(candidate.sourceIds))
  ) {
    return null;
  }
  return seed as ScheduleSeed;
}
