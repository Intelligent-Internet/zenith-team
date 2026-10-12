import { useCallback, useEffect, useRef, useState } from "react";
import { useParams, useSearchParams } from "react-router";
import { ArtifactFileContent } from "@/features/chat-panel/components/artifacts/artifact-file-preview";
import type { components } from "@/shared/api/generated/api.generated";
import { shouldUseDesktopHashRouter } from "@/app/desktop/desktop-environment.service";
import { desktopAppUrl } from "@/shared/lib/desktop-app-url";
import { Button } from "@/shared/ui/button";
import { ChevronDown, FileText, Monitor } from "@/shared/ui/icons";
import { Logo } from "@/shared/ui/logo";
import { ArtifactReader } from "../components/artifact-reader";
import { ArtifactDownloadButton } from "../components/artifact-download-button";
import {
  downloadPublicArtifactFile,
  useGetPublicArtifactQuery,
} from "../api/artifact-sharing-api";
import "@/features/chat-panel/styles/conversation-artifacts.css";
import "../styles/public-artifact-page.css";

export function PublicArtifactPage() {
  const { token = "" } = useParams();
  return <PublicArtifactContent key={token} token={token} />;
}

function PublicArtifactContent({ token }: { token: string }) {
  const [searchParams, setSearchParams] = useSearchParams();
  const artifact = useGetPublicArtifactQuery(token, {
    skip: !token,
    pollingInterval: 10000,
    refetchOnMountOrArgChange: true,
    refetchOnFocus: true,
    refetchOnReconnect: true,
  });
  const [message, setMessage] = useState("");
  const detail = artifact.isError ? undefined : artifact.currentData;
  const selected =
    detail?.items.find(
      (file) => String(file.ordinal) === searchParams.get("item"),
    ) ??
    detail?.items.find((file) => file.role === "primary") ??
    detail?.items[0];
  const isDesktop = shouldUseDesktopHashRouter();
  const desktopUrl = desktopAppUrl(
    `/share/artifacts/${encodeURIComponent(token)}${selected ? `?item=${selected.ordinal}` : ""}`,
  );
  const attemptedDesktopOpen = useRef(false);

  useEffect(() => {
    if (!detail || isDesktop || attemptedDesktopOpen.current) return;
    attemptedDesktopOpen.current = true;
    try {
      window.location.assign(desktopUrl);
    } catch {
      // The browser may require a click on Open in app to launch the protocol.
    }
  }, [desktopUrl, detail, isDesktop]);

  if (!detail) {
    if (isDesktop) {
      return (
        <main className="artifact-share-page">
          <div className="artifact-share-state" role="status">
            {artifact.isLoading
              ? "Loading artifact…"
              : "This link is unavailable or has been revoked."}
          </div>
        </main>
      );
    }
    return (
      <main className="public-artifact-state">
        <Logo className="public-artifact-logo" />
        <p>Shared artifact</p>
        <p role="status">
          {artifact.isLoading
            ? "Loading artifact…"
            : "This link is unavailable or has been revoked."}
        </p>
      </main>
    );
  }

  const download = async () => {
    if (!selected) return;
    setMessage("");
    try {
      const blob = await downloadPublicArtifactFile(token, selected.ordinal);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = selected.filename.replace(/[\\/\u0000-\u001f]/g, "_");
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) {
      setMessage("The file is unavailable or this link has been revoked.");
      void artifact.refetch();
      throw error;
    }
  };

  if (isDesktop) {
    return (
      <main className="artifact-share-page">
        <ArtifactReader
          title={detail.title}
          summary={detail.summary}
          files={detail.items.map((file) => ({
            id: String(file.ordinal),
            label: file.label || file.filename,
          }))}
          selectedId={selected ? String(selected.ordinal) : undefined}
          onSelect={(item) => setSearchParams({ item })}
          notice={
            message ? (
              <p className="artifact-share-notice" role="alert">
                {message}
              </p>
            ) : null
          }
          fileActions={
            selected ? (
              <ArtifactDownloadButton
                key={selected.ordinal}
                variant="ghost"
                onDownload={download}
              />
            ) : null
          }
        >
          {selected ? (
            <PublicArtifactFilePreview
              key={selected.ordinal}
              token={token}
              file={selected}
            />
          ) : null}
        </ArtifactReader>
      </main>
    );
  }

  return (
    <main className="public-artifact-page">
      <header className="public-artifact-toolbar">
        <Logo className="public-artifact-logo" />
        <div className="public-artifact-file">
          <FileText size={16} aria-hidden />
          {detail.items.length > 1 && selected ? (
            <div className="public-artifact-file-picker">
              <select
                aria-label="Artifact file"
                value={selected.ordinal}
                onChange={(event) =>
                  setSearchParams({ item: event.target.value })
                }
              >
                {detail.items.map((file) => (
                  <option key={file.ordinal} value={file.ordinal}>
                    {file.label || file.filename}
                  </option>
                ))}
              </select>
              <ChevronDown size={14} aria-hidden />
            </div>
          ) : (
            <span title={detail.title}>{detail.title}</span>
          )}
        </div>
        <div className="public-artifact-actions">
          <Button
            variant="outline"
            className="public-artifact-open-app"
            asChild
          >
            <a href={desktopUrl} aria-label="Open in Zenith Team">
              <Monitor size={16} aria-hidden />
              Open in app
            </a>
          </Button>
          {selected ? (
            <ArtifactDownloadButton
              key={selected.ordinal}
              variant="ghost"
              onDownload={download}
            />
          ) : null}
        </div>
      </header>
      {message ? (
        <p className="public-artifact-notice" role="alert">
          {message}
        </p>
      ) : null}
      {selected ? (
        <section
          key={selected.ordinal}
          className="public-artifact-preview"
          aria-label="Artifact preview"
        >
          <PublicArtifactFilePreview token={token} file={selected} />
        </section>
      ) : (
        <p className="public-artifact-state" role="status">
          This artifact has no files to display.
        </p>
      )}
    </main>
  );
}

function PublicArtifactFilePreview({
  token,
  file,
}: {
  token: string;
  file: components["schemas"]["PublicArtifactItem"];
}) {
  const { ordinal } = file;
  const loadFile = useCallback(
    (signal: AbortSignal) => downloadPublicArtifactFile(token, ordinal, signal),
    [token, ordinal],
  );
  return (
    <ArtifactFileContent
      file={{ ...file, id: String(ordinal), title: file.label }}
      loadFile={loadFile}
    />
  );
}
