const { app } = require("electron");
const {
  canSaveSession,
  ensureDaemonSession,
  ensureStoredSession,
  installStoredSession,
  readStoredSession,
  signOutStoredSession,
} = require("./desktop-auth-store.cjs");
const { writeMainLog } = require("./diagnostics.cjs");
const { createDesktopRuntimeEnv } = require("./release-env.cjs");
const { getDesktopVariant } = require("../shared/desktop-variant.cjs");

let beforeDesktopSessionReplace = async () => {};

const defaultApi = createDesktopAuthApi({
  app,
  env: createDesktopRuntimeEnv(),
  fetch: globalThis.fetch,
  beforeSessionReplace: (...args) => beforeDesktopSessionReplace(...args),
  store: {
    canSaveSession,
    ensureDaemonSession,
    ensureStoredSession,
    installStoredSession,
    readStoredSession,
    signOutStoredSession,
  },
  writeMainLog,
});

function createDesktopAuthApi(dependencies) {
  const {
    app: electronApp,
    beforeSessionReplace = async () => {},
    env = {},
    fetch: fetchImplementation,
    store,
    writeMainLog: writeLog = () => {},
  } = dependencies;

  if (typeof fetchImplementation !== "function") {
    throw new Error("Desktop auth HTTP client is unavailable");
  }

  async function sendSessionRequest(apiBaseUrl, path, body) {
    const trustedBaseUrl = trustedApiBaseUrl(apiBaseUrl, {
      app: electronApp,
      env,
    });
    const response = await fetchOrNetworkFailure(
      fetchImplementation,
      `${trustedBaseUrl}${path}`,
      {
        body: JSON.stringify(body),
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        method: "POST",
      },
    );
    if (!response?.ok) {
      throw await rejectedByServer(response);
    }
    const payload = await safeJSON(response);
    if (payload?.status === "pending_verification") {
      return parsePendingEmailVerification(payload);
    }
    const session = parseBearerSession(payload);
    // From here the server holds a live session for this sign-in. Unless
    // this computer keeps it, it is ended before the error goes back.
    let stored = null;
    try {
      if (store.canSaveSession?.() === false) {
        // Nothing is replaced: an older session this computer cannot read
        // right now stays for when it can.
        writeLog("desktop-auth-session-install-failed", {
          message: "Secure storage is unavailable on this computer",
        });
      } else {
        const current = await store.readStoredSession?.();
        if (
          (current && !current.ok) ||
          (current?.accessToken &&
            ownerFromAccessToken(current.accessToken) !== session.user.id)
        ) {
          await beforeSessionReplace();
        }
        stored = await store.installStoredSession({
          accessToken: session.session.accessToken,
          accessExpiresAt: session.session.accessExpiresAt,
          apiBaseUrl: trustedBaseUrl,
          refreshExpiresAt: session.session.refreshExpiresAt,
          refreshToken: session.refreshToken,
        });
      }
    } catch (error) {
      // An older session could not be cleared first, so this one was not
      // saved either.
      writeLog("desktop-auth-session-install-failed", {
        message: error?.message,
      });
    }
    if (!stored?.ok) {
      // The cause is in the main log (desktop-auth-session-install-failed).
      // The password, code or Google account was right; only this computer
      // could not keep the sign-in, and never in a plain file instead.
      await endUnsavedSession(trustedBaseUrl, session.refreshToken);
      throw new DesktopAuthError(
        "session_not_saved",
        "Zenith Team Desktop could not save the session on this computer",
      );
    }
    // The daemon gets a fresh session of its own. Sign-in does not
    // wait on it: the agent service asks again before it starts.
    void store.ensureDaemonSession?.({ replace: true });
    return {
      result: "session",
      user: session.user,
      companies: session.companies,
      session: {
        transport: "bearer",
        id: session.session.id,
        issuedAt: session.session.issuedAt,
        accessToken: session.session.accessToken,
        accessExpiresAt: session.session.accessExpiresAt,
        refreshExpiresAt: session.session.refreshExpiresAt,
      },
    };
  }

  // endUnsavedSession signs out a session the server just started that this
  // computer could not keep, so no live session is left that nobody holds.
  // The sign-in already failed; this only records whether the server
  // ended it.
  async function endUnsavedSession(trustedBaseUrl, refreshToken) {
    try {
      const response = await fetchImplementation(
        `${trustedBaseUrl}/auth/logout`,
        {
          body: JSON.stringify({ client: "desktop", refreshToken }),
          headers: {
            Accept: "application/json",
            "Content-Type": "application/json",
          },
          method: "POST",
          signal: AbortSignal.timeout(10_000),
        },
      );
      writeLog(
        response?.ok
          ? "desktop-auth-unsaved-session-ended"
          : "desktop-auth-unsaved-session-end-failed",
        { status: response?.status ?? null },
      );
    } catch (error) {
      writeLog("desktop-auth-unsaved-session-end-failed", {
        message: error?.message,
      });
    }
  }

  return {
    async getProviders(input) {
      const trustedBaseUrl = trustedApiBaseUrl(input?.apiBaseUrl, {
        app: electronApp,
        env,
      });
      const response = await fetchImplementation(
        `${trustedBaseUrl}/auth/providers`,
        {
          headers: { Accept: "application/json" },
          method: "GET",
        },
      );
      const payload = await safeJSON(response);
      const google = response?.ok
        ? normalizeGoogleProviderCapability(payload)
        : null;
      if (!google) {
        throw new Error("Zenith Team auth providers response is invalid");
      }
      return { items: [google] };
    },
    async startGoogleOAuth(input) {
      const trustedBaseUrl = trustedApiBaseUrl(input?.apiBaseUrl, {
        app: electronApp,
        env,
      });
      const body = desktopGoogleStart(input);
      const response = await fetchImplementation(
        `${trustedBaseUrl}/auth/google/desktop/start`,
        {
          body: JSON.stringify(body),
          headers: {
            Accept: "application/json",
            "Content-Type": "application/json",
          },
          method: "POST",
        },
      );
      const payload = await safeJSON(response);
      if (!response?.ok) {
        throw new Error(
          "Zenith Team Desktop Google sign-in could not be started",
        );
      }
      return parseDesktopGoogleStart(payload);
    },
    completeGoogleOAuth(input) {
      const completion = desktopGoogleCompletion(input);
      return sendSessionRequest(
        input.apiBaseUrl,
        "/auth/google/desktop/complete",
        completion,
      );
    },
    // Password auth must run in the main process: the renderer's requests
    // carry an Origin header, which the API only accepts for the cookie
    // (web) transport. Routing through here keeps the refresh token in the
    // desktop vault, exactly like the Google flow.
    loginWithPassword(input) {
      return sendSessionRequest(input?.apiBaseUrl, "/auth/login", {
        email: requiredString(input?.email),
        password: requiredString(input?.password),
        client: "desktop",
        clientMetadata: passwordClientMetadata(input?.clientMetadata),
      });
    },
    signupWithPassword(input) {
      return sendSessionRequest(input?.apiBaseUrl, "/auth/signup", {
        email: requiredString(input?.email),
        password: requiredString(input?.password),
        displayName: requiredString(input?.displayName),
        client: "desktop",
        clientMetadata: passwordClientMetadata(input?.clientMetadata),
      });
    },
    verifyEmail(input) {
      return sendSessionRequest(
        input?.apiBaseUrl,
        "/auth/email-verification/verify",
        {
          client: "desktop",
          code: requiredString(input?.code),
          email: requiredString(input?.email),
          verificationId: requiredString(input?.verificationId),
        },
      );
    },
    resendEmailVerification(input) {
      return sendVerificationLookupRequest(
        input?.apiBaseUrl,
        "/auth/email-verification/resend",
        input,
      );
    },
    getEmailVerificationStatus(input) {
      return sendVerificationLookupRequest(
        input?.apiBaseUrl,
        "/auth/email-verification/status",
        input,
      );
    },
    async refreshSession(input) {
      const trustedBaseUrl = trustedApiBaseUrl(input?.apiBaseUrl, {
        app: electronApp,
        env,
      });
      const stored = await store.ensureStoredSession();
      if (stored?.notSaved) {
        throw new DesktopAuthError(
          "session_not_saved",
          "Zenith Team Desktop could not save the renewed session",
        );
      }
      // A renewal that failed keeps its cause, so an unreachable or failing
      // server is retried and only a refusal or no session signs out.
      if (stored && !stored.ok && stored.kind) {
        throw new DesktopAuthError(
          stored.kind,
          "Desktop authentication session could not be renewed",
        );
      }
      if (
        !stored?.ok ||
        !stored.accessToken ||
        stored.apiBaseUrl !== trustedBaseUrl
      ) {
        throw new DesktopAuthError(
          "session_missing",
          "Desktop authentication session is missing",
        );
      }
      const response = await fetchOrNetworkFailure(
        fetchImplementation,
        `${trustedBaseUrl}/me`,
        {
          headers: {
            Accept: "application/json",
            Authorization: `Bearer ${stored.accessToken}`,
          },
          method: "GET",
        },
      );
      if (!response?.ok) {
        throw await rejectedByServer(response);
      }
      const payload = await safeJSON(response);
      const metadata = sessionMetadataFromAccessToken(stored.accessToken);
      return {
        user: parseAuthUser(payload),
        session: {
          transport: "bearer",
          id: metadata.id,
          issuedAt: metadata.issuedAt,
          accessToken: stored.accessToken,
          accessExpiresAt: requiredString(stored.accessExpiresAt),
          refreshExpiresAt: requiredString(stored.refreshExpiresAt),
        },
      };
    },
    async logoutSession(input) {
      const stored = await store.readStoredSession();
      if (!stored?.ok || !stored.accessToken) return;
      const trustedBaseUrl = trustedApiBaseUrl(input?.apiBaseUrl, {
        app: electronApp,
        env,
      });
      if (stored.apiBaseUrl !== trustedBaseUrl) {
        throw new Error("Desktop authentication session origin changed");
      }
      // The app holds its own refresh token and ends its own session;
      // a server that refuses fails the sign-out so the app can say so.
      await store.signOutStoredSession();
    },
  };

  async function sendVerificationLookupRequest(apiBaseUrl, path, input) {
    const trustedBaseUrl = trustedApiBaseUrl(apiBaseUrl, {
      app: electronApp,
      env,
    });
    const response = await fetchImplementation(`${trustedBaseUrl}${path}`, {
      body: JSON.stringify({
        client: "desktop",
        email: requiredString(input?.email),
        verificationId: requiredString(input?.verificationId),
      }),
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      method: "POST",
    });
    const payload = await safeJSON(response);
    if (!response?.ok) {
      throw new Error("Zenith Team Desktop email verification request failed");
    }
    if (payload?.status === "pending_verification") {
      return parsePendingEmailVerification(payload);
    }
    if (payload?.status === "expired" || payload?.status === "not_found") {
      return { status: payload.status };
    }
    throw new Error(
      "Zenith Team Desktop email verification response is invalid",
    );
  }
}

