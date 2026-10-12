import { canvasPath, type CanvasScope } from "./canvas-api";

export type WikiDocumentScope = { companyId: string; wikiPageId: string };
export type OutputDocumentScope = {
  companyId: string;
  outputId: string;
  outputItemId: string;
};
export type DocumentScope =
  CanvasScope | WikiDocumentScope | OutputDocumentScope;

export function documentScopeKey(scope: DocumentScope) {
  return `${scope.companyId}:${"projectId" in scope ? scope.projectId : "outputId" in scope ? `output:${scope.outputId}:${scope.outputItemId}` : `wiki:${scope.wikiPageId}`}`;
}

export function blockDocumentPath(scope: DocumentScope, versionId: string) {
  if ("outputId" in scope)
    return `/companies/${encodeURIComponent(scope.companyId)}/artifacts/${encodeURIComponent(scope.outputId)}/review/items/${encodeURIComponent(scope.outputItemId)}/document`;
  return "projectId" in scope
    ? `${canvasPath(scope)}/versions/${encodeURIComponent(versionId)}/document`
    : `/companies/${encodeURIComponent(scope.companyId)}/wiki/pages/${encodeURIComponent(scope.wikiPageId)}/document`;
}

// Keep aligned with the Wiki write boundary's ValidateStandaloneBlocks restriction.
const projectBlockTypes = new Set([
  "artifactReference",
  "taskReference",
  "taskCollection",
  "outputCollection",
  "workflowReference",
  "fileBlock",
  "audioBlock",
  "videoBlock",
]);

export function supportsDocumentBlock(scope: DocumentScope, type: string) {
  return "projectId" in scope || !projectBlockTypes.has(type);
}
