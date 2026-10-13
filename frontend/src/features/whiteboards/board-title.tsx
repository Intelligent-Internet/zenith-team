import { useRef, useState } from "react";
import { toast } from "@/shared/ui/sonner";
import { useRenameWhiteboardMutation, type BoardScope } from "./board-api";

/**
 * The Whiteboard's title in its header. Whoever may edit the Board renames it
 * in place: Enter or leaving the field saves, Escape keeps the old title.
 */
export function BoardTitle({
  scope,
  title,
  canEdit,
  as: Heading,
}: {
  scope: BoardScope;
  title: string;
  canEdit: boolean;
  as: "h1" | "h2";
}) {
  const [rename] = useRenameWhiteboardMutation();
  const [draft, setDraft] = useState<string>();
  // Enter or Escape ends the edit before the field's blur can end it again.
  const editing = useRef(false);

  function finish(save: boolean) {
    if (!editing.current) return;
    editing.current = false;
    const next = draft?.trim() ?? "";
    setDraft(undefined);
    if (!save || !next || next === title) return;
    rename({ ...scope, title: next })
      .unwrap()
      .catch((error: { message?: string }) =>
        toast.error(error.message ?? "The whiteboard could not be renamed."),
      );
  }

  if (draft !== undefined)
    return (
      <input
        autoFocus
        className="wb-title-field"
        aria-label="Whiteboard title"
        value={draft}
        maxLength={200}
        onFocus={(event) => event.currentTarget.select()}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => finish(true)}
        onKeyDown={(event) => {
          if (event.nativeEvent.isComposing) return;
          if (event.key === "Enter") {
            event.preventDefault();
            finish(true);
          } else if (event.key === "Escape") {
            // The panel around the Board must not close on this Escape.
            event.preventDefault();
            event.stopPropagation();
            finish(false);
          }
        }}
      />
    );
  if (!canEdit) return <Heading title={title}>{title}</Heading>;
  return (
    <Heading>
      <button
        type="button"
        className="wb-title-button"
        title="Rename whiteboard"
        onClick={() => {
          editing.current = true;
          setDraft(title);
        }}
      >
        {title}
      </button>
    </Heading>
  );
}