function passwordClientMetadata(metadata) {
  const body = {};
  if (metadata?.appVersion) {
    body.appVersion = requiredString(metadata.appVersion);
  }
  if (metadata?.platform) {
    body.platform = requiredString(metadata.platform);
  }
  return body;
}

function desktopGoogleStart(input) {
  const metadata = input?.clientMetadata;
  const body = {
    redirectUri: requiredString(input?.redirectUri),
    codeChallenge: requiredString(input?.codeChallenge),
    clientMetadata: {},
  };
  if (body.codeChallenge.length !== 43) {
    throw new Error("Desktop Google OAuth start is invalid");
  }
  if (metadata?.appVersion) {
    body.clientMetadata.appVersion = requiredString(metadata.appVersion);
  }
  if (metadata?.platform) {
    body.clientMetadata.platform = requiredString(metadata.platform);
  }
  return body;
}

function parseDesktopGoogleStart(payload) {
  const response = {
    flowId: requiredString(payload?.flowId),
    state: requiredString(payload?.state),
    authorizationUrl: requiredString(payload?.authorizationUrl),
    expiresAt: requiredString(payload?.expiresAt),
  };
  const authorizationUrl = new URL(response.authorizationUrl);
  if (
    authorizationUrl.protocol !== "https:" ||
    authorizationUrl.hostname !== "accounts.google.com" ||
    !Number.isFinite(Date.parse(response.expiresAt))
  ) {
    throw new Error("Zenith Team Desktop Google sign-in response is invalid");
  }
  return response;
}

