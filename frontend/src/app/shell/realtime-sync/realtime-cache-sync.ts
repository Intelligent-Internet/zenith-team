import type { WikiTag } from "@/shared/api/wiki-tags";
import { zenithTeamApi } from "@/shared/api/zenith-team-api";
import type { ZenithTeamRealtimeEvent } from "@/shared/realtime/realtime-client.service";
import { realtimeEventConversationId } from "@/shared/realtime/realtime-client.service";
import { applyAuthCacheEvent } from "@/app/shell/realtime-sync/auth-cache-updater";
import { applyConversationCacheEvent } from "@/app/shell/realtime-sync/conversation-cache-updater";
import { applyEntityCacheEvent } from "@/app/shell/realtime-sync/entity-cache-updater";
import { applyHumanInputCacheEvent } from "@/app/shell/realtime-sync/human-input-cache-updater";
import { applyInboxCacheEvent } from "@/app/shell/realtime-sync/inbox-cache-updater";
import { applyProjectAccessCacheEvent } from "@/app/shell/realtime-sync/project-access-cache-updater";
import {
  type RealtimeRefreshDomain,
  type RealtimeSyncContext,
} from "@/app/shell/realtime-sync/realtime-sync-context";
import { applyWorkCacheEvent } from "@/app/shell/realtime-sync/work-cache-updater";
import type { AppDispatch } from "@/state/store";

const refreshBatchDelayMs = 75;
// A Run that publishes dozens of Wiki pages emits one frame per page.
const wikiRefreshQuietMs = 400;
const wikiRefreshMaxWaitMs = 2000;

