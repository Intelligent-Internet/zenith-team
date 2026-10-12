import type { Ref } from "react";
import { Files } from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";

export function ConversationArtifactsToggle({
  count,
  hasMore = false,
  disabled = false,
  open,
  panelId,
  onToggle,
  ref,
}: {
  count?: number;
  hasMore?: boolean;
  disabled?: boolean;
  open: boolean;
  panelId?: string;
  onToggle: () => void;
  ref?: Ref<HTMLButtonElement>;
}) {
  const countLabel =
    count === undefined ? "" : ` (${count}${hasMore ? "+" : ""})`;
  const label = `${open ? "Hide" : "View"} artifacts${countLabel}`;

  return (
    <Tooltip label={label} disabled={disabled}>
      <button
        ref={ref}
        className="artifacts-icon-button artifacts-toggle"
        aria-label={label}
        aria-expanded={open}
        aria-controls={panelId}
        disabled={disabled}
        type="button"
        onClick={onToggle}
      >
        <Files size={18} aria-hidden />
      </button>
    </Tooltip>
  );
}
