import {
  ArrowLeft,
  Download,
  Inbox,
  LoaderCircle,
  Paperclip,
  RefreshCcw,
} from "@/shared/ui/icons";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  useDownloadMailboxAttachmentMutation,
  useGetMailboxMessageQuery,
  useLazyListMailboxMessagesQuery,
  useListMailboxMessagesQuery,
  useMarkMailboxMessageReadMutation,
} from "@/features/mailbox/api/mailbox-api";
import type {
  MailboxAttachment,
  MailboxMessage,
  MailboxMessagePage,
} from "@/features/mailbox/model/mailbox";
import { subscribeToMailboxChanges } from "@/features/mailbox/model/mailbox-realtime";
import { isRetryableZenithTeamQueryError } from "@/shared/api/zenith-team-api";
import { cx } from "@/shared/lib/cx";
import { subscribeToRefetchSignals } from "@/shared/realtime/refetch-signals";
import { Button } from "@/shared/ui/core/button";
import { Icon } from "@/shared/ui/icon";
import "@/features/mailbox/styles/mailbox-inbox.css";

const mailboxPageSize = 50;
const mailboxControlClassName = "mailbox-inbox-control";

export function MailboxInbox({
  companyId,
  enabled = true,
  mailboxId,
  unreadCount = 0,
}: {
  companyId: string;
  enabled?: boolean;
  mailboxId: string;
  unreadCount?: number;
}) {
  const [selectedMessageId, setSelectedMessageId] = useState<string | null>(
    null,
  );
  const [olderPages, setOlderPages] = useState<MailboxMessagePage[]>([]);
  const [locallyReadMessageIds, setLocallyReadMessageIds] = useState<
    Set<string>
  >(() => new Set());
  const paginationGenerationRef = useRef(0);
  const firstPageBoundaryRef = useRef<{
    cursor: string | null;
    mailboxId: string;
  } | null>(null);
  const firstPageQuery = useListMailboxMessagesQuery(
    { companyId, limit: mailboxPageSize },
    {
      refetchOnMountOrArgChange: 30,
      skip: !enabled,
    },
  );
  const [loadPage, loadPageState] = useLazyListMailboxMessagesQuery();
  const firstPage = firstPageQuery.currentData;
  const messages = useMemo(
    () =>
      mergeMessages(
        firstPage?.messages ?? [],
        olderPages,
        locallyReadMessageIds,
      ),
    [firstPage?.messages, locallyReadMessageIds, olderPages],
  );
  const lastPage = olderPages[olderPages.length - 1];
  const nextCursor =
    olderPages.length > 0
      ? (lastPage?.nextCursor ?? null)
      : (firstPage?.nextCursor ?? null);

  const rebasePagination = useCallback(() => {
    paginationGenerationRef.current += 1;
    setOlderPages([]);
  }, []);

  useEffect(() => {
    firstPageBoundaryRef.current = null;
    rebasePagination();
    setLocallyReadMessageIds(new Set());
    setSelectedMessageId(null);
  }, [rebasePagination, mailboxId]);

  useEffect(() => {
    if (!firstPage || !mailboxId) {
      return;
    }

    const nextBoundary = {
      cursor: firstPage.nextCursor,
      mailboxId,
    };
    const previousBoundary = firstPageBoundaryRef.current;
    firstPageBoundaryRef.current = nextBoundary;
    if (
      previousBoundary?.mailboxId === mailboxId &&
      previousBoundary.cursor !== nextBoundary.cursor
    ) {
      rebasePagination();
    }
  }, [firstPage, rebasePagination, mailboxId]);

  const markMessageLocallyRead = useCallback((messageId: string) => {
    setLocallyReadMessageIds((current) => {
      const next = new Set(current);
      next.add(messageId);
      return next;
    });
  }, []);

  const rollbackLocallyReadMessage = useCallback((messageId: string) => {
    setLocallyReadMessageIds((current) => {
      if (!current.has(messageId)) {
        return current;
      }
      const next = new Set(current);
      next.delete(messageId);
      return next;
    });
  }, []);

  useEffect(() => {
    if (!enabled) {
      return undefined;
    }
    return subscribeToMailboxChanges(companyId, (change) => {
      if (change.reason === "message_read" && change.messageId) {
        markMessageLocallyRead(change.messageId);
        return;
      }
      if (
        change.reason === "message_received" ||
        change.reason === "message_removed"
      ) {
        rebasePagination();
      }
    });
  }, [companyId, markMessageLocallyRead, enabled, rebasePagination]);

  useEffect(() => {
    if (!enabled) {
      return undefined;
    }
    return subscribeToRefetchSignals(rebasePagination);
  }, [enabled, rebasePagination]);

  async function loadMore() {
    if (!nextCursor || loadPageState.isFetching) {
      return;
    }
    const paginationGeneration = paginationGenerationRef.current;
    try {
      const page = await loadPage({
        companyId,
        cursor: nextCursor,
        limit: mailboxPageSize,
      }).unwrap();
      if (paginationGeneration !== paginationGenerationRef.current) {
        return;
      }
      setOlderPages((current) => [...current, page]);
    } catch {
      // The lazy-query state renders the error next to the action.
    }
  }

  function refresh() {
    rebasePagination();
    void firstPageQuery.refetch();
  }

  return (
    <div
      className="mailbox-inbox"
      data-reading={Boolean(selectedMessageId)}
      data-empty={messages.length === 0 && !selectedMessageId}
    >
      <div className="mailbox-inbox__layout">
        <section aria-label="Mailbox messages" className="mailbox-inbox__list">
          <div className="flex shrink-0 items-center justify-between gap-[12px] p-[14px_16px] [border-bottom:1px_solid_var(--workspace-divider)]">
            <div className="min-w-0">
              <p className="m-0 truncate text-sm font-bold text-[var(--workspace-native-text)]">
                Inbox
              </p>
              <p className="m-0 mt-1 text-[11px] text-[var(--workspace-native-text-muted)]">
                {unreadCount} unread · 30-day history
              </p>
            </div>
            <button
              aria-label="Refresh mailbox"
              className={cx(
                mailboxControlClassName,
                "grid size-9 shrink-0 place-items-center rounded-full disabled:opacity-55",
              )}
              disabled={firstPageQuery.isFetching}
              title="Refresh"
              type="button"
              onClick={refresh}
            >
              <RefreshCcw
                className={cx(
                  "size-4",
                  firstPageQuery.isFetching && "animate-spin",
                )}
                aria-hidden
              />
            </button>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto [scrollbar-width:thin]">
            <MailboxMessageList
              error={firstPageQuery.isError}
              loading={!firstPage && firstPageQuery.isFetching}
              messages={messages}
              selectedMessageId={selectedMessageId}
              onRefresh={refresh}
              onSelect={setSelectedMessageId}
            />
            {nextCursor ? (
              <div className="p-4 pt-2">
                <Button
                  className={cx(mailboxControlClassName, "w-full")}
                  disabled={loadPageState.isFetching}
                  size="sm"
                  type="button"
                  variant="outline"
                  onClick={() => void loadMore()}
                >
                  {loadPageState.isFetching ? (
                    <LoaderCircle className="size-4 animate-spin" aria-hidden />
                  ) : null}
                  Load more
                </Button>
                {loadPageState.isError ? (
                  <p className="mb-0 mt-2 text-center text-xs text-[var(--workspace-native-danger)]">
                    Older messages could not be loaded.
                  </p>
                ) : null}
              </div>
            ) : null}
          </div>
        </section>

        <section
          aria-label="Mailbox message detail"
          className="mailbox-inbox__detail"
        >
          {selectedMessageId ? (
            <MailboxMessageDetailView
              key={selectedMessageId}
              companyId={companyId}
              messageId={selectedMessageId}
              summary={messages.find(
                (message) => message.id === selectedMessageId,
              )}
              onBack={() => setSelectedMessageId(null)}
              onRead={markMessageLocallyRead}
              onReadRollback={rollbackLocallyReadMessage}
            />
          ) : (
            <div className="grid h-full place-items-center px-8 text-center">
              <div>
                <span className="mx-auto grid size-16 place-items-center rounded-[22px] border border-[var(--workspace-divider)] text-[var(--workspace-native-text-muted)]">
                  <Icon name="email" className="size-8" />
                </span>
                <p className="mb-0 mt-5 text-sm font-bold text-[var(--workspace-native-text)]">
                  Select a message to read it
                </p>
                <p className="mb-0 mt-1.5 text-xs text-[var(--workspace-native-text-muted)]">
                  Choose an email from the inbox to view its contents.
                </p>
              </div>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

function MailboxMessageList({
  error,
  loading,
  messages,
  selectedMessageId,
  onRefresh,
  onSelect,
}: {
  error: boolean;
  loading: boolean;
  messages: MailboxMessage[];
  selectedMessageId: string | null;
  onRefresh: () => void;
  onSelect: (messageId: string) => void;
}) {
  if (loading && messages.length === 0) {
    return <MailboxLoading label="Loading messages…" />;
  }
  if (error && messages.length === 0) {
    return (
      <div className="p-5 text-center">
        <p className="m-0 text-sm font-semibold text-[var(--workspace-native-danger)]">
          Messages are unavailable right now.
        </p>
        <Button
          className={cx(mailboxControlClassName, "mt-4")}
          size="sm"
          type="button"
          variant="outline"
          onClick={onRefresh}
        >
          Try again
        </Button>
      </div>
    );
  }
  if (messages.length === 0) {
    return (
      <div className="grid h-full min-h-64 place-items-center px-7 text-center">
        <div>
          <span className="mx-auto grid size-14 place-items-center rounded-[20px] border border-[var(--workspace-divider)] text-[var(--workspace-native-text-muted)]">
            <Inbox className="size-7" aria-hidden />
          </span>
          <p className="mb-0 mt-4 text-sm font-bold text-[var(--workspace-native-text)]">
            Your inbox is empty
          </p>
          <p className="mb-0 mt-1.5 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
            New messages sent to your company email will appear here.
          </p>
        </div>
      </div>
    );
  }
  return (
    <ul className="m-0 list-none px-3 pb-3">
      {messages.map((message, index) => (
        <li key={message.id}>
          {index > 0 ? (
            <div className="h-[1px] m-[0_12px] [background:var(--workspace-divider)]" />
          ) : null}
          <button
            aria-current={selectedMessageId === message.id ? "true" : undefined}
            className={cx(
              "mailbox-inbox__message relative w-full rounded-lg px-3 py-3 text-left transition-colors duration-150",
              selectedMessageId === message.id && "is-selected",
            )}
            type="button"
            onClick={() => onSelect(message.id)}
          >
            {!message.readAt ? (
              <span
                aria-label="Unread"
                className="absolute right-3 top-4 size-2 rounded-full bg-[var(--workspace-notification-indicator,#b6404d)]"
              />
            ) : null}
            <span
              className={cx(
                "block truncate pr-5 text-sm",
                message.readAt
                  ? "font-semibold text-[var(--workspace-native-text)]"
                  : "font-semibold text-[var(--workspace-native-text)]",
              )}
            >
              {message.sender}
            </span>
            <span
              className={cx(
                "mt-1 block truncate text-xs",
                message.readAt
                  ? "text-[var(--workspace-native-text-muted)]"
                  : "font-semibold text-[var(--workspace-native-text)]",
              )}
            >
              {message.subject || "(No subject)"}
            </span>
            <span className="mt-2 flex items-center justify-between gap-2 text-[11px] text-[var(--workspace-native-text-muted)]">
              <time dateTime={message.receivedAt}>
                {formatMailboxTime(message.receivedAt)}
              </time>
              {message.hasAttachments ? (
                <Paperclip className="size-3.5" aria-label="Has attachments" />
              ) : null}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function MailboxMessageDetailView({
  companyId,
  messageId,
  summary,
  onBack,
  onRead,
  onReadRollback,
}: {
  companyId: string;
  messageId: string;
  summary?: MailboxMessage;
  onBack: () => void;
  onRead: (messageId: string) => void;
  onReadRollback: (messageId: string) => void;
}) {
  const detailQuery = useGetMailboxMessageQuery(
    { companyId, messageId },
    { refetchOnMountOrArgChange: 30 },
  );
  const [markRead] = useMarkMailboxMessageReadMutation();
  const markedReadIds = useRef(new Set<string>());
  const [readStatusError, setReadStatusError] = useState(false);
  const detail = detailQuery.currentData;

  useEffect(() => {
    const detail = detailQuery.currentData;
    if (
      !detail ||
      detail.id !== messageId ||
      detail.readAt ||
      summary?.readAt ||
      markedReadIds.current.has(messageId)
    ) {
      return;
    }
    markedReadIds.current.add(messageId);
    onRead(messageId);
    void markRead({ companyId, messageId })
      .unwrap()
      .catch(() => {
        onReadRollback(messageId);
        setReadStatusError(true);
      });
  }, [
    companyId,
    detailQuery.currentData,
    markRead,
    messageId,
    onRead,
    onReadRollback,
    summary?.readAt,
  ]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <button className="mailbox-inbox__back" type="button" onClick={onBack}>
        <ArrowLeft className="size-4" aria-hidden />
        Back to messages
      </button>
      {detail &&
      detailQuery.isError &&
      isRetryableZenithTeamQueryError(detailQuery.error) ? (
        <div className="px-5 pt-4 sm:px-7">
          <MailboxRefreshNotice
            label="Message could not be refreshed."
            retrying={detailQuery.isFetching}
            onRetry={() => void detailQuery.refetch()}
          />
        </div>
      ) : null}
      {!detail && detailQuery.isFetching ? (
        <MailboxLoading label="Opening message…" />
      ) : !detail ||
        (detailQuery.isError &&
          !isRetryableZenithTeamQueryError(detailQuery.error)) ? (
        <MailboxDetailError
          code={mailboxErrorCode(detailQuery.error)}
          onRetry={() => void detailQuery.refetch()}
        />
      ) : (
        <MailboxDetailBody
          companyId={companyId}
          messageId={messageId}
          detail={detail}
          readStatusError={readStatusError}
        />
      )}
    </div>
  );
}

function MailboxDetailBody({
  companyId,
  messageId,
  detail,
  readStatusError,
}: {
  companyId: string;
  messageId: string;
  readStatusError: boolean;
  detail: NonNullable<
    ReturnType<typeof useGetMailboxMessageQuery>["currentData"]
  >;
}) {
  const attachments = detail.attachments ?? [];
  const emailDocument = useMemo(
    () => createMailboxEmailDocument(detail.html ?? ""),
    [detail.html],
  );

  return (
    <div className="min-h-0 flex-1 overflow-y-auto [scrollbar-width:thin]">
      <header className="px-5 py-5 sm:px-7">
        <h2 className="m-0 text-xl font-semibold leading-7 text-[var(--workspace-native-text)] sm:text-2xl">
          {detail.subject || "(No subject)"}
        </h2>
        <div className="mt-4 grid gap-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
          <MailboxHeader label="From" values={[detail.from || detail.sender]} />
          <MailboxHeader label="To" values={detail.to} />
          <MailboxHeader label="Cc" values={detail.cc} />
          <MailboxHeader label="Bcc" values={detail.bcc} />
          <MailboxHeader label="Reply to" values={detail.replyTo} />
          <p className="m-0 mt-1 text-[var(--workspace-native-text-muted)]">
            <time dateTime={detail.receivedAt}>
              {formatFullDate(detail.receivedAt)}
            </time>
          </p>
          {readStatusError ? (
            <p
              className="m-0 mt-2 text-[var(--workspace-native-danger)]"
              role="status"
            >
              Read status could not be saved. Reopen this message to retry.
            </p>
          ) : null}
        </div>
      </header>

      {attachments.length > 0 ? (
        <MailboxAttachments
          attachments={attachments}
          companyId={companyId}
          messageId={messageId}
        />
      ) : null}

      <div className="p-4 sm:p-6">
        {detail.html ? (
          <iframe
            className="min-h-[420px] w-full rounded-lg border-0 bg-white [color-scheme:normal]"
            referrerPolicy="no-referrer"
            sandbox=""
            srcDoc={emailDocument}
            title={`Email: ${detail.subject || "No subject"}`}
          />
        ) : (
          <pre className="m-0 whitespace-pre-wrap break-words px-1 py-2 font-sans text-sm leading-6 text-[var(--workspace-native-text)]">
            {detail.text || "This message has no displayable body."}
          </pre>
        )}
      </div>
    </div>
  );
}

function MailboxAttachments({
  attachments,
  companyId,
  messageId,
}: {
  attachments: MailboxAttachment[];
  companyId: string;
  messageId: string;
}) {
  const [downloadAttachment, downloadState] =
    useDownloadMailboxAttachmentMutation();
  const [activeAttachmentId, setActiveAttachmentId] = useState<string | null>(
    null,
  );
  const [downloadErrorCode, setDownloadErrorCode] = useState<string | null>(
    null,
  );
  const downloadingRef = useRef(false);

  async function openAttachment(attachmentId: string) {
    if (downloadingRef.current) {
      return;
    }
    downloadingRef.current = true;
    setActiveAttachmentId(attachmentId);
    setDownloadErrorCode(null);
    let pendingWindow: Window | null = null;
    try {
      pendingWindow = window.open("about:blank", "_blank");
      if (pendingWindow) {
        pendingWindow.opener = null;
      }
      const download = await downloadAttachment({
        attachmentId,
        companyId,
        messageId,
      }).unwrap();
      const url = new URL(download.url);
      if (url.protocol !== "https:") {
        throw new Error("Mailbox attachment URL must use HTTPS");
      }
      if (pendingWindow) {
        pendingWindow.location.replace(url.href);
      } else {
        window.open(url.href, "_blank", "noopener,noreferrer");
      }
    } catch (error) {
      pendingWindow?.close();
      setDownloadErrorCode(mailboxErrorCode(error) ?? "unknown");
    } finally {
      downloadState.reset();
      setActiveAttachmentId(null);
      downloadingRef.current = false;
    }
  }

  return (
    <section className="px-5 py-4 sm:px-7">
      <p className="m-0 mb-2 text-[11px] font-bold uppercase tracking-[0.12em] text-[var(--workspace-native-text-muted)]">
        Attachments
      </p>
      <div className="flex flex-wrap gap-2">
        {attachments.map((attachment) => (
          <button
            className="flex max-w-full items-center gap-2 rounded-xl border border-[var(--workspace-divider)] bg-transparent px-3 py-2 text-left transition hover:border-[var(--workspace-divider)] focus-visible:outline-none"
            disabled={activeAttachmentId !== null}
            key={attachment.id}
            type="button"
            onClick={() => void openAttachment(attachment.id)}
          >
            {activeAttachmentId === attachment.id ? (
              <LoaderCircle
                className="size-4 shrink-0 animate-spin text-[var(--workspace-native-accent)]"
                aria-hidden
              />
            ) : (
              <Download
                className="size-4 shrink-0 text-[var(--workspace-native-accent)]"
                aria-hidden
              />
            )}
            <span className="min-w-0">
              <span className="block truncate text-xs font-bold text-[var(--workspace-native-text)]">
                {attachment.filename}
              </span>
              <span className="block text-[10px] text-[var(--workspace-native-text-muted)]">
                {attachment.sizeBytes == null
                  ? attachment.mimeType
                  : `${attachment.mimeType} · ${formatBytes(attachment.sizeBytes)}`}
              </span>
            </span>
          </button>
        ))}
      </div>
      {downloadErrorCode ? (
        <p
          className="mb-0 mt-2 text-xs font-semibold text-[var(--workspace-native-danger)]"
          role="alert"
        >
          {mailboxDetailErrorMessage(downloadErrorCode)}
        </p>
      ) : null}
    </section>
  );
}

function MailboxHeader({
  label,
  values,
}: {
  label: string;
  values: string[] | null | undefined;
}) {
  if (!values?.length) {
    return null;
  }
  return (
    <p className="m-0 flex min-w-0 gap-2">
      <span className="w-14 shrink-0 font-semibold text-[var(--workspace-native-text-muted)]">
        {label}
      </span>
      <span className="min-w-0 break-words text-[var(--workspace-native-text-muted)]">
        {values.join(", ")}
      </span>
    </p>
  );
}

export function MailboxLoading({ label }: { label: string }) {
  return (
    <div className="grid h-full min-h-52 place-items-center text-center">
      <div>
        <span className="mx-auto grid size-12 place-items-center rounded-2xl border border-[var(--workspace-divider)]">
          <LoaderCircle
            className="size-5 animate-spin text-[var(--workspace-native-accent)]"
            aria-hidden
          />
        </span>
        <p className="mb-0 mt-3 text-sm font-semibold text-[var(--workspace-native-text-muted)]">
          {label}
        </p>
      </div>
    </div>
  );
}

export function MailboxRefreshNotice({
  label,
  retrying,
  onRetry,
}: {
  label: string;
  retrying: boolean;
  onRetry: () => void;
}) {
  return (
    <p
      className="mb-0 mt-3 text-xs text-[var(--workspace-native-text-muted)]"
      role="status"
    >
      {label} Showing the last available content.{" "}
      <button
        className="underline underline-offset-2 disabled:opacity-55"
        disabled={retrying}
        type="button"
        onClick={onRetry}
      >
        {retrying ? "Retrying…" : "Try again"}
      </button>
    </p>
  );
}

export function MailboxStateError({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="grid h-full place-items-center p-8 text-center">
      <div>
        <p className="m-0 text-base font-bold">Mailbox is unavailable</p>
        <p className="mb-0 mt-2 text-sm text-[var(--workspace-native-text-muted)]">
          Check your connection and try again.
        </p>
        <Button
          className={cx(mailboxControlClassName, "mt-5")}
          size="sm"
          type="button"
          variant="outline"
          onClick={onRetry}
        >
          Try again
        </Button>
      </div>
    </div>
  );
}

function MailboxDetailError({
  code,
  onRetry,
}: {
  code: string | null;
  onRetry: () => void;
}) {
  const expired = code === "mailbox_message_expired";
  return (
    <div className="grid h-full place-items-center p-8 text-center">
      <div>
        <p className="m-0 text-base font-bold">
          {expired ? "This message has expired" : "Message is unavailable"}
        </p>
        <p className="mb-0 mt-2 max-w-sm text-sm leading-6 text-[var(--workspace-native-text-muted)]">
          {mailboxDetailErrorMessage(code)}
        </p>
        {!expired ? (
          <Button
            className={cx(mailboxControlClassName, "mt-5")}
            size="sm"
            type="button"
            variant="outline"
            onClick={onRetry}
          >
            Try again
          </Button>
        ) : null}
      </div>
    </div>
  );
}

function mergeMessages(
  firstPageMessages: MailboxMessage[],
  olderPages: MailboxMessagePage[],
  locallyReadMessageIds: Set<string>,
) {
  const messages = new Map<string, MailboxMessage>();
  for (const message of firstPageMessages) {
    messages.set(message.id, message);
  }
  for (const page of olderPages) {
    for (const message of page.messages) {
      if (!messages.has(message.id)) {
        messages.set(message.id, message);
      }
    }
  }
  return [...messages.values()].map((message) =>
    !message.readAt && locallyReadMessageIds.has(message.id)
      ? { ...message, readAt: new Date().toISOString() }
      : message,
  );
}

function mailboxErrorCode(error: unknown): string | null {
  if (!error || typeof error !== "object" || !("code" in error)) {
    return null;
  }
  return typeof error.code === "string" ? error.code : null;
}

function mailboxDetailErrorMessage(code: string | null) {
  switch (code) {
    case "mailbox_message_expired":
      return "Zenith Team keeps mailbox messages for 30 days. This message is no longer available from the email provider.";
    case "mailbox_provider_rate_limited":
      return "The email provider is busy. Wait a moment, then try again.";
    case "mailbox_provider_unavailable":
      return "The email provider could not return this content. Try again shortly.";
    case "mailbox_message_not_found":
      return "This message is no longer in the shared mailbox.";
    default:
      return "The message could not be loaded. Check your connection and try again.";
  }
}

function createMailboxEmailDocument(content: string) {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <style>
      html,
      body {
        min-height: 100%;
        margin: 0;
        background: #fff !important;
      }

      body {
        box-sizing: border-box;
        padding: 22px;
        color: #242424;
        font-family: system-ui, sans-serif;
        font-size: 14px;
        line-height: 1.7;
        overflow-wrap: anywhere;
      }

      *,
      *::before,
      *::after {
        box-sizing: border-box;
      }

      h1,
      h2,
      h3,
      h4,
      h5,
      h6 {
        margin: 1.35em 0 0.55em;
        color: #242424;
        line-height: 1.3;
      }

      p,
      blockquote,
      pre,
      ul,
      ol,
      table {
        margin: 0 0 1em;
      }

      a {
        color: #245dcc;
        text-underline-offset: 3px;
      }

      blockquote {
        margin-left: 0;
        padding-left: 16px;
        color: #666;
      }

      pre {
        max-width: 100%;
        overflow-x: auto;
        white-space: pre-wrap;
      }

      table {
        max-width: 100%;
        border-collapse: collapse;
      }

      th,
      td {
        padding: 8px 10px;
        border: 1px solid #ddd;
        vertical-align: top;
      }

      hr {
        margin: 1.5em 0;
        border: 0;
        border-top: 1px solid #ddd;
      }

      @media (max-width: 520px) {
        body {
          padding: 16px;
        }
      }
    </style>
  </head>
  <body>${content}</body>
</html>`;
}

function formatMailboxTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "Unknown time";
  }
  const now = Date.now();
  if (now - date.getTime() < 24 * 60 * 60 * 1_000) {
    return new Intl.DateTimeFormat(undefined, {
      hour: "2-digit",
      minute: "2-digit",
    }).format(date);
  }
  return new Intl.DateTimeFormat(undefined, {
    day: "numeric",
    month: "short",
  }).format(date);
}

function formatFullDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "Unknown time";
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

function formatBytes(value: number) {
  if (value < 1_024) {
    return `${value} B`;
  }
  if (value < 1_024 * 1_024) {
    return `${(value / 1_024).toFixed(1)} KB`;
  }
  return `${(value / (1_024 * 1_024)).toFixed(1)} MB`;
}
