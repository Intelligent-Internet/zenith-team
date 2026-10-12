import { useState } from "react";
import { Link } from "react-router";
import type { ActivityEventSummary } from "@/shared/api/types";
import {
  activityActorName,
  activityPersonName,
} from "@/shared/lib/activity-actor";
import { changedActivityFields } from "@/shared/ui/activity-snapshot-details";
import { DisclosureSummary } from "@/shared/ui/disclosure-summary";
import { ArrowRight, Building2, Minus, Plus } from "@/shared/ui/icons";
import {
  PROJECT_PERMISSION_LABELS,
  type ProjectPermission,
} from "@/shared/permissions";
import { PROJECT_PERMISSION_ICONS } from "@/features/company-plan/projects/access/project-access-permission-icons";
import {
  accessActivityChange,
  activityFieldActorKind,
  activityFieldLabel,
  isAccessActivity,
  type AccessActivityChange,
} from "@/features/home/model/activity-presentation";

export function CompanyActivityDetails({
  actorNames,
  event,
}: {
  actorNames: ReadonlyMap<string, string> | null;
  event: ActivityEventSummary;
}) {
  const [rawOpen, setRawOpen] = useState(false);
  const details = event.data;
  // An access row shows what it changed, or nothing beyond its title.
  const accessRow = isAccessActivity(event);
  const access = accessActivityChange(event);
  const changed =
    details.schemaVersion === 1
      ? changedActivityFields(details.before, details.after)
      : [];
  const fields = [...changed].sort((left, right) =>
    left === "status" ? -1 : right === "status" ? 1 : 0,
  );

  return (
    <div className="company-activity-details">
      {event.run_id ? (
        <p className="company-activity-details__run">
          {event.on_behalf_of
            ? `Done by ${activityActorName(event.actor)} for ${activityPersonName(event.on_behalf_of)} in `
            : "Done in "}
          <Link to={`/app/runs/${event.run_id}`}>
            Run {event.run_id.slice(0, 8)}
          </Link>
        </p>
      ) : null}
      {event.revision != null ? (
        <p className="company-activity-details__revision">
          Revision {event.revision}
        </p>
      ) : null}
      {accessRow ? (
        access ? (
          <AccessChangeDetails actorNames={actorNames} change={access} />
        ) : null
      ) : details.schemaVersion === 1 && fields.length > 0 ? (
        <dl className="company-activity-details__changes">
          {fields.map((field) => (
            <div key={field}>
              <dt>{activityFieldLabel(field)}</dt>
              <dd>
                <span className="sr-only">Before: </span>
                <ChangeValue
                  actorNames={actorNames}
                  field={field}
                  value={details.before?.[field]}
                />
                <ArrowRight aria-hidden="true" size={16} />
                <span className="sr-only">After: </span>
                <ChangeValue
                  actorNames={actorNames}
                  field={field}
                  value={details.after?.[field]}
                />
              </dd>
            </div>
          ))}
        </dl>
      ) : (
        <p className="company-activity-details__note">
          {details.schemaVersion !== 1 || (!details.before && !details.after)
            ? "No change snapshot was recorded for this event."
            : !details.before
              ? "Created with the recorded values."
              : !details.after
                ? "Deleted. The previous values are available below."
                : "No field changes in this snapshot."}
        </p>
      )}
      <details
        className="company-activity-details__raw"
        onToggle={(event) => setRawOpen(event.currentTarget.open)}
      >
        <DisclosureSummary>View raw data</DisclosureSummary>
        {rawOpen ? (
          details.schemaVersion === 1 ? (
            <div className="company-activity-details__snapshots">
              <RawSnapshot label="Before" value={details.before} />
              <RawSnapshot label="After" value={details.after} />
            </div>
          ) : (
            <RawSnapshot label="Event data" value={details.payload} />
          )
        ) : null}
      </details>
    </div>
  );
}

