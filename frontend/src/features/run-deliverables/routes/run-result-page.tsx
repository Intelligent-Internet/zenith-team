import { DeliveryFreshness } from "../components/delivery-freshness";
import { RunWikiSources } from "@/features/company-wiki/agent-reads/run-wiki-sources";
import { OpenArtifactCanvasButton } from "@/features/run-deliverables/components/open-artifact-canvas-button";
import { ArtifactFilePreview } from "@/features/chat-panel/components/artifacts/artifact-file-preview";
import { artifactFileSelection } from "@/features/chat-panel/model/artifact-file-access";
import { RunContext } from "../components/run-context";
import {
  AlertCircle,
  FileText,
  Download,
  ExternalLink,
  Globe2,
  LoaderCircle,
  PackageCheck,
  RefreshCcw,
  Rocket,
  Trash2,
} from "@/shared/ui/icons";
import { useState } from "react";
import {
  Navigate,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";

import { readSessionSnapshot } from "@/shared/session/session";
import { ArtifactAppView } from "@/features/chat-panel/components/artifacts/artifact-app-view";
import {
  useGetRunDetailsQuery,
  useGetRunResultQuery,
  usePublishRunOutputMutation,
  useUnpublishRunOutputMutation,
} from "@/features/run-deliverables/api/run-deliverables-api";
import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import { PublicationSubdomainForm } from "@/features/run-deliverables/components/publication-subdomain-form";
import { OutputDetailsPreview } from "@/features/run-deliverables/components/output-details-preview";
import type {
  OutputPublication,
  RunOutputItem,
} from "@/features/run-deliverables/model/run-deliverable";
import { cx } from "@/shared/lib/cx";

import "@/features/chat-panel/styles/conversation-artifacts.css";

export function RunResultPage() {
  const navigate = useNavigate();
  const { runId = "" } = useParams();
  const [searchParams, setItemParams] = useSearchParams();
  const artifactId = searchParams.get("artifactId") ?? undefined;
  const companyId = readSessionSnapshot().company?.id ?? "";
  const query = useGetRunResultQuery(
    { artifactId, companyId, runId },
    { skip: !companyId || !runId },
  );
  const runDetails = useGetRunDetailsQuery(
    { companyId, runId },
    { skip: !companyId || !runId },
  );
  const [publish, publishState] = usePublishRunOutputMutation();
  const [unpublish, unpublishState] = useUnpublishRunOutputMutation();
  const [downloadingOrdinal, setDownloadingOrdinal] = useState<number | null>(
    null,
  );
  const [downloadError, setDownloadError] = useState<number | null>(null);
  const [publicationAction, setPublicationAction] = useState<
    "idle" | "confirm_publish" | "confirm_unpublish"
  >("idle");
  const [publicationError, setPublicationError] = useState<string | null>(null);
  const result = query.currentData;
  const output = result?.output;
  const deliverable = result?.deliverable;
  const orderedItems = output?.items ?? [];
  const selectedItem =
    orderedItems.find(
      (item) => String(item.ordinal) === searchParams.get("item"),
    ) ?? orderedItems[0];
  const publication = result?.publication;
  const appArtifact =
    result && output?.type === "app"
      ? {
          runId,
          runtimeId: result.runtimeId,
          runStatus: result.runStatus,
          artifact: output,
          deliverable: result.deliverable,
          publication: result.publication,
        }
      : null;

  const downloadItem = async (item: RunOutputItem) => {
    if (!output || downloadingOrdinal !== null) return;
    setDownloadingOrdinal(item.ordinal);
    setDownloadError(null);
    try {
      const blob = await runDeliverableService.downloadItem(
        companyId,
        runId,
        output.id,
        item.ordinal,
      );
      const objectUrl = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = objectUrl;
      link.download = item.displayName;
      link.click();
      window.setTimeout(() => URL.revokeObjectURL(objectUrl), 0);
    } catch {
      setDownloadError(item.ordinal);
    } finally {
      setDownloadingOrdinal(null);
    }
  };

  const publishOutput = async () => {
    if (publishState.isLoading) return;
    setPublicationError(null);
    try {
      await publish({ companyId, runId }).unwrap();
      setPublicationAction("idle");
      await query.refetch();
    } catch {
      setPublicationError(
        "Publishing could not start. Refresh the result and try again.",
      );
      await query.refetch();
    }
  };

  const unpublishOutput = async () => {
    if (!publication || unpublishState.isLoading) return;
    setPublicationError(null);
    try {
      await unpublish({
        companyId,
        expectedRevision: publication.revision,
        runId,
      }).unwrap();
      setPublicationAction("idle");
      await query.refetch();
    } catch {
      setPublicationError(
        "The publication changed or could not be removed. Its latest state has been loaded.",
      );
      await query.refetch();
    }
  };

  // A Run never presents a Whiteboard; one opens on its own Artifact page.
  if (output?.type === "whiteboard")
    return (
      <Navigate
        to={`/companies/${encodeURIComponent(companyId)}/artifacts/${encodeURIComponent(output.id)}`}
        replace
      />
    );

  return (
    <main className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain bg-[var(--workspace-content-bg)] text-[var(--workspace-native-text)] [scrollbar-gutter:stable]">
      <div className="relative mx-auto flex w-full max-w-6xl flex-col gap-5 px-7 py-6 max-[700px]:px-3 max-[700px]:py-4">
        <RunContext companyId={companyId} runId={runId} />
        {runDetails.currentData &&
        runDetails.currentData.runType !== "schedule" ? (
          <div className="rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] p-4">
            <RunWikiSources
              companyId={companyId}
              runId={runId}
              agentId={runDetails.currentData.agentId}
              runStatus={runDetails.currentData.status}
            />
          </div>
        ) : null}
        {query.isLoading ? (
          <RunResultLoading />
        ) : query.isError ? (
          <RunResultError onRetry={() => void query.refetch()} />
        ) : !result || !output ? (
          <NoPresentedResult
            runStatus={result?.runStatus}
            onRetry={() => void query.refetch()}
          />
        ) : (
          <>
            <header className="overflow-hidden rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)]">
              <div className="border-b border-[var(--workspace-divider)] px-6 py-5 max-[700px]:px-4">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.16em] text-[var(--workspace-native-info)]">
                    <PackageCheck className="size-4" aria-hidden />
                    Artifact details
                  </div>
                  <OpenArtifactCanvasButton
                    companyId={companyId}
                    output={output}
                  />
                  <span className="rounded-full border border-[var(--workspace-divider)] px-3 py-1 text-xs font-bold text-[var(--workspace-native-text-muted)]">
                    Saved Output
                  </span>
                </div>
                <h2 className="m-0 mt-3 max-w-4xl text-2xl font-semibold leading-8 tracking-[-0.025em] text-[var(--workspace-native-text)] max-[700px]:text-xl">
                  {output.title}
                </h2>
                <p className="m-0 mt-3 max-w-3xl text-sm leading-6 text-[var(--workspace-native-text-muted)]">
                  {output.summary}
                </p>
                <DeliveryFreshness output={output} />
              </div>
              <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-5 px-6 py-4 text-xs text-[var(--workspace-native-text-muted)] max-[700px]:grid-cols-1 max-[700px]:px-4">
                <span>
                  Presented {formatDateTime(output.createdAt)} ·{" "}
                  {output.type === "app" ? (
                    "Interactive app preview"
                  ) : (
                    <>
                      {orderedItems.length} item
                      {orderedItems.length === 1 ? "" : "s"}
                    </>
                  )}
                </span>
                <span className="inline-flex items-center gap-2 font-semibold text-[var(--workspace-native-text-muted)]">
                  <PackageCheck
                    className="size-4 text-[var(--workspace-native-success)]"
                    aria-hidden
                  />
                  {output.type === "app"
                    ? "Runtime-bound preview"
                    : "Frozen Artifact"}
                </span>
              </div>
            </header>

            {selectedItem && output.type !== "app" ? (
              <section
                className="min-w-0 overflow-hidden [border:1px_solid_var(--workspace-divider)] rounded-[12px]"
                aria-label="Artifact preview"
              >
                <div
                  className="run-document-tabs flex gap-[8px] p-[12px] overflow-x-auto [border-bottom:1px_solid_var(--workspace-divider)]"
                  role="group"
                  aria-label="Choose a file to preview"
                >
                  {orderedItems.map((item) => (
                    <button
                      key={item.id}
                      type="button"
                      aria-pressed={item.id === selectedItem.id}
                      title={item.displayName}
                      onClick={() =>
                        setItemParams(
                          (current) => {
                            const next = new URLSearchParams(current);
                            next.set("item", String(item.ordinal));
                            return next;
                          },
                          { replace: true },
                        )
                      }
                    >
                      {item.displayName}
                    </button>
                  ))}
                </div>
                <div className="flex flex-col h-[min(65vh,_720px)] min-h-[300px] overflow-auto">
                  <ArtifactFilePreview
                    key={selectedItem.id}
                    companyId={companyId}
                    file={artifactFileSelection(runId, output.id, selectedItem)}
                  />
                </div>
              </section>
            ) : null}
            {output.type === "app" ? (
              <OutputDetailsPreview
                key={`${companyId}:${runId}:${output.id}`}
                companyId={companyId}
                runId={runId}
                output={output}
              />
            ) : null}

            <div className="grid grid-cols-[minmax(0,1fr)_360px] items-start gap-6 max-[980px]:grid-cols-1">
              <section
                aria-labelledby="submitted-items-heading"
                className="min-w-0"
              >
                <div className="mb-3 flex items-end justify-between gap-3 px-1">
                  <div>
                    <p className="m-0 text-[11px] font-semibold uppercase tracking-[0.16em] text-[var(--workspace-native-text-muted)]">
                      {deliverable ? "Complete submission" : "Saved Artifact"}
                    </p>
                    <h2
                      id="submitted-items-heading"
                      className="m-0 mt-1 text-base font-semibold text-[var(--workspace-native-text)]"
                    >
                      {output.type === "app"
                        ? "Interactive app preview"
                        : "Artifact contents"}
                    </h2>
                  </div>
                </div>
                {appArtifact ? (
                  <div className="flex h-[min(70vh,680px)] min-h-[420px] overflow-hidden rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] max-[700px]:min-h-[360px]">
                    <ArtifactAppView
                      artifact={appArtifact}
                      companyId={companyId}
                      onEscape={() => navigate(-1)}
                    />
                  </div>
                ) : (
                  <ol className="m-0 grid list-none gap-3 p-0">
                    {orderedItems.map((item) => (
                      <OutputItemCard
                        downloadError={downloadError === item.ordinal}
                        downloading={downloadingOrdinal === item.ordinal}
                        item={item}
                        key={`${item.ordinal}:${item.label}`}
                        onDownload={() => void downloadItem(item)}
                      />
                    ))}
                  </ol>
                )}
                {output.submissionNotes ? (
                  <div className="mt-4 rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] p-5">
                    <h3 className="m-0 text-sm font-semibold text-[var(--workspace-native-text)]">
                      Submission notes
                    </h3>
                    <p className="m-0 mt-2 whitespace-pre-wrap text-sm leading-6 text-[var(--workspace-native-text-muted)]">
                      {output.submissionNotes}
                    </p>
                  </div>
                ) : null}
              </section>

              <aside className="self-start rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-panel-raised-bg)] p-5">
                <PendingTerminalState runStatus={result.runStatus} />
                {result.canPublish || result.canUnpublish || publication ? (
                  <PublicationPanel
                    action={publicationAction}
                    canPublish={result.canPublish}
                    canUnpublish={result.canUnpublish}
                    error={publicationError}
                    publication={publication}
                    publishing={publishState.isLoading}
                    unpublishing={unpublishState.isLoading}
                    onActionChange={setPublicationAction}
                    onPublish={() => void publishOutput()}
                    onUnpublish={() => void unpublishOutput()}
                  />
                ) : null}
                {publication && result.publicationSubdomain ? (
                  <PublicationSubdomainForm
                    key={publication.id}
                    companyId={companyId}
                    runId={runId}
                    publication={publication}
                    options={result.publicationSubdomain}
                  />
                ) : null}
              </aside>
            </div>
          </>
        )}
      </div>
    </main>
  );
}

