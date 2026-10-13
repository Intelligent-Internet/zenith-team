import axios, {
  AxiosHeaders,
  type AxiosError,
  type AxiosInstance,
  type AxiosRequestConfig,
  type InternalAxiosRequestConfig,
} from "axios";
import {
  cacheStoredAccessToken,
  clearStoredAccessToken,
  getStoredAccessToken,
  getStoredAuthorizationHeader,
} from "@/lib/auth-token";
import { plainApiErrorCopy } from "@/lib/api-error-copy";
import type {
  DesktopSessionRotation,
  SessionRotation,
} from "@/shared/api/types";

const DEFAULT_LOCAL_API_ORIGIN = "http://localhost:8788";
const DEFAULT_API_REQUEST_TIMEOUT_MS = 20_000;
const PUBLIC_ROUTE_PREFIXES = [
  "/",
  "/invite/",
  "/join/",
  "/share/artifacts/",
  "/login",
  "/terms",
  "/privacy",
  "/auth/",
];

export type ApiErrorBody = {
  code: string;
  detail: string;
  requestId: string | null;
  [key: string]: unknown;
};

type ApiErrorResponseData = Record<string, unknown> | string | null | undefined;

type RefreshableRequestConfig = InternalAxiosRequestConfig & {
  _boardlyAuthReplay?: boolean;
  _boardlyReadRetry?: boolean;
};

type ApiClientRequestConfig = Omit<
  AxiosRequestConfig,
  "baseURL" | "method" | "url"
>;

export const BOARDLY_API_ROUTE_UNAVAILABLE_MESSAGE =
  "Zenith Team could not be reached. Check your connection and try again.";
export const BOARDLY_API_TIMEOUT_MESSAGE =
  "Zenith Team took too long to respond. Check the API and try again.";

export function resolveApiRequestTimeoutMs(configuredTimeout?: string) {
  const timeoutMs = Number(configuredTimeout);
  return Number.isFinite(timeoutMs) && timeoutMs > 0
    ? timeoutMs
    : DEFAULT_API_REQUEST_TIMEOUT_MS;
}

function isLocalHostname(hostname: string) {
  return (
    hostname === "localhost" ||
    hostname === "127.0.0.1" ||
    hostname === "0.0.0.0" ||
    hostname === "::1" ||
    hostname === "[::1]" ||
    hostname.endsWith(".localhost")
  );
}

function defaultApiOrigin() {
  if (typeof window === "undefined") {
    return DEFAULT_LOCAL_API_ORIGIN;
  }

  if (window.location.port && window.location.port !== "5173") {
    return window.location.origin;
  }

  if (!isLocalHostname(window.location.hostname)) {
    return window.location.origin;
  }

  if (window.location.hostname === "127.0.0.1") {
    return "http://127.0.0.1:8788";
  }

  if (
    window.location.hostname === "::1" ||
    window.location.hostname === "[::1]"
  ) {
    return "http://[::1]:8788";
  }

  return DEFAULT_LOCAL_API_ORIGIN;
}

function isUnsafeLoopbackOverride(rawApiUrl: string) {
  if (
    typeof window === "undefined" ||
    window.location.protocol === "boardly-app:" ||
    isLocalHostname(window.location.hostname)
  ) {
    return false;
  }

  try {
    const apiUrl = new URL(rawApiUrl, window.location.origin);
    return isLocalHostname(apiUrl.hostname);
  } catch {
    return false;
  }
}

