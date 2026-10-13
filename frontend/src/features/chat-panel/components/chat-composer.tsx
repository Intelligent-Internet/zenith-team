import { ArtifactPickerDialog } from "./artifact-picker/artifact-picker-dialog";
import { ArtifactDraftCards } from "./artifact-picker/artifact-draft-cards";
import type { LibraryArtifact } from "../api/artifact-library-api";
import { persistor, useAppDispatch, useAppSelector } from "@/state/store";
import { setArtifactDraft } from "@/state/slice/chat";
import {
  PromptInput,
  PromptInputBody,
  type PromptInputMessage,
  type PromptSubmissionStatus,
  PromptInputTextarea,
} from "@/shared/ui/core/ai-elements/prompt-input";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type DragEvent,
  type ReactNode,
} from "react";
import { ChatComposerAttachments } from "@/features/chat-panel/components/chat-composer-attachments";
import { ChatComposerFormatting } from "@/features/chat-panel/components/chat-composer-formatting";
import { ChatComposerEmoji } from "@/features/chat-panel/components/chat-composer-emoji";
import {
  dragEventHasFiles,
  isDragLeavingDropTarget,
} from "@/features/chat-panel/components/chat-composer-drag-drop";
import { ChatComposerToolbar } from "@/features/chat-panel/components/chat-composer-toolbar";
import {
  ChatMessageSuggestions,
  type ChatMessageSuggestion,
} from "@/features/chat-panel/components/chat-message-suggestions";
import { useAutoResizeChatComposer } from "@/features/chat-panel/hooks/use-auto-resize-chat-composer";
import { useComposerFocusRequest } from "@/features/chat-panel/hooks/use-composer-focus-request";
import { cx } from "@/shared/lib/cx";
import type { ConversationCurrentRunView } from "@/features/chat-panel/hooks/use-conversation-run-queue";
import type { ConversationRunQueueAvailability } from "@/features/chat-panel/hooks/use-conversation-run-queue";
import {
  useChatSessionPendingPrompt,
  useOptionalChatSessionAttachments,
  useOptionalChatSessionTextSelection,
} from "@/shared/chat/chat-session-memory";
import { describeConversationSendFailure } from "@/shared/realtime/conversation-socket.service";
import { useChatMentions } from "@/features/chat-panel/hooks/use-chat-mentions";
import { ChatMentionSuggestions } from "@/features/chat-panel/components/chat-mention-suggestions";
import { ChatMentionHighlights } from "@/features/chat-panel/components/chat-mention-highlights";
import {
  findChatMentionRanges,
  type ChatMentionMember,
} from "@/features/chat-panel/model/chat-mentions";
import { ChatComposerPendingPrompt } from "./chat-composer-pending-prompt";

const emptyArtifactDraft: LibraryArtifact[] = [];

type ChatComposerProps = {
  sessionKey: string;
  artifactContext?: { companyId: string; draftKey: string };
  ariaLabel?: string;
  attachmentsEnabled?: boolean;
  disabled?: boolean;
  /**
   * The viewer may not post here at all (the placeholder says why), not
   * just wait: nothing to add is offered, so "Add to message" is hidden.
   */
  readOnly?: boolean;
  sendBlocked?: boolean;
  currentRun: ConversationCurrentRunView | null;
  draftMessage: string;
  emojiEnabled?: boolean;
  focusRequestKey: number;
  formattingEnabled?: boolean;
  isExpanded: boolean;
  isQueueFull: boolean;
  isStopping: boolean;
  minimal?: boolean;
  mentionMembers?: readonly ChatMentionMember[];
  modelControls?: ReactNode;
  context?: ReactNode;
  onCreateWhiteboard?: () => void;
  onDraftMessageChange: (message: string) => void;
  onFocus?: () => void;
  onSendMessage: (message: PromptInputMessage) => Promise<void>;
  prepareMessage?: (message: PromptInputMessage) => PromptInputMessage;
  onStop?: () => void;
  placeholder?: string;
  queueAvailability: ConversationRunQueueAvailability;
  queueLimit: number;
  suggestions?: readonly ChatMessageSuggestion[];
  sendDisabledReason?: string;
};

