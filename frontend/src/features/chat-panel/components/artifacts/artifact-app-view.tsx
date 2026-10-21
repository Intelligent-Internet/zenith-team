import type { DesktopAppPreviewStatus } from "../../../../../electron/shared/desktop-bridge.types";
import { useEffect, useRef, useState } from "react";
import {
  CircleX,
  ExternalLink,
  Globe,
  LoaderCircle,
  RotateCw,
} from "@/shared/ui/icons";

import { getZenithTeamDesktopBridge } from "@/shared/desktop/desktop-environment.service";
import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import { DesktopWebsitePreview } from "@/features/chat-panel/components/desktop-website-preview";
import { ArtifactFileActions } from "@/features/chat-panel/components/artifacts/artifact-file-actions";
import { ArtifactPreviewMessage } from "@/features/chat-panel/components/artifacts/artifact-file-preview";
import {
  artifactAppPreviewReason,
  artifactAppUrl,
  artifactFileSelection,
  artifactUsesLocalAppPreview,
} from "@/features/chat-panel/model/artifact-file-access";
import { toast } from "@/shared/ui/sonner";

export function ArtifactAppView({
  artifact,
  companyId,
  onEscape,
}: {
  artifact: ConversationArtifact;
  companyId: string;
  onEscape: () => void;
}) {
  const publication = artifact.publication;
  const usesLocalPreview = artifactUsesLocalAppPreview(artifact);
  const publishedUrl = artifactAppUrl(
    publication?.customUrl || publication?.url || "",
  );
  const desktop = getZenithTeamDesktopBridge();
  const [preview, setPreview] = useState<
    | { state: "loading" }
    | { state: "error"; reason: string }
    | { state: "local"; value: DesktopAppPreviewStatus }
    | { state: "ready"; url: string }
  >(
    usesLocalPreview
      ? { state: "loading" }
      : publishedUrl
        ? { state: "ready", url: publishedUrl }
        : { state: "error", reason: "The app preview is unavailable." },
  );
  const viewerIdRef = useRef("");
  const [openRevision, setOpenRevision] = useState(0);
  const [previewRevision, setPreviewRevision] = useState(0);
  const [restarting, setRestarting] = useState(false);
  const [stopping, setStopping] = useState(false);
  const primary = artifact.artifact.items.find(
    (item) => item.role === "primary",
  );
  const readyUrl =
    preview.state === "ready"
      ? preview.url
      : preview.state === "local" &&
          preview.value.lifecycle === "running" &&
          preview.value.availability === "available"
        ? preview.value.url
        : "";
  const [currentUrl, setCurrentUrl] = useState(readyUrl);

  useEffect(() => {
    let active = true;
    if (!usesLocalPreview) {
      setPreview(
        publishedUrl
          ? { state: "ready", url: publishedUrl }
          : { state: "error", reason: "The app preview is unavailable." },
      );
      return () => {
        active = false;
      };
    }
    // The app runs on the computer that built it, and only that computer's
    // owner is told which runtime it is (ADR 0014).
    const runtimeId = artifact.runtimeId;
    if (!desktop?.appPreview || !runtimeId || !artifact.runId) {
      setPreview({
        state: "error",
        reason:
          "This local app can only be opened in Zenith Team Desktop on its originating device.",
      });
      return () => {
        active = false;
      };
    }
    const bridge = desktop.appPreview;
    const viewerId = crypto.randomUUID();
    viewerIdRef.current = viewerId;
    setPreview({ state: "loading" });
    const unsubscribe = bridge.onEvent((event) => {
      if (!active) return;
      if (event.ok) {
        if (event.value.viewerId !== viewerIdRef.current) return;
        setPreview({ state: "local", value: event.value });
        return;
      }
      if (event.viewerId && event.viewerId !== viewerIdRef.current) return;
      setPreview({ state: "error", reason: event.reason });
    });
    void bridge
      .open({
        viewerId,
        companyId,
        outputId: artifact.artifact.id,
        runId: artifact.runId,
        runtimeId,
      })
      .then((result) => {
        if (!active) {
          if (result.ok) void bridge.close(result.value.viewerId);
          return;
        }
        if (result.ok) {
          viewerIdRef.current = result.value.viewerId;
          setPreview({ state: "local", value: result.value });
        } else {
          setPreview({ state: "error", reason: result.reason });
        }
      })
      .catch(() => {
        if (active) {
          setPreview({
            state: "error",
            reason: "The local app preview could not be started.",
          });
        }
      });
    return () => {
      active = false;
      unsubscribe();
      viewerIdRef.current = "";
      void bridge.close(viewerId);
    };
  }, [
    artifact.artifact.id,
    artifact.runId,
    artifact.runtimeId,
    companyId,
    desktop,
    usesLocalPreview,
    openRevision,
    publishedUrl,
  ]);

  useEffect(() => setCurrentUrl(readyUrl), [readyUrl]);

  const restartPreview = async () => {
    const viewerId = viewerIdRef.current;
    if (!desktop?.appPreview || !usesLocalPreview || !viewerId || restarting)
      return;
    setRestarting(true);
    try {
      const result = await desktop.appPreview.restart(viewerId);
      if (!result.ok) {
        toast.error(result.reason);
        return;
      }
      setPreview({ state: "local", value: result.value });
      setPreviewRevision((revision) => revision + 1);
    } catch {
      const reason = "The local app preview could not be restarted.";
      setPreview({ state: "error", reason });
      toast.error(reason);
    } finally {
      setRestarting(false);
    }
  };

  const stopPreview = async () => {
    const viewerId = viewerIdRef.current;
    if (!desktop?.appPreview || !viewerId || stopping) return;
    setStopping(true);
    try {
      const result = await desktop.appPreview.stop(viewerId);
      if (!result.ok) {
        toast.error(result.reason);
        return;
      }
      setPreview({ state: "local", value: result.value });
    } catch {
      toast.error("The local app preview could not be stopped.");
    } finally {
      setStopping(false);
    }
  };

  if (usesLocalPreview && !readyUrl) {
    const status = preview.state === "local" ? preview.value : undefined;
    const loading =
      preview.state === "loading" ||
      restarting ||
      stopping ||
      status?.lifecycle === "starting" ||
      status?.lifecycle === "stopping";
    return (
      <AppPreviewAvailability
        loading={loading}
        onRestart={
          desktop?.appPreview && status?.activationId && viewerIdRef.current
            ? restartPreview
            : undefined
        }
        onStart={
          desktop?.appPreview &&
          artifact.runtimeId &&
          (preview.state === "error" ||
            !viewerIdRef.current ||
            status?.lifecycle === "stopped")
            ? () => setOpenRevision((revision) => revision + 1)
            : undefined
        }
        onStop={
          desktop?.appPreview &&
          viewerIdRef.current &&
          (status?.lifecycle === "starting" || status?.lifecycle === "running")
            ? stopPreview
            : undefined
        }
        reason={
          preview.state === "error"
            ? preview.reason
            : status
              ? appPreviewStatusReason(status)
              : undefined
        }
        stopping={stopping}
        title={appPreviewStatusTitle(status, loading)}
      />
    );
  }

  if (
    !readyUrl ||
    (!usesLocalPreview && (!publication || publication.status !== "published"))
  ) {
    return (
      <div className="artifact-app-empty">
        <div className="grid w-[52px] h-[52px] place-items-center rounded-[15px] [background:var(--workspace-control-bg,_#efeee8)] text-[var(--workspace-native-info,_#3567a7)]">
          {publication && publication.status !== "failed" ? (
            <LoaderCircle className="artifact-spinner" size={24} />
          ) : (
            <Globe size={24} />
          )}
        </div>
        <h3>{publicationStatusTitle(publication?.status)}</h3>
        <p>{publicationStatusDetail(publication?.status)}</p>
        {primary ? (
          <ArtifactFileActions
            key={artifact.artifact.id}
            output={artifact.artifact}
            companyId={companyId}
            file={artifactFileSelection(
              artifact.runId,
              artifact.artifact.id,
              primary,
            )}
          />
        ) : null}
      </div>
    );
  }

  const bridge = desktop?.websitePreview;
  const actions = (
    <>
      {primary ? (
        <ArtifactFileActions
          key={artifact.artifact.id}
          output={artifact.artifact}
          companyId={companyId}
          file={artifactFileSelection(
            artifact.runId,
            artifact.artifact.id,
            primary,
          )}
        />
      ) : null}
      {usesLocalPreview && desktop?.appPreview ? (
        <button
          aria-label="Restart app preview"
          className="artifacts-icon-button"
          disabled={restarting}
          onClick={() => void restartPreview()}
          title="Restart app preview"
          type="button"
        >
          <RotateCw
            className={restarting ? "artifact-spinner" : undefined}
            size={17}
          />
        </button>
      ) : null}
      {usesLocalPreview && desktop?.appPreview ? (
        <button
          aria-label="Stop app preview"
          className="artifacts-icon-button"
          disabled={stopping}
          onClick={() => void stopPreview()}
          title="Stop app preview"
          type="button"
        >
          <CircleX size={17} />
        </button>
      ) : null}
      <ExternalAppLink url={currentUrl} />
    </>
  );
  return (
    <div className="artifact-app-view">
      {!bridge ? (
        <div className="artifact-app-toolbar">
          <Globe aria-hidden size={15} />
          <span title={currentUrl}>{currentUrl}</span>
          {actions}
        </div>
      ) : null}
      {bridge ? (
        <div className="artifact-app-native">
          <DesktopWebsitePreview
            key={previewRevision}
            bridge={bridge}
            actions={actions}
            onEscape={onEscape}
            onUrlChange={setCurrentUrl}
            title={artifact.artifact.title}
            url={readyUrl}
          />
        </div>
      ) : (
        <EmbeddedAppPreview
          key={previewRevision}
          artifact={artifact}
          url={readyUrl}
        />
      )}
    </div>
  );
}

