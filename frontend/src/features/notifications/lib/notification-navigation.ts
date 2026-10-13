import { notificationService } from "@/features/notifications/api/notification.service";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import type {
  InboxItem,
  NotificationKind,
} from "@/features/notifications/model/notification";
import { TEAM_PROJECT_ACCESS_VIEW } from "@/features/company-plan/projects/access/team-access-path";
import { resolveSettingsRoute } from "@/features/settings/model/settings-navigation";
import { approvalService } from "@/shared/api/approval.service";
import { questionService } from "@/shared/api/question.service";
import type { ApprovalDto, QuestionDto } from "@/shared/api/types";
import { runDeliverablesApi } from "@/features/run-deliverables/api/run-deliverables-api";
import store from "@/state/store";

export const NOTIFICATIONS_ROUTE = "/settings/notifications";
// A Board's page, where a mention in its chat opens.
const boardPath = /^\/whiteboards\/[0-9a-f-]{36}$/i;
const INTERNAL_ORIGIN = "https://boardly.internal";
const MAX_INTERNAL_PATH_LENGTH = 1_024;

export function sanitizeNotificationPath(
  value: unknown,
  kind?: NotificationKind,
): string {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > MAX_INTERNAL_PATH_LENGTH ||
    !value.startsWith("/") ||
    value.startsWith("//") ||
    value.includes("\\")
  ) {
    return NOTIFICATIONS_ROUTE;
  }

  try {
    const url = new URL(value, INTERNAL_ORIGIN);
    if (
      url.origin !== INTERNAL_ORIGIN ||
      url.username ||
      url.password ||
      url.hash
    ) {
      return NOTIFICATIONS_ROUTE;
    }
    if (url.pathname === "/inbox" || url.pathname === "/notifications") {
      return `${NOTIFICATIONS_ROUTE}${url.search}`;
    }
    if (url.pathname === NOTIFICATIONS_ROUTE) {
      return `${url.pathname}${url.search}`;
    }
    if (isCompanyResearchWikiPath(url, kind)) {
      return `${url.pathname}${url.search}`;
    }
    if (
      url.pathname !== "/calendar" &&
      url.pathname !== "/team" &&
      url.pathname !== "/app" &&
      url.pathname !== "/approvals" &&
      url.pathname !== "/revenue" &&
      url.pathname !== "/analytics" &&
      !isScheduleDetailPath(url.pathname) &&
      !isRuntimeDetailPath(url.pathname) &&
      !boardPath.test(url.pathname)
    ) {
      return NOTIFICATIONS_ROUTE;
    }
    if (!kind || !hasRequiredNotificationTarget(url, kind)) {
      return NOTIFICATIONS_ROUTE;
    }
    return `${url.pathname}${url.search}`;
  } catch {
    return NOTIFICATIONS_ROUTE;
  }
}

// The company research Run finishes into the Wiki, not a chat: its terminal
// notices link to the Wiki home or to the overview page it wrote.
const wikiPagePath = /^\/wiki(?:\/pages\/[0-9a-f-]{36})?$/i;

export function isCompanyResearchWikiPath(
  url: URL,
  kind?: NotificationKind,
): boolean {
  return (
    (kind === "run_completed" || kind === "run_failed") &&
    wikiPagePath.test(url.pathname) &&
    !url.search
  );
}

// Whether a notice's own deep link is the Wiki target the research Run wrote.
export function isCompanyResearchWikiNotice(
  item: Pick<InboxItem, "deepLink" | "kind">,
): boolean {
  if (typeof item.deepLink !== "string") return false;
  try {
    return isCompanyResearchWikiPath(
      new URL(item.deepLink, INTERNAL_ORIGIN),
      item.kind,
    );
  } catch {
    return false;
  }
}

// The conversation a notice opens, read from its deep link.
export function notificationConversationId(
  item: Pick<InboxItem, "deepLink" | "kind">,
): string | null {
  return new URL(
    sanitizeNotificationPath(item.deepLink, item.kind),
    INTERNAL_ORIGIN,
  ).searchParams.get("conversationId");
}

