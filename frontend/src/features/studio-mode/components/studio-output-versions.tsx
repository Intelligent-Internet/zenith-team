import { useState } from "react";
import { Check, ChevronLeft, ChevronRight, Loader2 } from "@/shared/ui/icons";
import type { StudioGraphNode } from "@/shared/studio-graph/model";

export function StudioOutputVersions({
  node,
  disabled,
  restoring,
  onRestore,
  onError,
}: {
  node: StudioGraphNode;
  disabled: boolean;
  restoring: boolean;
  onRestore: (nodeId: string, versionId: string) => Promise<void>;
  onError: (error: unknown) => void;
}) {
  const [selection, setSelection] = useState<{
    activeId: string;
    id: string;
  } | null>(null);
  let metadata: Record<string, unknown> = {};
  try {
    metadata = JSON.parse(node.raw.metadataJson) ?? {};
  } catch {
    /* No stored history. */
  }
  const versions = (
    Array.isArray(metadata.output_versions) ? metadata.output_versions : []
  )
    .flatMap((value: unknown) => {
      if (!value || typeof value !== "object") return [];
      const record = value as Record<string, unknown>;
      return typeof record.id === "string" &&
        record.id.trim() &&
        typeof record.version === "number" &&
        Number.isFinite(record.version) &&
        record.version > 0
        ? [{ id: record.id, version: Math.floor(record.version) }]
        : [];
    })
    .sort((a, b) => a.version - b.version);
  const activeId =
    typeof metadata.active_output_version_id === "string"
      ? metadata.active_output_version_id
      : "";
  const selectedId = selection?.activeId === activeId ? selection.id : activeId;
  const index = versions.findIndex((version) => version.id === selectedId);
  const selectedIndex = index < 0 ? versions.length - 1 : index;
  const selected = versions[selectedIndex];
  const hasOutput = Boolean(node.mediaUrl || node.outputText);
  if (!selected || (versions.length < 2 && hasOutput)) return null;
  const active = selected.id === activeId && hasOutput;
  const select = (index: number) =>
    setSelection({ activeId, id: versions[index].id });
  return (
    <section
      className="studio-settings__section studio-settings__versions"
      aria-label="Output versions"
    >
      <h3 className="studio-settings__section-title">Versions</h3>
      <div className="studio-settings__version-controls">
        <button
          type="button"
          aria-label="Previous version"
          title="Previous version"
          disabled={disabled || selectedIndex <= 0}
          onClick={() => select(selectedIndex - 1)}
        >
          <ChevronLeft size={16} />
        </button>
        <button
          type="button"
          className="studio-settings__version"
          data-active={active}
          disabled={disabled || active}
          aria-label={
            active
              ? `Version ${selected.version}, current`
              : `Apply version ${selected.version}`
          }
          title={
            active
              ? `v${selected.version} · Current`
              : `Apply v${selected.version}`
          }
          aria-busy={restoring}
          onClick={() => void onRestore(node.id, selected.id).catch(onError)}
        >
          {restoring ? (
            <Loader2 size={15} className="studio-settings__saving-icon" />
          ) : (
            <>
              <span className="studio-settings__version-number">
                v{selected.version}
              </span>
              {!active && (
                <span className="studio-settings__version-apply">
                  <Check size={14} /> Apply
                </span>
              )}
            </>
          )}
        </button>
        <button
          type="button"
          aria-label="Next version"
          title="Next version"
          disabled={disabled || selectedIndex >= versions.length - 1}
          onClick={() => select(selectedIndex + 1)}
        >
          <ChevronRight size={16} />
        </button>
      </div>
    </section>
  );
}
