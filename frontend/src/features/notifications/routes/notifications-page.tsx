import { useListWorkspaceDirectConversationsQuery } from "@/shared/api/conversation-api";
import { activateAccountNotification } from "@/features/notifications/lib/account-notification-navigation";
import { ACCOUNT_INBOX_SCOPE } from "@/features/notifications/model/notification";
import "@/features/notifications/styles/notifications.css";
import {
  Archive,
  ArchiveRestore,
  ArrowUpRight,
  Inbox,
  Check,
  CheckCheck,
  ChevronRight,
  Circle,
  RefreshCcw,
  WifiOff,
} from "@/shared/ui/icons";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";

import { readSessionSnapshot } from "@/shared/session/session";
import {
  useGetNotificationCountsQuery,
  useLazyListNotificationsQuery,
  useListNotificationsQuery,
  useMutateNotificationMutation,
  useReadAllNotificationsMutation,
} from "@/features/notifications/api/notifications-api";
import { NotificationKindLabel } from "@/features/notifications/components/notification-kind-label";
import { useNoticeBody } from "@/features/notifications/hooks/use-notice-body";
import {
  isCompanyResearchWikiNotice,
  notificationConversationId,
} from "@/features/notifications/lib/notification-navigation";
import {
  type InboxFilterKind,
  type InboxItem,
  isNotificationKind,
  neutralNotice,
  type NotificationKind,
  notificationKindLabels,
  notificationKinds,
  type NotificationMutation,
  type NotificationStatus,
} from "@/features/notifications/model/notification";
import { cx } from "@/shared/lib/cx";

const statusTabs: Array<{ label: string; value: NotificationStatus }> = [
  { label: "Active", value: "active" },
  { label: "Unread", value: "unread" },
  { label: "Archived", value: "archived" },
  { label: "All", value: "all" },
];

