import { Tooltip } from "@/shared/ui/tooltip";
import { activateAccountNotification } from "@/features/notifications/lib/account-notification-navigation";
import {
  ACCOUNT_INBOX_SCOPE,
  neutralNotice,
} from "@/features/notifications/model/notification";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import {
  Archive,
  Bell,
  CheckCheck,
  ChevronRight,
  RefreshCcw,
} from "@/shared/ui/icons";
import { useCallback, useRef, useState } from "react";
import { useNavigate } from "react-router";

import {
  useGetNotificationCountsQuery,
  useListNotificationsQuery,
  useReadAllNotificationsMutation,
} from "@/features/notifications/api/notifications-api";
import { ContentPanel } from "@/shared/ui/content-panel";
import { NotificationKindLabel } from "@/features/notifications/components/notification-kind-label";
import { useNoticeBody } from "@/features/notifications/hooks/use-notice-body";
import { NOTIFICATIONS_ROUTE } from "@/features/notifications/lib/notification-navigation";
import type { InboxItem } from "@/features/notifications/model/notification";
import { cx } from "@/shared/lib/cx";
import { TitlebarUnreadBadge } from "@/shared/ui/titlebar-unread-badge";

type TopNotificationMenuProps = {
  className?: string;
};

export function TopNotificationMenu({ className }: TopNotificationMenuProps) {
  const navigate = useNavigate();
  const companyId = ACCOUNT_INBOX_SCOPE;
  const countsQuery = useGetNotificationCountsQuery(companyId);
  const recentQuery = useListNotificationsQuery({
    companyId,
    limit: 6,
    status: "active",
  });
  const [readAll, readAllState] = useReadAllNotificationsMutation();
  const [isMenuOpen, setIsMenuOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const openedWithPointerRef = useRef(false);
  const items = recentQuery.currentData?.items ?? [];
  const unreadCount = countsQuery.isError
    ? 0
    : (countsQuery.currentData?.unreadCount ?? 0);
  const refreshNotifications = useCallback(() => {
    void countsQuery.refetch();
    void recentQuery.refetch();
  }, [companyId, countsQuery, recentQuery]);
  const openItem = useCallback(
    (item: InboxItem) => {
      setIsMenuOpen(false);
      void activateAccountNotification({
        notificationId: item.id,
      }).then((path) => {
        if (path) navigate(path);
      });
    },
    [navigate],
  );
  const markAllRead = useCallback(() => {
    if (unreadCount === 0 || readAllState.isLoading) {
      return;
    }
    void readAll(companyId);
  }, [companyId, readAll, readAllState.isLoading, unreadCount]);

  return (
    <DropdownMenu.Root
      open={isMenuOpen}
      onOpenChange={(open) => {
        setIsMenuOpen(open);
        if (open) {
          refreshNotifications();
        }
      }}
    >
      <Tooltip label="Notifications" disabled={isMenuOpen}>
        <DropdownMenu.Trigger asChild>
          <button
            ref={triggerRef}
            aria-label={
              unreadCount > 0
                ? `Notifications, ${unreadCount} unread`
                : "Notifications"
            }
            className={cx(
              "relative grid size-9 shrink-0 place-items-center rounded-full text-white/46 transition hover:bg-white/10 hover:text-white",
              className,
            )}
            type="button"
            onKeyDown={() => {
              openedWithPointerRef.current = false;
            }}
            onPointerDown={() => {
              openedWithPointerRef.current = true;
            }}
          >
            <Bell className="size-5" aria-hidden="true" />
            <TitlebarUnreadBadge count={unreadCount} />
          </button>
        </DropdownMenu.Trigger>
      </Tooltip>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          className="workspace-notification-control z-[90] !border-0 !bg-transparent !p-0 !text-white !shadow-none"
          collisionPadding={12}
          side="bottom"
          sideOffset={14}
          onCloseAutoFocus={(event) => {
            if (!openedWithPointerRef.current) {
              return;
            }
            event.preventDefault();
            triggerRef.current?.blur();
          }}
        >
          <ContentPanel
            className="workspace-notification-menu h-[min(520px,calc(100vh-112px))] w-[min(400px,calc(100vw-24px))] overflow-hidden rounded-3xl"
            contentClassName="workspace-notification-menu workspace-notification-control pointer-events-auto relative h-full w-full flex-col rounded-3xl p-4 text-white"
          >
            <div className="relative z-10 flex items-start justify-between border-b border-white/18 px-2 pb-3 pt-1">
              <div>
                <p className="m-0 text-lg font-extrabold leading-none text-white">
                  Inbox
                </p>
                <p className="m-0 mt-2 text-xs font-medium leading-none text-white/58">
                  {countsQuery.isError
                    ? "Could not load unread count"
                    : !countsQuery.currentData
                      ? "Loading notifications…"
                      : unreadCount > 0
                        ? `${unreadCount} unread`
                        : "No unread notifications"}
                </p>
              </div>
              <div className="flex items-center gap-1">
                <button
                  aria-label="Mark all notifications read"
                  className="grid size-8 place-items-center rounded-full text-white/50 transition hover:bg-white/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-35"
                  disabled={unreadCount === 0 || readAllState.isLoading}
                  title="Mark all read"
                  type="button"
                  onClick={markAllRead}
                >
                  <CheckCheck className="size-4" aria-hidden />
                </button>
                <button
                  aria-label="Refresh notifications"
                  className="grid size-8 place-items-center rounded-full text-white/50 transition hover:bg-white/10 hover:text-white"
                  type="button"
                  onClick={refreshNotifications}
                >
                  <RefreshCcw
                    aria-hidden
                    className={cx(
                      "size-4",
                      (countsQuery.isFetching || recentQuery.isFetching) &&
                        "animate-spin",
                    )}
                  />
                </button>
              </div>
            </div>
            <div className="relative z-10 min-h-0 flex-1 overflow-y-auto py-3 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
              <RecentNotificationRows
                error={recentQuery.isError}
                items={items}
                loading={recentQuery.isLoading}
                onOpen={openItem}
              />
            </div>
            <button
              className="relative z-10 flex h-11 w-full items-center justify-center gap-2 border-t border-white/14 pt-3 text-sm font-bold text-white/72 transition hover:text-white"
              type="button"
              onClick={() => {
                setIsMenuOpen(false);
                navigate(NOTIFICATIONS_ROUTE);
              }}
            >
              View full Inbox
              <ChevronRight className="size-4" aria-hidden />
            </button>
          </ContentPanel>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function RecentNotificationRows({
  error,
  items,
  loading,
  onOpen,
}: {
  error: boolean;
  items: InboxItem[];
  loading: boolean;
  onOpen: (item: InboxItem) => void;
}) {
  // Mounted only while the menu is open, so the channel list loads then.
  const noticeBody = useNoticeBody();
  if (loading && items.length === 0) {
    return (
      <p className="m-0 px-2 py-6 text-center text-sm font-medium text-white/58">
        Loading notifications…
      </p>
    );
  }
  if (error && items.length === 0) {
    return (
      <p
        className="m-0 rounded-2xl border border-red/24 bg-red/12 px-3 py-3 text-sm font-semibold text-red-100"
        role="status"
      >
        Notifications are unavailable. Try refreshing.
      </p>
    );
  }
  if (items.length === 0) {
    return (
      <div className="grid min-h-48 place-items-center px-6 text-center">
        <div>
          <CheckCheck
            className="mx-auto size-6 text-emerald-200/70"
            aria-hidden
          />
          <p className="m-0 mt-3 text-sm font-bold text-white/76">
            Nothing new
          </p>
          <p className="m-0 mt-1 text-xs leading-5 text-white/45">
            Updates that need your attention will appear here.
          </p>
        </div>
      </div>
    );
  }
  return (
    <ul className="m-0 flex list-none flex-col gap-2 p-0">
      {items.map((item) => (
        <li key={item.id}>
          <button
            className={cx(
              "relative w-full rounded-2xl border px-3 py-3 text-left transition hover:border-white/24 hover:bg-white/12",
              !item.unread
                ? "border-white/10 bg-white/[0.05]"
                : neutralNotice(item.kind)
                  ? "border-[color-mix(in_srgb,var(--workspace-native-text-muted,#a3a3a3)_32%,transparent)] bg-[color-mix(in_srgb,var(--workspace-native-text-muted,#a3a3a3)_12%,transparent)]"
                  : "border-[color-mix(in_srgb,var(--workspace-notification-bg,#b6404d)_20%,transparent)] bg-[color-mix(in_srgb,var(--workspace-notification-bg,#b6404d)_9%,transparent)]",
            )}
            type="button"
            onClick={() => onOpen(item)}
          >
            {item.unread ? (
              <span
                aria-label="Unread"
                className={cx(
                  "absolute right-3 top-3 size-2 rounded-full",
                  neutralNotice(item.kind)
                    ? "bg-[var(--workspace-native-text-muted,#a3a3a3)]"
                    : "bg-[var(--workspace-notification-indicator,#b6404d)]",
                )}
              />
            ) : null}
            <span className="flex items-center gap-2 pr-5 text-[12px] font-bold uppercase tracking-[0.12em] text-white/46">
              {item.archivedAt ? (
                <Archive className="size-3" aria-hidden />
              ) : null}
              <NotificationKindLabel kind={item.kind} />
            </span>
            <span className="mt-1.5 block pr-4 text-sm font-bold leading-5 text-white">
              {item.title}
            </span>
            <span className="mt-1 block line-clamp-2 text-xs font-medium leading-5 text-white/55">
              {noticeBody(item)}
            </span>
            <time className="mt-2 block text-[11px] font-medium text-white/36">
              {formatNotificationTime(item.createdAt)}
            </time>
          </button>
        </li>
      ))}
    </ul>
  );
}

function formatNotificationTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(undefined, {
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    month: "short",
  }).format(date);
}