function desktopGoogleCompletion(input) {
  const completion = {
    flowId: requiredString(input?.flowId),
    redirectUri: requiredString(input?.redirectUri),
    state: requiredString(input?.state),
    code: optionalString(input?.code),
    error: optionalString(input?.error),
    verifier: requiredString(input?.verifier),
  };
  if (Boolean(completion.code) === Boolean(completion.error)) {
    throw new Error("Desktop Google OAuth completion is invalid");
  }
  if (completion.error && completion.error !== "access_denied") {
    throw new Error("Desktop Google OAuth completion is invalid");
  }
  return completion;
}

function trustedApiBaseUrl(rawValue, { app: electronApp, env }) {
  const candidate = normalizeApiBaseUrl(rawValue);
  const variant = getDesktopVariant(env, {
    fallback: electronApp?.isPackaged ? "prod" : "local",
  });
  const configured = normalizeApiBaseUrl(
    env.ZENITH_TEAM_DESKTOP_API_BASE_URL || `${variant.apiOrigin}/api/v1`,
  );
  if (electronApp?.isPackaged) {
    if (candidate !== configured) {
      throw new Error("Desktop API origin is not trusted");
    }
    return candidate;
  }
  const parsed = new URL(candidate);
  if (candidate === configured || isLoopback(parsed.hostname)) {
    return candidate;
  }
  throw new Error("Desktop API origin is not trusted");
}

