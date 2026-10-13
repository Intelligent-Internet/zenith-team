export type CanvasScope = { companyId: string; projectId: string };
export const canvasPath = ({ companyId, projectId }: CanvasScope) =>
  `/companies/${encodeURIComponent(companyId)}/projects/${encodeURIComponent(projectId)}/canvas`;
