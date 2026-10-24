import { type CanvasScope } from "../api/canvas-scope";

export type WikiDocumentScope = { companyId: string; wikiPageId: string };
export type OutputDocumentScope = {
  companyId: string;
  outputId: string;
  outputItemId: string;
};
export type PageDocumentScope = { companyId: string; pageId: string };
export type DocumentScope =
  | CanvasScope
  | WikiDocumentScope
  | PageDocumentScope
  | { companyId: string; conversationId: string };

export function documentScopeKey(scope: DocumentScope) {
  if ("pageId" in scope) return `${scope.companyId}:page:${scope.pageId}`;
  if ("conversationId" in scope)
    return `${scope.companyId}:chat:${scope.conversationId}`;
  return `${scope.companyId}:${"projectId" in scope ? scope.projectId : `wiki:${scope.wikiPageId}`}`;
}

export function blockDocumentPath(scope: DocumentScope) {
  if ("pageId" in scope)
    return `/companies/${encodeURIComponent(scope.companyId)}/pages/${encodeURIComponent(scope.pageId)}/document`;
  if ("wikiPageId" in scope)
    return `/companies/${encodeURIComponent(scope.companyId)}/wiki/pages/${encodeURIComponent(scope.wikiPageId)}/document`;
  throw new Error("An editable document requires its Page identity.");
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
