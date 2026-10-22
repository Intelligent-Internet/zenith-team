const ZENITH_TEAM_PROTOCOL = "zenith-team";

const allowedRoutePrefixes = [
  "/agents",
  "/analytics",
  "/app",
  "/approvals",
  "/assets",
  "/auth/google/callback",
  "/login",
  "/onboarding",
  "/resources",
  "/settings",
  "/team",
];

function registerDeepLinkProtocol(
  app,
  processInfo = process,
  protocol = ZENITH_TEAM_PROTOCOL,
) {
  if (app.isPackaged) {
    app.setAsDefaultProtocolClient(protocol);
    return;
  }

  const entrypoint = processInfo.argv[1];

  if (entrypoint) {
    app.setAsDefaultProtocolClient(protocol, processInfo.execPath, [
      entrypoint,
    ]);
  }
}

function findDeepLinkUrl(argv, protocol = ZENITH_TEAM_PROTOCOL) {
  return argv.find((value) => isZenithTeamDeepLinkUrl(value, protocol)) ?? null;
}

function isZenithTeamDeepLinkUrl(value, protocol = ZENITH_TEAM_PROTOCOL) {
  return typeof value === "string" && value.startsWith(`${protocol}://`);
}

function routeDeepLinkToWindow(mainWindow, url, protocol = ZENITH_TEAM_PROTOCOL) {
  const route = getRouteFromDeepLinkUrl(url, protocol);

  if (!route || !mainWindow || mainWindow.isDestroyed()) {
    return false;
  }

  const navigate = () => {
    void mainWindow.webContents.executeJavaScript(
      `window.location.hash = ${JSON.stringify(route)}`,
    );
  };

  if (mainWindow.webContents.isLoading()) {
    mainWindow.webContents.once("did-finish-load", navigate);
  } else {
    navigate();
  }

  return true;
}

function getRouteFromDeepLinkUrl(url, protocol = ZENITH_TEAM_PROTOCOL) {
  if (!isZenithTeamDeepLinkUrl(url, protocol)) {
    return null;
  }

  if (hasUnsafeRawDeepLinkPath(url, protocol)) {
    return null;
  }

  let parsedUrl;

  try {
    parsedUrl = new URL(url);
  } catch {
    return null;
  }

  const routePath = normalizeRoutePath(parsedUrl);
  if (
    (routePath.startsWith("/companies/") || routePath.startsWith("/share/")) &&
    (parsedUrl.username ||
      parsedUrl.password ||
      parsedUrl.port ||
      parsedUrl.hash)
  ) {
    return null;
  }
  if (routePath === "/invite" || routePath.startsWith("/invite/")) {
    return /^\/invite\/[A-Za-z0-9_-]{43}$/.test(routePath) &&
      !parsedUrl.username &&
      !parsedUrl.password &&
      !parsedUrl.port &&
      !parsedUrl.search &&
      !parsedUrl.hash
      ? routePath
      : null;
  }

  const route = createRouteFromNormalizedPath(
    routePath,
    parsedUrl.searchParams,
  );

  if (!route || !isAllowedRoutePath(route.pathname)) {
    return null;
  }

  return appendSearchParams(route, parsedUrl.searchParams);
}

function normalizeRoutePath(parsedUrl) {
  const hostPath = parsedUrl.hostname ? `/${parsedUrl.hostname}` : "";
  const pathname = parsedUrl.pathname === "/" ? "" : parsedUrl.pathname;
  return `${hostPath}${pathname}` || "/app";
}

function isAllowedRoute(routePath) {
  if (routePath.startsWith("/share/")) {
    return /^\/share\/artifacts\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      routePath,
    );
  }
  if (routePath.startsWith("/companies/")) {
    return /^\/companies\/[0-9a-f-]{36}\/(?:artifacts|pages)\/[0-9a-f-]{36}$/i.test(
      routePath,
    );
  }
  return allowedRoutePrefixes.some(
    (prefix) => routePath === prefix || routePath.startsWith(`${prefix}/`),
  );
}

