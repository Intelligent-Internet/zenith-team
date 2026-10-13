import { useMemo, useState, type ReactNode } from "react";
import { useListGroupsQuery } from "@/features/agents/api/groups-api";
import {
  useListManagedProjectsQuery,
  useListProjectsQuery,
} from "@/features/company-plan/api/project-api";
import { CompanyActivityDetails } from "@/features/home/components/company-activity-details";
import { useCompanyActivity } from "@/features/home/hooks/use-company-activity";
import {
  activityPeriods,
  activityPresentation,
  activitySubjects,
  formatActivityDate,
  type ActivityNames,
} from "@/features/home/model/activity-presentation";
import type { ActivityEventSummary } from "@/shared/api/types";
import { useCan } from "@/shared/permissions";
import {
  ChevronDown,
  ChevronRight,
  CircleAlert,
  Info,
  NotebookText,
  RefreshCw,
} from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";
import "@/features/home/styles/company-activity-log.css";

export function CompanyActivityLog({
  companyId,
  companyName,
}: {
  companyId: string;
  companyName: string;
}) {
  const activity = useCompanyActivity(companyId);
  const { query, events } = activity;
  const names = useActivityNames(companyId, activity.actorNames);
  const isLoading = !query.currentData && !query.isError;
  const isRefreshing = query.isFetching && !query.isFetchingNextPage;
  const isLoadMoreError = query.isError && query.direction === "forward";
  const dayGroups = new Map<string, ActivityEventSummary[]>();
  for (const event of events) {
    const day = formatActivityDate(event.created_at);
    const group = dayGroups.get(day);
    if (group) group.push(event);
    else dayGroups.set(day, [event]);
  }

  return (
    <section
      className="company-activity-log"
      aria-label={`${companyName} activity log`}
    >
      <div className="company-activity-log__toolbar">
        <div className="company-activity-log__filters">
          <ActivityFilter
            label="Filter by activity type"
            value={activity.entityType}
            onChange={activity.setEntityType}
          >
            <option value="all">All activity</option>
            {Object.entries(activitySubjects).map(([value, subject]) => (
              <option key={value} value={value}>
                {subject.plural}
              </option>
            ))}
          </ActivityFilter>
          <ActivityFilter
            label="Filter by actor"
            value={activity.actor.value}
            onChange={activity.setActor}
          >
            {activity.actorOptions.groups.map((actor) => (
              <option key={actor.value} value={actor.value}>
                {actor.label}
              </option>
            ))}
            {activity.actorOptions.people.length > 0 ? (
              <optgroup label="People">
                {activity.actorOptions.people.map((actor) => (
                  <option key={actor.value} value={actor.value}>
                    {actor.label}
                  </option>
                ))}
              </optgroup>
            ) : null}
            {activity.actorOptions.agents.length > 0 ? (
              <optgroup label="Agents">
                {activity.actorOptions.agents.map((actor) => (
                  <option key={actor.value} value={actor.value}>
                    {actor.label}
                  </option>
                ))}
              </optgroup>
            ) : null}
            {activity.isRosterLoading ? (
              <option disabled>Loading people and agents…</option>
            ) : null}
            {activity.isRosterError ? (
              <option disabled>
                Some people or agents could not be loaded
              </option>
            ) : null}
          </ActivityFilter>
          <ActivityFilter
            label="Filter by time period"
            value={activity.period}
            onChange={(value) => {
              const period = activityPeriods.find(
                (period) => period.value === value,
              );
              if (period) activity.setPeriod(period.value);
            }}
          >
            {activityPeriods.map((period) => (
              <option key={period.value} value={period.value}>
                {period.label}
              </option>
            ))}
          </ActivityFilter>
          {activity.hasFilters ? (
            <button
              className="company-activity-log__clear"
              type="button"
              onClick={activity.clearFilters}
            >
              Clear filters
            </button>
          ) : null}
        </div>
        <div className="company-activity-log__actions">
          <span className="company-activity-log__order">Newest first</span>
          <Tooltip label="Refresh activity">
            <button
              aria-label={
                isRefreshing ? "Refreshing activity…" : "Refresh activity"
              }
              className="collection-toolbar-control collection-toolbar-control--icon"
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
              type="button"
            >
              <RefreshCw
                aria-hidden="true"
                className={isRefreshing ? "animate-spin" : undefined}
                size={18}
              />
            </button>
          </Tooltip>
        </div>
      </div>

      {query.isError && !isLoadMoreError && events.length > 0 ? (
        <p className="company-activity-log__notice" role="status">
          Couldn’t refresh activity. Showing the last loaded events.
        </p>
      ) : null}

      <div className="company-activity-log__feed" aria-busy={query.isFetching}>
        {isLoading ? (
          <div className="company-activity-log__state" role="status">
            <RefreshCw aria-hidden="true" className="animate-spin" size={24} />
            <h2>Loading activity…</h2>
          </div>
        ) : query.isError && events.length === 0 ? (
          <div className="company-activity-log__state" role="status">
            <CircleAlert aria-hidden="true" size={28} />
            <h2>Activity could not be loaded</h2>
            <p>Try again to load company changes.</p>
            <button
              className="collection-toolbar-control"
              onClick={() => void query.refetch()}
              type="button"
              disabled={query.isFetching}
            >
              Try again
            </button>
          </div>
        ) : events.length === 0 ? (
          <div className="company-activity-log__state" role="status">
            <NotebookText aria-hidden="true" size={32} />
            <h2>
              {activity.hasFilters ? "No matching activity" : "No activity yet"}
            </h2>
            <p>
              {activity.hasFilters
                ? "Try another type, person, or time period."
                : "Recorded actions and changes will appear here."}
            </p>
            {activity.hasFilters ? (
              <button
                className="collection-toolbar-control"
                type="button"
                onClick={activity.clearFilters}
              >
                Clear filters
              </button>
            ) : null}
          </div>
        ) : (
          <div className="company-activity-log__list collection-list">
            <div
              className="company-activity-log__columns min-h-[38px] text-workspace-muted text-workspace-caption leading-workspace-caption font-[500]"
              aria-hidden="true"
            >
              <span>Activity</span>
              <span>By</span>
              <span>Time</span>
              <span />
            </div>
            {[...dayGroups].map(([day, dayEvents]) => (
              <section
                className="company-activity-log__day"
                key={day}
                aria-label={day}
              >
                <h2>{day}</h2>
                <ol>
                  {dayEvents.map((event) => (
                    <ActivityLogEntry
                      actorNames={activity.actorNames}
                      event={event}
                      key={event.event_id}
                      names={names}
                    />
                  ))}
                </ol>
              </section>
            ))}
          </div>
        )}
      </div>

      {query.currentData ? (
        <footer className="company-activity-log__footer">
          <div className="company-activity-log__count">
            <span role="status">
              {events.length.toLocaleString()}{" "}
              {events.length === 1 ? "event" : "events"} loaded
            </span>
            {query.currentData.pages[0]?.historyCoverage === "partial" ? (
              <Tooltip label="This feed contains recorded activity and may not include every past change.">
                <button
                  className="company-activity-log__coverage"
                  type="button"
                  aria-label="About activity history"
                >
                  <Info size={15} aria-hidden="true" />
                </button>
              </Tooltip>
            ) : null}
          </div>
          <div className="company-activity-log__pagination">
            {isLoadMoreError ? (
              <span role="status">
                Couldn’t load older activity. Try again.
              </span>
            ) : null}
            {query.hasNextPage ? (
              <button
                className="collection-toolbar-control"
                disabled={query.isFetching}
                onClick={() => void query.fetchNextPage()}
                type="button"
              >
                {query.isFetchingNextPage
                  ? "Loading older activity…"
                  : isLoadMoreError
                    ? "Try again"
                    : "Load older activity"}
              </button>
            ) : events.length > 0 ? (
              <span>End of recorded activity</span>
            ) : null}
          </div>
        </footer>
      ) : null}
    </section>
  );
}

