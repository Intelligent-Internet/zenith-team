export type NodePreviewRequest = { blob: Blob; format: "image" | "text" };
export type NodePreviewData = {
  value: string;
  dimensions?: { width: number; height: number };
};
export type NodePreviewResponse = NodePreviewData | { error: string };