function OutputItemCard({
  downloadError,
  downloading,
  item,
  onDownload,
}: {
  downloadError: boolean;
  downloading: boolean;
  item: RunOutputItem;
  onDownload: () => void;
}) {
  const isFinal = item.role === "primary" || item.role === "final";
  return (
    <li
      className={cx(
        "group rounded-lg border bg-[var(--workspace-panel-bg)] p-5 transition",
        isFinal
          ? "border-[color-mix(in_srgb,var(--workspace-native-info)_25%,var(--workspace-panel-bg))]"
          : "border-[var(--workspace-divider)]",
      )}
    >
      <div className="flex items-start gap-4">
        <div
          className={cx(
            "grid size-11 shrink-0 place-items-center rounded-md border",
            isFinal
              ? "border-[color-mix(in_srgb,var(--workspace-native-info)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-info-soft)] text-[var(--workspace-native-info)]"
              : "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)]",
          )}
        >
          <FileText className="size-5" aria-hidden />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="m-0 text-base font-semibold text-[var(--workspace-native-text)]">
              {item.label}
            </h3>
          </div>
          <p className="m-0 mt-2 truncate text-sm font-semibold text-[var(--workspace-native-text)]">
            {item.displayName}
          </p>
          <p className="m-0 mt-1 text-xs text-[var(--workspace-native-text-muted)]">
            {item.detail}
          </p>
          <p
            className={cx(
              "m-0 mt-3 text-xs leading-5",
              downloadError
                ? "text-[var(--workspace-native-danger)]"
                : "text-[var(--workspace-native-text-muted)]",
            )}
            role={downloadError ? "alert" : undefined}
          >
            {downloadError
              ? "The file could not be downloaded. Try again."
              : "The original file is saved securely and available to download."}
          </p>
        </div>
        <button
          className="inline-flex shrink-0 items-center gap-1.5 rounded-lg border border-[var(--workspace-divider)] px-3 py-2 text-xs font-bold text-[var(--workspace-native-text-muted)] transition hover:border-[var(--workspace-native-accent)] hover:text-[var(--workspace-native-info)] disabled:opacity-50"
          disabled={downloading}
          type="button"
          onClick={onDownload}
        >
          {downloading ? (
            <LoaderCircle className="size-3.5 animate-spin" aria-hidden />
          ) : (
            <Download className="size-3.5" aria-hidden />
          )}
          {downloading ? "Downloading…" : "Download file"}
        </button>
      </div>
    </li>
  );
}

