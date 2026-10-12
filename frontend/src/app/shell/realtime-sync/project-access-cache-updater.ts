import type { components as ApiComponents } from "@/shared/api/generated/api.generated";
import { projectApi } from "@/features/company-plan/api/project-api";
import { ACCOUNT_INBOX_SCOPE } from "@/features/notifications/model/notification";
import { boardlyApi } from "@/shared/api/boardly-api";
import { workspaceApi } from "@/shared/api/workspace-api";
import type { BoardlyRealtimeEvent } from "@/shared/realtime/realtime-client.service";
import { toast } from "@/shared/ui/sonner";
import { forgetTaskChats } from "@/state/slice/chat";
import { forgetConversationWorkspaceTasks } from "@/state/slice/conversation-dock";
import type { RealtimeSyncContext } from "@/app/shell/realtime-sync/realtime-sync-context";

type ProjectAccessChangedPayload =
  ApiComponents["schemas"]["ProjectAccessChangedPayload"];

type ProjectTasks = { taskIds: Set<string>; conversationIds: Set<string> };

/**
 * `project.access_changed` (ADR 0017) names only the Project: who may do
 * what on it changed. Everything read under it is read again, and the
 * Project itself is read once more; a 404 means the viewer lost it, so it
 * and its Task chats leave the caches and, when they are on it, the screen.
 */
export function applyProjectAccessCacheEvent(
  context: RealtimeSyncContext,
  event: BoardlyRealtimeEvent,
): boolean {
  if (event.type !== "project.access_changed") return false;
  const { projectId } = event.payload as Partial<ProjectAccessChangedPayload>;
  if (typeof projectId !== "string") return true;
  const { companyId } = context;
  // The Project's Tasks as the viewer last read them, taken before the
  // refetch drops them, so a lost Project also closes its Task chats.
  const tasks = cachedProjectTasks(context, projectId);
  context.dispatch(
    boardlyApi.util.invalidateTags([
      { type: "Project", id: companyId },
      { type: "Project", id: projectId },
      { type: "Project", id: `${projectId}:grants` },
      // What each Group holds on the Projects the viewer sees.
      { type: "Group", id: companyId },
      { type: "Task", id: companyId },
      { type: "Conversation", id: companyId },
      { type: "Message" },
      { type: "Run", id: companyId },
      { type: "Approval", id: companyId },
      { type: "Question", id: companyId },
      { type: "Deliverable" },
      { type: "Artifact" },
      { type: "Canvas" },
      { type: "CanvasHistory" },
      { type: "Wiki" },
      { type: "Whiteboard" },
      { type: "Inbox", id: companyId },
      { type: "Inbox", id: `${companyId}:counts` },
      // The bell, its recent list and the dock badge read the account-wide
      // Inbox, which holds this Company's items too.
      { type: "Inbox", id: ACCOUNT_INBOX_SCOPE },
      { type: "Inbox", id: `${ACCOUNT_INBOX_SCOPE}:counts` },
      { type: "Activity", id: companyId },
    ]),
  );
  context.requestRefresh(
    "tasks",
    "taskDependencies",
    "taskWorkspaces",
    "directConversations",
  );
  void confirmProjectAccess(context, projectId, tasks);
  return true;
}

// One check per Project at a time: this frame and a screen whose read of the
// Project answered 404 (`useProjectAccessLoss`) can both ask, and the viewer
// is moved and told once.
const confirming = new Map<string, Promise<boolean>>();

/**
 * Reads the Project once more; a 404 means the viewer lost it, so it and its
 * Task chats leave the caches and, when they are on it, the screen. Answers
 * whether the viewer still sees the Project.
 */
export function confirmProjectAccess(
  context: RealtimeSyncContext,
  projectId: string,
  tasks: ProjectTasks = cachedProjectTasks(context, projectId),
): Promise<boolean> {
  const key = `${context.companyId}:${projectId}`;
  const running = confirming.get(key);
  if (running) return running;
  const next = confirmProjectStillVisible(context, projectId, tasks).finally(
    () => confirming.delete(key),
  );
  confirming.set(key, next);
  return next;
}

function cachedProjectTasks(
  context: RealtimeSyncContext,
  projectId: string,
): ProjectTasks {
  const state = context.getState();
  const found: ProjectTasks = {
    taskIds: new Set(),
    conversationIds: new Set(),
  };
  for (const argument of workspaceApi.util.selectCachedArgsForQuery(
    state,
    "listWorkspaceTasks",
  )) {
    if (argument.companyId !== context.companyId) continue;
    const page =
      workspaceApi.endpoints.listWorkspaceTasks.select(argument)(state).data;
    for (const task of page?.items ?? []) {
      if (task.projectId !== projectId) continue;
      found.taskIds.add(task.id);
      found.conversationIds.add(task.conversationId);
    }
  }
  return found;
}

// A realtime project refetch refreshes the viewer's own access; only a
// 404 says they no longer see the Project at all.
async function confirmProjectStillVisible(
  context: RealtimeSyncContext,
  projectId: string,
  tasks: ProjectTasks,
): Promise<boolean> {
  const { companyId } = context;
  const result = await context.dispatch(
    projectApi.endpoints.getProject.initiate(
      { companyId, id: projectId },
      { forceRefetch: true, subscribe: false },
    ),
  );
  const status = (result.error as { status?: number | null } | undefined)
    ?.status;
  if (status !== 404) return true;
  context.dispatch(
    projectApi.util.updateQueryData("listProjects", companyId, (page) => {
      page.items = page.items.filter((project) => project.id !== projectId);
    }),
  );
  // What the viewer kept of its Task chats goes too: the in-memory
  // transcripts and drafts, and the dock tabs saved on disk.
  const taskIds = [...tasks.taskIds];
  if (taskIds.length > 0) {
    context.dispatch(forgetTaskChats(taskIds.map((id) => `task:${id}`)));
    context.dispatch(forgetConversationWorkspaceTasks({ companyId, taskIds }));
  }
  const left = context.leave?.(({ pathname, search }) => {
    if (pathname.startsWith(`/projects/${projectId}`)) return "/projects";
    const params = new URLSearchParams(search);
    const taskId = params.get("taskId");
    const conversationId = params.get("conversationId");
    const onLostTask =
      (taskId !== null && tasks.taskIds.has(taskId)) ||
      (conversationId !== null && tasks.conversationIds.has(conversationId));
    // The same page without the lost Task: Work, or the chat's home.
    return onLostTask ? pathname : null;
  });
  if (left) toast("You no longer have access to this project.");
  return false;
}
