export type BoardlyProxyEnv = {
  BOARDLY_API_ORIGIN?: string;
};

type Fetcher = (request: Request) => Promise<Response>;

type WorkerRequestInit = RequestInit & {
  duplex?: "half";
};

export async function proxyBoardlyRequest(
  request: Request,
  targetOrigin: string | undefined,
  serviceName: string,
  fetcher: Fetcher = fetch,
): Promise<Response> {
  const targetUrl = resolveTargetUrl(request.url, targetOrigin);

  if (!targetUrl) {
    return missingTargetOriginResponse(serviceName);
  }

  return fetcher(createUpstreamRequest(request, targetUrl));
}

export function resolveTargetUrl(
  requestUrl: string,
  targetOrigin: string | undefined,
): URL | null {
  const normalizedOrigin = targetOrigin?.trim();
  if (!normalizedOrigin) {
    return null;
  }

  try {
    const incomingUrl = new URL(requestUrl);
    const upstreamUrl = new URL(normalizedOrigin);
    const basePath = upstreamUrl.pathname.replace(/\/+$/, "");

    upstreamUrl.pathname = `${basePath}${incomingUrl.pathname}`;
    upstreamUrl.search = incomingUrl.search;
    upstreamUrl.hash = "";

    return upstreamUrl;
  } catch {
    return null;
  }
}

export function createUpstreamRequest(
  request: Request,
  targetUrl: URL,
): Request {
  const incomingUrl = new URL(request.url);
  const headers = new Headers(request.headers);
  headers.delete("connection");
  headers.delete("content-length");
  headers.delete("host");
  headers.set("x-forwarded-host", incomingUrl.host);
  headers.set("x-forwarded-proto", incomingUrl.protocol.replace(":", ""));

  const init: WorkerRequestInit = {
    body: shouldProxyBody(request.method) ? request.body : undefined,
    headers,
    method: request.method,
    redirect: "manual",
  };

  if (init.body) {
    init.duplex = "half";
  }

  return new Request(targetUrl, init);
}

function shouldProxyBody(method: string): boolean {
  return method !== "GET" && method !== "HEAD";
}

function missingTargetOriginResponse(serviceName: string): Response {
  return Response.json(
    {
      code: "boardly_proxy_origin_not_configured",
      message: `${serviceName} origin is not configured for this deployment.`,
    },
    {
      status: 503,
    },
  );
}