export function applyRealtimeCacheEvent(
  context: RealtimeSyncContext,
  event: ZenithTeamRealtimeEvent,
) {
  // An open Board catches up from its own subscription; its sharing is the
  // Whiteboard Artifact's, refreshed below with every other Artifact.
  if (event.type === "whiteboard.changed") return;
  // A new title names the Whiteboard in Artifact lists too; an open Board
  // re-reads its own header from its subscription.
  if (event.type === "whiteboard.renamed") {
    context.requestRefresh("artifacts");
    return;
  }
  if (event.type === "calendar.changed") {
    context.dispatch(
      zenithTeamApi.util.invalidateTags([{ type: "Calendar", id: "events" }]),
    );
    return;
  }
  if (event.type === "schedule.changed") {
    context.dispatch(
      zenithTeamApi.util.invalidateTags([
        { type: "Schedule", id: context.companyId },
      ]),
    );
    return;
  }
  // A Group was created, renamed, filled, emptied or deleted. The frame
  // names only the Group: the Groups and every Project grant
  // list (which shows Group names and member counts) read again, and so
  // does the feed, where the change is recorded. Access a change moves
  // arrives as project.access_changed.
  if (event.type === "group.changed" || event.type === "group.deleted") {
    context.dispatch(
      zenithTeamApi.util.invalidateTags([
        { type: "Group", id: context.companyId },
        { type: "Activity", id: context.companyId },
      ]),
    );
    return;
  }
  if (event.type === "conversation.discussion_resync") {
    context.dispatch(
      zenithTeamApi.util.invalidateTags([
        { type: "ChannelAgent", id: context.companyId },
        { type: "AgentSkill", id: context.companyId },
        { type: "ChannelAgentActivity", id: context.companyId },
        { type: "DiscussionDetail" },
        { type: "DiscussionList" },
        { type: "Artifact" },
        { type: "Deliverable" },
      ]),
    );
    return;
  }
  if (event.type === "conversation.channel_agents_changed") {
    const id = realtimeEventConversationId(event);
    context.requestRefresh("channelActivity");
    if (id && event.payload.activityOnly !== true) {
      context.dispatch(
        zenithTeamApi.util.invalidateTags([{ type: "ChannelAgent", id }]),
      );
    }
    return;
  }
  if (
    event.type === "conversation.discussion_changed" ||
    event.type === "conversation.discussion_access_revoked"
  ) {
    const id = realtimeEventConversationId(event);
    if (id)
      context.dispatch(
        zenithTeamApi.util.invalidateTags([
          { type: "ChannelAgent", id },
          { type: "ChannelAgentActivity", id },
        ]),
      );
  }
  if (event.type === "runtime.capabilities_changed") {
    context.requestRefresh("connectors", "plugins");
    return;
  }
  if (event.type === "connector.policy_changed") {
    context.requestRefresh("connectors");
    return;
  }
  if (
    event.type === "canvas.document_changed" ||
    event.type === "canvas.changed"
  ) {
    context.requestRefresh("artifacts");
  }
  if (event.type === "artifact.changed") {
    context.requestRefresh("artifacts");
    return;
  }
  if (
    event.type === "agent.status_changed" ||
    event.type === "agent.binding_changed" ||
    event.type === "agent.updated" ||
    event.type === "daemon.roster_changed"
  ) {
    context.dispatch(
      zenithTeamApi.util.invalidateTags([
        { type: "ChannelAgent", id: context.companyId },
        { type: "AgentSkill", id: context.companyId },
        { type: "Connector", id: context.companyId },
        { type: "Plugin", id: context.companyId },
      ]),
    );
  }
  if (
    event.type === "conversation.discussion_access_revoked" ||
    event.type === "conversation.discussion_access_granted"
  ) {
    const id = realtimeEventConversationId(event);
    context.dispatch(
      zenithTeamApi.util.invalidateTags([
        ...(id ? [{ type: "DiscussionDetail" as const, id }] : []),
        { type: "Artifact" },
        { type: "Deliverable" },
        { type: "Canvas" },
        { type: "CanvasHistory" },
        // A chat's people read the Whiteboards shared into it.
        { type: "Whiteboard" },
      ]),
    );
  }
  if (event.type === "artifact.access_changed") {
    context.dispatch(
      zenithTeamApi.util.invalidateTags([
        { type: "Artifact" },
        { type: "Deliverable" },
        { type: "Canvas" },
        { type: "CanvasHistory" },
        // An open Whiteboard re-reads whether this member may edit or share it.
        { type: "Whiteboard" },
        { id: context.companyId, type: "Inbox" },
        { id: `${context.companyId}:counts`, type: "Inbox" },
        { id: context.companyId, type: "Activity" },
      ]),
    );
    return;
  }
  if (
    event.type === "task.report_ready" ||
    event.type === "task.report_changed"
  ) {
    context.dispatch(
      zenithTeamApi.util.invalidateTags([
        { id: context.companyId, type: "TaskReport" },
      ]),
    );
    return;
  }
  if (event.type === "inbox.created" || event.type === "inbox.updated") {
    applyInboxCacheEvent(
      { ...context, companyId: event.companyId ?? "" },
      event,
    );
    return;
  }
  if (event.type === "activity.created") {
    context.dispatch(
      zenithTeamApi.util.invalidateTags([
        { id: context.companyId, type: "Activity" },
        { type: "Deliverable" },
      ]),
    );
  }
  if (event.type === "run.studio_canvas_added") {
    context.requestRefresh("artifacts");
  }
  if (applyProjectAccessCacheEvent(context, event)) return;
  if (applyAuthCacheEvent(context, event)) return;
  if (applyEntityCacheEvent(context, event)) return;
  if (applyWorkCacheEvent(context, event)) return;
  if (applyConversationCacheEvent(context, event)) return;
  if (applyHumanInputCacheEvent(context, event)) return;
  applyInboxCacheEvent(context, event);
}

export function createRealtimeRefreshQueue({
  companyId,
  dispatch,
}: {
  companyId: string;
  dispatch: AppDispatch;
}) {
  const pending = new Set<RealtimeRefreshDomain>();
  let timer: number | undefined;
  const pendingWiki = new Map<string, WikiTag>();
  let wikiTimer: number | undefined;
  let wikiBurstStartedAt = 0;

  const flush = () => {
    timer = undefined;
    const domains = new Set(pending);
    pending.clear();
    refreshRealtimeDomains({ companyId, dispatch, domains });
  };
  const flushWiki = () => {
    wikiTimer = undefined;
    const tags = [...pendingWiki.values()];
    pendingWiki.clear();
    dispatch(zenithTeamApi.util.invalidateTags(tags));
  };

  return {
    dispose() {
      if (timer !== undefined) window.clearTimeout(timer);
      timer = undefined;
      pending.clear();
      if (wikiTimer !== undefined) window.clearTimeout(wikiTimer);
      wikiTimer = undefined;
      pendingWiki.clear();
    },
    request(...domains: RealtimeRefreshDomain[]) {
      domains.forEach((domain) => pending.add(domain));
      if (timer === undefined) {
        timer = window.setTimeout(flush, refreshBatchDelayMs);
      }
    },
    // Waits for the burst to go quiet, but never longer than the cap, so a
    // steady trickle of Wiki frames still refreshes.
    requestWiki(...tags: WikiTag[]) {
      tags.forEach((tag) => pendingWiki.set(tag.id, tag));
      const now = Date.now();
      if (wikiTimer === undefined) {
        wikiBurstStartedAt = now;
      } else {
        window.clearTimeout(wikiTimer);
      }
      const remaining = wikiBurstStartedAt + wikiRefreshMaxWaitMs - now;
      wikiTimer = window.setTimeout(
        flushWiki,
        Math.max(0, Math.min(wikiRefreshQuietMs, remaining)),
      );
    },
  };
}

