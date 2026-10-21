import type { ActivityEventSummary } from "@/shared/api/types";
import { DisclosureSummary } from "@/shared/ui/disclosure-summary";

export function ActivitySnapshotDetails({
  event,
}: {
  event: ActivityEventSummary;
}) {
  const details = event.data;
  if (details.schemaVersion !== 1) {
    return (
      <details className="mt-1 min-w-0 text-xs">
        <DisclosureSummary className="text-inherit opacity-75">
          View event information
        </DisclosureSummary>
        <p className="mt-2">
          No before/after snapshot was recorded for this event.
        </p>
        <pre className="mt-1 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded border border-current/15 p-2 text-[11px]">
          {JSON.stringify(details.payload, null, 2)}
        </pre>
      </details>
    );
  }
  const changed = [
    ...new Set(
      changedActivityFields(details.before, details.after).map(
        activitySnapshotFieldLabel,
      ),
    ),
  ];
  const revisionLabel =
    event.revision == null ? "View change" : `View revision ${event.revision}`;
  const label =
    changed.length > 0
      ? `${revisionLabel} · changed ${changed.join(", ")}`
      : revisionLabel;
  return (
    <details className="mt-1 min-w-0 text-xs">
      <DisclosureSummary className="text-inherit opacity-75">
        {label}
      </DisclosureSummary>
      {changed.length > 0 ? (
        <p className="mt-2">Changed: {changed.join(", ")}</p>
      ) : null}
      <div className="mt-2 grid min-w-0 gap-3 sm:grid-cols-2">
        <Snapshot
          label="Before"
          value={details.before}
          empty="No previous state recorded"
        />
        <Snapshot label="After" value={details.after} empty="Entity deleted" />
      </div>
    </details>
  );
}

function Snapshot({
  label,
  value,
  empty,
}: {
  label: string;
  value: Record<string, unknown> | null;
  empty: string;
}) {
  return (
    <div className="min-w-0">
      <strong>{label}</strong>
      <pre className="mt-1 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded border border-current/15 p-2 text-[11px]">
        {value === null ? empty : JSON.stringify(value, null, 2)}
      </pre>
    </div>
  );
}

// Fields every update rewrites; listing them would tell the reader nothing.
const bookkeepingFields = new Set(["revision", "updatedAt"]);

// Plain words for snapshot fields whose split name would not read well.
// Every other field reads as its camelCase words without a trailing "Id".
// Older Task snapshots name the executing Agent assigneeAgentId; it reads
// "executed by" too, since the assignee is always a member.
const fieldLabels: Record<string, string> = {
  assigneeUserId: "assignee",
  assigneeAgentId: "executed by",
  executedByAgentId: "executed by",
  creatorUserId: "creator",
  creatorAgentId: "creator",
  createdByUserId: "creator",
  prerequisiteIds: "dependencies",
  milestoneType: "milestone",
  completedAt: "completion",
  pinnedAt: "pinned",
  archivedAt: "archived",
  fireAt: "scheduled time",
  taskExecutionGeneration: "execution generation",
};

export function activitySnapshotFieldLabel(key: string) {
  return (
    fieldLabels[key] ??
    key
      .replace(/Ids?$/, "")
      .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
      .toLowerCase()
  );
}

// The keys whose value differs between two full version-1 snapshots. Create
// (no before) and delete (no after) have nothing to compare, and legacy
// version-0 payloads never reach here, so no change is inferred from them.
export function changedActivityFields(
  before: Record<string, unknown> | null,
  after: Record<string, unknown> | null,
): string[] {
  if (!before || !after) return [];
  const keys = Object.keys({ ...before, ...after }).filter(
    (key) =>
      !bookkeepingFields.has(key) &&
      JSON.stringify(before[key]) !== JSON.stringify(after[key]),
  );
  return keys;
}
