import { useEffect, useRef, useState } from "react";
import { skipToken } from "@reduxjs/toolkit/query";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import {
  ArrowLeft,
  Check,
  Eye,
  PackageCheck,
  Search,
  X,
} from "@/shared/ui/icons";
import {
  artifactSourceLabel,
  useGetArtifactLibraryInfiniteQuery,
  useLazyGetArtifactSelectionQuery,
  type LibraryArtifact,
  type LibraryQuery,
} from "../../api/artifact-library-api";
import { useGetArtifactQuery } from "@/features/run-deliverables/api/run-deliverables-api";
import { ConversationArtifactViewer } from "../artifacts/conversation-artifact-viewer";
import { ArtifactPickerThumbnail } from "./artifact-picker-thumbnail";
import "../../styles/conversation-artifacts.css";
import "./artifact-picker.css";

export function ArtifactPickerDialog({
  companyId,
  selected,
  onAttach,
  onClose,
  mode = "share",
}: {
  companyId: string;
  selected: readonly LibraryArtifact[];
  onAttach: (items: LibraryArtifact[]) => void;
  onClose: () => void;
  mode?: "share" | "reference";
}) {
  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [ownership, setOwnership] = useState<LibraryQuery["ownership"]>();
  const [source, setSource] = useState<LibraryQuery["source"]>();
  const [selection, setSelection] = useState<LibraryArtifact[]>([...selected]);
  const [verifySelection, verification] = useLazyGetArtifactSelectionQuery();
  const [selectionError, setSelectionError] = useState("");
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const [preview, setPreview] = useState<LibraryArtifact>();
  const searchInput = useRef<HTMLInputElement>(null);
  const previewTrigger = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    const timer = setTimeout(() => setDebouncedSearch(search.trim()), 250);
    return () => clearTimeout(timer);
  }, [search]);
  const query = useGetArtifactLibraryInfiniteQuery(
    { companyId, q: debouncedSearch || undefined, ownership, source },
    { refetchOnMountOrArgChange: true },
  );
  const items = query.isError
    ? []
    : (query.currentData?.pages.flatMap((page) => page.items) ?? []);
  const detail = useGetArtifactQuery(
    preview ? { companyId, artifactId: preview.id } : skipToken,
    { refetchOnMountOrArgChange: true },
  );
  const result = detail.isError ? undefined : detail.currentData?.result;
  const goBack = () => {
    setPreview(undefined);
    requestAnimationFrame(() => previewTrigger.current?.focus());
  };
  const toggle = (item: LibraryArtifact) =>
    setSelection((current) =>
      current.some((a) => a.id === item.id)
        ? current.filter((a) => a.id !== item.id)
        : current.length < 10 && item.canShare
          ? [...current, item]
          : current,
    );
  // Reconcile visible selections after an access refresh without dropping selections on other pages.
  useEffect(() => {
    if (!query.currentData || query.isError) return;
    const current = new Map(
      query.currentData.pages
        .flatMap((page) => page.items)
        .map((item) => [item.id, item]),
    );
    setSelection((previous) =>
      previous.flatMap((item) => {
        const fresh = current.get(item.id);
        return fresh ? (fresh.canShare ? [fresh] : []) : [item];
      }),
    );
  }, [query.currentData, query.isError]);
  const attach = async () => {
    if (verification.isFetching || !selection.length) return;
    setSelectionError("");
    try {
      const page = await verifySelection(
        { companyId, ids: selection.map((item) => item.id) },
        false,
      ).unwrap();
      if (!mounted.current) return;
      const available = new Map(
        page.items
          .filter((item) => item.canShare)
          .map((item) => [item.id, item]),
      );
      const refreshed = selection.flatMap((item) =>
        available.has(item.id) ? [available.get(item.id)!] : [],
      );
      setSelection(refreshed);
      if (refreshed.length !== selection.length) {
        setSelectionError(
          "Some artifacts are no longer available to attach. Review your selection.",
        );
        return;
      }
      onAttach(refreshed);
    } catch {
      if (mounted.current)
        setSelectionError("Couldn’t check artifact access. Please try again.");
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent
        className="artifact-picker"
        showCloseButton={false}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          searchInput.current?.focus();
        }}
        onEscapeKeyDown={(event) => {
          if (preview) {
            event.preventDefault();
            goBack();
          }
        }}
      >
        <header className="artifact-picker-header">
          <div className="artifact-picker-heading flex items-center gap-[12px]">
            <span className="grid place-items-center w-[44px] h-[44px] rounded-[12px] [background:var(--picker-hover)] text-[var(--picker-accent)]">
              <PackageCheck size={23} />
            </span>
            <div>
              <DialogTitle>Select artifacts</DialogTitle>
              <DialogDescription>
                Bring saved work into this conversation.
              </DialogDescription>
            </div>
          </div>
          <button
            type="button"
            className="artifact-picker-icon-button"
            aria-label="Close artifact picker"
            onClick={onClose}
          >
            <X size={19} />
          </button>
        </header>
        <div className="artifact-picker-browse" hidden={Boolean(preview)}>
          <div className="artifact-picker-controls">
            <label className="artifact-picker-search">
              <Search size={18} />
              <input
                ref={searchInput}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                maxLength={200}
                placeholder="Search artifacts…"
                aria-label="Search artifacts"
              />
              {search && (
                <button
                  type="button"
                  aria-label="Clear search"
                  onClick={() => setSearch("")}
                >
                  <X size={14} />
                </button>
              )}
            </label>
            <div className="artifact-picker-filters">
              <div
                className="artifact-picker-tabs flex gap-[4px]"
                aria-label="Artifact ownership"
              >
                {(
                  [
                    { label: "All", value: undefined },
                    { label: "Mine", value: "owned" },
                    { label: "Shared with me", value: "shared" },
                  ] as const
                ).map((tab) => (
                  <button
                    type="button"
                    key={tab.label}
                    aria-pressed={ownership === tab.value}
                    onClick={() => setOwnership(tab.value)}
                  >
                    {tab.label}
                  </button>
                ))}
              </div>
              <select
                aria-label="Artifact source"
                value={source ?? ""}
                onChange={(e) =>
                  setSource(
                    (e.target.value || undefined) as LibraryQuery["source"],
                  )
                }
              >
                <option value="">All sources</option>
                <option value="channel">Channels</option>
                <option value="direct">Direct chats</option>
                <option value="task">Tasks</option>
                <option value="schedule">Schedules</option>
              </select>
            </div>
          </div>
          <div
            className="artifact-picker-list"
            aria-label="Artifacts"
            aria-busy={query.isFetching}
          >
            {query.isError ? (
              <div className="artifact-picker-empty" role="alert">
                <PackageCheck size={32} />
                <strong>Artifacts couldn’t be loaded</strong>
                <p>Your selections are kept. Try again.</p>
                <button type="button" onClick={() => void query.refetch()}>
                  Retry
                </button>
              </div>
            ) : !query.currentData ? (
              <div className="artifact-picker-empty" role="status">
                Loading artifacts…
              </div>
            ) : !items.length ? (
              <div className="artifact-picker-empty">
                <PackageCheck size={32} />
                <strong>
                  {search || ownership || source
                    ? "No matching artifacts"
                    : "No artifacts yet"}
                </strong>
                <p>
                  {search || ownership || source
                    ? "Try another search or source."
                    : "Saved results from your chats and tasks will appear here."}
                </p>
              </div>
            ) : (
              items.map((item) => {
                const checked = selection.some((a) => a.id === item.id);
                return (
                  <div
                    key={item.id}
                    className="artifact-picker-row"
                    data-selected={checked}
                  >
                    <label
                      className="artifact-picker-choice"
                      data-disabled={!item.canShare}
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        disabled={
                          verification.isFetching ||
                          !item.canShare ||
                          (!checked && selection.length >= 10)
                        }
                        onChange={() => toggle(item)}
                        aria-label={`Select ${item.title}`}
                      />
                      <span className="artifact-picker-check" aria-hidden>
                        {checked && <Check size={12} />}
                      </span>
                      <ArtifactPickerThumbnail
                        companyId={companyId}
                        artifact={item}
                      />
                      <span className="artifact-picker-copy flex flex-col gap-[3px] min-w-0">
                        <strong title={item.title}>{item.title}</strong>
                        <span
                          className="text-[length:12px] text-[var(--picker-muted)]"
                          title={artifactSourceLabel(item.source)}
                        >
                          {artifactSourceLabel(item.source)}
                        </span>
                        <span className="text-[length:11px] text-[var(--picker-muted)] opacity-[0.8]">
                          {item.ownerName} ·{" "}
                          {new Intl.DateTimeFormat(undefined, {
                            month: "short",
                            day: "numeric",
                          }).format(new Date(item.createdAt))}
                          {!item.canShare ? " · View only" : ""}
                        </span>
                      </span>
                    </label>
                    <button
                      type="button"
                      className="artifact-picker-icon-button"
                      title={`Preview ${item.title}`}
                      aria-label={`Preview ${item.title}`}
                      onClick={(event) => {
                        previewTrigger.current = event.currentTarget;
                        setPreview(item);
                      }}
                    >
                      <Eye size={18} />
                    </button>
                  </div>
                );
              })
            )}
            {query.hasNextPage && !query.isError && (
              <button
                className="artifact-picker-load"
                type="button"
                disabled={query.isFetching}
                onClick={() => void query.fetchNextPage()}
              >
                {query.isFetchingNextPage ? "Loading…" : "Load more"}
              </button>
            )}
          </div>
        </div>
        {preview && (
          <div className="artifact-picker-preview text-[length:var(--workspace-text-body,_14px)] flex-1 min-h-0 flex flex-col overflow-hidden [border-top:1px_solid_var(--picker-border)]">
            {result?.output ? (
              <ConversationArtifactViewer
                companyId={companyId}
                artifact={{
                  runId: preview.runId,
                  runtimeId: result.runtimeId,
                  runStatus: result.runStatus,
                  artifact: result.output,
                  deliverable: result.deliverable,
                  publication: result.publication,
                }}
                onBack={goBack}
                onClose={goBack}
              />
            ) : (
              <div className="artifact-picker-empty">
                <button type="button" onClick={goBack}>
                  <ArrowLeft size={16} />
                  Back to artifacts
                </button>
                <p role="status">
                  {detail.isError
                    ? "This artifact is no longer available."
                    : "Loading preview…"}
                </p>
              </div>
            )}
          </div>
        )}
        {selectionError && (
          <p
            className="shrink-0 m-0 p-[10px_26px] text-[var(--color-danger,_#b94a48)] text-[length:12px] [border-top:1px_solid_var(--picker-border)]"
            role="alert"
          >
            {selectionError}
          </p>
        )}
        <footer className="artifact-picker-footer">
          <div>
            <strong>
              {selection.length
                ? `${selection.length} of 10 selected`
                : "Choose up to 10 artifacts"}
            </strong>
            <span>
              {mode === "share"
                ? "Shared with this conversation when you send."
                : "Added as context when you send to the Agent."}
            </span>
          </div>
          <div className="artifact-picker-actions">
            <button type="button" onClick={onClose}>
              Cancel
            </button>
            <button
              type="button"
              className="artifact-picker-primary"
              disabled={
                !selection.length ||
                query.isError ||
                query.isFetching ||
                verification.isFetching
              }
              onClick={() => void attach()}
            >
              {verification.isFetching ? "Checking…" : "Attach"}
              {selection.length ? ` (${selection.length})` : ""}
            </button>
          </div>
        </footer>
      </DialogContent>
    </Dialog>
  );
}
