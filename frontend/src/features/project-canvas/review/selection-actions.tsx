import { MessageSquare, X } from "@/shared/ui/icons";

export function SelectionActions({
  label,
  busy,
  onComment,
  onCancel,
}: {
  label: string;
  busy: boolean;
  onComment: () => void;
  onCancel: () => void;
}) {
  return (
    <div
      className="pc-selection-feedback"
      role="region"
      aria-label="Comment selection"
    >
      <div role="status">
        <strong>Selected for comment</strong>
        <span>{label}</span>
      </div>
      <button
        type="button"
        className="pc-primary"
        disabled={busy}
        onClick={onComment}
      >
        <MessageSquare size={15} aria-hidden /> Add comment
      </button>
      <button
        type="button"
        disabled={busy}
        onClick={onCancel}
        aria-label="Cancel comment selection"
      >
        <X size={15} aria-hidden /> Cancel
      </button>
    </div>
  );
}