export function normalizeApiBaseUrl(rawApiUrl: string) {
  const trimmedApiUrl = rawApiUrl
    .trim()
    .replace(/^['"]+|['"]+$/g, "")
    .trim()
    .replace(/\/+$/, "");
  if (trimmedApiUrl.endsWith("/api/v1")) {
    return trimmedApiUrl;
  }
  if (trimmedApiUrl.endsWith("/api")) {
    return `${trimmedApiUrl}/v1`;
  }
  return `${trimmedApiUrl}/api/v1`;
}

export function resolveBoardlyApiBaseUrl(configuredApiUrl?: string) {
  const configured = configuredApiUrl?.trim();
  const apiOrigin =
    configured && !isUnsafeLoopbackOverride(configured)
      ? configured
      : defaultApiOrigin();

  return normalizeApiBaseUrl(apiOrigin);
}

function isPublicRoute(path: string): boolean {
  return PUBLIC_ROUTE_PREFIXES.some((prefix) =>
    prefix === "/" ? path === "/" : path.startsWith(prefix),
  );
}

export function resolveClientRoutePath(pathname: string, hash: string) {
  const hashRoute = hash.replace(/^#/, "");
  if (hashRoute.startsWith("/")) {
    return hashRoute.split("?", 1)[0] || "/";
  }
  return pathname;
}

function normalizeApiErrorBody(
  error: AxiosError<ApiErrorResponseData>,
): ApiErrorBody | null {
  const body = error.response?.data;
  if (
    !body ||
    typeof body !== "object" ||
    typeof body.code !== "string" ||
    typeof body.detail !== "string" ||
    (body.requestId !== null && typeof body.requestId !== "string")
  ) {
    return null;
  }

  return body as ApiErrorBody;
}

function getApiErrorCodeFromBody(body: ApiErrorBody | null): string | null {
  return body?.code ?? null;
}

function getApiErrorMessageFromBody(body: ApiErrorBody | null): string | null {
  const detail = body?.detail.trim();
  return detail || null;
}

function requiresInteractiveReauthentication(
  error: AxiosError<ApiErrorResponseData>,
) {
  return (
    getApiErrorCodeFromBody(normalizeApiErrorBody(error)) ===
    "owner_reauthentication_required"
  );
}

function isApiRouteUnavailableError(
  error: AxiosError<ApiErrorResponseData>,
): boolean {
  return !error.response && error.message === "Network Error";
}

function isCanceledApiRequest(error: AxiosError<ApiErrorResponseData>) {
  return axios.isCancel(error) || error.code === "ERR_CANCELED";
}

function isIdempotentRead(config: RefreshableRequestConfig) {
  const method = config.method?.toUpperCase();
  return method === "GET" || method === "HEAD";
}

function shouldRetryIdempotentRead(
  error: AxiosError<ApiErrorResponseData>,
  config: RefreshableRequestConfig,
) {
  if (
    config._boardlyReadRetry ||
    !isIdempotentRead(config) ||
    isCanceledApiRequest(error)
  ) {
    return false;
  }

  if (!error.response) {
    return true;
  }

  return error.response.status >= 500 && error.response.status < 600;
}

function isApiRequestTimeoutError(error: AxiosError<ApiErrorResponseData>) {
  return (
    !error.response &&
    (error.code === "ECONNABORTED" || error.code === "ETIMEDOUT")
  );
}

function getApiTimeoutMessage(
  error: AxiosError<ApiErrorResponseData>,
): string | null {
  return isApiRequestTimeoutError(error) ? BOARDLY_API_TIMEOUT_MESSAGE : null;
}

function getApiRouteUnavailableMessage(
  error: AxiosError<ApiErrorResponseData>,
): string | null {
  return isApiRouteUnavailableError(error)
    ? BOARDLY_API_ROUTE_UNAVAILABLE_MESSAGE
    : null;
}

export class ApiClientError extends Error {
  readonly body: ApiErrorBody | null;
  readonly code: string | null;
  readonly cause: AxiosError<ApiErrorResponseData>;
  readonly status: number | null;

  constructor(error: AxiosError<ApiErrorResponseData>) {
    const body = normalizeApiErrorBody(error);
    super(
      plainApiErrorCopy(body?.code, body?.detail) ??
        getApiErrorMessageFromBody(body) ??
        getApiTimeoutMessage(error) ??
        getApiRouteUnavailableMessage(error) ??
        error.message,
    );
    this.name = "ApiClientError";
    this.body = body;
    this.code = getApiErrorCodeFromBody(body);
    this.cause = error;
    this.status = error.response?.status ?? null;
  }
}

function toApiClientError(error: AxiosError<ApiErrorResponseData>) {
  return new ApiClientError(error);
}

export function isApiClientError(error: unknown): error is ApiClientError {
  return error instanceof ApiClientError;
}

export function isAuthenticationFailure(error: unknown): boolean {
  if (error instanceof SessionLostError) return true;
  return isApiClientError(error)
    ? error.status === 401
    : axios.isAxiosError(error) && error.response?.status === 401;
}

// The saved session cannot be refreshed and will not become refreshable by
// waiting: the server rejected the credential, or the desktop has nothing
// saved to refresh with. It is an authentication failure — the way back is
// to sign in — not a connectivity problem to retry (docs/api/authentication.md:
// every /auth/refresh error is a credential failure).
export class SessionLostError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SessionLostError";
  }
}

// Whether a failed request reached the server at all. A network failure or
// a timeout leaves `status` null; a server answer, even 5xx, carries one.
export function isApiUnreachableError(error: unknown): boolean {
  if (isApiClientError(error)) return error.status === null;
  return axios.isAxiosError(error) && !error.response;
}

export function getApiErrorBody(error: unknown): ApiErrorBody | null {
  return isApiClientError(error) ? error.body : null;
}

export function getApiErrorCode(error: unknown): string | null {
  return isApiClientError(error) ? error.code : null;
}

export function getApiErrorMessage(error: unknown, fallback: string): string {
  if (isApiClientError(error) && error.message.trim()) {
    return error.message.trim();
  }

  if (error instanceof Error && error.message.trim()) {
    return error.message.trim();
  }

  return fallback;
}

export const BOARDLY_API_BASE_URL = resolveBoardlyApiBaseUrl(
  import.meta.env.VITE_API_URL,
);
export const BOARDLY_API_REQUEST_TIMEOUT_MS = resolveApiRequestTimeoutMs(
  import.meta.env.VITE_API_TIMEOUT_MS,
);

// Every set-valued query parameter in the API travels as one comma-separated
// value; repeating the parameter is a 422. Axios would otherwise emit
// `status[]=a&status[]=b`, so array params are joined here rather than at each
// call site.
function serializeQueryParams(params: Record<string, unknown>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null) continue;
    if (Array.isArray(value)) {
      if (value.length === 0) continue;
      search.append(key, value.join(","));
      continue;
    }
    search.append(key, String(value));
  }
  return search.toString();
}

