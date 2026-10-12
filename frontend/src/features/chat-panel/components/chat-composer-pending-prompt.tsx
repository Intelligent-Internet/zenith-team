import type { PendingChatPrompt } from "@/shared/chat/chat-session-memory";
import { cx } from "@/shared/lib/cx";

export function ChatComposerPendingPrompt({
  prompt,
  isExpanded,
  retryDisabled,
  onRetry,
  onDismiss,
}: {
  prompt: PendingChatPrompt;
  isExpanded: boolean;
  retryDisabled: boolean;
  onRetry: () => void;
  onDismiss: () => void;
}) {
  if (prompt.status !== "error") return null;
  return (
    <div
      className={cx(
        "workspace-chat-composer-accessory mb-2 shrink-0 rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2 text-xs leading-5 text-[var(--workspace-native-text)]",
        isExpanded
          ? "mx-auto w-[calc(100%-48px)] max-w-[832px]"
          : "mx-4 w-[calc(100%-32px)]",
      )}
      role="alert"
    >
      <p className="m-0 font-semibold">{prompt.error}</p>
      <p className="m-0 line-clamp-2 whitespace-pre-wrap break-words text-[var(--workspace-native-text-muted)]">
        {prompt.text}
      </p>
      <div className="mt-1 flex gap-4">
        <button
          className="font-semibold underline underline-offset-4 disabled:opacity-50"
          disabled={retryDisabled}
          onClick={onRetry}
          type="button"
        >
          Retry sending
        </button>
        <button
          className="font-semibold underline underline-offset-4"
          onClick={onDismiss}
          type="button"
        >
          Dismiss
        </button>
      </div>
    </div>
  );
}
