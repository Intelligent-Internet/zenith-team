import { readSessionSnapshot } from "@/shared/session/session";
import { useEffect, useRef, useState } from "react";
import { toast } from "@/shared/ui/sonner";

type TitleDraft = { value: string; base: string };
const pendingTitles = new Map<string, Promise<void>>();
const titleDrafts = new Map<string, TitleDraft>();

function readDraft(key?: string): TitleDraft | null {
  if (!key) return null;
  if (titleDrafts.has(key)) return titleDrafts.get(key)!;
  try {
    const value = JSON.parse(sessionStorage.getItem(key) || "null");
    return value &&
      typeof value.value === "string" &&
      typeof value.base === "string"
      ? value
      : null;
  } catch {
    return null;
  }
}

export function DocumentTitle({
  title,
  onRename,
  maxLength = 240,
  recoveryKey,
  readOnly = false,
  onPendingChange,
  onSaveErrorChange,
}: {
  title: string;
  onRename?: (title: string, previousTitle: string) => Promise<void>;
  maxLength?: number;
  recoveryKey?: string;
  readOnly?: boolean;
  onPendingChange?: (pending: boolean) => void;
  onSaveErrorChange?: (error: string) => void;
}) {
  const [draft, setDraft] = useState<TitleDraft | null>(() =>
    readDraft(recoveryKey),
  );
  const userId = useRef(readSessionSnapshot().userId);
  const current = useRef(draft);
  const [committedTitle, setCommittedTitle] = useState(title);
  useEffect(() => {
    if (!current.current) setCommittedTitle(title);
  }, [title]);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => onSaveErrorChange?.(error), [error, onSaveErrorChange]);
  const mounted = useRef(true);
  const flight = useRef<Promise<void> | null>(null);
  const latest = useRef({ onRename, onPendingChange });
  latest.current = { onRename, onPendingChange };
  const saveRef = useRef<() => Promise<void>>(async () => {});
  const update = (next: TitleDraft | null) => {
    current.current = next;
    latest.current.onPendingChange?.(!!next);
    setDraft(next);
    setError("");
    if (recoveryKey) {
      if (next) titleDrafts.set(recoveryKey, next);
      else titleDrafts.delete(recoveryKey);
      try {
        if (next) sessionStorage.setItem(recoveryKey, JSON.stringify(next));
        else sessionStorage.removeItem(recoveryKey);
      } catch {
        setError(
          "Draft recovery is unavailable. Keep this Page open until the title saves.",
        );
      }
    }
  };
  const save = (): Promise<void> => {
    if (flight.current) return flight.current;
    if (!current.current || !latest.current.onRename) return Promise.resolve();
    const previous = recoveryKey ? pendingTitles.get(recoveryKey) : undefined;
    const job = (async () => {
      await previous;
      while (
        current.current &&
        latest.current.onRename &&
        readSessionSnapshot().userId === userId.current
      ) {
        const submitted = current.current;
        const value = submitted.value.trim();
        if (!value) {
          if (mounted.current)
            setError("Enter a title, or discard the change.");
          return;
        }
        if (mounted.current) setSaving(true);
        try {
          await latest.current.onRename(value, submitted.base);
        } catch (reason) {
          const message =
            reason instanceof Error
              ? reason.message
              : "The title could not be saved.";
          if (mounted.current) setError(`${message} Your title is kept here.`);
          else
            toast.error(
              "The Page title could not be saved. Reopen the Page to retry your draft.",
            );
          return;
        }
        const next =
          current.current === submitted
            ? null
            : { ...current.current!, base: value };
        current.current = next;
        if (recoveryKey) {
          // A newly opened editor may already have written a more recent draft.
          const stored = readDraft(recoveryKey);
          if (
            stored?.base === submitted.base &&
            (stored.value === submitted.value ||
              (mounted.current && stored.value === next?.value))
          ) {
            if (next) titleDrafts.set(recoveryKey, next);
            else titleDrafts.delete(recoveryKey);
            try {
              if (next)
                sessionStorage.setItem(recoveryKey, JSON.stringify(next));
              else sessionStorage.removeItem(recoveryKey);
            } catch {
              /* The last submitted title is already saved remotely. */
            }
          }
        }
        if (mounted.current) {
          setCommittedTitle(value);
          setDraft(next);
          setError("");
          latest.current.onPendingChange?.(!!next);
        }
      }
    })().finally(() => {
      flight.current = null;
      if (mounted.current) setSaving(false);
      if (recoveryKey && pendingTitles.get(recoveryKey) === job)
        pendingTitles.delete(recoveryKey);
    });
    flight.current = job;
    if (recoveryKey) pendingTitles.set(recoveryKey, job);
    return job;
  };
  saveRef.current = save;
  useEffect(() => {
    latest.current.onPendingChange?.(!!draft || saving);
  }, [draft, saving]);
  useEffect(() => {
    if (!draft || error || readOnly) return;
    const timer = setTimeout(() => void saveRef.current(), 500);
    return () => clearTimeout(timer);
  }, [draft, error, readOnly]);
  useEffect(() => {
    mounted.current = true;
    const protect = (event: BeforeUnloadEvent) => {
      if (current.current) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", protect);
    return () => {
      mounted.current = false;
      void saveRef
        .current()
        .finally(() => window.removeEventListener("beforeunload", protect));
    };
  }, []);
  const value = draft?.value ?? committedTitle;
  return (
    <div className="pc-document-title-field">
      {!onRename || readOnly ? (
        <h1 className="pc-document-title">{value}</h1>
      ) : (
        <textarea
          aria-label="Document title"
          aria-invalid={!!error}
          rows={1}
          value={value}
          maxLength={maxLength}
          onChange={(event) =>
            update({
              value: event.target.value.replace(/\s*\n\s*/g, " "),
              base: current.current?.base ?? committedTitle,
            })
          }
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.nativeEvent.isComposing) {
              event.preventDefault();
              event.currentTarget.blur();
            }
            if (event.key === "Escape") {
              event.preventDefault();
              event.stopPropagation();
              event.currentTarget.blur();
            }
          }}
          onBlur={() => void save()}
        />
      )}
      {error && (
        <div className="pc-inline-error" role="alert">
          <span>{error}</span>
          <button
            disabled={saving || !onRename}
            onClick={() => {
              if (current.current) update({ ...current.current, base: title });
              void save();
            }}
          >
            Retry
          </button>
          <button
            disabled={saving}
            onClick={() => {
              setCommittedTitle(title);
              update(null);
            }}
          >
            Discard title change
          </button>
        </div>
      )}
    </div>
  );
}
