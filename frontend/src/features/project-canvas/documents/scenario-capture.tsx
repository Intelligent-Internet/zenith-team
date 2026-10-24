import { supportsCaptureVersions } from "./intelligent-ui/versions";
import { CaptureVersionBoundary } from "./intelligent-ui/capture-version-boundary";
import { CaptureAssumptions } from "./capture-assumptions";
import { useEffect, useRef, useState } from "react";
import type { Editor } from "@tiptap/react";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import {
  usePreviewPresentationMutation,
  useSavePresentationMutation,
  type PageScope,
  type Presentation,
} from "../api/page-api";
import { BlockDocument, SnapshotDocuments } from "./block-editor-schema";
import { collectCaptureInputs } from "./intelligent-ui/capture-inputs";

type Collected = Awaited<ReturnType<typeof collectCaptureInputs>>;
type Prepared = Collected & {
  presentation: Presentation;
  sequence: number;
  requestKey: string;
};

export function ScenarioCapture({
  scope,
  editor,
  flush,
  getSequence,
  onClose,
}: {
  scope: PageScope;
  editor: Editor;
  flush: () => Promise<boolean>;
  getSequence: () => number;
  onClose: () => void;
}) {
  const [preview] = usePreviewPresentationMutation();
  const [save] = useSavePresentationMutation();
  const [prepared, setPrepared] = useState<Prepared>();
  const [saved, setSaved] = useState<Presentation>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const operation = useRef<AbortController | null>(null);
  useEffect(() => () => operation.current?.abort(), []);
  const run = async (action: (signal: AbortSignal) => Promise<void>) => {
    if (operation.current) return;
    const controller = new AbortController();
    operation.current = controller;
    setBusy(true);
    setError("");
    try {
      await action(controller.signal);
    } catch (reason) {
      if (!controller.signal.aborted)
        setError(
          (reason as { message?: string })?.message ??
            "The Page or its sources changed. Prepare a new preview and try again.",
        );
    } finally {
      if (!controller.signal.aborted) setBusy(false);
      if (operation.current === controller) operation.current = null;
    }
  };
  const prepare = () =>
    run(async (signal) => {
      setPrepared(undefined);
      if (!(await flush()))
        throw new Error("Wait for the Page to save, then prepare the capture.");
      const sequence = getSequence();
      const inputs = await collectCaptureInputs(editor, signal);
      inputs.validate();
      const request = preview({
        ...scope,
        input: { sequence, selected: inputs.selected },
      });
      const abort = () => request.abort();
      signal.addEventListener("abort", abort, { once: true });
      try {
        const presentation = await request.unwrap();
        inputs.validate();
        if (getSequence() !== sequence)
          throw new Error("The Page changed. Prepare a new preview.");
        if (!signal.aborted)
          setPrepared({
            ...inputs,
            presentation,
            sequence,
            requestKey: crypto.randomUUID(),
          });
      } finally {
        signal.removeEventListener("abort", abort);
      }
    });
  const capture = () =>
    run(async (signal) => {
      if (!prepared) return;
      if (!supportsCaptureVersions(prepared.presentation.evaluations))
        throw new Error(
          "Update Zenith Team before reviewing and saving this capture format.",
        );
      if (!(await flush()))
        throw new Error(
          "Wait for the Page to save, then prepare a new preview.",
        );
      prepared.validate();
      if (getSequence() !== prepared.sequence)
        throw new Error("The Page changed. Prepare a new preview.");
      const result = await save({
        ...scope,
        input: {
          sequence: prepared.sequence,
          selected: prepared.selected,
          fingerprint: prepared.presentation.fingerprint,
          requestKey: prepared.requestKey,
        },
      }).unwrap();
      if (!signal.aborted) setSaved(result);
    });
  const shown = saved ?? prepared?.presentation;
  const button =
    "rounded-workspace-control border border-workspace-divider px-3 py-2 text-sm disabled:opacity-50 focus-visible:outline focus-visible:outline-2";
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent
        className="flex max-h-[90dvh] w-[calc(100vw-2rem)] flex-col gap-4 sm:max-w-4xl"
        onInteractOutside={(event) => busy && event.preventDefault()}
        onEscapeKeyDown={(event) => busy && event.preventDefault()}
      >
        <header>
          <DialogTitle>
            {saved ? "Capture saved" : "Capture this scenario"}
          </DialogTitle>
          <DialogDescription>
            Review a frozen copy with the current inputs and source
            observations. Open included sources keep their current inputs;
            sources that are not open use their saved defaults.
          </DialogDescription>
        </header>
        <div className="min-h-0 overflow-auto" aria-busy={busy}>
          {error && (
            <p role="alert" className="mb-3 text-sm">
              {error}
            </p>
          )}
          {shown ? (
            <>
              {shown.issues.map((issue, index) => (
                <p key={index} role="alert">
                  {issue}
                </p>
              ))}
              <CaptureVersionBoundary evaluations={shown.evaluations}>
                <CaptureAssumptions evaluations={shown.evaluations} />
                <SnapshotDocuments.Provider value={shown.documents}>
                  <BlockDocument
                    scope={scope}
                    content={shown.content}
                    mode="snapshot"
                  />
                </SnapshotDocuments.Provider>
              </CaptureVersionBoundary>
            </>
          ) : (
            <p className="text-sm text-workspace-muted">
              Prepare a preview to review the exact values before saving.
            </p>
          )}
        </div>
        <footer className="flex flex-wrap justify-end gap-2">
          <span className="mr-auto self-center text-sm" role="status">
            {busy
              ? prepared
                ? "Saving capture…"
                : "Preparing preview…"
              : saved
                ? "Saved as an immutable capture."
                : ""}
          </span>
          <button
            type="button"
            className={button}
            disabled={busy}
            onClick={onClose}
          >
            {saved ? "Done" : "Cancel"}
          </button>
          {!saved && (
            <>
              <button
                type="button"
                className={button}
                disabled={busy}
                onClick={() => void prepare()}
              >
                {prepared ? "Refresh preview" : "Prepare preview"}
              </button>
              <button
                type="button"
                className={button}
                disabled={
                  busy ||
                  !prepared?.presentation.canExport ||
                  !supportsCaptureVersions(prepared?.presentation.evaluations)
                }
                onClick={() => void capture()}
              >
                Save capture
              </button>
            </>
          )}
        </footer>
      </DialogContent>
    </Dialog>
  );
}
