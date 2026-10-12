import type { DesktopResourceFilePayload } from "@/app/desktop/desktop-environment.service";

export type DesktopResourceFileDescriptor = {
  blob: Blob;
  filename?: string | null;
  mimeType?: string | null;
  resourceId: string;
  title: string;
};

export async function createDesktopResourceFilePayload({
  blob,
  filename,
  mimeType,
  resourceId,
  title,
}: DesktopResourceFileDescriptor): Promise<DesktopResourceFilePayload> {
  return {
    bytes: await blob.arrayBuffer(),
    filename: normalizeResourceFilename(
      filename ?? title,
      mimeType ?? blob.type,
    ),
    mimeType: mimeType ?? blob.type,
    resourceId,
    title,
  };
}

export function normalizeResourceFilename(
  value: string,
  mimeType?: string | null,
) {
  const name = value.trim() || "boardly-resource";

  if (/\.[A-Za-z0-9]{1,12}$/.test(name)) {
    return name;
  }

  const extension = extensionFromMimeType(mimeType);
  return extension ? `${name}.${extension}` : name;
}

function extensionFromMimeType(mimeType?: string | null) {
  switch (mimeType) {
    case "application/pdf":
      return "pdf";
    case "application/json":
      return "json";
    case "image/jpeg":
      return "jpg";
    case "image/png":
      return "png";
    case "image/webp":
      return "webp";
    case "text/csv":
      return "csv";
    case "text/markdown":
      return "md";
    case "text/plain":
      return "txt";
    default:
      return null;
  }
}