export function NotificationsPage() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const companyId = ACCOUNT_INBOX_SCOPE;
  const status = parseStatus(searchParams.get("status"));
  const kind = parseKind(searchParams.get("kind"));
  const pageKey = `${companyId}:${status}:${kind ?? "any"}`;
  const listQuery = useListNotificationsQuery({
    companyId,
    kind,
    limit: 30,
    status,
  });
  const currentCompanyId = readSessionSnapshot().company?.id ?? "";
  const conversationsQuery = useListWorkspaceDirectConversationsQuery(
    currentCompanyId,
    { skip: !currentCompanyId },
  );
  const conversationTitles = new Map(
    (conversationsQuery.currentData?.items ?? []).map((conversation) => [
      conversation.id,
      conversation.title,
    ]),
  );
  const noticeBody = useNoticeBody();
  const countsQuery = useGetNotificationCountsQuery(companyId);
  const [loadNotificationsPage] = useLazyListNotificationsQuery();
  const [mutateItem] = useMutateNotificationMutation();
  const [readAll, readAllState] = useReadAllNotificationsMutation();
  const [items, setItems] = useState<InboxItem[]>([]);
  const [itemsPageKey, setItemsPageKey] = useState(pageKey);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [selectedId, setSelectedId] = useState<string>();
  const [mutationId, setMutationId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const isOnline = useOnlineStatus();
  const activePageKeyRef = useRef(pageKey);
  activePageKeyRef.current = pageKey;
  const visibleItems = itemsPageKey === pageKey ? items : [];

  useEffect(() => {
    setItems([]);
    setItemsPageKey(pageKey);
    setNextCursor(null);
    setLoadingMore(false);
    setMutationId(null);
    setActionError(null);
  }, [pageKey]);

  useEffect(() => {
    if (!listQuery.currentData) {
      return;
    }
    setItems(listQuery.currentData.items);
    setItemsPageKey(pageKey);
    setNextCursor(listQuery.currentData.nextCursor);
  }, [listQuery.currentData, pageKey]);

  const refresh = useCallback(() => {
    setActionError(null);
    void listQuery.refetch();
    void countsQuery.refetch();
  }, [countsQuery, listQuery]);

  const changeStatus = useCallback(
    (nextStatus: NotificationStatus) => {
      const next = new URLSearchParams(searchParams);
      if (nextStatus === "active") {
        next.delete("status");
      } else {
        next.set("status", nextStatus);
      }
      setItems([]);
      setNextCursor(null);
      setActionError(null);
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams],
  );

  const changeKind = useCallback(
    (nextKind: InboxFilterKind | undefined) => {
      const next = new URLSearchParams(searchParams);
      if (nextKind) {
        next.set("kind", nextKind);
      } else {
        next.delete("kind");
      }
      setItems([]);
      setNextCursor(null);
      setActionError(null);
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams],
  );

  const applyMutation = useCallback(
    async (item: InboxItem, mutation: NotificationMutation) => {
      const mutationPageKey = pageKey;
      setMutationId(item.id);
      setActionError(null);
      try {
        const result = await mutateItem({
          companyId,
          expectedRevision: item.revision,
          itemId: item.id,
          mutation,
        }).unwrap();
        if (activePageKeyRef.current !== mutationPageKey) {
          return;
        }
        const { counts: _counts, ...updated } = result;
        setItems((current) => updateVisibleItems(current, updated, status));
      } catch (error) {
        if (activePageKeyRef.current !== mutationPageKey) {
          return;
        }
        setActionError(
          getMutationErrorMessage(error) ??
            "Notification could not be updated. Refresh and try again.",
        );
      } finally {
        if (activePageKeyRef.current === mutationPageKey) {
          setMutationId(null);
        }
      }
    },
    [companyId, mutateItem, pageKey, status],
  );

  const openItem = useCallback(
    (item: InboxItem) => {
      void activateAccountNotification({
        notificationId: item.id,
      }).then((path) => {
        if (path) navigate(path);
      });
    },
    [navigate],
  );

  const loadMore = useCallback(async () => {
    if (!nextCursor || loadingMore) {
      return;
    }
    const requestedPageKey = pageKey;
    setLoadingMore(true);
    setActionError(null);
    try {
      const next = await loadNotificationsPage({
        companyId,
        cursor: nextCursor,
        kind,
        limit: 30,
        status,
      }).unwrap();
      if (activePageKeyRef.current !== requestedPageKey) {
        return;
      }
      setItems((current) => mergeInboxItems(current, next.items));
      setNextCursor(next.nextCursor);
    } catch {
      if (activePageKeyRef.current !== requestedPageKey) {
        return;
      }
      setActionError("More notifications could not be loaded. Try again.");
    } finally {
      if (activePageKeyRef.current === requestedPageKey) {
        setLoadingMore(false);
      }
    }
  }, [
    companyId,
    kind,
    loadNotificationsPage,
    loadingMore,
    nextCursor,
    pageKey,
    status,
  ]);

  const unreadCount = countsQuery.currentData?.unreadCount ?? 0;
  const selectedItem =
    visibleItems.find((item) => item.id === selectedId) ?? visibleItems[0];
  const selectedConversationTitle = selectedItem
    ? conversationTitles.get(notificationConversationId(selectedItem) ?? "")
    : undefined;
  const pageState = useMemo(() => {
    if (listQuery.isLoading && visibleItems.length === 0) return "loading";
    if (listQuery.isError && visibleItems.length === 0) return "error";
    if (visibleItems.length === 0) return "empty";
    return "ready";
  }, [listQuery.isError, listQuery.isLoading, visibleItems.length]);

  return (
    <div
      className="workspace-inbox-page flex h-[min(72vh,800px)] min-h-80 flex-col gap-4"
      aria-label="Notifications"
    >
      <div className="flex flex-wrap items-center justify-end gap-2">
        <button
          className="dashboard-button"
          disabled={unreadCount === 0 || readAllState.isLoading || !isOnline}
          type="button"
          onClick={() => void readAll(companyId)}
        >
          <CheckCheck className="size-4" aria-hidden />
          Mark all read
        </button>
        <button
          aria-label="Refresh notifications"
          className="dashboard-button"
          title="Refresh notifications"
          type="button"
          onClick={refresh}
        >
          <RefreshCcw
            aria-hidden
            className={cx(
              "size-[18px] shrink-0",
              (listQuery.isFetching || countsQuery.isFetching) &&
                "animate-spin",
            )}
          />
          Refresh
        </button>
      </div>

      <div
        className="workspace-inbox-layout"
        data-empty={pageState !== "ready"}
      >
        <aside className="workspace-inbox-list" aria-label="Inbox updates">
          <div className="workspace-inbox-filters">
            <div
              className="flex items-center gap-1"
              role="group"
              aria-label="Filter notifications"
            >
              {statusTabs.map((tab) => (
                <button
                  aria-pressed={status === tab.value}
                  className={cx(
                    "rounded-xl px-4 py-2 text-sm font-bold transition",
                    status === tab.value
                      ? "bg-white/14 text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.14)]"
                      : "text-white/50 hover:bg-white/[0.07] hover:text-white/76",
                  )}
                  key={tab.value}
                  type="button"
                  onClick={() => changeStatus(tab.value)}
                >
                  {tab.label}
                  {tab.value === "unread" && unreadCount > 0 ? (
                    <span className="ml-2 rounded-full bg-[var(--workspace-notification-bg,#b6404d)] px-2 py-0.5 text-[11px] text-[var(--workspace-notification-foreground,#fff)]">
                      {unreadCount}
                    </span>
                  ) : null}
                </button>
              ))}
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2 px-2">
              <p className="m-0 text-xs font-semibold text-white/40">
                {unreadCount === 0
                  ? "No unread notifications"
                  : `${unreadCount} unread`}
              </p>
              <select
                aria-label="Kind"
                className="min-h-[28px] p-[2px_8px] [border:1px_solid_var(--workspace-divider-strong)] rounded-[6px] [background:var(--workspace-input-bg)] text-workspace-text text-workspace-caption leading-workspace-caption font-[500]"
                value={kind ?? ""}
                onChange={(event) => changeKind(parseKind(event.target.value))}
              >
                <option value="">All kinds</option>
                {notificationKinds.map((value) => (
                  <option key={value} value={value}>
                    {notificationKindLabels[value]}
                  </option>
                ))}
              </select>
            </div>
          </div>

          {!isOnline ? (
            <div
              className="workspace-inbox-notice workspace-inbox-notice--offline flex items-center gap-3 px-4 py-3 text-sm font-semibold"
              role="status"
            >
              <WifiOff className="size-4 shrink-0" aria-hidden />
              You’re offline. Saved notifications remain visible; changes will
              be available after reconnecting.
            </div>
          ) : null}
          {actionError ? (
            <div
              className="workspace-inbox-notice workspace-inbox-notice--error px-4 py-3 text-sm font-semibold"
              role="alert"
            >
              {actionError}
            </div>
          ) : null}

          <div className="workspace-inbox-items">
            <NotificationHistoryState
              conversationTitles={conversationTitles}
              noticeBody={noticeBody}
              items={visibleItems}
              kind={kind}
              pageState={pageState}
              status={status}
              mutationId={mutationId}
              online={isOnline}
              onMutate={applyMutation}
              onOpen={(item) => setSelectedId(item.id)}
              selectedId={selectedItem?.id}
              onRetry={refresh}
            />
            {pageState === "ready" && nextCursor ? (
              <div className="border-t border-white/10 p-4 text-center">
                <button
                  className="inline-flex h-10 items-center gap-2 rounded-xl border border-white/14 bg-white/[0.06] px-5 text-sm font-bold text-white/66 transition hover:bg-white/12 hover:text-white disabled:opacity-45"
                  disabled={loadingMore || !isOnline}
                  type="button"
                  onClick={() => void loadMore()}
                >
                  {loadingMore ? "Loading…" : "Load older notifications"}
                  {!loadingMore ? (
                    <ChevronRight className="size-4 rotate-90" aria-hidden />
                  ) : null}
                </button>
              </div>
            ) : null}
          </div>
        </aside>
        {pageState === "ready" ? (
          <section
            className="workspace-inbox-detail"
            aria-label="Notification details"
            aria-live="polite"
          >
            {selectedItem ? (
              <>
                <div className="flex items-center gap-[14px] text-workspace-caption leading-workspace-caption text-workspace-muted">
                  <NotificationKindLabel kind={selectedItem.kind} />
                  <time dateTime={selectedItem.createdAt}>
                    {formatNotificationDate(selectedItem.createdAt)}
                  </time>
                </div>
                <h2>{selectedItem.title}</h2>
                <p>{noticeBody(selectedItem)}</p>
                <div className="flex flex-wrap gap-[8px] m-[26px_0]">
                  <button
                    className="dashboard-button dashboard-button-primary"
                    disabled={!isOnline}
                    onClick={() => openItem(selectedItem)}
                  >
                    {notificationTargetLabel(selectedItem)}
                    <ArrowUpRight size={15} />
                  </button>
                  <button
                    className="dashboard-button"
                    disabled={!isOnline || mutationId === selectedItem.id}
                    onClick={() =>
                      void applyMutation(
                        selectedItem,
                        selectedItem.archivedAt ? "restore" : "archive",
                      )
                    }
                  >
                    <Archive size={14} />
                    {selectedItem.archivedAt ? "Restore" : "Archive"}
                  </button>
                </div>
                {selectedConversationTitle &&
                !selectedItem.title.includes(selectedConversationTitle) ? (
                  <div className="workspace-inbox-context mt-[26px] pt-[20px] [border-top:1px_solid_var(--workspace-divider)]">
                    <h3>Current conversation title</h3>
                    <p>{selectedConversationTitle}</p>
                  </div>
                ) : null}
              </>
            ) : (
              <div className="grid [place-content:center] [justify-items:center] h-full text-center text-workspace-muted">
                <Inbox size={28} />
                <h2>Notification details</h2>
                <p>Select an update to view its details here.</p>
              </div>
            )}
          </section>
        ) : null}
      </div>
    </div>
  );
}

