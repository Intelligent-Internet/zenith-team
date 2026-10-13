import {
  conversationApi,
  useRenameWorkspaceConversationMutation,
} from "@/shared/api/conversation-api";
import { type FormEvent, useId, useRef, useState } from "react";

import { directConversationTitle } from "@/features/chat-workspace/model/chat-navigator-browse";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import type { DirectConversationListItem } from "@/shared/api/types";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import "@/features/chat-workspace/styles/conversation-rename-dialog.css";

export function RenameConversationDialog({
  conversation,
  title,
  returnFocus,
  fallbackFocus,
  onClose,
}: {
  conversation: DirectConversationListItem;
  title: string;
  returnFocus: HTMLElement;
  fallbackFocus: HTMLElement | null;
  onClose: () => void;
}) {
  const [draft, setDraft] = useState(title);
  const [expectedRevision, setExpectedRevision] = useState(
    conversation.revision,
  );
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [latestTitle, setLatestTitle] = useState<string | null>(null);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const inputId = useId();
  const [rename] = useRenameWorkspaceConversationMutation();
  const [refreshConversations] =
    conversationApi.useLazyListWorkspaceChatConversationsQuery();
  const normalized = draft.replace(
    /^[\p{White_Space}\uFEFF]+|[\p{White_Space}\uFEFF]+$/gu,
    "",
  );
  const length = [...normalized].length;

  async function reloadLatestTitle() {
    setNeedsRefresh(true);
    setError("");
    try {
      const page = await refreshConversations(
        conversation.companyId,
        false,
      ).unwrap();
      const latest = page?.items.find((item) => item.id === conversation.id);
      if (!latest) {
        setError(
          "This conversation is no longer available. Your draft is kept.",
        );
        return;
      }
      setExpectedRevision(latest.revision);
      setLatestTitle(
        directConversationTitle(latest.title, latest.agent?.name ?? "Agent"),
      );
      setNeedsRefresh(false);
    } catch (cause) {
      setError(
        getBoardlyQueryErrorMessage(
          cause,
          "The latest title could not be loaded.",
        ),
      );
    }
  }

  async function refreshAfterConflict() {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    try {
      await reloadLatestTitle();
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (inFlight.current || needsRefresh) return;
    if (!normalized || length > 120) {
      setError(
        normalized
          ? "Use at most 120 characters."
          : "Enter a conversation title.",
      );
      inputRef.current?.focus();
      return;
    }
    inFlight.current = true;
    setBusy(true);
    setError("");
    try {
      await rename({
        companyId: conversation.companyId,
        conversationId: conversation.id,
        request: { title: normalized, expectedRevision },
      }).unwrap();
      toast.success("Conversation renamed");
      onClose();
    } catch (cause) {
      if (
        cause &&
        typeof cause === "object" &&
        "code" in cause &&
        cause.code === "conversation_conflict"
      ) {
        setConflict(true);
        setLatestTitle(null);
        await reloadLatestTitle();
      } else {
        setError(
          getBoardlyQueryErrorMessage(
            cause,
            "The conversation could not be renamed. Your draft is kept.",
          ),
        );
      }
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !inFlight.current) onClose();
      }}
    >
      <DialogContent
        className="conversation-rename-dialog"
        overlayClassName="conversation-rename-dialog__overlay"
        showCloseButton={false}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          inputRef.current?.focus();
          inputRef.current?.select();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          if (returnFocus.isConnected) returnFocus.focus();
          else if (fallbackFocus?.isConnected) fallbackFocus.focus();
        }}
      >
        <form onSubmit={submit}>
          <header>
            <DialogTitle>Rename conversation</DialogTitle>
            <DialogDescription>
              Give this chat a title you can find again.
            </DialogDescription>
          </header>
          <div className="conversation-rename-dialog__field grid gap-[8px] text-[length:13px]">
            <label htmlFor={inputId}>Conversation title</label>
            <input
              id={inputId}
              ref={inputRef}
              autoComplete="off"
              disabled={busy}
              maxLength={240}
              value={draft}
              aria-invalid={Boolean(error) || length > 120}
              aria-describedby={`${inputId}-hint${error ? ` ${inputId}-error` : ""}`}
              onChange={(event) => {
                setDraft(event.target.value);
                setError("");
              }}
            />
            <p
              id={`${inputId}-hint`}
              className="conversation-rename-dialog__hint"
            >
              <span>Up to 120 characters</span>
              <span>{length}/120</span>
            </p>
          </div>
          {conflict ? (
            <div
              className="conversation-rename-dialog__conflict [border:1px_solid_var(--workspace-divider)] rounded-[8px] p-[12px] [background:var(--workspace-control-bg)] text-[length:13px] leading-[1.5] wrap-anywhere"
              role="status"
            >
              <p>This conversation changed elsewhere. Your draft is kept.</p>
              {latestTitle !== null ? (
                <p>
                  Latest title: <strong>{latestTitle}</strong>
                </p>
              ) : busy ? (
                <p>Loading the latest title…</p>
              ) : null}
              {needsRefresh && !busy ? (
                <button
                  type="button"
                  onClick={() => void refreshAfterConflict()}
                >
                  Reload latest title
                </button>
              ) : null}
            </div>
          ) : null}
          {error ? (
            <p
              id={`${inputId}-error`}
              className="m-0 text-[var(--workspace-native-danger,_#c2415d)] text-[length:13px] leading-[1.5]"
              role="alert"
            >
              {error}
            </p>
          ) : null}
          <footer>
            <button type="button" disabled={busy} onClick={onClose}>
              Cancel
            </button>
            <button
              className="conversation-rename-dialog__save"
              type="submit"
              disabled={busy || needsRefresh}
            >
              {busy
                ? needsRefresh
                  ? "Loading…"
                  : "Saving…"
                : conflict
                  ? "Save my title"
                  : "Save title"}
            </button>
          </footer>
        </form>
      </DialogContent>
    </Dialog>
  );
}
