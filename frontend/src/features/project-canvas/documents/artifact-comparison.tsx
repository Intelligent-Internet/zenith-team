import { useState } from "react";
import { skipToken } from "@reduxjs/toolkit/query";
import { ArrowUpRight } from "@/shared/ui/icons";
import { CanvasDialog } from "../canvas/canvas-dialog";
import { ArtifactViewer } from "../previews/artifact-viewer";
import { useCanvasHistoryQuery } from "../api/canvas-api";
import type { ArtifactCollectionProps } from "../canvas/artifact-collection";

export function ArtifactComparison({
  artifacts,
  scope,
  onOpen,
  onClose,
}: ArtifactCollectionProps & { onClose: () => void }) {
  return (
    <CanvasDialog
      title="Compare artifacts"
      description="Inspect two artifacts or two versions side by side."
      wide
      onClose={onClose}
    >
      <div className="pc-comparison">
        <ComparisonPane
          artifacts={artifacts}
          scope={scope}
          initialId={artifacts[0]?.id ?? ""}
          label="Left"
          onOpen={onOpen}
        />
        <ComparisonPane
          artifacts={artifacts}
          scope={scope}
          initialId={artifacts[1]?.id ?? artifacts[0]?.id ?? ""}
          label="Right"
          onOpen={onOpen}
        />
      </div>
    </CanvasDialog>
  );
}
function ComparisonPane({
  artifacts,
  scope,
  onOpen,
  initialId,
  label,
}: ArtifactCollectionProps & { initialId: string; label: string }) {
  const [artifactId, setArtifactId] = useState(initialId);
  const [versionId, setVersionId] = useState("");
  const artifact = artifacts.find((row) => row.id === artifactId);
  const history = useCanvasHistoryQuery(
    artifact ? { ...scope, artifactId: artifact.id } : skipToken,
  );
  const versions = history.isError ? [] : (history.currentData?.versions ?? []);
  const version = versions.find(
    (row) => row.id === (versionId || artifact?.latestVersionId),
  );
  return (
    <section className="pc-comparison-pane" aria-label={`${label} comparison`}>
      <header>
        <label>
          {label} artifact
          <select
            aria-label={`${label} artifact`}
            value={artifactId}
            onChange={(event) => {
              setArtifactId(event.target.value);
              setVersionId("");
            }}
          >
            {!artifact && (
              <option value={artifactId}>Choose an artifact</option>
            )}
            {artifacts.map((row) => (
              <option key={row.id} value={row.id}>
                {row.title}
              </option>
            ))}
          </select>
        </label>
        <label>
          Version
          <select
            aria-label={`${label} version`}
            value={versionId || artifact?.latestVersionId || ""}
            onChange={(event) => setVersionId(event.target.value)}
          >
            {versions.map((row) => (
              <option key={row.id} value={row.id}>
                v{row.number}
              </option>
            ))}
          </select>
        </label>
        {artifact && version && (
          <button
            className="pc-icon-button"
            title="Open review"
            aria-label={`Review ${label.toLowerCase()} artifact`}
            onClick={() => onOpen(artifact, version.id)}
          >
            <ArrowUpRight size={17} />
          </button>
        )}
      </header>
      <div className="pc-comparison-content flex flex-col relative flex-1 min-h-0 overflow-hidden [background:var(--workspace-content-bg)]">
        {version ? (
          <ArtifactViewer scope={scope} version={version} />
        ) : (
          <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
            {history.isError ? (
              <>
                <p>This artifact couldn’t be opened.</p>
                <button
                  className="pc-button"
                  onClick={() => void history.refetch()}
                >
                  Try again
                </button>
              </>
            ) : history.isFetching ? (
              "Loading version…"
            ) : (
              "Choose an accessible artifact and version."
            )}
          </div>
        )}
      </div>
    </section>
  );
}