function ActivityFilter({
  children,
  label,
  onChange,
  value,
}: {
  children: ReactNode;
  label: string;
  onChange: (value: string) => void;
  value: string;
}) {
  return (
    <label
      className="company-activity-log__filter collection-toolbar-control"
      data-filtered={value !== "all"}
    >
      <span className="sr-only">{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>
        {children}
      </select>
      <ChevronDown size={16} aria-hidden="true" />
    </label>
  );
}

// The names an access row falls back to when it carries none of its own
// (rows written before 2026-10-08; Access DESIGN 5): members from the
// roster, the Company's Groups, and the Projects the viewer reads or
// manages, archived ones included. A row names nothing the viewer could not
// already see.
function useActivityNames(
  companyId: string,
  actorNames: ReadonlyMap<string, string> | null,
): ActivityNames {
  const managesProjects = useCan("projects.manage");
  const groups = useListGroupsQuery(companyId);
  const readable = useListProjectsQuery(companyId);
  const managed = useListManagedProjectsQuery(companyId, {
    skip: !managesProjects,
  });
  return useMemo(() => {
    const groupNames = new Map(
      (groups.currentData?.items ?? []).map((group) => [group.id, group.name]),
    );
    const projectTitles = new Map(
      [
        ...(managed.currentData?.items ?? []),
        ...(readable.currentData?.items ?? []),
      ].map((project) => [project.id, project.title]),
    );
    return {
      person: (userId) => actorNames?.get(`user:${userId}`),
      group: (groupId) => groupNames.get(groupId),
      project: (projectId) => projectTitles.get(projectId),
    };
  }, [
    actorNames,
    groups.currentData,
    readable.currentData,
    managed.currentData,
  ]);
}

function ActivityLogEntry({
  actorNames,
  event,
  names,
}: {
  actorNames: ReadonlyMap<string, string> | null;
  event: ActivityEventSummary;
  names: ActivityNames;
}) {
  const [open, setOpen] = useState(false);
  const {
    icon: EntryIcon,
    title,
    context,
    actor,
  } = activityPresentation(event, names);
  const createdAt = new Date(event.created_at);
  const validDate = !Number.isNaN(createdAt.getTime());

  return (
    <li data-activity-entry="true">
      <details
        className="company-activity-log__entry"
        onToggle={(event) => setOpen(event.currentTarget.open)}
      >
        <summary className="company-activity-log__row collection-list__row">
          <span className="collection-list__identity">
            <span
              className="grid w-[32px] h-[32px] flex-none place-items-center rounded-[6px] [background:var(--workspace-control-bg)] text-workspace-muted"
              aria-hidden="true"
            >
              <EntryIcon size={18} />
            </span>
            <span className="block min-w-0 wrap-anywhere">
              <span className="text-workspace-text text-workspace-body leading-workspace-body font-[600]">
                {title}
              </span>
              <span className="collection-list__secondary" title={context}>
                {context}
              </span>
            </span>
          </span>
          <span className="company-activity-log__actor" title={actor}>
            <span className="sr-only">By </span>
            {actor}
          </span>
          <time
            className="company-activity-log__time"
            dateTime={event.created_at}
            title={validDate ? createdAt.toLocaleString() : undefined}
          >
            {validDate
              ? createdAt.toLocaleTimeString(undefined, {
                  hour: "2-digit",
                  minute: "2-digit",
                  hour12: false,
                })
              : "—"}
          </time>
          <ChevronRight
            className="company-activity-log__chevron"
            aria-hidden="true"
            size={16}
          />
        </summary>
        {open ? (
          <CompanyActivityDetails actorNames={actorNames} event={event} />
        ) : null}
      </details>
    </li>
  );
}
