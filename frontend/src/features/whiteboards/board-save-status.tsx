import type { ReactNode } from "react";
import * as Popover from "@radix-ui/react-popover";

export type BoardSaveState =
  "idle" | "saving" | "recovering" | "attention" | "offline" | "read-only";

const statusLabels: Record<BoardSaveState, string> = {
  idle: "",
  saving: "Saving…",
  recovering: "Recovering drafts…",
  attention: "Changes need attention",
  offline: "Offline · unsaved",
  "read-only": "Read-only",
};
const feedbackLabels = {
  saving: "Saving…",
  saved: "Saved",
  failed: "Couldn't save",
};

export function BoardSaveStatus({
  state,
  feedback,
  recovery,
}: {
  state: BoardSaveState;
  feedback: "saving" | "saved" | "failed" | null;
  recovery?: ReactNode;
}) {
  let label = statusLabels[state];
  if (state === "idle" || state === "saving" || state === "recovering") {
    label = feedback ? feedbackLabels[feedback] : "";
  }
  if (recovery && !label) label = "Save details";

  const status = (
    <span
      className="wb-board-status"
      data-state={state}
      data-feedback={feedback || undefined}
      role="status"
      aria-live="polite"
      aria-atomic="true"
      title={label || undefined}
    >
      {label}
    </span>
  );

  if (!recovery) return status;

  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          type="button"
          className="wb-save-details"
          aria-label={`${label}. View save details`}
        >
          {status}
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="wb-recovery-popover"
          align="start"
          sideOffset={8}
          collisionPadding={12}
          aria-label="Whiteboard save details"
        >
          <h2>Save details</h2>
          <div className="wb-recovery-list">{recovery}</div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
