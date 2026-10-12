import { Loader2, RefreshCw, Sparkles } from "@/shared/ui/icons";
import { isStudioNodeRunning, type StudioGraphNode } from "./model";
import "./studio-generate-button.css";

export function StudioGenerateButton({
  node,
  pending = false,
  disabledReason,
  onGenerate,
  onError,
}: {
  node: StudioGraphNode;
  pending?: boolean;
  disabledReason?: string;
  onGenerate: (nodeId: string) => Promise<void>;
  onError?: (error: unknown) => void;
}) {
  const running = isStudioNodeRunning(node);
  const busy = pending || running;
  const retry = node.raw.status === "failed" || node.raw.status === "cancelled";
  const regenerate = Boolean(
    node.mediaUrl || node.outputText || node.raw.status === "success",
  );
  const label = busy
    ? node.raw.status === "cancelling"
      ? "Cancelling…"
      : "Generating…"
    : retry
      ? "Try again"
      : regenerate
        ? "Regenerate"
        : "Generate";
  const Icon = busy ? Loader2 : retry || regenerate ? RefreshCw : Sparkles;
  return (
    <button
      type="button"
      className="studio-generate-button nodrag nopan"
      disabled={busy || Boolean(disabledReason)}
      aria-label={`${label.replace(/…$/, "")} ${node.label}`}
      aria-busy={busy}
      title={disabledReason || `${label.replace(/…$/, "")} ${node.label}`}
      onPointerDown={(event) => event.stopPropagation()}
      onDoubleClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => event.stopPropagation()}
      onClick={(event) => {
        event.stopPropagation();
        void onGenerate(node.id).catch((error: unknown) => onError?.(error));
      }}
    >
      <Icon size={15} aria-hidden />
      <span aria-live="polite">{label}</span>
    </button>
  );
}