function ChangeValue({
  actorNames,
  field,
  value,
}: {
  actorNames: ReadonlyMap<string, string> | null;
  field: string;
  value: unknown;
}) {
  const status =
    field === "status" && typeof value === "string" ? value : undefined;
  return (
    <span className="company-activity-details__value" data-status={status}>
      {formatChangeValue(field, value, actorNames)}
    </span>
  );
}

function formatChangeValue(
  field: string,
  value: unknown,
  actorNames: ReadonlyMap<string, string> | null,
): string {
  if (value === null || value === undefined || value === "") return "Not set";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  if (typeof value === "number") return value.toLocaleString();
  if (typeof value === "string") {
    if (field === "status") return activityFieldLabel(value);
    const actorKind = activityFieldActorKind(field);
    // A removed member or deleted Agent is no longer on the roster; the full
    // id stays in the raw data. While the roster is loading or could not be
    // read, only the short id shows: nobody is called gone before we know.
    if (actorKind) {
      const shortId = value.slice(0, 8);
      if (!actorNames) return shortId;
      return (
        actorNames.get(`${actorKind}:${value}`) ??
        `Unknown ${actorKind === "user" ? "member" : "agent"} (${shortId})`
      );
    }
    if (field.endsWith("At") && !Number.isNaN(Date.parse(value))) {
      return new Date(value).toLocaleString();
    }
    return value;
  }
  return JSON.stringify(value, null, 2);
}

// An access row's change (Access DESIGN 23): a plus over the permissions
// it gave and a minus over those it took away, each with its icon and
// today's name; a visibility change as "Everyone → Limited"; the people a
// Group's row counts, those the roster names and "+N" for the rest. No ids.
function AccessChangeDetails({
  actorNames,
  change,
}: {
  actorNames: ReadonlyMap<string, string> | null;
  change: AccessActivityChange;
}) {
  if (change.kind === "visibility") {
    return (
      <p className="company-activity-details__access-line">
        <VisibilityValue value={change.before} />
        <ArrowRight aria-hidden="true" size={16} />
        <span className="sr-only">to </span>
        <VisibilityValue value={change.after} />
      </p>
    );
  }
  if (change.kind === "people") {
    const known = change.userIds.flatMap((userId) => {
      const name = actorNames?.get(`user:${userId}`);
      return name ? [name] : [];
    });
    // The title already counts them; nobody named adds nothing.
    if (known.length === 0) return null;
    const rest = change.userIds.length - known.length;
    return (
      <p className="company-activity-details__access-line">
        {known.join(", ")}
        {rest > 0 ? ` +${rest}` : null}
      </p>
    );
  }
  return (
    <ul className="company-activity-details__access">
      {change.added.length > 0 ? (
        <li data-change="added">
          <Plus aria-hidden="true" size={16} />
          <span className="sr-only">Added: </span>
          <PermissionList permissions={change.added} />
        </li>
      ) : null}
      {change.removed.length > 0 ? (
        <li data-change="removed">
          <Minus aria-hidden="true" size={16} />
          <span className="sr-only">Removed: </span>
          <PermissionList permissions={change.removed} />
        </li>
      ) : null}
    </ul>
  );
}

function PermissionList({
  permissions,
}: {
  permissions: readonly ProjectPermission[];
}) {
  return (
    <span className="company-activity-details__permissions">
      {permissions.map((permission) => {
        const Icon = PROJECT_PERMISSION_ICONS[permission];
        return (
          <span
            className="company-activity-details__permission"
            key={permission}
          >
            <Icon aria-hidden="true" size={14} />
            {PROJECT_PERMISSION_LABELS[permission]}
          </span>
        );
      })}
    </span>
  );
}

function VisibilityValue({ value }: { value: string }) {
  return (
    <span className="company-activity-details__permission">
      {value === "company" ? (
        <>
          <Building2 aria-hidden="true" size={14} />
          Everyone
        </>
      ) : (
        "Limited"
      )}
    </span>
  );
}

function RawSnapshot({ label, value }: { label: string; value: unknown }) {
  return (
    <div>
      <h4>{label}</h4>
      <pre>
        {value == null ? "No snapshot" : JSON.stringify(value, null, 2)}
      </pre>
    </div>
  );
}