function normalizeApiBaseUrl(rawValue) {
  const value = requiredString(rawValue).replace(/\/+$/, "");
  const parsed = new URL(value);
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
    parsed.username ||
    parsed.password ||
    parsed.search ||
    parsed.hash ||
    parsed.pathname !== "/api/v1"
  ) {
    throw new Error("Desktop API origin is invalid");
  }
  if (parsed.protocol === "http:" && !isLoopback(parsed.hostname)) {
    throw new Error("Desktop API origin is invalid");
  }
  return parsed.href.replace(/\/$/, "");
}

function isLoopback(hostname) {
  return (
    hostname === "localhost" ||
    hostname === "127.0.0.1" ||
    hostname === "[::1]" ||
    hostname.endsWith(".localhost")
  );
}

async function safeJSON(response) {
  try {
    return await response.json();
  } catch {
    throw new DesktopAuthError(
      "server",
      "Zenith Team Desktop authentication response is invalid",
    );
  }
}

// What went wrong, in terms the sign-in screens can act on. Every failure
// used to collapse into one sentence ("Account could not be created."),
// which hid whether the person mistyped, the email is taken, the server is
// down, or the saved sign-in is gone — each needs a different next step
// (docs/api/errors.md: retry once, then surface the requestId; a message
// names the target, the consequence and the recovery).
//   network           — the server could not be reached at all
//   server            — the server answered 5xx or with an unusable body
//   rejected          — the server refused the request with an error envelope
//   session_missing   — no saved desktop session to refresh with
//   session_not_saved — the server accepted the sign-in (or renewed the
//                       session) but this computer could not save it:
//                       secure storage is unavailable or the store could not
//                       be written. A new sign-in's session was ended on the
//                       server; a renewal stays recoverable from the store.
class DesktopAuthError extends Error {
  constructor(kind, message, details = {}) {
    super(message);
    this.name = "DesktopAuthError";
    this.kind = kind;
    this.status = details.status ?? null;
    this.code = details.code ?? null;
    this.detail = details.detail ?? null;
    this.requestId = details.requestId ?? null;
  }
}

