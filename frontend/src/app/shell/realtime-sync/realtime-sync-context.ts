import type { WikiTag } from "@/shared/api/wiki-tags";
import type { AppDispatch, RootState } from "@/state/store";

export type RealtimeRefreshDomain =
  | "artifacts"
  | "channelActivity"
  | "agents"
  | "analytics"
  | "approvalStats"
  | "cockpit"
  | "connectors"
  | "directConversations"
  | "runtimes"
  | "plugins"
  | "shell"
  | "tasks"
  | "taskDependencies"
  | "taskWorkspaces";

export type RealtimeSyncContext = {
  companyId: string;
  dispatch: AppDispatch;
  getState: () => RootState;
  pathname: string;
  requestRefresh: (...domains: RealtimeRefreshDomain[]) => void;
  /**
   * Invalidates Wiki cache tags once a burst of Wiki frames settles: a Run
   * that publishes dozens of pages refetches the open views once.
   */
  requestWikiRefresh: (...tags: WikiTag[]) => void;
  /**
   * Leaves a page the viewer can no longer see: `destination` reads the
   * live location and names where to go instead, or null to stay. Answers
   * true when it left.
   */
  leave?: (
    destination: (location: {
      pathname: string;
      search: string;
    }) => string | null,
  ) => boolean;
};

export function replaceVersioned<T extends { id: string; revision: number }>(
  items: T[],
  value: T,
) {
  const index = items.findIndex((candidate) => candidate.id === value.id);
  if (index === -1) {
    items.unshift(value);
    return;
  }
  if (items[index].revision <= value.revision) {
    items[index] = value;
  }
}

export function removeVersioned<T extends { id: string; revision: number }>(
  items: T[],
  id: string,
  revision: number,
) {
  const index = items.findIndex((candidate) => candidate.id === id);
  if (index !== -1 && items[index].revision <= revision) {
    items.splice(index, 1);
  }
}
