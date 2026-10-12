export type PreviewRequest = {
  requestId: number;
  format: "html" | "docx" | "text";
  buffer: ArrayBuffer;
};
export type PreviewResponse =
  | { requestId: number; content: string; error?: never }
  | { requestId: number; error: string; content?: never };
