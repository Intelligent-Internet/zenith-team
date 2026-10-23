import { createSourceRecoveryMeasurement } from "./intelligent-ui/performance";
import { useEffect, useRef, useState } from "react";
import type { Editor } from "@tiptap/react";
import type { Node as DocumentNode } from "@tiptap/pm/model";
import { reconcileSourceReplacement } from "./source-replacement";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import { usePreviewPageSourceMutation, type PageScope } from "../api/page-api";
import type { components } from "@/shared/api/generated/api.generated";

import { readSessionSnapshot } from "@/shared/session/session";
import {
  readSourceDrafts,
  createSourceDraftWriter,
  removeSourceDraft,
  type SourceDraft,
} from "./document-cache";
import { downloadDocument } from "./document-export";
import { BlockDocument } from "./block-editor-schema";

type Preview = components["schemas"]["PageSourcePreview"];

// Diagnostics use UTF-8 bytes; browser selections use UTF-16 code units.
function sourcePosition(source: string, byteOffset: number) {
  let bytes = 0,
    offset = 0,
    line = 1,
    column = 1;
  for (const character of source) {
    const code = character.codePointAt(0)!;
    const width = code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
    if (bytes + width > byteOffset) break;
    bytes += width;
    offset += character.length;
    if (character === "\n") {
      line++;
      column = 1;
    } else column++;
  }
  return { offset, line, column };
}

function locate(editor: Editor, blockId: string) {
  let found: { node: DocumentNode; position: number } | undefined;
  editor.state.doc.descendants((node, position) => {
    if (node.attrs.blockId === blockId) found = { node, position };
  });
  if (!found)
    throw new Error(
      "This block was removed. Close the editor and select another block.",
    );
  return found;
}

