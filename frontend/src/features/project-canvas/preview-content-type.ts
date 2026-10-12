// Some uploaded files arrive as octet-stream. Use known extensions only when
// the source has no more specific MIME type; the saved bytes remain unchanged.
export function previewContentType(
  type?: string | null,
  filename?: string | null,
) {
  if (
    /\.(md|markdown)$/i.test(filename ?? "") &&
    (!type || type === "text/plain" || type === "application/octet-stream")
  )
    return "text/markdown";
  if (type && type !== "application/octet-stream") return type;
  const extension = filename?.split(".").pop()?.toLowerCase() ?? "";
  const types: Record<string, string> = {
    png: "image/png",
    jpg: "image/jpeg",
    jpeg: "image/jpeg",
    gif: "image/gif",
    webp: "image/webp",
    avif: "image/avif",
    svg: "image/svg+xml",
    pdf: "application/pdf",
    md: "text/markdown",
    markdown: "text/markdown",
    txt: "text/plain",
    html: "text/html",
    htm: "text/html",
    docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    mp3: "audio/mpeg",
    m4a: "audio/mp4",
    wav: "audio/wav",
    ogg: "audio/ogg",
    mp4: "video/mp4",
    webm: "video/webm",
  };
  return types[extension] ?? type ?? "";
}
