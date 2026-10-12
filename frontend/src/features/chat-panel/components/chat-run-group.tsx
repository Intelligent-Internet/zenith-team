import { useEffect, useState } from "react";
import type { ChatRunDisplayStatus } from "@/features/chat-panel/model/chat-run-groups";
import {
  ChevronDown,
  ChevronRight,
  CircleAlert,
  Square,
} from "@/shared/ui/icons";

const runStatusLabels: Record<ChatRunDisplayStatus, string> = {
  finished: "Run finished",
  needs_input: "Needs input",
  stopped: "Stopped",
  working: "Working",
};

export function ChatRunToggle({
  createdAt,
  isExpanded,
  onToggle,
  status,
}: {
  createdAt?: string;
  isExpanded: boolean;
  onToggle: (trigger: HTMLButtonElement) => void;
  status: ChatRunDisplayStatus;
}) {
  const label = runStatusLabels[status];
  const action = isExpanded ? "Collapse" : "Expand";
  const DisclosureIcon = isExpanded ? ChevronDown : ChevronRight;

  return (
    <button
      aria-expanded={isExpanded}
      aria-label={`${action} run details: ${label.toLowerCase()}`}
      className="workspace-chat-run-toggle group/run-toggle inline-flex max-w-full items-center gap-1.5 rounded-md px-1 py-0.5 text-xs font-normal leading-[18px] text-[var(--workspace-native-text-muted)] transition-colors hover:text-[var(--workspace-native-text)] focus-visible:bg-[var(--workspace-hover-bg)]"
      onClick={(event) => onToggle(event.currentTarget)}
      title={
        status === "finished"
          ? `This run has ended. Task progress and result review are tracked separately. ${action} run details.`
          : `${label} · ${action} run details`
      }
      type="button"
    >
      <RunStatusMark status={status} />
      <span className="min-w-0 truncate">
        <span aria-live="polite">{label}</span>
        {status === "working" && createdAt ? (
          <ChatRunElapsedTime createdAt={createdAt} />
        ) : null}
      </span>
      <DisclosureIcon
        aria-hidden="true"
        className="size-3.5 shrink-0 text-[var(--workspace-native-text-muted)] transition-colors group-hover/run-toggle:text-[var(--workspace-native-text)]"
      />
    </button>
  );
}

function ChatRunElapsedTime({ createdAt }: { createdAt: string }) {
  const [now, setNow] = useState(Date.now);
  const createdAtMs = Date.parse(createdAt);

  useEffect(() => {
    if (!Number.isFinite(createdAtMs)) return;
    const tick = () => setNow(Date.now());
    tick();
    const interval = window.setInterval(tick, 1000);
    return () => window.clearInterval(interval);
  }, [createdAtMs]);

  if (!Number.isFinite(createdAtMs)) return null;
  const elapsedSeconds = Math.max(0, Math.floor((now - createdAtMs) / 1000));
  const seconds = elapsedSeconds % 60;
  const minutes = Math.floor(elapsedSeconds / 60) % 60;
  const hours = Math.floor(elapsedSeconds / 3600);
  const duration = hours
    ? `${hours}h ${String(minutes).padStart(2, "0")}m ${String(seconds).padStart(2, "0")}s`
    : elapsedSeconds >= 60
      ? `${minutes}m ${String(seconds).padStart(2, "0")}s`
      : `${seconds}s`;

  return (
    <span aria-hidden="true">
      {" for "}
      <span className="tabular-nums">{duration}</span>
    </span>
  );
}

function RunStatusMark({ status }: { status: ChatRunDisplayStatus }) {
  if (status === "needs_input") {
    return (
      <CircleAlert
        aria-hidden
        size={12}
        className="shrink-0 text-[var(--workspace-native-approval)]"
      />
    );
  }
  return status === "stopped" ? (
    <Square aria-hidden size={12} className="shrink-0" />
  ) : null;
}
