import { ArrowRight, Check, Search, X } from "@/shared/ui/icons";
import { useId, useRef, useState } from "react";

import { Avatar, AvatarFallback, AvatarImage } from "@/shared/ui/core/avatar";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { personInitials } from "@/shared/ui/person-initials";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import "@/features/chat-workspace/styles/new-direct-message-dialog.css";

export type DirectMessageRecipient = {
  id: string;
  name: string;
  description: string;
  avatarSrc?: string;
  kind: "agent" | "human";
};

export function NewDirectMessageDialog({
  open,
  onOpenChange,
  recipients,
  onSelect,
  isLoading = false,
  hasError = false,
  onRetry,
  mode = "create",
  errorMessage,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  recipients: DirectMessageRecipient[];
  onSelect: (
    recipients: DirectMessageRecipient[],
  ) => Promise<boolean> | boolean;
  isLoading?: boolean;
  hasError?: boolean;
  onRetry?: () => void;
  mode?: "create" | "invite";
  errorMessage?: string;
}) {
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<DirectMessageRecipient[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const inFlight = useRef(false);
  const searchId = useId();
  const searchRef = useRef<HTMLInputElement | null>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const listRef = useRef<HTMLUListElement | null>(null);
  const searchTerm = query.trim().toLocaleLowerCase();
  const visibleRecipients = recipients.filter((recipient) =>
    `${recipient.name} ${recipient.description}`
      .toLocaleLowerCase()
      .includes(searchTerm),
  );

  function selectRecipient(recipient: DirectMessageRecipient) {
    setSubmitError("");
    setSelected((current) => {
      const exists = current.some(
        (item) => item.kind === recipient.kind && item.id === recipient.id,
      );
      if (exists)
        return current.filter(
          (item) => item.kind !== recipient.kind || item.id !== recipient.id,
        );
      return [...current, recipient];
    });
  }

  async function startChat() {
    if (inFlight.current || selected.length === 0) return;
    if (
      selected.some(
        (item) =>
          !recipients.some(
            (recipient) =>
              recipient.kind === item.kind && recipient.id === item.id,
          ),
      )
    ) {
      setSubmitError(
        "Some selected recipients are no longer available. Remove them and try again.",
      );
      return;
    }
    inFlight.current = true;
    setSubmitting(true);
    setSubmitError("");
    try {
      if (await onSelect(selected)) onOpenChange(false);
      else
        setSubmitError(
          mode === "invite"
            ? "Couldn’t invite members. Your selections have been kept. Try again."
            : "Couldn’t open the chat. Your selections have been kept. Try again.",
        );
    } catch {
      setSubmitError(
        mode === "invite"
          ? "Couldn’t invite members. Your selections have been kept. Try again."
          : "Couldn’t open the chat. Your selections have been kept. Try again.",
      );
    } finally {
      inFlight.current = false;
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!inFlight.current) onOpenChange(next);
      }}
    >
      <DialogContent
        className="new-direct-message-dialog z-[221] flex w-[min(520px,_calc(100vw_-_32px))] max-w-[min(520px,_calc(100vw_-_32px))] max-h-[calc(100dvh_-_48px)] flex-col gap-0 overflow-hidden [border:1px_solid_var(--workspace-divider,_#e5dfe8)] rounded-[var(--workspace-radius-overlay,_14px)] p-0 [background:var(--workspace-overlay-bg,_#fff)] text-[var(--workspace-native-text,_#2d2631)] [box-shadow:var(--workspace-overlay-shadow,_0_16px_48px_rgb(37_28_45_/_12%))] [font-family:var(--workspace-font-body,_sans-serif)]"
        overlayClassName="new-direct-message-dialog__overlay"
        showCloseButton={false}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          openerRef.current =
            document.activeElement instanceof HTMLElement
              ? document.activeElement
              : null;
          setQuery("");
          setSelected([]);
          setSubmitError("");
          searchRef.current?.focus();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          if (openerRef.current?.isConnected) openerRef.current.focus();
        }}
      >
        <header className="new-direct-message-dialog__header">
          <div className="min-w-0 flex-1">
            <DialogTitle className="m-0 [font-family:var(--workspace-font-heading,_sans-serif)] text-[length:21px] font-[700] leading-[1.4]">
              {mode === "invite" ? "Invite members" : "New chat"}
            </DialogTitle>
            <DialogDescription className="m-[6px_0_0] text-[var(--workspace-native-text-muted,_#706875)] text-[length:13px] leading-[1.5]">
              {mode === "invite"
                ? "Choose people and agents to add to this group direct message."
                : "Choose people and agents to start a chat together."}
            </DialogDescription>
          </div>
          <DialogClose asChild>
            <button
              disabled={submitting}
              aria-label={
                mode === "invite" ? "Close invite members" : "Close new chat"
              }
              className="new-direct-message-dialog__close"
              title="Close"
              type="button"
            >
              <X aria-hidden="true" size={18} />
            </button>
          </DialogClose>
        </header>

        <div className="new-direct-message-dialog__search">
          <Search aria-hidden="true" size={18} />
          <label className="sr-only" htmlFor={searchId}>
            Search people and agents
          </label>
          <input
            autoComplete="off"
            id={searchId}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown") {
                event.preventDefault();
                listRef.current?.querySelector("button")?.focus();
              }
            }}
            placeholder="Search by name or role…"
            ref={searchRef}
            type="search"
            value={query}
          />
        </div>

        {selected.length > 0 ? (
          <div
            className="new-direct-message-dialog__selected"
            aria-label="Selected recipients"
          >
            {selected.map((recipient) => (
              <button
                key={`${recipient.kind}:${recipient.id}`}
                type="button"
                disabled={submitting}
                onClick={() => selectRecipient(recipient)}
                aria-label={`Remove ${recipient.name}`}
              >
                <span>{recipient.name}</span>
                <X size={14} aria-hidden="true" />
              </button>
            ))}
          </div>
        ) : null}
        <div className="min-h-0 overflow-y-auto overscroll-contain p-[0_12px_12px]">
          {hasError ? (
            <div
              className="new-direct-message-dialog__error flex items-center justify-between gap-[12px] m-[0_12px_12px] [border:1px_solid_var(--workspace-divider,_#e5dfe8)] rounded-[9px] p-[12px] text-[var(--workspace-native-text-muted,_#706875)] text-[length:13px] leading-[1.5]"
              role="alert"
            >
              <p>We couldn’t load all recipients.</p>
              {onRetry ? (
                <button onClick={onRetry} type="button">
                  Try again
                </button>
              ) : null}
            </div>
          ) : null}
          {visibleRecipients.length > 0 ? (
            <ul
              aria-label="Available people and agents"
              className="grid gap-[2px] m-0 p-0 [list-style:none]"
              onKeyDown={(event) => {
                if (event.key !== "ArrowDown" && event.key !== "ArrowUp") {
                  return;
                }
                const buttons = Array.from(
                  listRef.current?.querySelectorAll("button") ?? [],
                );
                const focusedIndex = buttons.findIndex(
                  (button) => button === document.activeElement,
                );
                if (focusedIndex < 0) return;
                event.preventDefault();
                if (event.key === "ArrowUp" && focusedIndex === 0) {
                  searchRef.current?.focus();
                  return;
                }
                const nextIndex =
                  event.key === "ArrowDown"
                    ? focusedIndex + 1
                    : focusedIndex - 1;
                buttons[Math.min(nextIndex, buttons.length - 1)]?.focus();
              }}
              ref={listRef}
            >
              {visibleRecipients.map((recipient) => (
                <li key={`${recipient.kind}:${recipient.id}`}>
                  <button
                    className="new-direct-message-dialog__recipient"
                    aria-pressed={selected.some(
                      (item) =>
                        item.kind === recipient.kind &&
                        item.id === recipient.id,
                    )}
                    disabled={
                      submitting ||
                      (selected.filter((item) => item.kind === recipient.kind)
                        .length >= (recipient.kind === "human" ? 49 : 20) &&
                        !selected.some(
                          (item) =>
                            item.kind === recipient.kind &&
                            item.id === recipient.id,
                        ))
                    }
                    onClick={() => selectRecipient(recipient)}
                    type="button"
                  >
                    {recipient.kind === "agent" ? (
                      <FoldAvatar
                        motion="still"
                        agentId={recipient.id}
                        className="new-direct-message-dialog__avatar"
                        size={40}
                      />
                    ) : (
                      <Avatar
                        aria-hidden="true"
                        className="new-direct-message-dialog__avatar new-direct-message-dialog__avatar--person"
                      >
                        {recipient.avatarSrc ? (
                          <AvatarImage
                            alt=""
                            className="object-cover"
                            src={recipient.avatarSrc}
                          />
                        ) : null}
                        <AvatarFallback className="rounded-[inherit] [background:var(--workspace-control-bg,_#f1edf4)] text-[var(--workspace-native-text,_#2d2631)] text-[length:12px] font-[600]">
                          {personInitials(recipient.name) || "?"}
                        </AvatarFallback>
                      </Avatar>
                    )}
                    <span className="grid min-w-0 flex-1 gap-[2px]">
                      <span className="new-direct-message-dialog__name">
                        {recipient.name}
                      </span>
                      <span className="new-direct-message-dialog__role">
                        {recipient.description}
                      </span>
                    </span>
                    <span className="shrink-0 rounded-[5px] p-[2px_6px] [background:var(--workspace-control-bg,_#f1edf4)] text-[var(--workspace-native-text-muted,_#706875)] text-[length:10px] font-[600] leading-[1.5]">
                      {recipient.kind === "agent" ? "Agent" : "Person"}
                    </span>
                    {selected.some(
                      (item) =>
                        item.kind === recipient.kind &&
                        item.id === recipient.id,
                    ) ? (
                      <Check
                        aria-hidden="true"
                        className="shrink-0 text-[var(--workspace-native-text-muted,_#706875)]"
                        size={17}
                      />
                    ) : (
                      <span className="w-[17px] shrink-0" />
                    )}
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p
              className="m-0 p-[32px_20px] text-[var(--workspace-native-text-muted,_#706875)] text-[length:13px] leading-[1.6] text-center"
              role="status"
            >
              {isLoading
                ? "Loading recipients…"
                : searchTerm
                  ? "No matches. Try another name or role."
                  : hasError
                    ? "Recipients will appear here once the connection recovers."
                    : "No recipients are available yet."}
            </p>
          )}
        </div>
        <footer className="new-direct-message-dialog__footer">
          <div>
            <p>
              {mode === "invite"
                ? "New members can read all retained messages and shared files."
                : "Direct messages stay private to the selected participants and you."}
            </p>
            <p aria-live="polite">{selected.length} selected</p>
            {errorMessage || submitError ? (
              <p
                className="new-direct-message-dialog__submit-error"
                role="alert"
              >
                {errorMessage || submitError}
              </p>
            ) : null}
          </div>
          <button
            className="new-direct-message-dialog__start"
            type="button"
            disabled={selected.length === 0 || submitting}
            onClick={() => void startChat()}
          >
            {mode === "invite"
              ? submitting
                ? "Inviting…"
                : "Invite members"
              : submitting
                ? "Opening…"
                : "Start chat"}
            <ArrowRight size={16} aria-hidden="true" />
          </button>
        </footer>
      </DialogContent>
    </Dialog>
  );
}
