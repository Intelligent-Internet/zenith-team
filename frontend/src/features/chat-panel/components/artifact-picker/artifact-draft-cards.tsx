import { PackageCheck, X } from "@/shared/ui/icons";
import {
  artifactSourceLabel,
  type LibraryArtifact,
} from "../../api/artifact-library-api";
import "./artifact-picker.css";
export function ArtifactDraftCards({
  artifacts,
  onRemove,
  disabled,
}: {
  artifacts: readonly LibraryArtifact[];
  onRemove: (id: string) => void;
  disabled?: boolean;
}) {
  if (!artifacts.length) return null;
  return (
    <div className="artifact-draft-cards" aria-label="Attached artifacts">
      {artifacts.map((artifact) => (
        <div className="artifact-draft-card" key={artifact.id}>
          <PackageCheck size={20} />
          <div>
            <strong title={artifact.title}>{artifact.title}</strong>
            <span title={artifactSourceLabel(artifact.source)}>
              {artifactSourceLabel(artifact.source)}
            </span>
          </div>
          <button
            type="button"
            disabled={disabled}
            aria-label={`Remove ${artifact.title}`}
            onClick={() => onRemove(artifact.id)}
          >
            <X size={14} />
          </button>
        </div>
      ))}
    </div>
  );
}
