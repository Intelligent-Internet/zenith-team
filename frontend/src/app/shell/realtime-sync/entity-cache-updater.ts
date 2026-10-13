import { resourceApi } from "@/shared/api/resource-api";
import { taskApi } from "@/shared/api/task-api";
import { activityApi } from "@/shared/api/activity-api";
import { companyApi } from "@/shared/api/company-api";
import type { components as ApiComponents } from "@/shared/api/generated/api.generated";
import { updateSessionCompanySummary } from "@/shared/session/session";
import { agentToAgentSummary } from "@/shared/api/adapters";
import { agentRuntimeApi } from "@/shared/api/agent-runtime-api";
import { boardlyApi } from "@/shared/api/boardly-api";
import type { AgentDto, CompanyResourceDto } from "@/shared/api/types";

import type { BoardlyRealtimeEvent } from "@/shared/realtime/realtime-client.service";
import {
  removeVersioned,
  replaceVersioned,
  type RealtimeSyncContext,
} from "@/app/shell/realtime-sync/realtime-sync-context";

type Company = ApiComponents["schemas"]["Company"];
type AgentStatusPayload = ApiComponents["schemas"]["AgentStatusPayload"];
type AgentStatusRow = AgentStatusPayload["agents"][number];
type DaemonRosterAction =
  ApiComponents["schemas"]["DaemonRosterPayload"]["action"];

export function applyEntityCacheEvent(
  context: RealtimeSyncContext,
  event: BoardlyRealtimeEvent,
) {
  switch (event.type) {
    case "wiki.page_created":
    case "wiki.page_updated":
    case "wiki.page_archived":
    case "wiki.page_restored":
      context.dispatch(
        boardlyApi.util.invalidateTags([
          { type: "Wiki", id: context.companyId },
          ...(hasStringField(event.payload, "id")
            ? [
                {
                  type: "Wiki" as const,
                  id: (event.payload as { id: string }).id,
                },
              ]
            : []),
        ]),
      );
      return true;
    case "wiki.collection_created":
    case "wiki.collection_updated":
    case "wiki.collection_deleted":
      context.dispatch(
        boardlyApi.util.invalidateTags([
          { type: "Wiki", id: context.companyId },
        ]),
      );
      return true;
    case "project.created":
    case "project.updated":
    case "project.archived":
      context.dispatch(
        boardlyApi.util.invalidateTags([
          { type: "Project", id: context.companyId },
        ]),
      );
      return true;
    case "onboarding.brief_updated":
      return true;
    case "company.created":
    case "company.updated":
      if (hasVersionedIdentity(event.payload, "id")) {
        updateCompany(context, event.payload as Company);
      }
      return true;
    case "agent.created":
    case "agent.updated":
      if (hasVersionedIdentity(event.payload, "id")) {
        updateAgent(context, event.payload as AgentDto);
      }
      return true;
    case "agent.binding_changed": {
      // The viewer set an Agent up on one of their computers, moved it, or
      // took it off (ADR 0014). That changes what they can run, whether the
      // Tasks they are assigned are ready, and the plugins and connectors
      // read off the runtime it now names, so those caches are refetched for
      // them alone — the frame reaches nobody else. The other members learn
      // about the Tasks from the task.updated that follows.
      context.requestRefresh("directConversations");
      context.dispatch(
        boardlyApi.util.invalidateTags([
          { id: context.companyId, type: "Agent" },
          { id: context.companyId, type: "AgentBinding" as const },
          { id: context.companyId, type: "Connector" as const },
          { id: context.companyId, type: "Plugin" as const },
          { id: context.companyId, type: "Task" as const },
        ]),
      );
      return true;
    }
    case "agent.status_changed": {
      // One of the viewer's computers came online or went offline. Whether
      // the Tasks they are assigned can start is read live off that
      // computer (ADR 0014). Every member gets those Tasks again as
      // task.updated; the viewer's own Task lists and task workspaces also
      // refetch here with the rosters.
      context.requestRefresh(
        "directConversations",
        "runtimes",
        "tasks",
        "taskWorkspaces",
      );
      const rows = agentStatusRows(event.payload);
      if (rows.length) updateAgentStatus(context, rows);
      return true;
    }
    case "daemon.roster_changed": {
      const action = rosterAction(event.payload);
      if (action === "tools_changed") {
        // Only runtime list/detail responses carry tools. Agent and Run
        // queries keep their caches until their own facts change.
        context.dispatch(
          boardlyApi.util.invalidateTags([
            { id: context.companyId, type: "Runtime" },
          ]),
        );
        return true;
      }
      context.requestRefresh("runtimes");
      // A rename (`update`) moved the machine name the Agent rows and the
      // conversation rows carry, and a removal (`remove`) moved this
      // member's bindings to another computer of theirs, taking their queued
      // runs with them (ADR 0014), so the rosters, the direct conversations
      // and the run lists refetch. The other actions only move liveness,
      // which `agent.status_changed` already patches.
      if (action === "update" || action === "remove") {
        context.requestRefresh("directConversations");
        context.dispatch(
          boardlyApi.util.invalidateTags([
            { id: context.companyId, type: "Agent" },
            { id: context.companyId, type: "Run" as const },
          ]),
        );
      }
      return true;
    }
    case "resource.created":
    case "resource.updated":
      if (hasVersionedIdentity(event.payload, "id")) {
        updateResource(context, event.payload as CompanyResourceDto);
      }
      return true;
    case "resource.deleted": {
      if (hasVersionedIdentity(event.payload, "resourceId")) {
        const receipt = event.payload as {
          resourceId: string;
          revision: number;
        };
        removeResource(context, receipt.resourceId, receipt.revision);
      }
      return true;
    }
    default:
      return false;
  }
}