function isAllowedRoutePath(routePath) {
  if (!isAllowedRoute(routePath) || hasUnsafeRoutePathSegments(routePath)) {
    return false;
  }

  return true;
}

function createRouteFromNormalizedPath(routePath, searchParams) {
  if (hasUnsafeRoutePathSegments(routePath)) {
    return null;
  }

  const segments = routePath.split("/").filter(Boolean);
  const [section, idOrSlug] = segments;
  const decodedIdOrSlug = idOrSlug ? decodeRouteSegment(idOrSlug) : null;

  if (routePath.startsWith("/companies/") || routePath.startsWith("/share/")) {
    if (
      !isAllowedRoutePath(routePath) ||
      [...searchParams].some(
        ([key, value]) =>
          key !== "item" || !/^(?:[0-9]|[12][0-9]|3[01])$/.test(value),
      ) ||
      [...searchParams.keys()].length > 1
    ) {
      return null;
    }
    return {
      pathname: routePath,
      searchParams: new URLSearchParams(searchParams),
    };
  }

  if (!section) {
    return { pathname: "/app", searchParams: new URLSearchParams() };
  }

  if (
    section === "company" &&
    (segments.length === 1 ||
      (segments.length === 2 && segments[1] === "settings"))
  ) {
    return {
      pathname: "/settings/general",
      searchParams: new URLSearchParams(),
    };
  }

  if (section === "conversation" && decodedIdOrSlug) {
    return createQueryRoute("/app", "conversationId", decodedIdOrSlug);
  }

  if (section === "approval" && decodedIdOrSlug) {
    return createQueryRoute("/approvals", "approvalId", decodedIdOrSlug);
  }

  if (section === "approvals" && decodedIdOrSlug && segments.length === 2) {
    return createQueryRoute("/approvals", "approvalId", decodedIdOrSlug);
  }

  if (!isAllowedRoutePath(routePath)) {
    return null;
  }

  return {
    pathname: routePath,
    searchParams: new URLSearchParams(searchParams),
  };
}

function createQueryRoute(pathname, key, value) {
  const routeSearchParams = new URLSearchParams();
  routeSearchParams.set(key, value);

  return {
    pathname,
    searchParams: routeSearchParams,
  };
}

function decodeRouteSegment(segment) {
  try {
    return decodeURIComponent(segment);
  } catch {
    return null;
  }
}

function appendSearchParams(route, originalSearchParams) {
  const nextSearchParams = new URLSearchParams(route.searchParams);

  for (const [key, value] of originalSearchParams) {
    if (!nextSearchParams.has(key)) {
      nextSearchParams.append(key, value);
    }
  }

  const search = nextSearchParams.toString();
  return search ? `${route.pathname}?${search}` : route.pathname;
}

function hasUnsafeRoutePathSegments(routePath) {
  return routePath
    .split("/")
    .filter(Boolean)
    .some((segment) => {
      try {
        const decodedSegment = decodeURIComponent(segment);
        return (
          decodedSegment === "." ||
          decodedSegment === ".." ||
          decodedSegment.includes("\\") ||
          decodedSegment.includes("\0")
        );
      } catch {
        return true;
      }
    });
}

function hasUnsafeRawDeepLinkPath(rawUrl, protocol = ZENITH_TEAM_PROTOCOL) {
  const rawPath = rawUrl.slice(`${protocol}://`.length).split(/[?#]/, 1)[0];

  return rawPath
    .split("/")
    .filter(Boolean)
    .some((segment) => isUnsafeRouteSegment(segment));
}

function isUnsafeRouteSegment(segment) {
  try {
    const decodedSegment = decodeURIComponent(segment);
    return (
      decodedSegment === "." ||
      decodedSegment === ".." ||
      decodedSegment.includes("\\") ||
      decodedSegment.includes("\0")
    );
  } catch {
    return true;
  }
}

module.exports = {
  ZENITH_TEAM_PROTOCOL,
  findDeepLinkUrl,
  getRouteFromDeepLinkUrl,
  registerDeepLinkProtocol,
  routeDeepLinkToWindow,
};