async function fetchOrNetworkFailure(fetchImplementation, url, init) {
  try {
    return await fetchImplementation(url, init);
  } catch (error) {
    throw new DesktopAuthError(
      "network",
      error instanceof Error && error.message
        ? error.message
        : "Zenith Team could not be reached",
    );
  }
}

// The server's error envelope is {detail, code, requestId}; the detail is a
// sentence written for a person (docs/api/errors.md), so it is kept.
async function rejectedByServer(response) {
  let envelope = null;
  try {
    envelope = await response.json();
  } catch {
    envelope = null;
  }
  const status = typeof response?.status === "number" ? response.status : null;
  const details = {
    status,
    code: typeof envelope?.code === "string" ? envelope.code : null,
    detail: typeof envelope?.detail === "string" ? envelope.detail : null,
    requestId:
      typeof envelope?.requestId === "string" ? envelope.requestId : null,
  };
  if (status === null || status >= 500 || !details.code) {
    return new DesktopAuthError(
      "server",
      "Zenith Team Desktop authentication request failed",
      details,
    );
  }
  return new DesktopAuthError(
    "rejected",
    details.detail || "Zenith Team Desktop authentication request failed",
    details,
  );
}

// The IPC result for a failed auth call: {ok:false, reason, kind, ...}.
// `reason` is the sentence to show; `kind` lets the renderer route a lost
// session to sign-in instead of a connectivity screen.
function describeAuthFailure(error, fallbackReason) {
  if (!(error instanceof DesktopAuthError)) {
    return { ok: false, reason: fallbackReason, kind: "server" };
  }
  const reference = error.requestId ? ` Reference ${error.requestId}.` : "";
  switch (error.kind) {
    case "network":
      return {
        ok: false,
        kind: "network",
        reason:
          "Zenith Team could not be reached. Check your connection and try again.",
      };
    case "session_missing":
      return {
        ok: false,
        kind: "session_missing",
        reason:
          "Your saved sign-in is no longer on this device. Sign in again.",
      };
    // The renderer words it for its own screen (auth-api-utils.ts); this
    // is the reason for any other caller.
    case "session_not_saved":
      return {
        ok: false,
        kind: "session_not_saved",
        reason: "Can't save sign-in on this computer.",
      };
    case "rejected":
      return {
        ok: false,
        kind: "rejected",
        reason: error.detail || fallbackReason,
        ...(error.code ? { code: error.code } : {}),
        ...(error.status !== null ? { status: error.status } : {}),
      };
    default:
      return {
        ok: false,
        kind: "server",
        reason: `Zenith Team is having trouble right now. Try again in a moment.${reference}`,
        ...(error.status !== null ? { status: error.status } : {}),
      };
  }
}

function parseBearerSession(payload) {
  const user = payload?.user;
  const session = payload?.session;
  const companies = payload?.companies;
  if (
    !user ||
    typeof user !== "object" ||
    !requiredString(user.id) ||
    !requiredString(user.email) ||
    !requiredString(user.displayName) ||
    session?.transport !== "bearer" ||
    !companies ||
    typeof companies !== "object" ||
    !Array.isArray(companies.items)
  ) {
    throw new Error("Zenith Team Desktop authentication response is invalid");
  }
  return {
    user: parseAuthUser(user),
    companies: { items: companies.items },
    session: {
      id: requiredString(session.id),
      issuedAt: requiredString(session.issuedAt),
      accessToken: requiredString(session.accessToken),
      accessExpiresAt: requiredString(session.accessExpiresAt),
      refreshExpiresAt: requiredString(session.refreshExpiresAt),
    },
    refreshToken: requiredString(session.refreshToken),
  };
}

function parsePendingEmailVerification(payload) {
  const attemptsRemaining = Number(payload?.attemptsRemaining);
  const maxAttempts = Number(payload?.maxAttempts);
  if (!Number.isInteger(attemptsRemaining) || !Number.isInteger(maxAttempts)) {
    throw new Error(
      "Zenith Team Desktop email verification response is invalid",
    );
  }
  return {
    result: "pending_verification",
    status: "pending_verification",
    email: requiredString(payload?.email),
    verificationId: requiredString(payload?.verificationId),
    expiresAt: requiredString(payload?.expiresAt),
    resendAvailableAt: requiredString(payload?.resendAvailableAt),
    attemptsRemaining,
    maxAttempts,
  };
}