export function SourceEditor({
  scope,
  blockId,
  intelligentUIAuthoringEnabled,
  editor,
  flush,
  getSequence,
  onClose,
}: {
  scope: PageScope;
  blockId: string;
  intelligentUIAuthoringEnabled: boolean;
  editor: Editor;
  flush: () => Promise<boolean>;
  getSequence: () => number;
  onClose: () => void;
}) {
  const [request] = usePreviewPageSourceMutation();
  const [recoveryTiming] = useState(createSourceRecoveryMeasurement);
  const [base, setBase] = useState<Preview>();
  const [source, setSource] = useState("");
  const [preview, setPreview] = useState<Preview>();
  const [previewDocument, setPreviewDocument] =
    useState<components["schemas"]["CanvasBlock"]>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [drafts, setDrafts] = useState<SourceDraft[]>([]);
  const [draftStatus, setDraftStatus] = useState("");
  const [documentKey] = useState(
    () =>
      `${readSessionSnapshot().userId}:${scope.companyId}:${scope.projectId}:${scope.pageId}:${blockId}:source`,
  );
  const [writeDraft] = useState(() => createSourceDraftWriter(documentKey));
  const draftKey = useRef(crypto.randomUUID());
  const writes = useRef(Promise.resolve());
  const writeRevision = useRef(0);
  const unsavedDraft = useRef(false);
  const [draftFailed, setDraftFailed] = useState(false);
  const alive = useRef(true);
  const sourceInput = useRef<HTMLTextAreaElement>(null);
  const running = useRef(false);
  const preparedDoc = useRef<DocumentNode | undefined>(undefined);
  useEffect(() => {
    alive.current = true;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!unsavedDraft.current) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => {
      window.removeEventListener("beforeunload", beforeUnload);
      alive.current = false;
      recoveryTiming.close();
    };
  }, []);
  const run = async (action: () => Promise<void>) => {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (reason) {
      if (
        alive.current &&
        reason &&
        typeof reason === "object" &&
        "status" in reason &&
        reason.status === 409
      )
        recoveryTiming.stale();
      if (alive.current)
        setError(
          (reason as { message?: string })?.message ??
            "The Page changed or the source is unavailable. Reload the source and try again.",
        );
    } finally {
      running.current = false;
      if (alive.current) setBusy(false);
    }
  };
  const saveDraft = (
    text: string,
    origin: Preview,
    recoveredBase?: Pick<
      SourceDraft,
      "baseSource" | "sequence" | "expectedHash"
    >,
  ) => {
    const revision = ++writeRevision.current;
    const key = draftKey.current;
    unsavedDraft.current = text !== origin.source;
    setDraftFailed(false);
    setDraftStatus("Saving draft on this device…");
    writes.current = writes.current
      .then(async () => {
        if (text === origin.source) await removeSourceDraft(documentKey, key);
        else
          await writeDraft({
            key,
            source: text,
            baseSource: recoveredBase?.baseSource ?? origin.source,
            sequence: recoveredBase?.sequence ?? origin.sequence,
            expectedHash: recoveredBase?.expectedHash ?? origin.expectedHash,
            touched: Date.now(),
          });
        if (revision === writeRevision.current) unsavedDraft.current = false;
        if (alive.current && revision === writeRevision.current)
          setDraftStatus(
            text === origin.source ? "" : "Draft saved on this device.",
          );
      })
      .catch(() => {
        if (alive.current && revision === writeRevision.current) {
          setDraftFailed(true);
          setDraftStatus(
            "Draft could not be saved on this device. Download your source before closing.",
          );
        }
      });
  };
  const preserveDraft = async () => {
    await writes.current;
    if (unsavedDraft.current)
      throw new Error(
        "Your draft has not saved on this device. Retry saving to continue. You can also download the source, then close without saving the draft.",
      );
  };
  const close = () =>
    run(async () => {
      await preserveDraft();
      if (alive.current) onClose();
    });
  const read = () =>
    run(async () => {
      await preserveDraft();
      if (!(await flush()))
        throw new Error("Wait for the Page to save, then load the source.");
      const doc = editor.state.doc;
      const result = await request({ ...scope, input: { blockId } }).unwrap();
      if (!alive.current) return;
      if (doc !== editor.state.doc || result.sequence !== getSequence())
        throw new Error("The Page changed. Load the source again.");
      setDraftStatus("");
      // A fresh authority check precedes exposing any locally retained source.
      await writes.current;
      let saved: SourceDraft[] = [];
      try {
        saved = await readSourceDrafts(documentKey);
      } catch {
        setDraftStatus("Draft recovery is unavailable on this device.");
      }
      if (!alive.current) return;
      if (doc !== editor.state.doc || result.sequence !== getSequence())
        throw new Error("The Page changed. Load the source again.");
      draftKey.current = crypto.randomUUID();
      setDrafts(saved);
      setBase(result);
      setSource(result.source);
      setPreview(undefined);
    });
  const staleSource = () => {
    recoveryTiming.stale();
    return new Error(
      "The Page changed. Copy your edits, then reload the source.",
    );
  };
  const compile = () =>
    run(async () => {
      setPreview(undefined);
      if (!base) return;
      if (!(await flush()))
        throw new Error("Wait for the Page to save before checking source.");
      if (getSequence() !== base.sequence) throw staleSource();
      const doc = editor.state.doc;
      const result = await request({
        ...scope,
        input: {
          blockId,
          source,
          languageVersion: 1,
          expectedSequence: base.sequence,
          expectedHash: base.expectedHash,
        },
      }).unwrap();
      if (!alive.current) return;
      if (doc !== editor.state.doc || getSequence() !== base.sequence)
        throw staleSource();
      if (
        result.content &&
        !result.diagnostics.some((item) => item.severity === "error")
      ) {
        const current = locate(editor, blockId);
        const replacement = editor.schema.nodeFromJSON(result.content);
        replacement.check();
        const tr = editor.state.tr;
        reconcileSourceReplacement(
          tr,
          current.node,
          replacement,
          current.position,
        );
        setPreviewDocument(tr.doc.toJSON());
      } else setPreviewDocument(undefined);
      preparedDoc.current = doc;
      setPreview(result);
    });
  const reactiveApplyDisabled =
    !intelligentUIAuthoringEnabled &&
    ((base?.schemaVersion ?? 1) >= 2 || (preview?.schemaVersion ?? 1) >= 2);
  const apply = () =>
    run(async () => {
      if (
        reactiveApplyDisabled ||
        !preview?.content ||
        !base ||
        preview.diagnostics.length
      )
        return;
      if (!(await flush()))
        throw new Error("Wait for the Page to save before applying.");
      if (!alive.current) return;
      if (!editor.isEditable || editor.isDestroyed)
        throw new Error(
          "Editing is unavailable. Keep your draft and reopen the Page when access is restored.",
        );
      if (
        editor.state.doc !== preparedDoc.current ||
        getSequence() !== base.sequence
      )
        throw staleSource();
      const current = locate(editor, blockId);
      const replacement = editor.schema.nodeFromJSON(preview.content);
      replacement.check();
      const tr = editor.state.tr;
      reconcileSourceReplacement(
        tr,
        current.node,
        replacement,
        current.position,
      );
      editor.view.dispatch(tr);
      setPreview(undefined);
      setBase(undefined);
      if (!(await flush()))
        throw new Error(
          "The edit is in your local document but has not saved yet. Keep the Page open until it reconnects.",
        );
      recoveryTiming.applied();
      unsavedDraft.current = false;
      await writes.current;
      await removeSourceDraft(documentKey, draftKey.current);
      if (alive.current) onClose();
    });
  const button =
    "rounded-workspace-control border border-workspace-divider px-3 py-2 text-sm disabled:opacity-50 focus-visible:outline focus-visible:outline-2";
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && void close()}>
      <DialogContent
        className="flex max-h-[90dvh] w-[calc(100vw-2rem)] flex-col gap-4 sm:max-w-4xl"
        onInteractOutside={(event) => busy && event.preventDefault()}
        onEscapeKeyDown={(event) => busy && event.preventDefault()}
      >
        <header>
          <DialogTitle>Edit block source</DialogTitle>
          <DialogDescription>
            Edit the selected block, check the source, then apply it to the
            Page. Keep the selected root’s type and ID. You can add, remove or
            reorder children; omit blockId on new blocks.
          </DialogDescription>
        </header>
        <div className="min-h-0 overflow-auto" aria-busy={busy}>
          {!intelligentUIAuthoringEnabled && (
            <p role="status" className="mb-3 text-sm text-workspace-muted">
              Reactive authoring is temporarily unavailable. You can edit
              ordinary content and keep or download your source draft.
            </p>
          )}
          {error && (
            <p role="alert" className="mb-3 text-sm">
              {error}
            </p>
          )}
          {base && drafts.length > 0 && (
            <details className="mb-3 text-sm">
              <summary className="cursor-pointer">
                Recover a source draft ({drafts.length})
              </summary>
              <p className="my-2 text-workspace-muted">
                These drafts are saved on this device. Recovery copies the
                source into this editor; check it against the current Page
                before applying.
              </p>
              {drafts.map((draft) => (
                <div
                  key={draft.key}
                  className="mb-2 flex flex-wrap items-center gap-2"
                >
                  <span>{new Date(draft.touched).toLocaleString()}</span>
                  <span>
                    {draft.expectedHash === base.expectedHash
                      ? "Same source base"
                      : "Page source has changed"}
                  </span>
                  <button
                    type="button"
                    className={button}
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        await preserveDraft();
                        if (!alive.current) return;
                        draftKey.current = crypto.randomUUID();
                        if (
                          draft.expectedHash !== base.expectedHash ||
                          draft.sequence !== base.sequence
                        )
                          recoveryTiming.stale();
                        setSource(draft.source);
                        setPreview(undefined);
                        saveDraft(draft.source, base, draft);
                      })
                    }
                  >
                    Recover copy
                  </button>
                  <button
                    type="button"
                    className={button}
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        await removeSourceDraft(documentKey, draft.key);
                        if (alive.current)
                          setDrafts((current) =>
                            current.filter((row) => row.key !== draft.key),
                          );
                      })
                    }
                  >
                    Delete draft
                  </button>
                </div>
              ))}
            </details>
          )}
          {base ? (
            <label className="grid gap-2 text-sm">
              Block source
              <textarea
                ref={sourceInput}
                className="min-h-72 w-full resize-y rounded-workspace-control border border-workspace-divider bg-transparent p-3 font-mono text-sm"
                value={source}
                disabled={busy}
                spellCheck={false}
                onChange={(event) => {
                  setSource(event.target.value);
                  saveDraft(event.target.value, base);
                  setPreview(undefined);
                }}
              />
            </label>
          ) : (
            <p className="text-sm text-workspace-muted">
              Load the saved source for this block to begin.
            </p>
          )}
          {preview?.diagnostics.map((diagnostic, index) => {
            const start = sourcePosition(source, diagnostic.start);
            const end = sourcePosition(source, diagnostic.end);
            return (
              <div
                role={diagnostic.severity === "error" ? "alert" : "status"}
                key={index}
                className="mt-3 text-sm"
              >
                <button
                  type="button"
                  className="underline underline-offset-2 focus-visible:outline focus-visible:outline-2"
                  onClick={() => {
                    sourceInput.current?.focus();
                    sourceInput.current?.setSelectionRange(
                      start.offset,
                      end.offset,
                    );
                  }}
                >
                  Line {start.line}, column {start.column}: {diagnostic.code}
                </button>
                <p>{diagnostic.repair}</p>
              </div>
            );
          })}
          {preview?.content && !preview.diagnostics.length && (
            <p role="status" className="mt-3 text-sm">
              Source checked against the current Page. Ready to apply.
            </p>
          )}
          {preview?.content &&
            previewDocument &&
            !preview.diagnostics.some((item) => item.severity === "error") && (
              <details className="mt-4">
                <summary className="cursor-pointer text-sm">
                  Preview in Page context
                </summary>
                <BlockDocument
                  content={previewDocument}
                  scope={scope}
                  mode="comparison"
                />
              </details>
            )}
        </div>
        <footer className="flex flex-wrap justify-end gap-2">
          <span className="mr-auto self-center text-sm" role="status">
            {busy ? "Working…" : draftStatus}
          </span>
          {base && (
            <button
              type="button"
              className={button}
              disabled={busy}
              onClick={() =>
                downloadDocument(
                  source,
                  "page-source.txt",
                  "text/plain;charset=utf-8",
                )
              }
            >
              Download source
            </button>
          )}
          {draftFailed && base && (
            <button
              type="button"
              className={button}
              disabled={busy}
              onClick={() => saveDraft(source, base)}
            >
              Retry saving draft
            </button>
          )}
          {draftFailed && (
            <button
              type="button"
              className={button}
              disabled={busy}
              onClick={onClose}
            >
              Close without saving draft
            </button>
          )}
          <button
            type="button"
            className={button}
            disabled={busy}
            onClick={() => void close()}
          >
            Close
          </button>
          <button
            type="button"
            className={button}
            disabled={busy}
            onClick={() => void read()}
          >
            {base ? "Reload source" : "Load source"}
          </button>
          <button
            type="button"
            className={button}
            disabled={busy || !base}
            onClick={() => void compile()}
          >
            Check source
          </button>
          <button
            type="button"
            className={button}
            disabled={
              busy ||
              reactiveApplyDisabled ||
              !preview?.content ||
              !!preview?.diagnostics.length
            }
            onClick={() => void apply()}
          >
            Apply to Page
          </button>
        </footer>
      </DialogContent>
    </Dialog>
  );
}