function PublicationPanel({
  action,
  canPublish,
  canUnpublish,
  error,
  publication,
  publishing,
  unpublishing,
  onActionChange,
  onPublish,
  onUnpublish,
}: {
  action: "idle" | "confirm_publish" | "confirm_unpublish";
  canPublish: boolean;
  canUnpublish: boolean;
  error: string | null;
  publication?: OutputPublication | null;
  publishing: boolean;
  unpublishing: boolean;
  onActionChange: (
    action: "idle" | "confirm_publish" | "confirm_unpublish",
  ) => void;
  onPublish: () => void;
  onUnpublish: () => void;
}) {
  const active = publication && publicationIsActive(publication.status);
  const published = publication?.status === "published";
  const providerLabel = publicationProviderLabel(publication?.providerType);

  return (
    <section
      aria-labelledby="publication-heading"
      className="mt-6 border-t border-[var(--workspace-divider)] pt-5"
    >
      <div className="flex items-center gap-2 text-[var(--workspace-native-info)]">
        <Globe2 className="size-5" aria-hidden />
        <h2 id="publication-heading" className="m-0 text-base font-semibold">
          Public app
        </h2>
      </div>

      {active ? (
        <div className="mt-4" aria-live="polite" role="status">
          <div className="flex items-center gap-3 rounded-md border border-[color-mix(in_srgb,var(--workspace-native-info)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-info-soft)] p-3">
            <LoaderCircle
              className="size-5 shrink-0 animate-spin text-[var(--workspace-native-info)]"
              aria-hidden
            />
            <div>
              <p className="m-0 text-sm font-semibold text-[var(--workspace-native-text)]">
                {publicationStatusLabel(publication.status)}
              </p>
              <p className="m-0 mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                You can leave this page. Zenith Team will keep this job and
                resume it after a server restart.
              </p>
            </div>
          </div>
        </div>
      ) : published ? (
        <div className="mt-4">
          <div className="rounded-md border border-[color-mix(in_srgb,var(--workspace-native-success)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-success-soft)] p-3">
            <p className="m-0 text-sm font-semibold text-[var(--workspace-native-success)]">
              Live on {providerLabel}
            </p>
            {publication.url ? (
              <a
                className="mt-2 inline-flex items-center gap-1.5 text-sm font-bold text-[var(--workspace-native-info)] underline decoration-[var(--workspace-native-info)] underline-offset-4 hover:text-[var(--workspace-native-text)]"
                href={publication.customUrl ?? publication.url}
                rel="noreferrer"
                target="_blank"
              >
                Open live app
                <ExternalLink className="size-3.5" aria-hidden />
              </a>
            ) : null}
          </div>
        </div>
      ) : publication?.status === "failed" ? (
        <div
          className="mt-4 rounded-md border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-danger-soft)] p-3"
          role="alert"
        >
          <p className="m-0 text-sm font-semibold text-[var(--workspace-native-danger)]">
            Publication failed
          </p>
          <p className="m-0 mt-1 text-xs leading-5 text-[var(--workspace-native-danger)]">
            {publication.failureCode === "provider_unavailable"
              ? "The hosting provider could not complete publication."
              : (publication.failureDetail ??
                "The app could not be published.")}
          </p>
          {!canPublish && !canUnpublish ? (
            <p className="m-0 mt-2 text-xs leading-5 text-[var(--workspace-native-danger)]">
              Recovery is unavailable for this result. Check owner access and
              the hosting configuration.
            </p>
          ) : null}
          {publication.url ? (
            <a
              className="mt-2 inline-flex items-center gap-1.5 text-xs font-bold text-[var(--workspace-native-info)] underline underline-offset-4"
              href={publication.url}
              rel="noreferrer"
              target="_blank"
            >
              The previous app may still be live
              <ExternalLink className="size-3" aria-hidden />
            </a>
          ) : null}
        </div>
      ) : publication?.status === "unpublished" ? (
        <p className="m-0 mt-3 text-sm leading-6 text-[var(--workspace-native-text-muted)]">
          The {providerLabel} deployment was removed. The saved application
          source is still available and can be published again.
        </p>
      ) : (
        <p className="m-0 mt-3 text-sm leading-6 text-[var(--workspace-native-text-muted)]">
          Publish this application through the configured hosting provider. Only
          publish source that is safe to send to that provider and expose
          publicly.
        </p>
      )}

      {action === "confirm_publish" ? (
        <div className="mt-4 rounded-md border border-[color-mix(in_srgb,var(--workspace-native-approval)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-approval-soft)] p-3">
          <p className="m-0 text-sm font-semibold text-[var(--workspace-native-approval)]">
            Make this output public?
          </p>
          <p className="m-0 mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
            The hosting provider will receive and build the application source,
            then expose the resulting app at a public URL. Anyone with the URL
            can open it.
          </p>
          <div className="mt-3 flex gap-2">
            <button
              className="inline-flex h-9 flex-1 items-center justify-center gap-2 rounded-lg bg-[var(--workspace-accent-fill)] px-3 text-xs font-semibold text-[var(--workspace-accent-foreground)] disabled:opacity-50"
              disabled={publishing}
              type="button"
              onClick={onPublish}
            >
              {publishing ? (
                <LoaderCircle className="size-3.5 animate-spin" aria-hidden />
              ) : (
                <Rocket className="size-3.5" aria-hidden />
              )}
              Confirm and publish
            </button>
            <button
              className="h-9 rounded-lg border border-[var(--workspace-divider)] px-3 text-xs font-bold text-[var(--workspace-native-text-muted)]"
              disabled={publishing}
              type="button"
              onClick={() => onActionChange("idle")}
            >
              Cancel
            </button>
          </div>
        </div>
      ) : action === "confirm_unpublish" ? (
        <div className="mt-4 rounded-md border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-danger-soft)] p-3">
          <p className="m-0 text-sm font-semibold text-[var(--workspace-native-danger)]">
            {publication?.status === "failed" && !publication.url
              ? "Clean up the failed publication?"
              : "Remove the public app?"}
          </p>
          <p className="m-0 mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
            Zenith Team will delete the {providerLabel} deployment and its
            managed build artifacts. The saved source remains available for a
            future publication.
          </p>
          <div className="mt-3 flex gap-2">
            <button
              className="inline-flex h-9 flex-1 items-center justify-center gap-2 rounded-lg bg-[var(--workspace-native-danger)] px-3 text-xs font-semibold text-[var(--workspace-danger-foreground)] disabled:opacity-50"
              disabled={unpublishing}
              type="button"
              onClick={onUnpublish}
            >
              {unpublishing ? (
                <LoaderCircle className="size-3.5 animate-spin" aria-hidden />
              ) : (
                <Trash2 className="size-3.5" aria-hidden />
              )}
              Confirm removal
            </button>
            <button
              className="h-9 rounded-lg border border-[var(--workspace-divider)] px-3 text-xs font-bold text-[var(--workspace-native-text-muted)]"
              disabled={unpublishing}
              type="button"
              onClick={() => onActionChange("idle")}
            >
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <div className="mt-4 grid gap-2">
          {canPublish ? (
            <button
              className="inline-flex h-9 items-center justify-center gap-2 rounded-md bg-[var(--workspace-accent-fill)] px-4 text-sm font-semibold text-[var(--workspace-accent-foreground)] transition hover:bg-[var(--workspace-accent-hover)]"
              type="button"
              onClick={() => onActionChange("confirm_publish")}
            >
              <Rocket className="size-4" aria-hidden />
              {publication?.status === "failed"
                ? "Try publishing again"
                : "Publish to web"}
            </button>
          ) : null}
          {canUnpublish ? (
            <button
              className="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-danger-soft)] px-4 text-sm font-semibold text-[var(--workspace-native-danger)] transition hover:bg-[var(--workspace-hover-bg)]"
              type="button"
              onClick={() => onActionChange("confirm_unpublish")}
            >
              <Trash2 className="size-4" aria-hidden />
              {publication?.status === "failed" && !publication.url
                ? "Clean up failed publication"
                : "Remove public app"}
            </button>
          ) : null}
        </div>
      )}

      {error ? (
        <p
          className="m-0 mt-3 rounded-md border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-danger-soft)] px-3 py-2 text-xs leading-5 text-[var(--workspace-native-danger)]"
          role="alert"
        >
          {error}
        </p>
      ) : null}
    </section>
  );
}