const rawApiClient = axios.create({
  baseURL: BOARDLY_API_BASE_URL,
  paramsSerializer: serializeQueryParams,
  timeout: BOARDLY_API_REQUEST_TIMEOUT_MS,
  withCredentials: true,
});

const refreshApiClient = axios.create({
  baseURL: BOARDLY_API_BASE_URL,
  timeout: BOARDLY_API_REQUEST_TIMEOUT_MS,
  withCredentials: true,
});

let refreshRequest: Promise<SessionRotation | DesktopSessionRotation> | null =
  null;

export function refreshAuthSession(): Promise<
  SessionRotation | DesktopSessionRotation
> {
  const desktopBridge =
    typeof window === "undefined" ? undefined : window.boardlyDesktop;
  const accessTokenBeforeRefresh = getStoredAccessToken();
  if (desktopBridge?.refreshAuthSession && accessTokenBeforeRefresh !== null) {
    refreshRequest ??= desktopBridge
      .refreshAuthSession(BOARDLY_API_BASE_URL)
      .then((result) => {
        if (!result.ok) {
          // A rejected credential or a missing saved session is lost for
          // good; only an unreachable or failing server is worth a retry.
          // So is a renewal this computer could not save
          // (session_not_saved): the saved session stays, and renews once
          // secure storage works again.
          if (result.kind === "rejected" || result.kind === "session_missing") {
            throw new SessionLostError(result.reason);
          }
          throw new Error(result.reason);
        }
        if (getStoredAccessToken() !== accessTokenBeforeRefresh) {
          throw new Error("Your session changed. Try again.");
        }
        cacheStoredAccessToken(result.response.session.accessToken);
        return result.response;
      })
      .finally(() => {
        refreshRequest = null;
      });
    return refreshRequest;
  }

  refreshRequest ??= refreshApiClient
    .post<SessionRotation>("/auth/refresh", {
      client: "web",
      clientMetadata: { platform: "web" },
    })
    .then((response) => response.data)
    .catch((error: unknown) => {
      // The server answered and refused: no cookie, an expired or revoked
      // family, a lost rotation race. None of these mend with a retry.
      if (
        axios.isAxiosError(error) &&
        error.response &&
        error.response.status < 500
      ) {
        throw new SessionLostError(
          getApiErrorMessage(
            toApiClientError(error as AxiosError<ApiErrorResponseData>),
            "Your session has ended. Sign in again.",
          ),
        );
      }
      throw error;
    })
    .finally(() => {
      refreshRequest = null;
    });

  return refreshRequest;
}

function readCookie(cookies: string, name: string): string | null {
  const prefix = `${name}=`;
  for (const part of cookies.split(";")) {
    const value = part.trim();
    if (value.startsWith(prefix)) {
      return decodeURIComponent(value.slice(prefix.length));
    }
  }

  return null;
}