export async function activateNotification({
  companyId,
  hintedPath,
  item,
  notificationId,
  onChanged,
}: {
  companyId: string;
  hintedPath?: string;
  item?: InboxItem;
  notificationId: string;
  onChanged?: () => void;
}): Promise<string> {
  const fallbackPath = item?.deepLink ?? hintedPath;
  try {
    let authoritativeItem =
      item ?? (await notificationService.get(companyId, notificationId));
    if (authoritativeItem.unread) {
      try {
        await notificationService.mutate(
          companyId,
          authoritativeItem.id,
          "read",
          authoritativeItem.revision,
        );
      } catch {
        authoritativeItem = await notificationService.get(
          companyId,
          notificationId,
        );
      }
      onChanged?.();
    }
    return resolveActivatedNotificationPath(
      companyId,
      authoritativeItem.deepLink,
      authoritativeItem.kind,
    );
  } catch {
    onChanged?.();
    return resolveActivatedNotificationPath(
      companyId,
      fallbackPath,
      item?.kind,
    );
  }
}

async function resolveActivatedNotificationPath(
  companyId: string,
  value: unknown,
  kind?: NotificationKind,
): Promise<string> {
  const path = sanitizeNotificationPath(value, kind);
  const url = new URL(path, INTERNAL_ORIGIN);
  if (
    kind === "run_completed" &&
    url.pathname === "/app" &&
    ["task", "direct"].includes(
      url.searchParams.get("conversationKind") ?? "",
    ) &&
    !url.searchParams.has("artifactId")
  ) {
    const runId = url.searchParams.get("runId");
    if (!runId) return path;
    try {
      // Older notifications identify a Run, not one of its Artifacts. Resolve
      // that Run's result once, then keep the exact Artifact in the URL.
      const result = await store
        .dispatch(
          runDeliverablesApi.endpoints.getRunResult.initiate(
            { companyId, runId },
            { subscribe: false, forceRefetch: true },
          ),
        )
        .unwrap();
      if (result.output) url.searchParams.set("artifactId", result.output.id);
      return `${url.pathname}${url.search}`;
    } catch {
      // The conversation still explains Runs without an accessible result.
      return path;
    }
  }
  if (url.pathname !== "/approvals" && url.pathname !== "/revenue") {
    return path;
  }
  try {
    if (kind === "approval_request") {
      const approvalId = url.searchParams.get("approvalId");
      if (approvalId) {
        return requestChatPath(
          await approvalService.get(approvalId, companyId),
        );
      }
    }
    if (kind === "question_request") {
      const questionId = url.searchParams.get("questionId");
      if (questionId) {
        const question = await findNotificationQuestion(companyId, questionId);
        if (question) return requestChatPath(question);
      }
    }
    return path;
  } catch {
    return path;
  }
}

async function findNotificationQuestion(companyId: string, questionId: string) {
  let cursor: string | undefined;
  const visitedCursors = new Set<string>();
  while (true) {
    const page = await questionService.list(
      {
        cursor,
        limit: 100,
        status: ["pending", "answered", "withdrawn", "expired"],
      },
      companyId,
    );
    const question = page.items.find((item) => item.id === questionId);
    if (question) return question;
    if (!page.nextCursor || visitedCursors.has(page.nextCursor))
      return undefined;
    visitedCursors.add(page.nextCursor);
    cursor = page.nextCursor;
  }
}

function requestChatPath(request: ApprovalDto | QuestionDto) {
  const isApproval = "requestedByAgent" in request;
  const agentId = isApproval
    ? request.requestedByAgent.id
    : request.askedByAgent.id;
  const href = createChatWorkspaceHref(
    request.taskId
      ? {
          agentId,
          conversationId: request.conversationId,
          kind: "task",
          taskId: request.taskId,
        }
      : {
          agentId,
          conversationId: request.conversationId,
          kind: "direct",
        },
  );
  return `${href}&${isApproval ? "approvalId" : "questionId"}=${encodeURIComponent(request.id)}`;
}

// The runtime detail is the only settings route a notification may open, and
// only the machine-offline item may open it.
function isRuntimeDetailPath(pathname: string): boolean {
  return Boolean(resolveSettingsRoute(pathname).runtimeId);
}