function hasStringField(payload: unknown, key: string) {
  return (
    typeof payload === "object" &&
    payload !== null &&
    typeof (payload as Record<string, unknown>)[key] === "string"
  );
}

function rosterAction(payload: unknown): DaemonRosterAction | null {
  return hasStringField(payload, "action")
    ? ((payload as Record<string, unknown>).action as DaemonRosterAction)
    : null;
}

// One frame carries every agent whose availability flipped in the same
// underlying transition, so all rows are applied in a single cache pass.
function agentStatusRows(payload: unknown): AgentStatusRow[] {
  const rows = (payload as Partial<AgentStatusPayload>)?.agents;
  if (!Array.isArray(rows)) return [];
  return rows.filter(
    (row) =>
      hasStringField(row, "agentId") &&
      typeof row.runtimeAvailable === "boolean",
  );
}

function hasVersionedIdentity(payload: unknown, idKey: string) {
  return (
    hasStringField(payload, idKey) &&
    typeof (payload as Record<string, unknown>).revision === "number"
  );
}

function updateCompany(context: RealtimeSyncContext, company: Company) {
  if (company.id !== context.companyId) return;
  updateSessionCompanySummary(company);
  // Company events omit viewer access; only the member's access event changes it.
  context.dispatch(
    companyApi.util.updateQueryData(
      "listWorkspaceCompanies",
      undefined,
      (page) => {
        const current = page.items.find((item) => item.id === company.id);
        if (current && current.revision <= company.revision) {
          Object.assign(current, company, {
            role: current.role,
            permissions: current.permissions,
          });
        }
      },
    ),
  );
  context.dispatch(
    companyApi.util.updateQueryData(
      "getWorkspaceCompany",
      context.companyId,
      (current) => {
        if (!current) return;
        if (current.revision <= company.revision) {
          Object.assign(current, company, {
            role: current.role,
            permissions: current.permissions,
          });
        }
      },
    ),
  );
  context.dispatch(
    activityApi.util.updateQueryData(
      "getWorkspaceCockpit",
      context.companyId,
      (cockpit) => {
        if (!cockpit) return;
        cockpit.credits.version = Math.max(
          cockpit.credits.version,
          company.revision,
        );
      },
    ),
  );
}