function publicationIsActive(status: OutputPublication["status"]) {
  return (
    status === "queued" ||
    status === "uploading" ||
    status === "building" ||
    status === "deploying" ||
    status === "routing" ||
    status === "unpublishing"
  );
}

function publicationStatusLabel(status: OutputPublication["status"]) {
  switch (status) {
    case "queued":
      return "Queued for publication";
    case "uploading":
      return "Preparing the application source";
    case "building":
      return "Building the app";
    case "deploying":
      return "Starting the hosting service";
    case "routing":
      return "Configuring the custom URL";
    case "unpublishing":
      return "Removing the public app";
    default:
      return "Updating publication";
  }
}

function publicationProviderLabel(providerType?: string) {
  switch (providerType) {
    case "cloud_run":
      return "Cloud Run";
    case "koyeb":
      return "Koyeb";
    case "azure_container_apps":
      return "Azure Container Apps";
    default:
      return "the hosting provider";
  }
}

function PendingTerminalState({ runStatus }: { runStatus: string }) {
  const terminalFailure = runStatus === "failed" || runStatus === "cancelled";
  const savedWithoutReview = runStatus === "completed";
  return (
    <div>
      {terminalFailure ? (
        <AlertCircle
          className="size-8 text-[var(--workspace-native-approval)]"
          aria-hidden
        />
      ) : savedWithoutReview ? (
        <PackageCheck
          className="size-8 text-[var(--workspace-native-success)]"
          aria-hidden
        />
      ) : (
        <LoaderCircle
          className="size-8 animate-spin text-[var(--workspace-native-info)]"
          aria-hidden
        />
      )}
      <h2 className="m-0 mt-4 text-base font-semibold text-[var(--workspace-native-text)]">
        {terminalFailure
          ? `Run ${humanizeStatus(runStatus)}`
          : savedWithoutReview
            ? "Artifact saved"
            : "Waiting for the run to finish"}
      </h2>
      <p className="m-0 mt-2 text-sm leading-6 text-[var(--workspace-native-text-muted)]">
        {terminalFailure
          ? "The saved Output remains available even though the Run did not complete."
          : savedWithoutReview
            ? "This Output is saved. Approval happens on the Task, when there is one."
            : "This Output is already saved. The Run is still in progress."}
      </p>
    </div>
  );
}

