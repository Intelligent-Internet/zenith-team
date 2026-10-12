import { cacheStoredAccessToken } from "@/lib/auth-token";
import { BOARDLY_API_BASE_URL } from "@/lib/api-client";
import { DesktopAuthBridgeError } from "@/shared/api/desktop-auth-error";
import type { DesktopLoginResponse } from "@/shared/api/types";
import {
  readStorageJson,
  removeStorageItem,
  writeStorageJson,
} from "@/shared/lib/browser-storage";

const GOOGLE_OAUTH_REQUEST_STORAGE_KEY = "boardly.google-oauth-request.v2";

type DesktopGoogleOAuthRequest = {
  expiresAt: string;
  flowId: string;
  redirectUri: string;
  state: string;
  verifier: string;
};

export class GoogleOAuthError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "GoogleOAuthError";
  }
}

export async function beginDesktopGoogleOAuthRedirect(): Promise<void> {
  const bridge = desktopBridge();
  if (
    !bridge.startGoogleOAuthLoopback ||
    !bridge.cancelGoogleOAuthLoopback ||
    !bridge.openExternal ||
    !bridge.completeGoogleOAuth ||
    !bridge.startGoogleOAuth
  ) {
    throw new GoogleOAuthError("Desktop Google sign-in is unavailable.");
  }

  report("google-oauth-begin", "Desktop Google OAuth flow started.");
  clearStoredRequest();
  const loopback = await bridge.startGoogleOAuthLoopback();
  if (!loopback.ok) {
    throw new GoogleOAuthError(
      loopback.reason || "Unable to start Google sign-in callback.",
    );
  }

  try {
    const environment = await bridge.getEnvironment();
    const verifier = createRandomToken(64);
    const codeChallenge = await createCodeChallenge(verifier);
    const startResult = await bridge.startGoogleOAuth({
      apiBaseUrl: BOARDLY_API_BASE_URL,
      redirectUri: loopback.redirectUri,
      codeChallenge,
      clientMetadata: {
        appVersion: environment.appVersion ?? undefined,
        platform: environment.platform,
      },
    });
    if (!startResult.ok) {
      throw new GoogleOAuthError(startResult.reason);
    }
    const started = startResult.response;
    assertTrustedGoogleAuthorizationUrl(started.authorizationUrl);
    assertStartResponse(started);
    writeStoredRequest({
      expiresAt: started.expiresAt,
      flowId: started.flowId,
      redirectUri: loopback.redirectUri,
      state: started.state,
      verifier,
    });

    const opened = await bridge.openExternal(started.authorizationUrl);
    if (!opened.ok) {
      throw new GoogleOAuthError(
        opened.reason || "Unable to open Google sign-in in your browser.",
      );
    }
    report(
      "google-oauth-external-opened",
      "Desktop Google OAuth authorization URL opened.",
    );
  } catch (error) {
    clearStoredRequest();
    await bridge.cancelGoogleOAuthLoopback();
    throw error;
  }
}

export async function completeDesktopGoogleOAuth(
  search: string,
): Promise<DesktopLoginResponse> {
  const bridge = desktopBridge();
  if (!bridge.completeGoogleOAuth) {
    throw new GoogleOAuthError("Desktop Google sign-in is unavailable.");
  }

  const completion = callbackCompletion(search);
  const request = readStoredRequest();
  clearStoredRequest();
  if (!request) {
    throw new GoogleOAuthError(
      "Google sign-in session expired. Please try again.",
    );
  }
  if (!isFresh(request.expiresAt)) {
    throw new GoogleOAuthError(
      "Google sign-in session expired. Please try again.",
    );
  }
  if (completion.state !== request.state) {
    throw new GoogleOAuthError(
      "Google sign-in state did not match. Please try again.",
    );
  }

  const result = await bridge.completeGoogleOAuth({
    apiBaseUrl: BOARDLY_API_BASE_URL,
    code: completion.code,
    error: completion.error,
    flowId: request.flowId,
    redirectUri: request.redirectUri,
    state: request.state,
    verifier: request.verifier,
  });
  if (!result.ok) {
    // Keeps the kind, so the callback page can say the sign-in could not
    // be saved here rather than that Google refused it.
    throw new DesktopAuthBridgeError(result);
  }
  cacheStoredAccessToken(result.response.session.accessToken);
  return result.response;
}

