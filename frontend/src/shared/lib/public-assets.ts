export function publicAssetPath(path: string): string {
  const baseUrl = import.meta.env.BASE_URL || "/";
  const normalizedBaseUrl = baseUrl.endsWith("/") ? baseUrl : `${baseUrl}/`;
  const normalizedPath = path.replace(/^\/+/, "");

  return `${normalizedBaseUrl}${normalizedPath}`;
}

export function publicAssetCssUrl(path: string): string {
  const assetPath = publicAssetPath(path);

  if (typeof window === "undefined") {
    return `url("${assetPath}")`;
  }

  return `url("${new URL(assetPath, window.location.href).href}")`;
}