function AppPreviewAvailability({
  loading,
  onRestart,
  onStart,
  onStop,
  reason,
  stopping,
  title,
}: {
  loading: boolean;
  onRestart?: () => Promise<void>;
  onStart?: () => void;
  onStop?: () => Promise<void>;
  reason?: string;
  stopping: boolean;
  title: string;
}) {
  return (
    <div className="artifact-app-empty">
      <div className="grid w-[52px] h-[52px] place-items-center rounded-[15px] [background:var(--workspace-control-bg,_#efeee8)] text-[var(--workspace-native-info,_#3567a7)]">
        {loading ? (
          <LoaderCircle className="artifact-spinner" size={24} />
        ) : (
          <Globe size={24} />
        )}
      </div>
      <h3>{title}</h3>
      <p>
        {loading
          ? reason || "Zenith Team is checking the development servers."
          : reason || "The app preview is unavailable."}
      </p>
      {(!loading && (onStart || onRestart)) || onStop ? (
        <div
          aria-label="App preview actions"
          className="artifact-file-actions inline-flex max-w-full flex-wrap shrink-0 items-center gap-[2px]"
        >
          {!loading && onStart ? (
            <button
              className="artifact-action-button"
              onClick={onStart}
              type="button"
            >
              <RotateCw size={15} />
              Start app
            </button>
          ) : !loading && onRestart ? (
            <button
              className="artifact-action-button"
              onClick={() => void onRestart()}
              type="button"
            >
              <RotateCw size={15} />
              Restart app
            </button>
          ) : null}
          {onStop ? (
            <button
              className="artifact-action-button"
              disabled={stopping}
              onClick={() => void onStop()}
              type="button"
            >
              <CircleX size={15} />
              {stopping ? "Stopping app" : "Stop app"}
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function appPreviewStatusTitle(
  status: DesktopAppPreviewStatus | undefined,
  loading: boolean,
) {
  if (status?.lifecycle === "stopping") return "Stopping app preview";
  if (status?.lifecycle === "failed") return "App preview failed";
  if (status?.lifecycle === "stopped") return "App preview stopped";
  if (status?.lifecycle === "running") return "App preview unavailable";
  return loading ? "Starting app preview" : "App preview unavailable";
}

function appPreviewStatusReason(status: DesktopAppPreviewStatus) {
  if (status.lifecycle === "starting") {
    return "Zenith Team is starting the app and waiting for it to respond.";
  }
  if (status.lifecycle === "stopping") {
    return status.reason === "app_preview_cleanup_blocked"
      ? "Zenith Team could not finish stopping the app and is retrying."
      : "Zenith Team is stopping the development commands.";
  }
  if (status.lifecycle === "stopped") {
    return "Start the app when you want to preview it again.";
  }
  if (status.reason === "app_preview_port_unavailable") {
    return "A required port is already in use. Stop the other preview or service, then try again.";
  }
  if (status.reason === "app_preview_platform_unsupported") {
    return "App preview supervision is not supported on this device.";
  }
  if (status.reason === "app_preview_command_exited") {
    return "A development command exited. Restart the app to try again.";
  }
  if (status.reason === "app_preview_readiness_failed") {
    return "The development servers did not become ready in time.";
  }
  if (status.lifecycle === "running") {
    return "The app is still running. Zenith Team will reconnect when its servers respond again.";
  }
  return "Restart the app to try again.";
}

function EmbeddedAppPreview({
  artifact,
  url,
}: {
  artifact: ConversationArtifact;
  url: string;
}) {
  const unavailableReason = artifactAppPreviewReason(url);
  const [reloadKey, setReloadKey] = useState(0);
  const [loadState, setLoadState] = useState<"loading" | "settled" | "slow">(
    "loading",
  );
  useEffect(() => {
    setLoadState("loading");
    const timeout = window.setTimeout(
      () => setLoadState((value) => (value === "loading" ? "slow" : value)),
      12_000,
    );
    return () => window.clearTimeout(timeout);
  }, [reloadKey, url]);
  if (unavailableReason) {
    return <ArtifactPreviewMessage>{unavailableReason}</ArtifactPreviewMessage>;
  }
  return (
    <div className="artifact-app-embed">
      <div
        className="flex min-h-[36px] items-center justify-between [border-bottom:1px_solid_var(--workspace-divider,_#dedbd2)] p-[5px_12px] text-[var(--workspace-native-text-muted,_#697168)] text-[length:12px]"
        role="status"
      >
        <span>
          {loadState === "loading"
            ? "Loading app preview…"
            : loadState === "slow"
              ? "This app is taking longer to load or may block preview."
              : "Interactive preview"}
        </span>
        <button
          aria-label="Reload app preview"
          className="artifacts-icon-button"
          onClick={() => setReloadKey((key) => key + 1)}
          type="button"
        >
          <RotateCw size={15} />
        </button>
      </div>
      <iframe
        allow="camera 'none'; microphone 'none'; geolocation 'none'; clipboard-read 'none'; clipboard-write 'none'; payment 'none'; usb 'none'"
        key={reloadKey}
        onError={() => setLoadState("slow")}
        onLoad={() => setLoadState("settled")}
        referrerPolicy="no-referrer"
        sandbox="allow-scripts allow-forms allow-downloads allow-popups"
        src={url}
        title={`App preview: ${artifact.artifact.title}`}
      />
    </div>
  );
}

function ExternalAppLink({ url }: { url: string }) {
  const safeUrl = artifactAppUrl(url);
  if (!safeUrl) return null;
  return (
    <a
      aria-label="Open app externally"
      className="artifacts-icon-button"
      href={safeUrl}
      onClick={(event) => {
        const bridge = getZenithTeamDesktopBridge();
        if (!bridge) return;
        event.preventDefault();
        void bridge
          .openExternal(safeUrl)
          .then((result) => {
            if (!result.ok) toast.error(result.reason);
          })
          .catch(() => toast.error("The app could not be opened."));
      }}
      rel="noopener noreferrer"
      target="_blank"
      title="Open app externally"
    >
      <ExternalLink aria-hidden size={17} />
    </a>
  );
}

function publicationStatusTitle(status?: string) {
  if (!status) return "App source saved";
  if (status === "failed") return "App publication failed";
  if (status === "unpublished") return "App is offline";
  if (status === "unpublishing") return "Taking app offline";
  return "Publishing app";
}

function publicationStatusDetail(status?: string) {
  if (!status) return "The source archive is ready to download.";
  if (status === "failed")
    return "The source is safe; publication can be retried from review.";
  if (status === "unpublished")
    return "The published app has been removed, but its source is still saved.";
  return "Zenith Team is preparing the public preview. This panel refreshes automatically.";
}
