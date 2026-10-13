import { useGetWorkspaceTaskQuery } from "@/shared/api/task-api";
import { DeliveryFreshness } from "../components/delivery-freshness";
import { artifactSourceHref } from "../model/artifact-source-href";
import { OpenArtifactCanvasButton } from "@/features/run-deliverables/components/open-artifact-canvas-button";
import { Pencil } from "@/shared/ui/icons";
import { ArtifactReader } from "../components/artifact-reader";
import { ArtifactDownloadButton } from "../components/artifact-download-button";
import { ArtifactSharingControls } from "../components/artifact-sharing-controls";
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { outputDocumentLocation } from "@/features/project-canvas/documents/output-document-location";
import {
  Link,
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";
import { ArrowLeft, Users, LockKeyhole, Studio } from "@/shared/ui/icons";

import { ArtifactFilePreview } from "@/features/chat-panel/components/artifacts/artifact-file-preview";
import { artifactFileSelection } from "@/features/chat-panel/model/artifact-file-access";
import {
  useClaimUnownedArtifactMutation,
  useGetArtifactQuery,
  useGetRunDetailsQuery,
  useListUnownedArtifactsQuery,
} from "@/features/run-deliverables/api/run-deliverables-api";
import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import { Button } from "@/shared/ui/button";
import "@/features/chat-panel/styles/conversation-artifacts.css";
import "../styles/artifact-sharing.css";

import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { toast } from "@/shared/ui/sonner";
import { StudioModePanel } from "@/features/studio-mode/components/studio-mode-panel";

const OutputReviewWorkspace = lazy(
  () => import("@/features/project-canvas/review/output-review-workspace"),
);

const OutputDocumentWorkspace = lazy(
  () => import("@/features/project-canvas/documents/output-document-workspace"),
);

export function SharedArtifactPage() {
  const { companyId = "", artifactId = "" } = useParams();
  return <SharedArtifactPageContent key={`${companyId}/${artifactId}`} />;
}

function SharedArtifactPageContent() {
  const { companyId = "", artifactId = "" } = useParams();
  const navigate = useNavigate();
  const { hash, state } = useLocation();
  const canvasReturnTo =
    typeof state?.canvasReturnTo === "string" &&
    state.canvasReturnTo.startsWith("/") &&
    !state.canvasReturnTo.startsWith("//")
      ? state.canvasReturnTo
      : "/app";
  const shareButtonRef = useRef<HTMLButtonElement>(null);
  const studioButtonRef = useRef<HTMLButtonElement>(null);
  const [sharingOpen, setSharingOpen] = useState(hash === "#access");
  const [studioOpen, setStudioOpen] = useState(false);
  const [documentOpen, setDocumentOpen] = useState(true);
  const isRecoveryPage = artifactId === "unowned";
  const [searchParams, setSearchParams] = useSearchParams();
  const argument = { companyId, artifactId };
  const artifact = useGetArtifactQuery(argument, {
    skip: !companyId || !artifactId || isRecoveryPage,
    refetchOnMountOrArgChange: true,
    refetchOnFocus: true,
    refetchOnReconnect: true,
  });
  const detail = artifact.isError ? undefined : artifact.currentData;
  const openSource = searchParams.get("view") === "source";
  const sourceHref = detail
    ? artifactSourceHref(detail, artifactId)
    : undefined;
  useEffect(() => {
    if (openSource && sourceHref && !artifact.isFetching && !artifact.isError)
      navigate(sourceHref, { replace: true });
  }, [openSource, sourceHref, navigate, artifact.isFetching, artifact.isError]);

  const sourceRun = useGetRunDetailsQuery(
    { companyId, runId: detail?.runId ?? "" },
    { skip: !detail || detail.result.output?.type !== "media" },
  );
  const studioConversationId =
    sourceRun.currentData && sourceRun.currentData.runType !== "schedule"
      ? sourceRun.currentData.conversationId
      : null;
  const studioTask = useGetWorkspaceTaskQuery(
    { companyId, taskId: sourceRun.currentData?.taskId ?? "" },
    { skip: !studioOpen || !sourceRun.currentData?.taskId },
  );
  const studioAgentId =
    sourceRun.currentData?.runType === "task"
      ? studioTask.currentData?.executedBy.kind === "agent"
        ? studioTask.currentData.executedBy.id
        : ""
      : (sourceRun.currentData?.agentId ?? "");
  const studioAgents = useListCompanyAgentsQuery(companyId, {
    skip: !studioOpen || !companyId,
  });
  const studioAgent = studioAgents.currentData?.items.find(
    (agent) => agent.id === studioAgentId,
  );
  const studioLookupError =
    studioTask.error?.message ?? studioAgents.error?.message;
  useEffect(() => {
    if (studioOpen && studioLookupError)
      toast.error(studioLookupError, {
        id: `artifact-studio-scope:${artifactId}`,
        position: "bottom-right",
      });
  }, [artifactId, studioOpen, studioLookupError]);
  const hasDetail = Boolean(detail);
  useEffect(() => {
    if (hasDetail && hash === "#access") {
      setSharingOpen(true);
    }
  }, [hasDetail, hash]);
  const recovery = useListUnownedArtifactsQuery(companyId, {
    skip: !companyId || (!artifact.isError && !isRecoveryPage),
  });
  const orphan = recovery.currentData?.items.find(
    (item) => item.id === artifactId,
  );
  const [claimArtifact, claimState] = useClaimUnownedArtifactMutation();
  const access = detail?.access;
  const output = detail?.result.output;
  const [message, setMessage] = useState("");
  const claim = async (id: string, revision: number) => {
    try {
      await claimArtifact({
        companyId,
        artifactId: id,
        expectedRevision: revision,
      }).unwrap();
      navigate(`/companies/${companyId}/artifacts/${id}`);
    } catch {
      setMessage("The artifact could not be claimed. Refresh and try again.");
    }
  };

  const exitAction = (
    <Button variant="ghost" className="artifact-back-action" asChild>
      <Link to="/app" aria-label="Back to workspace">
        <ArrowLeft size={18} aria-hidden />
        <span className="artifact-back-label">Back to workspace</span>
      </Link>
    </Button>
  );

  if (isRecoveryPage) {
    return (
      <main className="artifact-share-state">
        <h1 className="text-2xl font-semibold">Artifacts needing an owner</h1>
        <p>
          Only minimal identifiers are shown until an authorized reviewer claims
          an artifact.
        </p>
        {recovery.isLoading ? <p role="status">Loading…</p> : null}
        {recovery.isError ? (
          <p role="alert">You cannot view the recovery list.</p>
        ) : null}
        {message ? <p role="alert">{message}</p> : null}
        {recovery.currentData?.items.map((item) => (
          <div
            key={item.id}
            className="flex flex-wrap items-center justify-between gap-3 border-b py-3"
          >
            <span>
              Run {item.runId} · Artifact {item.id}
            </span>
            <button
              type="button"
              disabled={claimState.isLoading}
              onClick={() => void claim(item.id, item.revision)}
            >
              Claim artifact
            </button>
          </div>
        ))}
      </main>
    );
  }

  if (
    artifact.isLoading ||
    (artifact.isFetching && !detail) ||
    (openSource && (sourceHref || artifact.isFetching))
  ) {
    return (
      <main className="artifact-share-page artifact-status-page">
        <nav className="artifact-status-back" aria-label="Artifact">
          {exitAction}
        </nav>
        <div className="artifact-share-state" role="status">
          Loading artifact…
        </div>
      </main>
    );
  }
  if (!detail || !output || !access) {
    return (
      <main className="artifact-share-page artifact-status-page">
        <nav className="artifact-status-back" aria-label="Artifact">
          {exitAction}
        </nav>
        <div className="artifact-share-state" role="alert">
          <LockKeyhole size={32} aria-hidden />
          <h1 className="text-xl font-semibold">This artifact is private</h1>
          <p>
            Sign in with an account that has access, or ask the person who
            shared this link to add you. The artifact may also have been
            removed.
          </p>
          {orphan ? (
            <Button
              variant="outline"
              disabled={claimState.isLoading}
              onClick={() => void claim(artifactId, orphan.revision)}
            >
              Claim unowned artifact
            </Button>
          ) : null}
          {message ? <p role="alert">{message}</p> : null}
        </div>
      </main>
    );
  }

  const selectedOrdinal = searchParams.get("item");
  const selected =
    output.items.find((item) => String(item.ordinal) === selectedOrdinal) ??
    output.items.find((item) => item.role === "primary") ??
    output.items[0];
  if (searchParams.get("view") === "canvas" && !output.canvasUrl)
    return (
      <main className="artifact-share-page artifact-share-page--document">
        <div className="artifact-document-share-body">
          <Suspense
            fallback={
              <div className="artifact-share-state" role="status">
                Opening canvas…
              </div>
            }
          >
            <OutputReviewWorkspace
              companyId={companyId}
              outputId={artifactId}
              initialVersionId={searchParams.get("versionId") ?? undefined}
              initialOutputItemId={
                searchParams.get("outputItemId") ?? selected?.id
              }
              onClose={() => navigate(canvasReturnTo)}
            />
          </Suspense>
        </div>
      </main>
    );
  const documentLocation = outputDocumentLocation(output);
  const reviewVersion = searchParams.get("versionId");
  const sharingControls = (
    <ArtifactSharingControls
      triggerRef={shareButtonRef}
      companyId={companyId}
      artifactId={artifactId}
      access={access}
      refreshing={artifact.isFetching}
      url={detail.url}
      title={output.title}
      open={sharingOpen}
      onOpenChange={(open) => {
        setSharingOpen(open);
        if (!open && hash === "#access")
          navigate(
            { hash: "", search: searchParams.toString() },
            { replace: true },
          );
      }}
    />
  );
  const download = async () => {
    if (!selected) return;
    setMessage("");
    try {
      const blob = await runDeliverableService.downloadArtifactFile(
        companyId,
        artifactId,
        selected.ordinal,
      );
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download =
        selected.displayName.replace(/[\\/\u0000-\u001f]/g, "_") || "artifact";
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1_000);
    } catch (error) {
      setMessage(
        "The file could not be downloaded. Your access may have changed.",
      );
      void artifact.refetch();
      throw error;
    }
  };

  if (
    documentOpen &&
    documentLocation &&
    searchParams.get("view") !== "submitted"
  )
    return (
      <main className="artifact-share-page artifact-share-page--document">
        <div className="artifact-document-share-tools">
          <span>Sharing follows the original artifact</span>
          <Button
            ref={shareButtonRef}
            variant="ghost"
            size="sm"
            onClick={() => setSharingOpen(true)}
          >
            <Users aria-hidden size={16} /> Share
          </Button>
        </div>
        <div className="artifact-document-share-body">
          <Suspense fallback={<p role="status">Opening document…</p>}>
            <OutputDocumentWorkspace
              companyId={companyId}
              location={{
                ...documentLocation,
                versionId: reviewVersion ?? documentLocation.versionId,
              }}
              reviewOnly={!!reviewVersion}
              initialOutputItemId={selected?.id}
              onClose={() => setDocumentOpen(false)}
              fallback={
                <div className="artifact-share-state" role="status">
                  <p>
                    The shared document is unavailable with your current access.
                  </p>
                  <Button onClick={() => setDocumentOpen(false)}>
                    View original files
                  </Button>
                </div>
              }
            />
          </Suspense>
        </div>
        {sharingControls}
      </main>
    );

  return (
    <main className="artifact-share-page artifact-share-page--studio-host">
      <ArtifactReader
        title={output.title}
        summary={output.summary}
        files={output.items.map((item) => ({
          id: item.id,
          label: item.displayName,
        }))}
        selectedId={selected?.id}
        onSelect={(id) => {
          const item = output.items.find((item) => item.id === id);
          if (item) {
            const params = new URLSearchParams(searchParams);
            params.set("item", String(item.ordinal));
            setSearchParams(params);
          }
        }}
        inert={studioOpen}
        artifactActions={
          <>
            <DeliveryFreshness output={output} />
            <OpenArtifactCanvasButton companyId={companyId} output={output} />
            {documentLocation && (
              <Button
                variant="ghost"
                onClick={() => {
                  setDocumentOpen(true);
                  const params = new URLSearchParams(searchParams);
                  params.delete("view");
                  setSearchParams(params);
                }}
              >
                <Pencil aria-hidden size={16} /> Open document
              </Button>
            )}
            {!selected ? (
              <Button
                ref={shareButtonRef}
                variant="outline"
                onClick={() => setSharingOpen(true)}
              >
                <Users aria-hidden size={16} /> Share
              </Button>
            ) : null}
          </>
        }
        notice={
          message ? (
            <p className="artifact-share-notice" role="status">
              {message}
            </p>
          ) : null
        }
        fileActions={
          <>
            {studioConversationId && output.type === "media" ? (
              <Button
                variant="ghost"
                ref={studioButtonRef}
                onClick={() => setStudioOpen(true)}
                aria-label="Open in Studio"
              >
                <Studio aria-hidden size={16} /> Studio
              </Button>
            ) : null}
            <Button
              ref={shareButtonRef}
              variant="ghost"
              onClick={() => setSharingOpen(true)}
            >
              <Users aria-hidden size={16} /> Share
            </Button>

            <ArtifactDownloadButton variant="ghost" onDownload={download} />
          </>
        }
      >
        {selected ? (
          <ArtifactFilePreview
            companyId={companyId}
            file={artifactFileSelection(detail.runId, artifactId, selected)}
          />
        ) : null}
      </ArtifactReader>
      {studioOpen && studioConversationId && output.type === "media" ? (
        <StudioModePanel
          fullPage
          companyId={companyId}
          conversationId={studioConversationId}
          // Resolve the current Agent binding so a Task handoff cannot select the previous profile.
          runtimeId={studioAgent?.binding?.runtime.id ?? ""}
          agentId={studioAgentId}
          onClose={() => {
            setStudioOpen(false);
            requestAnimationFrame(() => studioButtonRef.current?.focus());
          }}
        />
      ) : null}
      {sharingControls}
    </main>
  );
}
