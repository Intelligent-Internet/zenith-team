import { useState } from "react";
import { DeliveryFreshness } from "@/features/run-deliverables/components/delivery-freshness";
import "../styles/task-detail-page.css";
import {
  ArrowUpRight,
  FileText,
  Globe2,
  LayoutDashboard,
} from "@/shared/ui/icons";
import { ArtifactThumbnail } from "@/features/chat-panel/components/artifacts/artifact-thumbnail";
import { artifactFileSelection } from "@/features/chat-panel/model/artifact-file-access";

import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import type { useConversationArtifacts } from "@/features/chat-panel/hooks/use-conversation-artifacts";

export function TaskDetailResults({
  companyId,
  sourceLabel,
  results,
  latestOnly,
  selectedIds = [],
  emptyLabel,
  onSelect,
}: {
  companyId: string;
  sourceLabel: string;
  results: ReturnType<typeof useConversationArtifacts>;
  latestOnly: boolean;
  selectedIds?: string[];
  emptyLabel: string;
  onSelect: (selection: ConversationArtifact, fileId?: string) => void;
}) {
  const [search, setSearch] = useState("");
  if (results.error)
    return (
      <p className="task-detail-empty" role="alert">
        {results.error}{" "}
        <button type="button" onClick={() => void results.refresh()}>
          Retry
        </button>
      </p>
    );
  if (results.isLoading)
    return (
      <p className="task-detail-empty" role="status">
        Loading saved results…
      </p>
    );
  const items =
    latestOnly && selectedIds.length
      ? selectedIds.flatMap((id) =>
          results.items.filter((item) => item.artifact.id === id),
        )
      : latestOnly
        ? results.items.slice(0, 1)
        : results.items;
  if (!items.length)
    return emptyLabel ? (
      <p className="task-detail-empty">{emptyLabel}</p>
    ) : null;
  const searchText = latestOnly ? "" : search.trim().toLocaleLowerCase();
  const matching = items.filter((result) =>
    [
      result.artifact.title,
      result.artifact.summary,
      ...result.artifact.items.flatMap((file) => [
        file.displayName,
        file.label,
      ]),
    ].some((value) => value.toLocaleLowerCase().includes(searchText)),
  );
  return (
    <div className="task-detail-results grid gap-[14px]">
      {!latestOnly ? (
        <input
          type="search"
          className="task-result-search"
          aria-label="Search loaded results and filenames"
          placeholder="Search loaded results and filenames…"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
      ) : null}
      {!matching.length ? (
        <p className="task-detail-empty" role="status">
          No matches in the loaded results.
          {results.hasMore
            ? " Load older results to continue searching."
            : " Try a different name."}
        </p>
      ) : null}
      {matching.map((result) => (
        <ResultGroup
          companyId={companyId}
          sourceLabel={sourceLabel}
          key={result.artifact.id}
          result={result}
          onSelect={onSelect}
        />
      ))}
    </div>
  );
}

function ResultGroup({
  companyId,
  sourceLabel,
  result,
  onSelect,
}: {
  companyId: string;
  sourceLabel: string;
  result: ConversationArtifact;
  onSelect: (selection: ConversationArtifact, fileId?: string) => void;
}) {
  return (
    <section className="task-detail-result" aria-label={result.artifact.title}>
      <header>
        <button
          type="button"
          className="task-result-title"
          onClick={() => onSelect(result)}
        >
          {result.artifact.title} <ArrowUpRight aria-hidden size={14} />
        </button>
        <span className="task-result-states">
          <small data-status={result.deliverable?.status ?? "saved"}>
            Saved
          </small>
          {result.publication ? (
            <small data-status={result.publication.status}>
              {publicationStatusLabel(result.publication.status)}
            </small>
          ) : null}
        </span>
      </header>
      <div className="task-result-origin">
        <span>
          {sourceLabel}
          {result.artifact.actorType === "user"
            ? " · Submitted by a teammate"
            : ""}
        </span>
        <span>
          {result.artifact.type === "whiteboard" ? (
            "Whiteboard"
          ) : (
            <>
              {result.artifact.items.length} saved{" "}
              {result.artifact.items.length === 1 ? "file" : "files"}
            </>
          )}
        </span>
        <time dateTime={result.artifact.createdAt}>
          {new Date(result.artifact.createdAt).toLocaleString(undefined, {
            month: "short",
            day: "numeric",
            year: "numeric",
            hour: "2-digit",
            minute: "2-digit",
          })}
        </time>
        {result.deliverable ? (
          <span>Review revision {result.deliverable.revision}</span>
        ) : null}
      </div>
      {result.artifact.summary ? <p>{result.artifact.summary}</p> : null}
      <DeliveryFreshness output={result.artifact} />
      <div className="grid gap-[10px]">
        {result.artifact.type === "app" ||
        result.artifact.type === "whiteboard" ? (
          <button
            type="button"
            className="task-detail-file"
            onClick={() => onSelect(result)}
          >
            <span className="task-detail-file-icon">
              {result.artifact.type === "app" ? (
                <Globe2 size={20} />
              ) : (
                <LayoutDashboard size={20} />
              )}
            </span>
            <span className="flex-1 min-w-0">
              <strong>
                {result.artifact.type === "app"
                  ? "Open app"
                  : "Open whiteboard"}
              </strong>
            </span>
            <ArrowUpRight size={16} />
          </button>
        ) : null}
        {result.artifact.items.map((file) => (
          <button
            type="button"
            className="task-detail-file"
            key={file.id}
            onClick={() => onSelect(result, file.id)}
          >
            <span
              className="task-detail-file-icon"
              data-preview={
                /^(image|video)\//.test(file.contentType) &&
                file.sizeBytes <= 10 * 1024 * 1024
              }
            >
              {/^(image|video)\//.test(file.contentType) &&
              file.sizeBytes <= 10 * 1024 * 1024 ? (
                <ArtifactThumbnail
                  companyId={companyId}
                  file={artifactFileSelection(
                    result.runId,
                    result.artifact.id,
                    file,
                  )}
                />
              ) : (
                <FileText aria-hidden size={20} />
              )}
            </span>
            <span className="flex-1 min-w-0">
              <strong>{file.displayName}</strong>
              <small>
                {file.role === "primary"
                  ? "Main file"
                  : file.role === "final"
                    ? "Final file"
                    : "Supporting file"}{" "}
                · {file.label}
              </small>
            </span>
            <small>{formatSize(file.sizeBytes)}</small>
            <ArrowUpRight size={16} />
          </button>
        ))}
      </div>
    </section>
  );
}

function publicationStatusLabel(
  status: NonNullable<ConversationArtifact["publication"]>["status"],
) {
  switch (status) {
    case "published":
      return "Published";
    case "unpublished":
      return "Unpublished";
    case "unpublishing":
      return "Unpublishing";
    case "failed":
      return "Publication failed";
    default:
      return "Publishing";
  }
}

function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