function NotificationHistoryState({
  conversationTitles,
  noticeBody,
  items,
  kind,
  mutationId,
  online,
  onMutate,
  onOpen,
  onRetry,
  pageState,
  status,
  selectedId,
}: {
  conversationTitles: Map<string, string | null>;
  noticeBody: (item: InboxItem) => string;
  selectedId?: string;
  items: InboxItem[];
  kind: InboxFilterKind | undefined;
  mutationId: string | null;
  online: boolean;
  onMutate: (item: InboxItem, mutation: NotificationMutation) => void;
  onOpen: (item: InboxItem) => void;
  onRetry: () => void;
  pageState: "loading" | "error" | "empty" | "ready";
  status: NotificationStatus;
}) {
  if (pageState === "loading") {
    return (
      <HistoryMessage
        icon={<RefreshCcw className="size-5 animate-spin" />}
        text="Loading notifications…"
      />
    );
  }
  if (pageState === "error") {
    return (
      <HistoryMessage
        action="Try again"
        icon={<WifiOff className="size-5" />}
        text={
          online
            ? "Notifications could not be loaded."
            : "Connect to the internet to refresh notifications."
        }
        onAction={onRetry}
      />
    );
  }
  if (pageState === "empty") {
    const copy = kind
      ? `No ${notificationKindLabels[kind].toLowerCase()} notifications in this view.`
      : status === "archived"
        ? "Archived notifications will appear here."
        : status === "unread"
          ? "You’ve read every notification in this Company."
          : "There are no notifications in this Company yet.";
    return (
      <HistoryMessage icon={<CheckCheck className="size-5" />} text={copy} />
    );
  }
  const groups = new Map<string, InboxItem[]>();
  for (const item of items) {
    const conversationId = notificationConversationId(item);
    const key =
      conversationId &&
      (item.kind === "run_completed" || item.kind === "run_failed")
        ? `${conversationId}:${item.kind}:${item.title}:${item.body}`
        : item.id;
    const group = groups.get(key) ?? [];
    group.push(item);
    groups.set(key, group);
  }
  const renderRow = (item: InboxItem) => (
    <NotificationHistoryRow
      body={noticeBody(item)}
      busy={mutationId === item.id}
      selected={selectedId === item.id}
      item={item}
      key={item.id}
      online={online}
      onMutate={onMutate}
      onOpen={onOpen}
      context={
        conversationTitles.get(notificationConversationId(item) ?? "") ??
        undefined
      }
    />
  );
  return (
    <ul className="m-0 list-none p-0">
      {[...groups.values()].map((group) => (
        <li
          key={group[0].id}
          className="workspace-inbox-group [list-style:none] [border-bottom:1px_solid_var(--workspace-divider)]"
        >
          <ul>{renderRow(group[0])}</ul>
          {group.length > 1 ? (
            <details
              open={
                group.slice(1).some((item) => item.id === selectedId) ||
                undefined
              }
            >
              <summary>
                {earlierSummary(group.length - 1, group[0].kind)}
                {group.slice(1).some((item) => item.unread) ? " · Unread" : ""}
              </summary>
              <ul>{group.slice(1).map(renderRow)}</ul>
            </details>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

function earlierSummary(count: number, kind: InboxItem["kind"]) {
  const noun =
    kind === "run_completed"
      ? count === 1
        ? "reply"
        : "replies"
      : count === 1
        ? "update"
        : "updates";
  return `${count} earlier ${noun} in this conversation`;
}

function notificationTargetLabel(item: InboxItem) {
  if (item.kind === "calendar_reminder" || item.kind === "calendar_update")
    return "Open calendar";
  if (notificationConversationId(item)) return "Open conversation";
  if (isCompanyResearchWikiNotice(item)) return "Open Wiki";
  if (item.kind === "approval_request") return "Review approval";
  if (item.kind === "question_request") return "View question";
  if (item.kind === "invitation_renewal") return "Review invitation";
  if (item.kind === "runtime_offline") return "View computer";
  if (item.kind === "analytics_budget") return "Open Analytics";
  if (item.kind === "project_access_granted") return "Open access";
  return "View details";
}

function NotificationHistoryRow({
  body,
  context,
  selected,
  busy,
  item,
  online,
  onMutate,
  onOpen,
}: {
  body: string;
  context?: string;
  selected: boolean;
  busy: boolean;
  item: InboxItem;
  online: boolean;
  onMutate: (item: InboxItem, mutation: NotificationMutation) => void;
  onOpen: (item: InboxItem) => void;
}) {
  return (
    <li
      data-selected={selected}
      className={cx(
        "workspace-inbox-row",
        "relative grid gap-4 px-5 py-5 transition md:grid-cols-[1fr_auto] md:px-6",
        !item.unread
          ? "bg-transparent"
          : neutralNotice(item.kind)
            ? "bg-[color-mix(in_srgb,var(--workspace-native-text-muted,#a3a3a3)_8%,transparent)]"
            : "bg-[color-mix(in_srgb,var(--workspace-notification-bg,#b6404d)_6%,transparent)]",
      )}
    >
      {item.unread ? (
        <span
          aria-label="Unread"
          className={cx(
            "absolute bottom-5 left-0 top-5 w-0.5 rounded-full",
            neutralNotice(item.kind)
              ? "bg-[var(--workspace-native-text-muted,#a3a3a3)]"
              : "bg-[var(--workspace-notification-indicator,#b6404d)]",
          )}
        />
      ) : null}
      <button
        className="workspace-inbox-select min-w-0 text-left"
        aria-pressed={selected}
        type="button"
        onClick={() => onOpen(item)}
      >
        <span className="flex flex-wrap items-center gap-2">
          <NotificationKindLabel kind={item.kind} />
          {item.archivedAt ? (
            <span className="inline-flex items-center gap-1 rounded-full bg-white/[0.07] px-2 py-1 text-[12px] font-bold uppercase text-white/38">
              <Archive className="size-3" aria-hidden />
              Archived
            </span>
          ) : null}
          <time
            dateTime={item.createdAt}
            className="text-xs text-[var(--workspace-native-text-muted)]"
          >
            {formatNotificationDate(item.createdAt)}
          </time>
        </span>
        <span
          className={cx(
            "mt-2 block text-base leading-6 text-white",
            item.unread ? "font-extrabold" : "font-semibold",
          )}
        >
          {item.title}
        </span>
        {context && !item.title.includes(context) ? (
          <span className="mt-1 block text-sm leading-6 text-[var(--workspace-native-text-muted)]">
            Conversation: {context}
          </span>
        ) : !context ? (
          <span className="mt-1 block text-sm leading-6 text-[var(--workspace-native-text-muted)]">
            {body}
          </span>
        ) : null}
        {context && showsBodyWithContext(item.kind) ? (
          // A failure's body says why it failed; hiding it behind the
          // conversation line would send the person to the chat to find out.
          <span className="mt-1 block text-sm leading-6 text-[var(--workspace-native-text-muted)]">
            {body}
          </span>
        ) : null}
      </button>
      <div className="workspace-inbox-row-actions flex items-center gap-1 self-center md:justify-end">
        <NotificationAction
          disabled={busy || !online}
          icon={
            item.readAt ? (
              <Circle className="size-4" />
            ) : (
              <Check className="size-4" />
            )
          }
          label={item.readAt ? "Mark unread" : "Mark read"}
          onClick={() => onMutate(item, item.readAt ? "unread" : "read")}
        />
        <NotificationAction
          disabled={busy || !online}
          icon={
            item.archivedAt ? (
              <ArchiveRestore className="size-4" />
            ) : (
              <Archive className="size-4" />
            )
          }
          label={item.archivedAt ? "Restore" : "Archive"}
          onClick={() =>
            onMutate(item, item.archivedAt ? "restore" : "archive")
          }
        />
      </div>
    </li>
  );
}

function NotificationAction({
  disabled,
  icon,
  label,
  onClick,
}: {
  disabled: boolean;
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      aria-label={label}
      className="dashboard-button workspace-inbox-item-action"
      disabled={disabled}
      title={label}
      type="button"
      onClick={onClick}
    >
      {icon}
      <span className="sr-only">{label}</span>
    </button>
  );
}

function HistoryMessage({
  action,
  icon,
  onAction,
  text,
}: {
  action?: string;
  icon: React.ReactNode;
  onAction?: () => void;
  text: string;
}) {
  return (
    <div className="grid min-h-64 place-items-center p-8 text-center">
      <div>
        <div className="mx-auto grid size-10 place-items-center rounded-full bg-white/[0.07] text-white/52">
          {icon}
        </div>
        <p className="m-0 mt-3 text-sm font-semibold text-white/52">{text}</p>
        {action && onAction ? (
          <button
            className="mt-4 rounded-xl border border-white/14 bg-white/[0.06] px-4 py-2 text-xs font-bold text-white/68 hover:bg-white/10 hover:text-white"
            type="button"
            onClick={onAction}
          >
            {action}
          </button>
        ) : null}
      </div>
    </div>
  );
}

function parseStatus(value: string | null): NotificationStatus {
  return value === "all" || value === "unread" || value === "archived"
    ? value
    : "active";
}

function parseKind(value: string | null): InboxFilterKind | undefined {
  return isNotificationKind(value) ? value : undefined;
}

function mergeInboxItems(current: InboxItem[], next: InboxItem[]) {
  const seen = new Set(current.map((item) => item.id));
  return [...current, ...next.filter((item) => !seen.has(item.id))];
}

function updateVisibleItems(
  items: InboxItem[],
  updated: InboxItem,
  status: NotificationStatus,
) {
  if (
    (status === "unread" && !updated.unread) ||
    (status === "archived" && !updated.archivedAt) ||
    (status === "active" && updated.archivedAt)
  ) {
    return items.filter((item) => item.id !== updated.id);
  }
  return items.map((item) => (item.id === updated.id ? updated : item));
}

function getMutationErrorMessage(error: unknown) {
  if (
    error &&
    typeof error === "object" &&
    "message" in error &&
    typeof error.message === "string"
  ) {
    return error.message;
  }
  return null;
}

function formatNotificationDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "medium",
  }).format(date);
}

function useOnlineStatus() {
  const [online, setOnline] = useState(() =>
    typeof navigator === "undefined" ? true : navigator.onLine,
  );
  useEffect(() => {
    const update = () => setOnline(navigator.onLine);
    window.addEventListener("online", update);
    window.addEventListener("offline", update);
    return () => {
      window.removeEventListener("online", update);
      window.removeEventListener("offline", update);
    };
  }, []);
  return online;
}

function showsBodyWithContext(kind: NotificationKind): boolean {
  return kind === "run_failed" || kind === "runtime_offline";
}
