import { apiClient } from "@/lib/api-client";
import { agentBusinessLabel } from "@/shared/agent-execution/business-labels";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import { activityToActivityEvent } from "@/shared/api/adapters";
import type {
  ActivityCalendarDto,
  ActivityCalendarQuery,
  ActivityEventSummary,
  ActivityListQuery,
  ActivityPageDto,
} from "@/shared/api/types";

const activityPageLimit = 100;

export type ActivityCalendarRequest = {
  companyId: string;
  endAt: string;
  monthKey: string;
  startAt: string;
  timeZone: string;
};

export type ActivityCalendarSummary = {
  countsByDate: Record<string, number>;
  monthKey: string;
  syncedAt: string;
};

export type ActivityPage = {
  activities: ActivityEventSummary[];
  nextCursor: string | null;
  historyCoverage: ActivityPageDto["historyCoverage"];
  historyStartsAt: ActivityPageDto["historyStartsAt"];
};

export type ActivityListOptions = {
  action?: ActivityListQuery["action"];
  actorId?: ActivityListQuery["actor_id"];
  actorType?: ActivityListQuery["actor_type"];
  revision?: ActivityListQuery["revision"];
  beforeRevision?: ActivityListQuery["before_revision"];
  cursor?: ActivityListQuery["cursor"];
  entityId?: ActivityListQuery["entity_id"];
  entityType?: ActivityListQuery["entity_type"];
  from?: ActivityListQuery["from"];
  limit?: ActivityListQuery["limit"];
  to?: ActivityListQuery["to"];
};

class ActivityService {
  async listActivityEvents(): Promise<ActivityEventSummary[]> {
    const companyId = requireActiveCompanyId();
    return this.listCompanyActivity(companyId);
  }

  async listCompanyActivity(
    companyId: string,
  ): Promise<ActivityEventSummary[]> {
    const page = await this.listCompanyActivityPage(companyId);
    return page.activities;
  }

  async listCompanyActivityPage(
    companyId: string,
    options: ActivityListOptions = {},
  ): Promise<ActivityPage> {
    const page = await apiClient.get<ActivityPageDto>(
      `/companies/${encodeURIComponent(companyId)}/activity`,
      {
        params: {
          ...(options.action ? { action: options.action } : {}),
          ...(options.actorId ? { actor_id: options.actorId } : {}),
          ...(options.actorType ? { actor_type: options.actorType } : {}),
          ...(options.revision === undefined
            ? {}
            : { revision: options.revision }),
          ...(options.beforeRevision === undefined
            ? {}
            : { before_revision: options.beforeRevision }),
          ...(options.cursor ? { cursor: options.cursor } : {}),
          ...(options.entityId ? { entity_id: options.entityId } : {}),
          ...(options.entityType ? { entity_type: options.entityType } : {}),
          ...(options.from ? { from: options.from } : {}),
          limit: options.limit ?? activityPageLimit,
          ...(options.to ? { to: options.to } : {}),
        },
      },
    );
    return {
      activities: page.items.map((activity) =>
        toActivityEventSummary(activityToActivityEvent(activity)),
      ),
      nextCursor: page.nextCursor,
      historyCoverage: page.historyCoverage,
      historyStartsAt: page.historyStartsAt,
    };
  }

  async getCompanyActivityCalendar(
    request: ActivityCalendarRequest,
  ): Promise<ActivityCalendarSummary> {
    const query: ActivityCalendarQuery = {
      month: request.monthKey,
      time_zone: request.timeZone,
    };
    const calendar = await apiClient.get<ActivityCalendarDto>(
      `/companies/${encodeURIComponent(request.companyId)}/activity/calendar`,
      { params: query },
    );

    return {
      countsByDate: calendar.days,
      monthKey: calendar.month,
      syncedAt: new Date().toISOString(),
    };
  }
}

export function activityDateKey(
  value: Date | string,
  timeZone: string,
): string | null {
  const date = typeof value === "string" ? new Date(value) : value;
  if (Number.isNaN(date.getTime())) {
    return null;
  }

  try {
    return createActivityDateKeyFormatter(timeZone)(date);
  } catch {
    return null;
  }
}

function createActivityDateKeyFormatter(timeZone: string) {
  const formatter = new Intl.DateTimeFormat("en-US", {
    day: "2-digit",
    month: "2-digit",
    timeZone,
    year: "numeric",
  });

  return (date: Date) => {
    const parts = Object.fromEntries(
      formatter
        .formatToParts(date)
        .filter((part) => part.type !== "literal")
        .map((part) => [part.type, part.value]),
    );
    if (!parts.year || !parts.month || !parts.day) {
      throw new Error("Activity date could not be formatted");
    }
    return `${parts.year}-${parts.month}-${parts.day}`;
  };
}

function toActivityEventSummary(
  event: ActivityEventSummary,
): ActivityEventSummary {
  return {
    ...event,
    summary: agentBusinessLabel(event.summary),
  };
}

export const activityService = new ActivityService();
