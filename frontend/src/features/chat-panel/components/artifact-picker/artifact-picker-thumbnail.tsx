import { FileText, Globe, Image, PackageCheck } from "@/shared/ui/icons";
import { artifactFileSelection } from "../../model/artifact-file-access";
import type { LibraryArtifact } from "../../api/artifact-library-api";
import { ArtifactThumbnail } from "../artifacts/artifact-thumbnail";

export function ArtifactPickerThumbnail({
  companyId,
  artifact,
}: {
  companyId: string;
  artifact: LibraryArtifact;
}) {
  const items = artifact.items;
  const primary = items.find((item) => item.role === "primary") ?? items[0];
  const image =
    items.find(
      (item) =>
        item.contentType.startsWith("image/") &&
        /preview|thumbnail|poster|cover/i.test(
          `${item.label} ${item.displayName}`,
        ),
    ) ?? items.find((item) => item.contentType.startsWith("image/"));
  const preview = primary?.contentType.startsWith("image/")
    ? primary
    : (image ??
      (primary?.contentType.startsWith("video/") ? primary : undefined));
  const TypeIcon = {
    app: Globe,
    text: FileText,
    media: Image,
    file: PackageCheck,
  }[artifact.type];

  return (
    <span className="artifact-picker-thumbnail" aria-hidden>
      {preview ? (
        <ArtifactThumbnail
          companyId={companyId}
          file={artifactFileSelection(artifact.runId, artifact.id, preview)}
        />
      ) : (
        <TypeIcon size={25} />
      )}
    </span>
  );
}