function parseAuthUser(user) {
  if (!user || typeof user !== "object") {
    throw new Error("Zenith Team Desktop authentication response is invalid");
  }
  return {
    ...user,
    id: requiredString(user.id),
    email: requiredString(user.email),
    displayName: requiredString(user.displayName),
    avatarUrl:
      user.avatarUrl === null ? null : optionalString(user.avatarUrl) || null,
    revision: Number(user.revision),
    createdAt: requiredString(user.createdAt),
  };
}

function requiredString(value) {
  if (typeof value !== "string" || !value.trim()) {
    throw new Error("Desktop authentication payload is invalid");
  }
  return value.trim();
}

function optionalString(value) {
  return typeof value === "string" ? value.trim() : "";
}

function ownerFromAccessToken(token) {
  return accessTokenClaims(token)?.sub ?? null;
}

function sessionMetadataFromAccessToken(token) {
  const claims = accessTokenClaims(token);
  const issuedAt = Number(claims?.iat);
  if (!claims?.sid || !Number.isFinite(issuedAt)) {
    throw new Error("Zenith Team Desktop authentication response is invalid");
  }
  return {
    id: claims.sid,
    issuedAt: new Date(issuedAt * 1000).toISOString(),
  };
}

function accessTokenClaims(token) {
  const segments = String(token).split(".");
  if (segments.length !== 3) return null;
  try {
    const payload = JSON.parse(
      Buffer.from(segments[1], "base64url").toString("utf8"),
    );
    return payload?.tokenUse === "access" &&
      typeof payload.sub === "string" &&
      typeof payload.sid === "string"
      ? payload
      : null;
  } catch {
    return null;
  }
}

// The deployed API can lag behind this desktop build during a rollout, so
// the providers parser accepts the previous response shape as well: the
// current contract is items[] with per-transport flags, the legacy contract
// exposed one enabled flag under providers.google. Callers always receive
// the current shape; the transport-specific flags degrade to the single
// legacy flag.
function normalizeGoogleProviderCapability(payload) {
  const item = Array.isArray(payload?.items)
    ? payload.items.find((entry) => entry?.provider === "google")
    : undefined;
  if (
    typeof item?.enabled === "boolean" &&
    typeof item?.webEnabled === "boolean" &&
    typeof item?.desktopEnabled === "boolean"
  ) {
    return {
      provider: "google",
      enabled: item.enabled,
      webEnabled: item.webEnabled,
      desktopEnabled: item.desktopEnabled,
    };
  }
  const legacy = payload?.providers?.google;
  if (typeof legacy?.enabled === "boolean") {
    return {
      provider: "google",
      enabled: legacy.enabled,
      webEnabled: legacy.enabled,
      desktopEnabled: legacy.enabled,
    };
  }
  return null;
}

function setBeforeDesktopSessionReplace(callback) {
  beforeDesktopSessionReplace =
    typeof callback === "function" ? callback : async () => {};
}

module.exports = {
  DesktopAuthError,
  completeGoogleOAuth: defaultApi.completeGoogleOAuth,
  createDesktopAuthApi,
  describeAuthFailure,
  getProviders: defaultApi.getProviders,
  getEmailVerificationStatus: defaultApi.getEmailVerificationStatus,
  loginWithPassword: defaultApi.loginWithPassword,
  logoutSession: defaultApi.logoutSession,
  refreshSession: defaultApi.refreshSession,
  resendEmailVerification: defaultApi.resendEmailVerification,
  setBeforeDesktopSessionReplace,
  signupWithPassword: defaultApi.signupWithPassword,
  startGoogleOAuth: defaultApi.startGoogleOAuth,
  trustedApiBaseUrl,
  verifyEmail: defaultApi.verifyEmail,
};
