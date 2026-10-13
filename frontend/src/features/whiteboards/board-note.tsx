import {
  useCallback,
  useLayoutEffect,
  useRef,
  useSyncExternalStore,
} from "react";
import * as Y from "yjs";
import {
  BoardNoteSession,
  boardMaxNoteText,
  noteText,
} from "./board-note-session";
export function BoardNote({
  session,
  readOnly,
  label,
}: {
  session: BoardNoteSession;
  readOnly: boolean;
  label: string;
}) {
  useSyncExternalStore(session.subscribe, session.snapshot);
  const editable =
    !readOnly &&
    session.canEdit &&
    session.loaded &&
    session.status !== "conflict";
  const input = useRef<HTMLTextAreaElement>(null);
  const composition = useRef<ReturnType<
    BoardNoteSession["startComposition"]
  > | null>(null);
  const selection = useRef<{
    anchor: Y.RelativePosition;
    head: Y.RelativePosition;
  } | null>(null);
  const restoreSelection = useCallback(
    (field: HTMLTextAreaElement) => {
      if (!selection.current || document.activeElement !== field) return;
      const anchor = Y.createAbsolutePositionFromRelativePosition(
        selection.current.anchor,
        session.doc,
      );
      const head = Y.createAbsolutePositionFromRelativePosition(
        selection.current.head,
        session.doc,
      );
      if (anchor && head) field.setSelectionRange(anchor.index, head.index);
    },
    [session],
  );
  const finishComposition = useCallback(
    (field: HTMLTextAreaElement) => {
      const current = composition.current;
      if (!current) return;
      composition.current = null;
      // Remote edits made while composing move the caret's index; keep it on
      // the composed text the same way the non-IME path does.
      const caret = session.finishComposition(current, field.value, {
        start: field.selectionStart,
        end: field.selectionEnd,
      });
      const text = session.text;
      if (field.value !== text) field.value = text;
      if (caret) selection.current = caret;
      restoreSelection(field);
    },
    [session, restoreSelection],
  );
  useLayoutEffect(() => {
    const field = input.current;
    return () => {
      // Culling or a remote move can unmount a note before compositionend.
      // Commit the current IME buffer through the same CRDT merge path.
      if (field) finishComposition(field);
    };
  }, [finishComposition]);
  useLayoutEffect(() => {
    const field = input.current;
    if (!field || composition.current || !session.loaded) return;
    let text = "";
    try {
      text = session.text;
    } catch {
      return;
    }
    if (field.value === text) return;
    field.value = text;
    restoreSelection(field);
  });
  const remember = () => {
    const field = input.current;
    if (!field || !session.loaded || composition.current) return;
    try {
      const text = noteText(session.doc);
      selection.current = {
        anchor: Y.createRelativePositionFromTypeIndex(
          text,
          field.selectionStart,
        ),
        head: Y.createRelativePositionFromTypeIndex(text, field.selectionEnd),
      };
    } catch {
      /* Initial hydration has not completed. */
    }
  };
  return (
    <>
      <textarea
        ref={input}
        className="wb-note-input nodrag nopan nowheel"
        aria-label={label}
        readOnly={!editable}
        maxLength={boardMaxNoteText}
        placeholder={
          session.status === "loading" ? "Loading note…" : "Write an idea…"
        }
        spellCheck
        onSelect={remember}
        onChange={(event) => {
          if (editable && !composition.current) {
            session.edit(event.target.value);
            remember();
          }
        }}
        onCompositionStart={() => {
          if (editable) composition.current = session.startComposition();
        }}
        onCompositionEnd={(event) => {
          finishComposition(event.currentTarget);
          remember();
        }}
        onBlur={(event) => finishComposition(event.currentTarget)}
        onKeyDown={(event) => {
          if (!editable) return;
          if (
            (event.metaKey || event.ctrlKey) &&
            event.key.toLowerCase() === "z"
          ) {
            event.preventDefault();
            event.stopPropagation();
            if (event.shiftKey) session.redo();
            else session.undo();
          }
          if (
            (event.metaKey || event.ctrlKey) &&
            event.key.toLowerCase() === "y"
          ) {
            event.preventDefault();
            event.stopPropagation();
            session.redo();
          }
        }}
      />
      {session.status === "failed" ? (
        // A note that could not load keeps any local draft; loading it again
        // is safe at any time.
        <div
          className="wb-content-unavailable nodrag nopan"
          title={session.error}
          onClick={(event) => event.stopPropagation()}
        >
          <span>Note unavailable</span>
          <button
            type="button"
            aria-label="Retry loading note"
            onClick={() => session.retry()}
          >
            Retry
          </button>
        </div>
      ) : (
        session.error && (
          <span
            className="absolute left-[8px] bottom-[3px] text-[length:10px] text-[#8b3120]"
            title={session.error}
          >
            {session.status === "offline"
              ? "Offline draft"
              : "Draft needs attention"}
          </span>
        )
      )}
      {!readOnly && !session.canEdit && (
        <span className="absolute left-[8px] bottom-[3px] text-[length:10px] text-[#8b3120]">
          Reopen this Board to edit after access changes.
        </span>
      )}
    </>
  );
}