export function convergeRealtimeCaches(
  dispatch: AppDispatch,
  companyId: string,
) {
  dispatch(
    zenithTeamApi.util.invalidateTags([
      { type: "Account" },
      { id: companyId, type: "Analytics" },
      { type: "Studio" },
      { id: companyId, type: "Activity" },
      { id: companyId, type: "Agent" },
      { id: companyId, type: "AgentSkill" },
      { type: "Approval" },
      { type: "Artifact" },
      { id: companyId, type: "Company" },
      { id: companyId, type: "Connector" },
      { id: companyId, type: "Plugin" },
      { id: companyId, type: "Conversation" },
      { type: "Deliverable" },
      { id: "", type: "Inbox" },
      { id: ":counts", type: "Inbox" },
      { id: companyId, type: "Inbox" },
      { id: `${companyId}:counts`, type: "Inbox" },
      { id: companyId, type: "Invitation" },
      { id: companyId, type: "Member" },
      { id: companyId, type: "ProactiveMode" },
      { id: companyId, type: "ProjectResource" },
      { type: "Question" },
      { id: companyId, type: "Runtime" },
      { id: companyId, type: "Task" },
      { id: companyId, type: "TaskReport" },
      { type: "Run" },
      { type: "AgentBinding" },
      { type: "Calendar" },
      { type: "ChannelAgent" },
      { type: "ChannelAgentActivity" },
      { type: "DiscussionDetail" },
      { type: "DiscussionList" },
      { type: "Wiki" },
      { type: "Whiteboard" },
      { type: "Schedule" },
      { type: "Project" },
      { type: "Workflow" },
      { type: "Mailbox" },
      { type: "AgentMail" },
    ]),
  );
}

function refreshRealtimeDomains({
  companyId,
  dispatch,
  domains,
}: {
  companyId: string;
  dispatch: AppDispatch;
  domains: Set<RealtimeRefreshDomain>;
}) {
  if (domains.has("shell")) {
    convergeRealtimeCaches(dispatch, companyId);
    return;
  }
  const tags: Record<
    Exclude<RealtimeRefreshDomain, "shell">,
    Parameters<typeof zenithTeamApi.util.invalidateTags>[0]
  > = {
    agents: [{ type: "Agent", id: `${companyId}:roster` }],
    analytics: [{ type: "Analytics", id: companyId }],
    approvalStats: [{ type: "Approval", id: `${companyId}:stats` }],
    artifacts: [{ type: "Artifact" }, { type: "Deliverable" }],
    channelActivity: [{ type: "ChannelAgentActivity", id: companyId }],
    cockpit: [{ type: "Company", id: `${companyId}:cockpit` }],
    connectors: [{ type: "Connector", id: companyId }],
    directConversations: [{ type: "Conversation", id: companyId }],
    runtimes: [{ type: "Runtime", id: companyId }],
    plugins: [{ type: "Plugin", id: companyId }],
    tasks: [
      { type: "Task", id: `${companyId}:list` },
      { type: "Calendar", id: `${companyId}:tasks` },
    ],
    taskDependencies: [{ type: "Task", id: `${companyId}:dependencies` }],
    taskWorkspaces: [
      { type: "Task", id: `${companyId}:workspace` },
      { type: "Deliverable", id: `${companyId}:results` },
    ],
  };
  // Delayed tag invalidation waits for older in-flight responses, then reads
  // only subscribed queries. Closed screens are evicted without issuing GETs.
  dispatch(
    zenithTeamApi.util.invalidateTags(
      [...domains].flatMap((domain) =>
        domain === "shell" ? [] : tags[domain],
      ),
    ),
  );
}