function isScheduleDetailPath(pathname: string): boolean {
  return /^\/scheduled\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
    pathname,
  );
}

function hasRequiredNotificationTarget(
  url: URL,
  kind: NotificationKind,
): boolean {
  const params = url.searchParams;
  if (kind === "discussion_mention" && boardPath.test(url.pathname)) {
    // A Board's chat opens on its Board, at the message or thread.
    const allowed = new Set([
      "companyId",
      "conversationId",
      "messageId",
      "threadRootId",
    ]);
    return (
      [...params.keys()].every((key) => allowed.has(key)) &&
      ["companyId", "conversationId", "messageId"].every((key) =>
        /^[0-9a-f-]{36}$/i.test(params.get(key) ?? ""),
      ) &&
      (!params.has("threadRootId") ||
        /^[0-9a-f-]{36}$/i.test(params.get("threadRootId") ?? ""))
    );
  }
  if (kind === "discussion_mention") {
    return (
      url.pathname === "/app" &&
      params.get("conversationKind") === "discussion" &&
      ["channel", "human_direct"].includes(
        params.get("discussionType") ?? "",
      ) &&
      /^[0-9a-f-]{36}$/i.test(params.get("conversationId") ?? "") &&
      /^[0-9a-f-]{36}$/i.test(params.get("messageId") ?? "")
    );
  }
  if (kind === "calendar_reminder" || kind === "calendar_update") {
    return (
      url.pathname === "/calendar" &&
      ((kind === "calendar_update" && !params.has("eventId")) ||
        /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
          params.get("eventId") ?? "",
        )) &&
      /^\d{4}-\d{2}-\d{2}$/.test(params.get("date") ?? "") &&
      [...params.keys()].every((key) => key === "eventId" || key === "date")
    );
  }
  if (kind === "invitation_renewal")
    return url.pathname === "/team" && !url.search;
  // Someone can now see a Project through a Group (Access TECH D3): the
  // notice opens that Project's access on the Team page.
  if (kind === "project_access_granted") {
    return (
      url.pathname === "/team" &&
      params.get("view") === TEAM_PROJECT_ACCESS_VIEW &&
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
        params.get("project") ?? "",
      ) &&
      [...params.keys()].every((key) => key === "view" || key === "project")
    );
  }
  if (kind === "runtime_offline") {
    return isRuntimeDetailPath(url.pathname);
  }
  if (kind === "blocker" && isScheduleDetailPath(url.pathname)) {
    return !url.search;
  }
  // Every notice about a Task links to the Task alone (ADR 0014).
  if (
    kind === "task_assigned" ||
    kind === "blocker" ||
    kind === "task_run_requested" ||
    kind === "task_handoff"
  ) {
    return url.pathname === "/app" && Boolean(params.get("taskId"));
  }
  if (kind === "analytics_budget") {
    return url.pathname === "/analytics";
  }
  if (kind === "huddle_missed") {
    return (
      url.pathname === "/app" &&
      params.get("conversationKind") === "discussion" &&
      (params.get("discussionType") === "human_direct" ||
        params.get("discussionType") === "channel") &&
      Boolean(params.get("conversationId"))
    );
  }
  if (kind === "approval_request" || kind === "question_request") {
    if (
      !params.get(kind === "approval_request" ? "approvalId" : "questionId")
    ) {
      return false;
    }
    if (url.pathname === "/app") {
      return Boolean(
        params.get("conversationId") &&
        params.get("agentId") &&
        (params.get("conversationKind") === "direct" ||
          (params.get("conversationKind") === "task" && params.get("taskId"))),
      );
    }
    return (
      (url.pathname === "/approvals" &&
        params.get("tab") ===
          (kind === "approval_request" ? "approvals" : "questions")) ||
      (kind === "approval_request" && url.pathname === "/revenue")
    );
  }
  return (
    url.pathname === "/app" &&
    Boolean(params.get("conversationId")) &&
    Boolean(params.get("agentId")) &&
    Boolean(params.get("runId"))
  );
}