function desktopBridge() {
  if (typeof window === "undefined" || !window.boardlyDesktop) {
    throw new GoogleOAuthError("Desktop Google sign-in is unavailable.");
  }
  return window.boardlyDesktop;
}

function callbackCompletion(search: string) {
  const params = new URLSearchParams(search);
  const state = params.get("state")?.trim() ?? "";
  const code = params.get("code")?.trim() ?? "";
  const error = params.get("error")?.trim() ?? "";
  if (!state || Boolean(code) === Boolean(error)) {
    throw new GoogleOAuthError("Google sign-in callback is invalid.");
  }
  if (error && error !== "access_denied") {
    throw new GoogleOAuthError("Google sign-in could not be completed.");
  }
  return { code, error, state };
}

function createRandomToken(byteLength: number) {
  if (typeof crypto === "undefined" || !crypto.getRandomValues) {
    throw new GoogleOAuthError(
      "Secure browser crypto is required for Google sign-in.",
    );
  }
  const bytes = new Uint8Array(byteLength);
  crypto.getRandomValues(bytes);
  return base64UrlEncode(bytes);
}

async function createCodeChallenge(verifier: string) {
  if (typeof crypto === "undefined" || !crypto.subtle) {
    throw new GoogleOAuthError(
      "Secure browser crypto is required for Google sign-in.",
    );
  }
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(verifier),
  );
  return base64UrlEncode(new Uint8Array(digest));
}

function base64UrlEncode(bytes: Uint8Array) {
  let binary = "";
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return btoa(binary)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/g, "");
}

function assertTrustedGoogleAuthorizationUrl(rawUrl: string) {
  let url: URL;
  try {
    url = new URL(rawUrl);
  } catch {
    throw new GoogleOAuthError("The Google authorization URL is invalid.");
  }
  if (url.protocol !== "https:" || url.hostname !== "accounts.google.com") {
    throw new GoogleOAuthError("The Google authorization URL is invalid.");
  }
}

function assertStartResponse(response: {
  expiresAt: string;
  flowId: string;
  state: string;
}) {
  if (
    !response.flowId.trim() ||
    !response.state.trim() ||
    !isFresh(response.expiresAt)
  ) {
    throw new GoogleOAuthError("Google sign-in could not be started.");
  }
}

function isFresh(expiresAt: string) {
  const expiry = Date.parse(expiresAt);
  return Number.isFinite(expiry) && expiry > Date.now();
}

function normalizeStoredRequest(
  value: unknown,
): DesktopGoogleOAuthRequest | null {
  if (!value || typeof value !== "object") {
    return null;
  }
  const request = value as Partial<DesktopGoogleOAuthRequest>;
  if (
    typeof request.expiresAt !== "string" ||
    typeof request.flowId !== "string" ||
    typeof request.redirectUri !== "string" ||
    typeof request.state !== "string" ||
    typeof request.verifier !== "string"
  ) {
    return null;
  }
  return {
    expiresAt: request.expiresAt,
    flowId: request.flowId,
    redirectUri: request.redirectUri,
    state: request.state,
    verifier: request.verifier,
  };
}

function readStoredRequest() {
  return readStorageJson<DesktopGoogleOAuthRequest | null>(
    "session",
    GOOGLE_OAUTH_REQUEST_STORAGE_KEY,
    normalizeStoredRequest,
    null,
  );
}

function writeStoredRequest(request: DesktopGoogleOAuthRequest) {
  writeStorageJson("session", GOOGLE_OAUTH_REQUEST_STORAGE_KEY, request);
}

function clearStoredRequest() {
  removeStorageItem("session", GOOGLE_OAUTH_REQUEST_STORAGE_KEY);
}

function report(kind: string, message: string) {
  window.boardlyDesktop?.reportDiagnostic?.({
    kind,
    message,
    source: "google-oauth",
  });
}