function RunResultLoading() {
  return (
    <div
      className="grid min-h-[55vh] place-items-center rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)]"
      role="status"
      aria-live="polite"
    >
      <div className="text-center">
        <LoaderCircle
          className="mx-auto size-7 animate-spin text-[var(--workspace-native-info)]"
          aria-hidden
        />
        <p className="m-0 mt-3 text-sm font-semibold text-[var(--workspace-native-text-muted)]">
          Loading the Artifact…
        </p>
      </div>
    </div>
  );
}

function RunResultError({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="grid min-h-[55vh] place-items-center rounded-lg border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-danger-soft)] p-6 text-center">
      <div>
        <AlertCircle
          className="mx-auto size-8 text-[var(--workspace-native-danger)]"
          aria-hidden
        />
        <h1 className="m-0 mt-4 text-2xl font-semibold text-[var(--workspace-native-text)]">
          The Artifact could not be loaded
        </h1>
        <p className="m-0 mt-2 text-sm text-[var(--workspace-native-text-muted)]">
          Check the connection and try loading the Output again.
        </p>
        <button
          className="mt-5 inline-flex h-9 items-center gap-2 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-4 text-sm font-bold text-[var(--workspace-native-text)]"
          type="button"
          onClick={onRetry}
        >
          <RefreshCcw className="size-4" aria-hidden /> Try again
        </button>
      </div>
    </div>
  );
}