export function resolveCookieCsrfToken(
  method: string,
  path: string,
  cookies: string,
): string | null {
  const normalizedMethod = method.toUpperCase();
  if (
    normalizedMethod === "GET" ||
    normalizedMethod === "HEAD" ||
    normalizedMethod === "OPTIONS"
  ) {
    return null;
  }

  const usesRefreshCookie = path === "/auth/refresh" || path === "/auth/logout";
  const canUseRefreshFallback =
    path === "/auth/login" ||
    path === "/auth/signup" ||
    path === "/auth/email-verification/verify" ||
    path === "/auth/email-verification/resend" ||
    path === "/auth/email-verification/status" ||
    path === "/auth/password-reset/request" ||
    path === "/auth/password-reset/confirm" ||
    path === "/auth/google/web/start";
  const accessCSRF = readCookie(cookies, "boardly_csrf");
  const refreshCSRF = readCookie(cookies, "boardly_refresh_csrf");

  return usesRefreshCookie
    ? refreshCSRF
    : (accessCSRF ?? (canUseRefreshFallback ? refreshCSRF : null));
}

function withCookieCSRF(config: InternalAxiosRequestConfig) {
  const method = config.method?.toUpperCase() ?? "GET";
  const path = config.url ?? "";
  const cookies = typeof document === "undefined" ? "" : document.cookie;
  const csrfToken = resolveCookieCsrfToken(method, path, cookies);

  if (!csrfToken) {
    return config;
  }

  const headers = AxiosHeaders.from(config.headers);
  headers.set("X-CSRF-Token", csrfToken);
  config.headers = headers;
  return config;
}

function withDesktopAuthorization(config: InternalAxiosRequestConfig) {
  const authorization = getStoredAuthorizationHeader();
  if (!authorization) {
    return config;
  }
  const headers = AxiosHeaders.from(config.headers);
  // A retried Axios config still contains the access token from the failed
  // request. Always replace it with the latest desktop token after refresh.
  headers.set("Authorization", authorization);
  config.headers = headers;
  return config;
}

rawApiClient.interceptors.request.use(withCookieCSRF);
rawApiClient.interceptors.request.use(withDesktopAuthorization);
refreshApiClient.interceptors.request.use(withCookieCSRF);

rawApiClient.interceptors.response.use(
  (response) => response,
  async (error: AxiosError<ApiErrorResponseData>) => {
    const originalConfig = error.config as RefreshableRequestConfig | undefined;
    const currentPath =
      typeof window === "undefined"
        ? "/"
        : resolveClientRoutePath(
            window.location.pathname,
            window.location.hash,
          );
    const publicRoute = isPublicRoute(currentPath);

    if (originalConfig && shouldRetryIdempotentRead(error, originalConfig)) {
      originalConfig._boardlyReadRetry = true;
      return rawApiClient(originalConfig);
    }

    if (
      error.response?.status !== 401 ||
      !originalConfig ||
      originalConfig._boardlyAuthReplay ||
      publicRoute ||
      requiresInteractiveReauthentication(error)
    ) {
      return Promise.reject(toApiClientError(error));
    }

    const accessTokenBeforeRefresh = getStoredAccessToken();
    try {
      await refreshAuthSession();
      originalConfig._boardlyAuthReplay = true;
      return rawApiClient(originalConfig);
    } catch (refreshError) {
      if (
        isAuthenticationFailure(refreshError) &&
        getStoredAccessToken() === accessTokenBeforeRefresh
      ) {
        clearStoredAccessToken();
        if (typeof window !== "undefined" && !publicRoute) {
          window.location.replace(window.boardlyDesktop ? "#/login" : "/login");
        }
      }
      // A failed refresh may mean the API is unreachable, not that the
      // session expired. Preserve the credential and the actual failure.
      return Promise.reject(
        axios.isAxiosError(refreshError)
          ? toApiClientError(refreshError)
          : refreshError,
      );
    }
  },
);

class ApiClient {
  constructor(private readonly client: AxiosInstance) {}

  async delete<T>(url: string, config?: ApiClientRequestConfig): Promise<T> {
    const response = await this.client.delete<T>(url, config);
    return response.data;
  }

  async get<T>(url: string, config?: ApiClientRequestConfig): Promise<T> {
    const response = await this.client.get<T>(url, config);
    return response.data;
  }

  async post<T>(
    url: string,
    data?: unknown,
    config?: ApiClientRequestConfig,
  ): Promise<T> {
    const response = await this.client.post<T>(url, data, config);
    return response.data;
  }

  async patch<T>(
    url: string,
    data?: unknown,
    config?: ApiClientRequestConfig,
  ): Promise<T> {
    const response = await this.client.patch<T>(url, data, config);
    return response.data;
  }

  async put<T>(
    url: string,
    data?: unknown,
    config?: ApiClientRequestConfig,
  ): Promise<T> {
    const response = await this.client.put<T>(url, data, config);
    return response.data;
  }
}

export const apiClient = new ApiClient(rawApiClient);