export function ChatComposer({
  sessionKey,
  artifactContext,
  ariaLabel = "Message agent",
  attachmentsEnabled = true,
  disabled = false,
  readOnly = false,
  sendBlocked = false,
  currentRun,
  draftMessage,
  emojiEnabled = false,
  focusRequestKey,
  formattingEnabled = false,
  isExpanded,
  isQueueFull,
  isStopping,
  minimal = false,
  mentionMembers,
  modelControls,
  context,
  onCreateWhiteboard,
  onDraftMessageChange,
  onFocus,
  onSendMessage,
  prepareMessage,
  onStop,
  placeholder = "Message agent",
  queueAvailability,
  queueLimit,
  suggestions,
  sendDisabledReason,
}: ChatComposerProps) {
  const pendingPrompt = useChatSessionPendingPrompt(sessionKey);
  const dispatch = useAppDispatch();
  const artifactKey = artifactContext
    ? `${artifactContext.companyId}:${artifactContext.draftKey}`
    : "";
  const draftArtifacts = useAppSelector(
    (state) => state.chat.artifactDrafts?.[artifactKey] ?? emptyArtifactDraft,
  );
  const updateArtifacts = (artifacts: LibraryArtifact[]) =>
    dispatch(setArtifactDraft({ key: artifactKey, artifacts }));
  const [artifactPickerOpen, setArtifactPickerOpen] = useState(false);
  const attachmentsController = useOptionalChatSessionAttachments();
  const missingFiles = attachmentsController?.missingFiles ?? [];
  const hasMissingFiles = missingFiles.length > 0;
  const effectiveSendDisabledReason =
    sendDisabledReason ??
    (hasMissingFiles
      ? "Reattach or dismiss the missing files before sending."
      : undefined);
  const selectionMemory = useOptionalChatSessionTextSelection();
  const selectionMemoryRef = useRef(selectionMemory);
  const [localDraftMessage, setLocalDraftMessage] = useState(draftMessage);
  const [promptAttachmentCount, setPromptAttachmentCount] = useState(0);
  const [isFileDropActive, setIsFileDropActive] = useState(false);
  const [sendFailure, setSendFailure] = useState<string | null>(null);
  const [submissionStatus, setSubmissionStatus] =
    useState<PromptSubmissionStatus>("idle");
  const isSubmitting =
    submissionStatus === "preparing" || submissionStatus === "sending";
  const pendingSendReason = pendingPrompt?.prompt
    ? pendingPrompt.prompt.status === "sending"
      ? "Sending previous message…"
      : "Retry or dismiss the previous message before sending."
    : undefined;
  const localDraftRef = useRef(draftMessage);
  const externalDraftRef = useRef(draftMessage);
  const draftFlushTimerRef = useRef<number | null>(null);
  const onDraftMessageChangeRef = useRef(onDraftMessageChange);
  const flushDraft = useCallback(() => {
    if (draftFlushTimerRef.current !== null) {
      window.clearTimeout(draftFlushTimerRef.current);
      draftFlushTimerRef.current = null;
    }
    const message = localDraftRef.current;

    if (message === externalDraftRef.current) {
      return;
    }

    externalDraftRef.current = message;
    onDraftMessageChangeRef.current(message);
  }, []);

  const hasPromptAttachments =
    (attachmentsEnabled && promptAttachmentCount > 0) ||
    draftArtifacts.length > 0;
  const { promptInputHeight, textareaRef, textareaStyle } =
    useAutoResizeChatComposer({
      additionalVerticalSpace:
        (context ? 60 : 0) +
        (sendFailure ? 72 : 0) +
        (sendDisabledReason ? 40 : 0) +
        (attachmentsEnabled && promptAttachmentCount > 0 ? 58 : 0) +
        (draftArtifacts.length > 0 ? 64 : 0) +
        (formattingEnabled ? 36 : 0),
      draftMessage: localDraftMessage,
      hasPromptAttachments,
    });

  useComposerFocusRequest({ focusRequestKey, textareaRef });

  useLayoutEffect(() => {
    if (submissionStatus === "sent") textareaRef.current?.focus();
  }, [submissionStatus, textareaRef]);

  useEffect(() => {
    selectionMemoryRef.current = selectionMemory;
  }, [selectionMemory]);

  useLayoutEffect(() => {
    const textarea = textareaRef.current;
    const selection = selectionMemory?.selection;
    if (!textarea || !selection) return;
    const maximum = textarea.value.length;
    textarea.setSelectionRange(
      Math.min(selection.start, maximum),
      Math.min(selection.end, maximum),
    );
  }, [selectionMemory?.selection, textareaRef]);

  useEffect(() => {
    if (document.activeElement === textareaRef.current) {
      onFocus?.();
    }
  }, [onFocus, textareaRef]);

  useEffect(() => {
    onDraftMessageChangeRef.current = onDraftMessageChange;
  }, [onDraftMessageChange]);

  useEffect(() => {
    externalDraftRef.current = draftMessage;
    localDraftRef.current = draftMessage;
    setLocalDraftMessage(draftMessage);
  }, [draftMessage]);

  useEffect(
    () => () => {
      flushDraft();
      const textarea = textareaRef.current;
      if (textarea) {
        selectionMemoryRef.current?.setSelection({
          end: textarea.selectionEnd,
          start: textarea.selectionStart,
        });
      }
    },
    [flushDraft, textareaRef],
  );

  useEffect(() => {
    const persistDraft = () => {
      flushDraft();
      void persistor.flush();
    };
    const persistHiddenDraft = () => {
      if (document.visibilityState === "hidden") persistDraft();
    };
    window.addEventListener("pagehide", persistDraft);
    document.addEventListener("visibilitychange", persistHiddenDraft);
    return () => {
      window.removeEventListener("pagehide", persistDraft);
      document.removeEventListener("visibilitychange", persistHiddenDraft);
    };
  }, [flushDraft]);

  const handleDraftChange = (message: string) => {
    setSendFailure(null);
    localDraftRef.current = message;
    setLocalDraftMessage(message);
    if (draftFlushTimerRef.current !== null) {
      window.clearTimeout(draftFlushTimerRef.current);
    }
    draftFlushTimerRef.current = window.setTimeout(() => {
      draftFlushTimerRef.current = null;
      if (localDraftRef.current === externalDraftRef.current) return;
      externalDraftRef.current = localDraftRef.current;
      onDraftMessageChangeRef.current(localDraftRef.current);
    }, 300);
  };

  const clearSubmittedDraft = () => {
    handleDraftChange("");
    flushDraft();
    selectionMemory?.setSelection({ start: 0, end: 0 });
  };

  const mentions = useChatMentions({
    disabled,
    members: mentionMembers,
    onDraftChange: handleDraftChange,
    textareaRef,
  });
  const mentionRanges = findChatMentionRanges(
    localDraftMessage,
    mentionMembers ?? [],
  );

  const handleFileDragEnter = (event: DragEvent<HTMLFormElement>) => {
    if (disabled || !attachmentsEnabled || !dragEventHasFiles(event)) {
      return;
    }

    event.preventDefault();
    setIsFileDropActive(true);
  };
  const handleFileDragOver = (event: DragEvent<HTMLFormElement>) => {
    if (disabled || !attachmentsEnabled || !dragEventHasFiles(event)) {
      return;
    }

    event.preventDefault();
    setIsFileDropActive(true);
  };
  const handleFileDragLeave = (event: DragEvent<HTMLFormElement>) => {
    if (isDragLeavingDropTarget(event)) {
      setIsFileDropActive(false);
    }
  };
  const handleFileDrop = (event: DragEvent<HTMLFormElement>) => {
    if (dragEventHasFiles(event)) {
      event.preventDefault();
    }

    setIsFileDropActive(false);
  };

  const handleSuggestionSelect = (message: string) => {
    if (disabled || localDraftRef.current.trim()) return;
    handleDraftChange(message);
    flushDraft();
    selectionMemory?.setSelection({
      start: message.length,
      end: message.length,
    });
    window.requestAnimationFrame(() => {
      textareaRef.current?.focus({ preventScroll: true });
      textareaRef.current?.setSelectionRange(message.length, message.length);
    });
  };

  return (
    <>
      {!disabled &&
      !pendingPrompt?.prompt &&
      !localDraftMessage.trim() &&
      !hasPromptAttachments &&
      !hasMissingFiles &&
      suggestions?.length ? (
        <ChatMessageSuggestions
          isExpanded={isExpanded}
          onSelect={handleSuggestionSelect}
          suggestions={suggestions}
        />
      ) : null}
      {pendingPrompt?.prompt ? (
        <ChatComposerPendingPrompt
          prompt={pendingPrompt.prompt}
          isExpanded={isExpanded}
          retryDisabled={
            disabled ||
            sendBlocked ||
            Boolean(sendDisabledReason) ||
            queueAvailability !== "ready"
          }
          onRetry={() => pendingPrompt.retry(onSendMessage)}
          onDismiss={pendingPrompt.dismiss}
        />
      ) : null}
      {hasMissingFiles ? (
        <div
          className={cx(
            "workspace-chat-composer-accessory mb-2 shrink-0 rounded-xl border border-[var(--workspace-native-approval)] bg-[var(--workspace-approval-soft,var(--workspace-control-bg))] p-3 text-xs leading-5 text-[var(--workspace-native-text)]",
            isExpanded
              ? "mx-auto w-[calc(100%-48px)] max-w-[832px]"
              : "mx-4 w-[calc(100%-32px)]",
          )}
          id={`${mentions.listId}-missing-attachments`}
          role="status"
        >
          <p className="m-0 font-semibold">
            Reattach{" "}
            {missingFiles.length === 1
              ? "your file"
              : `${missingFiles.length} files`}{" "}
            after reload
          </p>
          <p className="m-0 mt-1 max-h-16 overflow-y-auto break-words">
            {missingFiles.map((file) => file.filename).join(", ")}
          </p>
          <p className="m-0 mt-1 text-[var(--workspace-native-text-muted)]">
            Reattach these files, or dismiss to send without them.
          </p>
          <div className="mt-2 flex items-center gap-4">
            <button
              className="font-semibold underline underline-offset-4 disabled:opacity-50"
              disabled={disabled || !attachmentsEnabled}
              type="button"
              onClick={() => attachmentsController?.openFileDialog()}
            >
              Reattach
            </button>
            <button
              className="font-semibold underline underline-offset-4"
              type="button"
              onClick={() => attachmentsController?.dismissMissingFiles()}
            >
              Dismiss
            </button>
          </div>
        </div>
      ) : null}
      <PromptInput
        artifactIds={draftArtifacts.map((a) => a.id)}
        attachmentsController={
          attachmentsEnabled ? attachmentsController : undefined
        }
        className={cx(
          "workspace-chat-composer relative z-10 mb-[38px] min-h-[92px] shrink-0 overflow-hidden rounded-xl border border-white/30 bg-white/[0.045] shadow-[inset_0_1px_0_rgba(255,255,255,0.18),inset_0_-1px_0_rgba(255,255,255,0.08)] transition-[height,border-color,box-shadow] duration-150 ease-out [&>[data-slot=input-group]]:relative [&>[data-slot=input-group]]:z-[1] [&>[data-slot=input-group]]:flex [&>[data-slot=input-group]]:h-full [&>[data-slot=input-group]]:w-full [&>[data-slot=input-group]]:flex [&>[data-slot=input-group]]:flex-col [&>[data-slot=input-group]]:items-stretch [&>[data-slot=input-group]]:rounded-xl [&>[data-slot=input-group]]:border-0 [&>[data-slot=input-group]]:bg-transparent [&>[data-slot=input-group]]:shadow-none [&>[data-slot=input-group]]:outline-none",
          minimal ? "border-white/12 bg-[#0F233F]" : "backdrop-blur-[10px]",
          isFileDropActive &&
            "border-yellow/70 shadow-[0_0_0_1px_rgba(246,231,121,0.38),0_18px_54px_rgba(0,0,0,0.28),inset_0_1px_0_rgba(255,255,255,0.22)]",
          isExpanded
            ? "mx-auto w-[calc(100%-48px)] max-w-[832px]"
            : "mx-4 w-[calc(100%-32px)]",
        )}
        data-boardly-desktop-drop-active={isFileDropActive ? "true" : "false"}
        data-formatting-enabled={formattingEnabled || undefined}
        style={{ height: formattingEnabled ? "auto" : promptInputHeight }}
        multiple
        onStatusChange={(status) => {
          setSubmissionStatus(status);
          if (status === "preparing") setSendFailure(null);
        }}
        onError={(error) => setSendFailure(error.message)}
        maxFiles={attachmentsEnabled ? undefined : 0}
        onDragEnter={handleFileDragEnter}
        onDragLeave={handleFileDragLeave}
        onDragOver={handleFileDragOver}
        onDrop={handleFileDrop}
        onDropCapture={(event) => {
          if (!attachmentsEnabled && dragEventHasFiles(event)) {
            event.preventDefault();
            event.stopPropagation();
          }
        }}
        onSubmitCapture={(event) => {
          if (
            disabled ||
            sendBlocked ||
            effectiveSendDisabledReason ||
            pendingSendReason ||
            isQueueFull
          ) {
            event.preventDefault();
            event.stopPropagation();
          }
        }}
        onSubmit={async (message) => {
          if (hasMissingFiles) {
            throw new Error("Reattach or dismiss missing files before sending");
          }
          if (disabled || sendBlocked || sendDisabledReason) {
            throw new Error("Sending is currently unavailable");
          }
          if (queueAvailability !== "ready") {
            setSendFailure(
              queueAvailability === "checking"
                ? "Zenith Team is checking the queue. Send when that check finishes."
                : "The queue is unavailable. Use Retry before sending.",
            );
            throw new Error("Conversation run queue is not ready");
          }
          setSendFailure(null);
          // Text-only submissions can release the draft immediately. Submissions
          // with files or artifacts retain their attachments until sending succeeds.
          if (
            pendingPrompt &&
            !message.files?.length &&
            !message.artifactIds?.length &&
            message.clientMessageId &&
            message.requestKey
          ) {
            const prepared = prepareMessage?.(message) ?? message;
            pendingPrompt.send(
              {
                text: prepared.text ?? "",
                clientMessageId: message.clientMessageId,
                requestKey: message.requestKey,
              },
              onSendMessage,
            );
            clearSubmittedDraft();
            return;
          }
          try {
            await onSendMessage(prepareMessage?.(message) ?? message);
            updateArtifacts([]);
          } catch (error) {
            setSendFailure(describeConversationSendFailure(error).message);
            throw error;
          }
          if (
            (message.text?.trim().length ?? 0) > 0 ||
            (message.files?.length ?? 0) > 0 ||
            (message.artifactIds?.length ?? 0) > 0
          ) {
            clearSubmittedDraft();
          }
        }}
      >
        {isFileDropActive ? (
          <div className="workspace-chat-file-drop pointer-events-none absolute inset-0 z-[4] grid place-items-center rounded-xl border border-yellow/50 bg-[#101714]/88 px-4 text-center text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.2)] backdrop-blur-md">
            <div className="flex max-w-[320px] flex-col items-center gap-1.5">
              <span className="text-sm font-black leading-5">
                Drop files to attach
              </span>
              <span className="text-xs font-semibold leading-4 text-white/62">
                Files will be attached to your draft.
              </span>
            </div>
          </div>
        ) : null}
        {formattingEnabled ? (
          <ChatComposerFormatting
            disabled={disabled || isSubmitting}
            onDraftChange={handleDraftChange}
            onSelectionChange={selectionMemory?.setSelection}
            textareaRef={textareaRef}
          />
        ) : null}
        {context}
        <PromptInputBody />
        {artifactPickerOpen && artifactContext && (
          <ArtifactPickerDialog
            mode="reference"
            companyId={artifactContext.companyId}
            selected={draftArtifacts}
            onClose={() => {
              setArtifactPickerOpen(false);
              requestAnimationFrame(() => textareaRef.current?.focus());
            }}
            onAttach={(artifacts) => {
              updateArtifacts(artifacts);
              setArtifactPickerOpen(false);
              requestAnimationFrame(() => textareaRef.current?.focus());
            }}
          />
        )}
        <ArtifactDraftCards
          artifacts={draftArtifacts}
          disabled={disabled || isSubmitting}
          onRemove={(id) =>
            updateArtifacts(draftArtifacts.filter((a) => a.id !== id))
          }
        />
        {attachmentsEnabled ? (
          <ChatComposerAttachments onCountChange={setPromptAttachmentCount} />
        ) : null}
        <PromptInputTextarea
          className={cx(
            "relative z-[1] mx-3 min-h-[24px] max-h-[112px] min-w-0 flex-none resize-none overflow-hidden border-0 bg-transparent p-0 text-sm font-medium leading-5 text-white outline-none placeholder:text-white/30 disabled:opacity-100 [caret-color:white]",
            hasPromptAttachments ? "mt-2" : "mt-[13px]",
          )}
          aria-label={ariaLabel}
          data-mentions-highlighted={mentionRanges.length > 0 || undefined}
          aria-autocomplete={mentionMembers?.length ? "list" : undefined}
          aria-controls={mentions.isOpen ? mentions.listId : undefined}
          aria-activedescendant={mentions.activeOptionId}
          aria-describedby={
            [
              sendDisabledReason ? `${mentions.listId}-send-hint` : undefined,
              hasMissingFiles
                ? `${mentions.listId}-missing-attachments`
                : undefined,
            ]
              .filter(Boolean)
              .join(" ") || undefined
          }
          disabled={disabled || isSubmitting}
          ref={textareaRef}
          style={textareaStyle}
          value={localDraftMessage}
          rows={1}
          placeholder={placeholder}
          onBlur={(event) => {
            mentions.dismiss();
            flushDraft();
            selectionMemory?.setSelection({
              end: event.currentTarget.selectionEnd,
              start: event.currentTarget.selectionStart,
            });
          }}
          onChange={(event) => {
            handleDraftChange(event.target.value);
            mentions.syncSelection(event.currentTarget);
          }}
          onSelect={(event) => {
            if (document.activeElement === event.currentTarget)
              mentions.syncSelection(event.currentTarget);
          }}
          onKeyDown={mentions.onKeyDown}
          onCompositionStart={mentions.onCompositionStart}
          onCompositionEnd={(event) =>
            mentions.onCompositionEnd(event.currentTarget)
          }
          onPaste={(event) => {
            if (
              !attachmentsEnabled &&
              Array.from(event.clipboardData.items).some(
                (item) => item.kind === "file",
              )
            ) {
              event.preventDefault();
            }
          }}
          onFocus={onFocus}
        />
        {mentionRanges.length > 0 ? (
          <ChatMentionHighlights
            ranges={mentionRanges}
            textareaRef={textareaRef}
            value={localDraftMessage}
          />
        ) : null}
        {sendDisabledReason ? (
          <p
            className="chat-composer-send-hint"
            id={`${mentions.listId}-send-hint`}
          >
            {sendDisabledReason}
          </p>
        ) : null}
        {sendFailure ? (
          <div
            aria-live="assertive"
            className="relative z-[2] mx-3 mt-2 rounded-lg border border-[color-mix(in_srgb,var(--workspace-native-danger)_28%,transparent)] bg-[var(--workspace-danger-soft)] px-2.5 py-2 text-[11px] font-medium leading-4 text-[var(--workspace-native-danger)]"
            role="alert"
          >
            <p>{sendFailure}</p>
            <p className="mt-1 text-[var(--workspace-native-text-muted)]">
              Your draft and attachments are still here.
              {submissionStatus === "error" ? (
                <button
                  className="ml-2 font-semibold text-[var(--workspace-native-danger)] underline underline-offset-2"
                  disabled={disabled || isSubmitting}
                  type="submit"
                >
                  Retry sending
                </button>
              ) : null}
            </p>
          </div>
        ) : null}
        <ChatComposerToolbar
          onSelectArtifact={
            artifactContext ? () => setArtifactPickerOpen(true) : undefined
          }
          onCreateWhiteboard={onCreateWhiteboard}
          attachmentsEnabled={attachmentsEnabled && !readOnly}
          modelControls={modelControls}
          emojiAction={
            emojiEnabled ? (
              <ChatComposerEmoji
                disabled={disabled || isSubmitting}
                onDraftChange={handleDraftChange}
                onSelectionChange={selectionMemory?.setSelection}
                textareaRef={textareaRef}
              />
            ) : undefined
          }
          currentRun={currentRun}
          disabled={disabled || isSubmitting}
          draftMessage={localDraftMessage}
          hasAttachments={hasPromptAttachments || hasMissingFiles}
          isQueueFull={isQueueFull}
          isStopping={isStopping}
          minimal={minimal}
          onMention={
            mentionMembers?.length ? mentions.insertTrigger : undefined
          }
          sendDisabledReason={
            sendBlocked
              ? "Saving chat settings…"
              : (effectiveSendDisabledReason ?? pendingSendReason)
          }
          queueAvailability={queueAvailability}
          queueLimit={queueLimit}
          onStop={onStop}
        />
      </PromptInput>
      {mentions.isOpen ? (
        <ChatMentionSuggestions
          activeIndex={mentions.activeIndex}
          listId={mentions.listId}
          members={mentions.matches}
          onActiveIndexChange={mentions.setActiveIndex}
          onSelect={mentions.selectMember}
          textareaRef={textareaRef}
        />
      ) : null}
    </>
  );
}