function updateAgent(context: RealtimeSyncContext, agent: AgentDto) {
  // `access` is a per-viewer verdict (ADR 0013), and a lifecycle frame
  // carries the ACTING member's — an owner's edit would tell every member's
  // cache they may configure the Agent. Keep this viewer's own verdicts from
  // the row already in the cache; when the frame changes what those verdicts
  // depend on (a new Agent, a new creator) ask the server for this viewer's
  // answer instead of guessing it.
  let needsOwnVerdict = false;
  let viewerAgent = agent;
  context.dispatch(
    agentRuntimeApi.util.updateQueryData(
      "listCompanyAgents",
      context.companyId,
      (page) => {
        const current = page.items.find((item) => item.id === agent.id);
        if (!current) {
          needsOwnVerdict = true;
          replaceVersioned(page.items, agent);
          return;
        }
        // The frame carries the Agent template. The binding, and the
        // availability and capacity that follow from it, are the viewer's
        // own and never travel on it (ADR 0014), so the cached ones stand
        // until the binding's own frame replaces them. `activeRunCount`
        // moves with Runs instead: their frames read the roster again.
        needsOwnVerdict =
          (current.createdBy?.id ?? null) !== (agent.createdBy?.id ?? null);
        viewerAgent = {
          ...agent,
          access: current.access,
          activeRunCount: current.activeRunCount,
          binding: current.binding,
          runtimeAvailable: current.runtimeAvailable,
          unavailableReason: current.unavailableReason,
        };
        replaceVersioned(page.items, viewerAgent);
      },
    ),
  );
  if (needsOwnVerdict) {
    context.dispatch(
      boardlyApi.util.invalidateTags([
        { type: "Agent", id: context.companyId },
      ]),
    );
  }

  if (agent.reportsTo === null) {
    context.dispatch(
      activityApi.util.updateQueryData(
        "getWorkspaceCockpit",
        context.companyId,
        (cockpit) => {
          if (!cockpit) return;
          cockpit.ceo_agent = agentToAgentSummary(viewerAgent);
        },
      ),
    );
  }

  updateAgentReferences(context, agent);
}

function updateAgentStatus(
  context: RealtimeSyncContext,
  rows: AgentStatusRow[],
) {
  const availabilityByAgentId = new Map(
    rows.map((row) => [row.agentId, row.runtimeAvailable]),
  );

  context.dispatch(
    agentRuntimeApi.util.updateQueryData(
      "listCompanyAgents",
      context.companyId,
      (page) => {
        for (const agent of page.items) {
          const runtimeAvailable = availabilityByAgentId.get(agent.id);
          if (runtimeAvailable !== undefined) {
            agent.runtimeAvailable = runtimeAvailable;
            agent.access.execute = runtimeAvailable;
            if (runtimeAvailable) agent.unavailableReason = null;
          }
        }
      },
    ),
  );
  // The frame says whether each Agent can run, not why it cannot, so the
  // roster refetches to learn the reason.
  if (rows.some((row) => !row.runtimeAvailable)) {
    context.dispatch(
      boardlyApi.util.invalidateTags([
        { id: context.companyId, type: "Agent" },
      ]),
    );
  }

  context.dispatch(
    activityApi.util.updateQueryData(
      "getWorkspaceCockpit",
      context.companyId,
      (cockpit) => {
        if (!cockpit || cockpit.ceo_agent.status === "archived") return;
        const runtimeAvailable = availabilityByAgentId.get(
          cockpit.ceo_agent.id,
        );
        if (runtimeAvailable === undefined) return;
        cockpit.ceo_agent.status = runtimeAvailable ? "active" : "inactive";
      },
    ),
  );
}

function updateAgentReferences(context: RealtimeSyncContext, agent: AgentDto) {
  const state = context.getState();
  for (const argument of taskApi.util.selectCachedArgsForQuery(
    state,
    "listWorkspaceTasks",
  )) {
    if (argument.companyId !== context.companyId) continue;
    context.dispatch(
      taskApi.util.updateQueryData("listWorkspaceTasks", argument, (page) => {
        for (const task of page.items) {
          // The Agent is the one doing the work; the assignee is always
          // a member (ADR 0014).
          updateActorName(task.executedBy, agent);
          updateActorName(task.creator, agent);
        }
      }),
    );
  }
}

function updateActorName(
  actor: { id: string; kind: string; name: string },
  agent: AgentDto,
) {
  if (actor.kind === "agent" && actor.id === agent.id) {
    actor.name = agent.name;
  }
}

function updateResource(
  context: RealtimeSyncContext,
  resource: CompanyResourceDto,
) {
  context.dispatch(
    resourceApi.util.updateQueryData(
      "listWorkspaceResources",
      context.companyId,
      (page) => {
        replaceVersioned(page.items, resource);
        page.items.sort((left, right) => left.position - right.position);
      },
    ),
  );
}

function removeResource(
  context: RealtimeSyncContext,
  resourceId: string,
  revision: number,
) {
  context.dispatch(
    resourceApi.util.updateQueryData(
      "listWorkspaceResources",
      context.companyId,
      (page) => {
        removeVersioned(page.items, resourceId, revision);
      },
    ),
  );
}
