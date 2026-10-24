import { zenithTeamRealtimeClient } from "@/shared/realtime/realtime-client.service";
import { subscribeToRefetchSignals } from "@/shared/realtime/refetch-signals";

export function watchPagePreviews(
  scope: {
    companyId: string;
    pageId: string;
    generationId?: string;
    runId?: string;
  },
  refresh: () => Promise<unknown>,
) {
  let stopped = false;
  let fetching = false;
  let pending = false;
  async function drain() {
    if (stopped || fetching || document.hidden) return;
    fetching = true;
    pending = false;
    try {
      await refresh();
    } catch {
      /* The query owns error presentation. */
    } finally {
      fetching = false;
      if (pending) void drain();
    }
  }
  const request = () => {
    pending = true;
    void drain();
  };
  const removeEvents = zenithTeamRealtimeClient.onEvent((event) => {
    if (event.companyId !== scope.companyId) return;
    if (
      event.type === "canvas.page_preview_changed" &&
      event.payload.pageId === scope.pageId &&
      (!scope.generationId ||
        event.payload.generationId === scope.generationId) &&
      (!scope.runId || event.payload.runId === scope.runId)
    )
      request();
    else if (
      event.type.startsWith("auth.") ||
      event.type.startsWith("project.") ||
      event.type === "conversation.discussion_resync" ||
      event.type === "conversation.discussion_access_revoked" ||
      event.type === "artifact.access_changed"
    )
      request();
  });
  const removeSignals = subscribeToRefetchSignals(request);
  const removeStatus = zenithTeamRealtimeClient.onStatus((status) => {
    if (status.activeCompanyId === scope.companyId && status.status === "ready")
      request();
  });
  const timer = window.setInterval(request, 30000);
  // Covers a hint received between the initial HTTP read and listener setup.
  request();
  return () => {
    stopped = true;
    clearInterval(timer);
    removeEvents();
    removeSignals();
    removeStatus();
  };
}