function NoPresentedResult({
  runStatus,
  onRetry,
}: {
  runStatus?: string;
  onRetry: () => void;
}) {
  return (
    <div className="grid min-h-[55vh] place-items-center rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] p-6 text-center">
      <div className="max-w-md">
        <FileText
          className="mx-auto size-8 text-[var(--workspace-native-text-muted)]"
          aria-hidden
        />
        <h1 className="m-0 mt-4 text-2xl font-semibold text-[var(--workspace-native-text)]">
          No result was presented
        </h1>
        <p className="m-0 mt-2 text-sm leading-6 text-[var(--workspace-native-text-muted)]">
          {runStatus
            ? `This run is ${humanizeStatus(runStatus)} and has no frozen result set.`
            : "This run has no frozen result set yet."}
        </p>
        <button
          className="mt-5 inline-flex h-9 items-center gap-2 rounded-md border border-[var(--workspace-divider)] px-4 text-sm font-bold text-[var(--workspace-native-text)] hover:text-[var(--workspace-native-text)]"
          type="button"
          onClick={onRetry}
        >
          <RefreshCcw className="size-4" aria-hidden /> Refresh
        </button>
      </div>
    </div>
  );
}

function humanizeStatus(value: string) {
  return value.replace(/_/g, " ");
}

function formatDateTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "recently"
    : new Intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      }).format(date);
}
