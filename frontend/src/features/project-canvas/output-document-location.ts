import type { RunOutput } from "@/features/run-deliverables/model/run-deliverable";

export function outputDocumentLocation(output: RunOutput) {
  if (
    output.type === "app" ||
    !output.items.some(
      (item) =>
        /\.(md|markdown)$/i.test(item.displayName) ||
        /^text\/(x-)?markdown$/i.test(item.contentType),
    )
  )
    return null;
  const projectId =
    output.canvasUrl &&
    /^\/projects\/([0-9a-f-]+)\/canvas\?/i.exec(output.canvasUrl)?.[1];
  const params = new URLSearchParams(output.canvasUrl?.split("?")[1]);
  const artifactId = params.get("artifactId"),
    versionId = params.get("versionId");
  return projectId && artifactId && versionId
    ? { projectId, artifactId, versionId }
    : null;
}
