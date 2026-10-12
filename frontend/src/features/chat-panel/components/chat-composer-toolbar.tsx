import { ChatAddMenu } from "./artifact-picker/chat-add-menu";
import {
  PromptInputButton,
  PromptInputSubmit,
  PromptInputTools,
  usePromptInputAttachments,
} from "@/shared/ui/core/ai-elements/prompt-input";
import { Icon } from "@/shared/ui/icon";
import { AtSign, ArrowUp, SquareFilled } from "@/shared/ui/icons";
import type { ReactNode } from "react";
import type {
  ConversationCurrentRunView,
  ConversationRunQueueAvailability,
} from "@/features/chat-panel/hooks/use-conversation-run-queue";

export function ChatComposerToolbar({
  onSelectArtifact,
  attachmentsEnabled = true,
  currentRun,
  disabled = false,
  draftMessage,
  emojiAction,
  modelControls,
  hasAttachments,
  isQueueFull,
  isStopping,
  minimal = false,
  onStop,
  onMention,
  sendDisabledReason,
  queueAvailability,
  queueLimit,
}: {
  onSelectArtifact?: () => void;
  attachmentsEnabled?: boolean;
  currentRun: ConversationCurrentRunView | null;
  disabled?: boolean;
  draftMessage: string;
  emojiAction?: ReactNode;
  modelControls?: ReactNode;
  hasAttachments: boolean;
  isQueueFull: boolean;
  isStopping: boolean;
  minimal?: boolean;
  onStop?: () => void;
  onMention?: () => void;
  sendDisabledReason?: string;
  queueAvailability: ConversationRunQueueAvailability;
  queueLimit: number;
}) {
  const queueUnavailable = queueAvailability === "unavailable";

  return (
    <PromptInputTools className="workspace-composer-tools relative z-[2] mt-auto flex w-full items-end gap-3 px-3 pb-[13px] text-white">
      <div className="flex min-w-0 flex-1 items-center gap-4 max-[460px]:gap-3">
        {attachmentsEnabled ? (
          <ChatPromptAttachmentButton
            disabled={disabled}
            onSelectArtifact={onSelectArtifact}
          />
        ) : null}
        {onMention ? (
          <PromptInputButton
            aria-label="Mention someone"
            title="Mention someone"
            type="button"
            disabled={disabled}
            className="workspace-composer-action"
            onMouseDown={(event) => event.preventDefault()}
            onClick={onMention}
          >
            <AtSign className="size-[18px]" />
          </PromptInputButton>
        ) : null}
        {emojiAction}
        {minimal && queueUnavailable ? (
          <span className="truncate text-[11px] font-semibold text-white/45">
            Queue unavailable
          </span>
        ) : null}
        {!minimal ? (
          <>
            <PromptInputButton
              className="grid size-[18px] place-items-center transition hover:text-white/72"
              type="button"
              aria-label="Open tools"
              disabled={disabled}
            >
              <Icon name="toolbox" className="size-[18px]" />
            </PromptInputButton>
            <div className="flex min-w-0 items-center gap-2 max-[460px]:hidden">
              <span className="whitespace-nowrap rounded-[6px] bg-white/[0.08] px-3 py-1.5 text-xs font-medium leading-none text-white/55">
                / command
              </span>
              <span className="whitespace-nowrap rounded-[6px] bg-white/[0.08] px-3 py-1.5 text-xs font-medium leading-none text-white/55">
                @ mention
              </span>
            </div>
          </>
        ) : null}
      </div>
      <div className="flex min-w-0 items-center gap-2">
        {modelControls}
        {!minimal ? (
          <PromptInputButton
            className="grid size-[18px] place-items-center text-white transition hover:text-white/72"
            type="button"
            aria-label="Record voice"
            disabled={disabled}
          >
            <Icon name="mic" className="size-[18px]" />
          </PromptInputButton>
        ) : null}
        <ChatPromptSubmitButton
          currentRun={currentRun}
          disabled={disabled}
          sendDisabledReason={sendDisabledReason}
          draftMessage={draftMessage}
          hasAttachments={hasAttachments}
          isQueueFull={isQueueFull}
          isStopping={isStopping}
          queueAvailability={queueAvailability}
          queueLimit={queueLimit}
          onStop={onStop}
        />
      </div>
    </PromptInputTools>
  );
}

function ChatPromptAttachmentButton({
  disabled,
  onSelectArtifact,
}: {
  disabled?: boolean;
  onSelectArtifact?: () => void;
}) {
  const attachments = usePromptInputAttachments();
  return (
    <ChatAddMenu
      disabled={disabled}
      onSelectArtifact={onSelectArtifact}
      onUpload={() => {
        if (attachments.fileInputRef.current)
          attachments.fileInputRef.current.value = "";
        attachments.openFileDialog();
      }}
    />
  );
}

function ChatPromptSubmitButton({
  currentRun,
  disabled = false,
  draftMessage,
  hasAttachments,
  isQueueFull,
  isStopping,
  queueAvailability,
  queueLimit,
  onStop,
  sendDisabledReason,
}: {
  currentRun: ConversationCurrentRunView | null;
  disabled?: boolean;
  draftMessage: string;
  hasAttachments: boolean;
  isQueueFull: boolean;
  isStopping: boolean;
  queueAvailability: ConversationRunQueueAvailability;
  queueLimit: number;
  onStop?: () => void;
  sendDisabledReason?: string;
}) {
  const attachments = usePromptInputAttachments();
  const hasContent =
    draftMessage.trim().length > 0 ||
    hasAttachments ||
    attachments.files.length > 0;
  const stopping = isStopping || currentRun?.cancellationPending === true;
  const canSend =
    !disabled &&
    !sendDisabledReason &&
    queueAvailability === "ready" &&
    hasContent &&
    !isQueueFull;

  // No onStop: the run is on someone else's computer, which only its
  // member, an owner or an admin may stop (ADR 0014).
  if (currentRun && onStop && !hasContent) {
    return (
      <PromptInputButton
        aria-label={stopping ? "Stopping run" : "Stop run"}
        className="workspace-chat-stop-button size-8 shrink-0 rounded-full p-0 disabled:cursor-not-allowed disabled:opacity-45"
        disabled={disabled || stopping}
        title={stopping ? "Stopping…" : "Stop"}
        type="button"
        onClick={onStop}
      >
        <SquareFilled
          aria-hidden
          className={stopping ? "motion-safe:animate-pulse" : undefined}
          size={16}
        />
      </PromptInputButton>
    );
  }

  return (
    <div className="flex items-center gap-2">
      {isQueueFull && hasContent ? (
        <span className="whitespace-nowrap text-[12px] font-semibold text-[var(--workspace-native-approval)]">
          Queue full ({queueLimit})
        </span>
      ) : null}
      <PromptInputSubmit
        data-ready={canSend || undefined}
        className="workspace-chat-send-button inline-grid size-8 min-h-8 min-w-8 max-w-8 flex-none aspect-square cursor-pointer place-items-center rounded-full p-0 leading-none shadow-none transition-colors disabled:cursor-not-allowed disabled:opacity-45"
        aria-label="Send message"
        disabled={!canSend}
        title={
          sendDisabledReason ??
          (queueAvailability === "unavailable"
            ? "Queue unavailable"
            : "Send message")
        }
      >
        <ArrowUp size={18} aria-hidden />
      </PromptInputSubmit>
    </div>
  );
}
