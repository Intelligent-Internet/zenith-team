function isSafeExternalUrl(url) {
  try {
    const parsedUrl = new URL(url);
    return parsedUrl.protocol === "https:" || parsedUrl.protocol === "http:";
  } catch {
    return false;
  }
}

function isSafePluginContinueUrl(value) {
  if (
    typeof value !== "string" ||
    value !== value.trim() ||
    value.length > 4096
  ) {
    return false;
  }
  try {
    const url = new URL(value);
    if (url.protocol !== "https:" || url.username || url.password || url.port) {
      return false;
    }
    if (!url.hash) return true;
    if (
      url.hostname !== "chatgpt.com" ||
      url.search ||
      (url.pathname !== "/apps" && !url.pathname.startsWith("/apps/")) ||
      !url.hash.startsWith("#settings/Connectors?")
    ) {
      return false;
    }
    const params = new URLSearchParams(
      url.hash.slice("#settings/Connectors?".length),
    );
    const keys = [...params.keys()];
    const connectorId = params.get("connector") ?? "";
    return (
      keys.length === 4 &&
      new Set(keys).size === 4 &&
      params.get("add-connector-link") === "true" &&
      params.get("product-sku") === "CODEX" &&
      params.get("referrer") === "codex" &&
      /^[A-Za-z0-9._:@+-]{1,240}$/.test(connectorId)
    );
  } catch {
    return false;
  }
}

function isAppNavigationUrl(targetUrl, rendererUrl) {
  try {
    const parsedTarget = new URL(targetUrl);
    const parsedRenderer = new URL(rendererUrl);

    if (parsedRenderer.protocol === "file:") {
      return (
        parsedTarget.protocol === "file:" &&
        parsedTarget.pathname === parsedRenderer.pathname
      );
    }

    if (parsedRenderer.protocol === "boardly-app:") {
      return (
        parsedTarget.protocol === parsedRenderer.protocol &&
        parsedTarget.hostname === parsedRenderer.hostname
      );
    }

    return parsedTarget.origin === parsedRenderer.origin;
  } catch {
    return false;
  }
}

module.exports = {
  isAppNavigationUrl,
  isSafeExternalUrl,
  isSafePluginContinueUrl,
};
