export type CommerceEnvironment = "sandbox" | "live";

export interface Entitlement {
  key: string;
  status: string;
  startsAt?: string;
  expiresAt?: string;
  revokedReason?: string;
  updatedAt: string;
}

export interface CommerceSession {
  assertion: string;
  expiresAt: string;
  entitlements: Entitlement[];
}

export interface CommerceCustomerContext {
  subject: string;
  sessionId: string;
  authenticatedAt: string;
  authenticationMethods: string[];
}

export interface BoardlyCommercePublicJWK {
  kty: "EC";
  crv: "P-256";
  use: "sig";
  alg: "ES256";
  kid: string;
  x: string;
  y: string;
}

export type BoardlyCommerceRequestPath =
  | "/api/v1/site-commerce/checkout-sessions"
  | "/api/v1/site-commerce/portal-sessions"
  | "/api/v1/site-commerce/entitlements/verify";

export interface BoardlyCommerceRequestClaims {
  iss: string;
  aud: "boardly-site-commerce";
  sub: string;
  iat: number;
  nbf: number;
  exp: number;
  jti: string;
  schemaVersion: 1;
  environment: CommerceEnvironment;
  origin: string;
  sessionId: string;
  authenticatedAt: string;
  authenticationMethods: string[];
  action: "checkout" | "portal" | "entitlements";
  method: "POST";
  path: BoardlyCommerceRequestPath;
  operationId: string;
  bodySha256: string;
  nonce?: string;
}

export interface BoardlyCommerceSigner {
  readonly environment: CommerceEnvironment;
  readonly keyId: string;
  readonly publicJwk: BoardlyCommercePublicJWK;
  signRequest(claims: Readonly<BoardlyCommerceRequestClaims>): Promise<string>;
}

export interface BoardlyCommerceConfig {
  apiBaseUrl: string;
  siteOrigin: string;
  siteId: string;
  getCustomerContext(): Promise<CommerceCustomerContext>;
  signer: BoardlyCommerceSigner;
}

export interface BoardlyCommerceClient {
  checkout(offerAlias: string): Promise<{ url: string }>;
  portal(): Promise<{ url: string }>;
  refreshSession(): Promise<CommerceSession>;
  verifySession(assertion: string): Promise<CommerceSession>;
  getEntitlements(): Promise<{ entitlements: Entitlement[] }>;
}

export interface BoardlySiteVerificationProof {
  schemaVersion: 2;
  siteAlias: string;
  manifestDigest: string;
}

export interface BoardlySiteVerificationDocument extends BoardlySiteVerificationProof {
  signingKeys: BoardlyCommercePublicJWK[];
}

type ServerEnvironment = Readonly<Record<string, string | undefined>>;

interface ErrorPayload {
  code?: unknown;
  detail?: unknown;
  requestId?: unknown;
}

export class BoardlyCommerceError extends Error {
  readonly status: number;
  readonly code: string;
  readonly detail: string;
  readonly requestId: string | undefined;
  readonly retryAfter: string | undefined;

  constructor(input: {
    status: number;
    code: string;
    detail: string;
    requestId?: string | undefined;
    retryAfter?: string | undefined;
  }) {
    super(input.detail);
    this.name = "BoardlyCommerceError";
    this.status = input.status;
    this.code = input.code;
    this.detail = input.detail;
    this.requestId = input.requestId;
    this.retryAfter = input.retryAfter;
  }
}

const RETRYABLE_STATUSES = new Set([502, 503, 504]);
const NON_RETRYABLE_COMMERCE_CODES = new Set([
  "commerce_checkout_tax_address_invalid",
  "commerce_provider_request_invalid",
]);
const COMMERCE_SESSION_ISSUER = "boardly-commerce";
const COMMERCE_SESSION_TYPE = "boardly-commerce+jwt";
const SITE_REQUEST_AUDIENCE = "boardly-site-commerce";
const SITE_REQUEST_TYPE = "boardly-site-request+jwt";
const CHALLENGE_PATH = "/api/v1/site-commerce/challenges";
const CHECKOUT_PATH = "/api/v1/site-commerce/checkout-sessions";
const PORTAL_PATH = "/api/v1/site-commerce/portal-sessions";
const ENTITLEMENT_PATH = "/api/v1/site-commerce/entitlements/verify";
const LOCAL_SIGNER_PATH = "/v1/commerce/dev/sign";
const JWK_CACHE_MILLISECONDS = 5 * 60_000;
const CLOCK_SKEW_SECONDS = 30;
const SITE_REQUEST_CLOCK_SKEW_SECONDS = 5;
const SITE_REQUEST_TTL_SECONDS = 55;
const SITE_REQUEST_MAX_TTL_SECONDS = 60;
const PORTAL_AUTHENTICATION_MAX_AGE_MILLISECONDS = 5 * 60_000;
const UPSTREAM_REQUEST_TIMEOUT_MILLISECONDS = 15_000;
const MAX_ASSERTION_BYTES = 64 << 10;
const MAX_JSON_RESPONSE_BYTES = 256 << 10;
const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/u;
const RFC3339_TIMESTAMP_PATTERN =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$/u;

export function createBoardlyCommerce(
  config: BoardlyCommerceConfig,
): BoardlyCommerceClient {
  const apiBaseUrl = normalizeAPIBaseURL(config.apiBaseUrl);
  const siteOrigin = normalizeSiteOrigin(config.siteOrigin);
  const siteId = requireUUID(config.siteId, "siteId");
  const signer = normalizeSigner(config.signer);
  const signerVerificationKey = lazySiteVerificationKey(signer.publicJwk);
  const keyResolver = createVerificationKeyResolver(apiBaseUrl);

  async function currentContext(): Promise<CommerceCustomerContext> {
    return currentCustomerContext(config.getCustomerContext);
  }

  async function signedRequest(
    context: CommerceCustomerContext,
    action: BoardlyCommerceRequestClaims["action"],
    path: BoardlyCommerceRequestPath,
    operationId: string,
    encodedBody: string,
    nonce?: string,
  ): Promise<string> {
    const issuedAt = Math.floor(Date.now() / 1000);
    const jti = createUUIDv7();
    const claims: BoardlyCommerceRequestClaims = {
      iss: siteId,
      aud: SITE_REQUEST_AUDIENCE,
      sub: context.subject,
      iat: issuedAt,
      nbf: issuedAt,
      exp: issuedAt + SITE_REQUEST_TTL_SECONDS,
      jti,
      schemaVersion: 1,
      environment: signer.environment,
      origin: siteOrigin,
      sessionId: context.sessionId,
      authenticatedAt: context.authenticatedAt,
      authenticationMethods: [...context.authenticationMethods],
      action,
      method: "POST",
      path,
      operationId,
      bodySha256: await sha256Hex(encodedBody),
      ...(nonce === undefined ? {} : { nonce }),
    };
    let token: string;
    try {
      token = await signer.signRequest(claims);
    } catch (error) {
      if (error instanceof BoardlyCommerceError) throw error;
      throw signingError();
    }
    await validateSignedRequest(token, claims, signer, signerVerificationKey);
    return token;
  }

  async function requestWithFreshProof<T>(
    context: CommerceCustomerContext,
    action: BoardlyCommerceRequestClaims["action"],
    path: BoardlyCommerceRequestPath,
    operationId: string,
    encodedBody: string,
    parse: (value: unknown) => T,
  ): Promise<T> {
    // A consumed nonce cannot be replayed: each retry uses a fresh challenge
    // and signed JWT while preserving the operation ID and exact body bytes.
    for (let attempt = 0; attempt < 2; attempt += 1) {
      try {
        const challenge =
          action === "entitlements"
            ? undefined
            : await createChallenge(apiBaseUrl, siteId, action, operationId);
        const token = await signedRequest(
          context,
          action,
          path,
          operationId,
          encodedBody,
          challenge?.nonce,
        );
        return await requestSignedJSON(
          apiBaseUrl,
          path,
          token,
          encodedBody,
          parse,
        );
      } catch (error) {
        if (attempt === 0 && retryableCommerceRequest(error)) continue;
        throw error;
      }
    }
    throw networkError();
  }

  async function checkout(offerAlias: string): Promise<{ url: string }> {
    const context = await currentContext();
    const operationId = createUUIDv7();
    const body = JSON.stringify({
      offerAlias: requireNonEmptyString(offerAlias, "offerAlias"),
      operationId,
    });
    return requestWithFreshProof(
      context,
      "checkout",
      CHECKOUT_PATH,
      operationId,
      body,
      parseHostedRedirect,
    );
  }

  async function portal(): Promise<{ url: string }> {
    const context = await currentContext();
    requireRecentPortalAuthentication(context);
    const operationId = createUUIDv7();
    const body = JSON.stringify({ operationId });
    return requestWithFreshProof(
      context,
      "portal",
      PORTAL_PATH,
      operationId,
      body,
      parseHostedRedirect,
    );
  }

  async function refreshSession(): Promise<CommerceSession> {
    const context = await currentContext();
    const operationId = createUUIDv7();
    const body = "{}";
    const envelope = await requestWithFreshProof(
      context,
      "entitlements",
      ENTITLEMENT_PATH,
      operationId,
      body,
      parseEntitlementEnvelope,
    );
    const session = await verifyCommerceSession(
      envelope.assertion,
      siteOrigin,
      siteId,
      signer.environment,
      context,
      true,
      keyResolver,
    );
    if (
      Date.parse(envelope.expiresAt) !== Date.parse(session.expiresAt) ||
      JSON.stringify(envelope.entitlements) !==
        JSON.stringify(session.entitlements)
    ) {
      throw invalidResponse();
    }
    return session;
  }

  async function verifySession(assertion: string): Promise<CommerceSession> {
    return verifyCommerceSession(
      assertion,
      siteOrigin,
      siteId,
      signer.environment,
      await currentContext(),
      false,
      keyResolver,
    );
  }

  return {
    checkout,
    portal,
    refreshSession,
    verifySession,
    async getEntitlements() {
      const session = await refreshSession();
      return { entitlements: session.entitlements };
    },
  };
}

export async function createBoardlyCommerceSignerFromEnvironment(
  environment: ServerEnvironment,
): Promise<BoardlyCommerceSigner> {
  const signerUrl = normalizeLocalSignerURL(
    requiredEnvironment(environment, "BOARDLY_COMMERCE_SIGNER_URL"),
  );
  const capability = requireCredential(
    requiredEnvironment(environment, "BOARDLY_COMMERCE_SIGNER_CAPABILITY"),
  );
  const expectedEnvironment = requireCommerceEnvironment(
    requiredEnvironment(environment, "BOARDLY_COMMERCE_ENVIRONMENT"),
  );
  const expectedKeyId = requireKeyID(
    requiredEnvironment(environment, "BOARDLY_COMMERCE_SIGNER_KEY_ID"),
  );
  const headers = {
    Accept: "application/json",
    Authorization: `BoardlyCommerceLease ${capability}`,
  };
  const metadata = await requestLocalSignerMetadata(signerUrl, headers);
  if (
    metadata.environment !== expectedEnvironment ||
    metadata.keyId !== expectedKeyId
  ) {
    throw signingError();
  }
  const publicJwk: BoardlyCommercePublicJWK = {
    kty: "EC",
    crv: "P-256",
    use: "sig",
    alg: "ES256",
    kid: metadata.keyId,
    x: metadata.x,
    y: metadata.y,
  };
  await validatePublicJWKThumbprint(publicJwk);
  const verificationKey = lazySiteVerificationKey(publicJwk);

  return {
    environment: expectedEnvironment,
    keyId: expectedKeyId,
    publicJwk,
    async signRequest(claims) {
      validateRequestClaims(claims, expectedEnvironment);
      const requestBody = JSON.stringify({
        schemaVersion: 1,
        subject: claims.sub,
        sessionId: claims.sessionId,
        authenticatedAt: claims.authenticatedAt,
        authenticationMethods: claims.authenticationMethods,
        method: claims.method,
        path: claims.path,
        operationId: claims.operationId,
        bodySha256: claims.bodySha256,
        ...(claims.nonce === undefined ? {} : { nonce: claims.nonce }),
      });
      const response = await requestLocalSignature(
        signerUrl,
        headers,
        requestBody,
      );
      if (response.keyId !== expectedKeyId) {
        throw signingError();
      }
      const signedClaims = await validateSignedRequest(
        response.token,
        claims,
        { environment: expectedEnvironment, keyId: expectedKeyId, publicJwk },
        verificationKey,
      );
      if (Date.parse(response.expiresAt) !== signedClaims.exp * 1000) {
        throw signingError();
      }
      return response.token;
    },
  };
}

export async function createBoardlySiteVerificationDocument(
  proof: unknown,
  signer: BoardlyCommerceSigner,
): Promise<BoardlySiteVerificationDocument> {
  const normalizedSigner = normalizeSigner(signer);
  if (
    !isRecord(proof) ||
    !hasExactKeys(proof, ["manifestDigest", "schemaVersion", "siteAlias"]) ||
    proof.schemaVersion !== 2 ||
    !validBoundedString(proof.siteAlias, 128) ||
    typeof proof.manifestDigest !== "string" ||
    !/^[0-9a-f]{64}$/u.test(proof.manifestDigest)
  ) {
    throw new TypeError("Boardly Site verification proof is invalid");
  }
  await validatePublicJWKThumbprint(normalizedSigner.publicJwk);
  return {
    schemaVersion: 2,
    siteAlias: proof.siteAlias,
    manifestDigest: proof.manifestDigest,
    signingKeys: [{ ...normalizedSigner.publicJwk }],
  };
}

export function hasActiveEntitlement(
  session: Pick<CommerceSession, "entitlements">,
  entitlementKey: string,
  now: number = Date.now(),
): boolean {
  const key = requireNonEmptyString(entitlementKey, "entitlementKey");
  return session.entitlements.some(
    (entitlement) =>
      entitlement.key === key &&
      entitlement.status === "active" &&
      (entitlement.expiresAt === undefined ||
        Date.parse(entitlement.expiresAt) > now),
  );
}

async function createChallenge(
  apiBaseUrl: URL,
  siteId: string,
  action: "checkout" | "portal",
  operationId: string,
): Promise<{ nonce: string; expiresAt: string }> {
  const body = JSON.stringify({
    schemaVersion: 1,
    siteId,
    action,
    operationId,
  });
  let response: Response;
  try {
    response = await fetch(
      new URL(CHALLENGE_PATH.slice(1), apiBaseUrl.origin),
      {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        body,
        cache: "no-store",
        credentials: "omit",
        redirect: "error",
        signal: upstreamRequestSignal(),
      },
    );
  } catch {
    throw networkError();
  }
  if (!response.ok) throw await commerceError(response);
  return parseChallenge(await readJSON(response));
}

async function requestSignedJSON<T>(
  apiBaseUrl: URL,
  path: BoardlyCommerceRequestPath,
  token: string,
  encodedBody: string,
  parse: (value: unknown) => T,
): Promise<T> {
  const headers = {
    Accept: "application/json",
    Authorization: `BoardlySite ${token}`,
    "Content-Type": "application/json",
  };
  let response: Response;
  try {
    response = await fetch(new URL(path.slice(1), apiBaseUrl.origin), {
      method: "POST",
      headers,
      body: encodedBody,
      cache: "no-store",
      credentials: "omit",
      redirect: "error",
      signal: upstreamRequestSignal(),
    });
  } catch (error) {
    throw networkError(error);
  }
  if (!response.ok) throw await commerceError(response);
  return parse(await readJSON(response));
}

function retryableCommerceRequest(error: unknown): boolean {
  if (!(error instanceof BoardlyCommerceError)) return false;
  if (NON_RETRYABLE_COMMERCE_CODES.has(error.code)) return false;
  return error.status === 0 || RETRYABLE_STATUSES.has(error.status);
}

async function requestLocalSignerMetadata(
  signerUrl: URL,
  headers: Record<string, string>,
): Promise<{
  environment: CommerceEnvironment;
  keyId: string;
  x: string;
  y: string;
}> {
  let response: Response;
  try {
    response = await fetch(signerUrl, {
      method: "GET",
      headers,
      cache: "no-store",
      credentials: "omit",
      redirect: "error",
      signal: upstreamRequestSignal(),
    });
  } catch {
    throw signingUnavailable();
  }
  if (!response.ok) throw signingError(response.status);
  const value = await readJSON(response);
  if (
    !isRecord(value) ||
    !hasExactKeys(value, [
      "environment",
      "keyId",
      "publicJwk",
      "schemaVersion",
    ]) ||
    value.schemaVersion !== 1 ||
    (value.environment !== "sandbox" && value.environment !== "live") ||
    typeof value.keyId !== "string" ||
    !isRecord(value.publicJwk) ||
    !hasExactKeys(value.publicJwk, ["crv", "kty", "x", "y"]) ||
    value.publicJwk.kty !== "EC" ||
    value.publicJwk.crv !== "P-256" ||
    typeof value.publicJwk.x !== "string" ||
    typeof value.publicJwk.y !== "string"
  ) {
    throw signingError();
  }
  return {
    environment: value.environment,
    keyId: requireKeyID(value.keyId),
    x: requireP256Coordinate(value.publicJwk.x),
    y: requireP256Coordinate(value.publicJwk.y),
  };
}

async function requestLocalSignature(
  signerUrl: URL,
  headers: Record<string, string>,
  body: string,
): Promise<{ token: string; expiresAt: string; keyId: string }> {
  let response: Response;
  try {
    response = await fetch(signerUrl, {
      method: "POST",
      headers: { ...headers, "Content-Type": "application/json" },
      body,
      cache: "no-store",
      credentials: "omit",
      redirect: "error",
      signal: upstreamRequestSignal(),
    });
  } catch {
    throw signingUnavailable();
  }
  if (!response.ok) throw signingError(response.status);
  const value = await readJSON(response);
  if (
    !isRecord(value) ||
    !hasExactKeys(value, ["expiresAt", "keyId", "schemaVersion", "token"]) ||
    value.schemaVersion !== 1 ||
    typeof value.token !== "string" ||
    typeof value.expiresAt !== "string" ||
    typeof value.keyId !== "string"
  ) {
    throw signingError();
  }
  return {
    token: requireJWT(value.token),
    expiresAt: requireTimestamp(value.expiresAt),
    keyId: requireKeyID(value.keyId),
  };
}

async function validateSignedRequest(
  token: string,
  expectedClaims: Readonly<BoardlyCommerceRequestClaims>,
  signer: Pick<BoardlyCommerceSigner, "environment" | "keyId" | "publicJwk">,
  verificationKey: () => Promise<CryptoKey>,
): Promise<BoardlyCommerceRequestClaims> {
  validateRequestClaims(expectedClaims, signer.environment);
  const jwt = parseJWT(token, signingError);
  if (
    !hasExactKeys(jwt.header, ["alg", "kid", "typ"]) ||
    jwt.header.alg !== "ES256" ||
    jwt.header.typ !== SITE_REQUEST_TYPE ||
    jwt.header.kid !== signer.keyId ||
    jwt.signature.length !== 64
  ) {
    throw signingError();
  }
  const signedClaims = normalizeSignedRequestClaims(jwt.claims);
  validateRequestClaims(signedClaims, signer.environment);
  if (!sameSignedRequestBinding(signedClaims, expectedClaims))
    throw signingError();
  let valid = false;
  try {
    valid = await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      await verificationKey(),
      jwt.signature,
      jwt.signingInput,
    );
  } catch {
    throw signingError();
  }
  if (!valid) throw signingError();
  return signedClaims;
}

function normalizeSignedRequestClaims(
  claims: Record<string, unknown>,
): BoardlyCommerceRequestClaims {
  return {
    ...claims,
    aud: requireSingleAudience(claims.aud, signingError),
  } as unknown as BoardlyCommerceRequestClaims;
}

function validateRequestClaims(
  claims: Readonly<BoardlyCommerceRequestClaims>,
  environment: CommerceEnvironment,
): void {
  const expectedKeys = [
    "action",
    "aud",
    "authenticatedAt",
    "authenticationMethods",
    "bodySha256",
    "environment",
    "exp",
    "iat",
    "iss",
    "jti",
    "method",
    "nbf",
    "operationId",
    "origin",
    "path",
    "schemaVersion",
    "sessionId",
    "sub",
    ...(claims.nonce === undefined ? [] : ["nonce"]),
  ];
  const nonceRequired =
    claims.action === "checkout" || claims.action === "portal";
  const lifetime = claims.exp - claims.iat;
  const now = Math.floor(Date.now() / 1000);
  const remainingLifetime = claims.exp - now;
  if (
    !isRecord(claims) ||
    !hasExactKeys(claims, expectedKeys) ||
    claims.schemaVersion !== 1 ||
    claims.aud !== SITE_REQUEST_AUDIENCE ||
    claims.environment !== environment ||
    !isUUID(claims.iss) ||
    !validBoundedString(claims.sub, 255) ||
    !validBoundedString(claims.sessionId, 255) ||
    !validTimestamp(claims.authenticatedAt) ||
    Date.parse(claims.authenticatedAt) >
      Date.now() + SITE_REQUEST_CLOCK_SKEW_SECONDS * 1000 ||
    !validAuthenticationMethods(claims.authenticationMethods) ||
    (claims.action !== "checkout" &&
      claims.action !== "portal" &&
      claims.action !== "entitlements") ||
    claims.method !== "POST" ||
    !isUUID(claims.jti) ||
    !isUUID(claims.operationId) ||
    claims.nbf !== claims.iat ||
    !Number.isSafeInteger(claims.iat) ||
    !Number.isSafeInteger(claims.nbf) ||
    !Number.isSafeInteger(claims.exp) ||
    lifetime <= 0 ||
    lifetime > SITE_REQUEST_MAX_TTL_SECONDS ||
    claims.iat > now + SITE_REQUEST_CLOCK_SKEW_SECONDS ||
    remainingLifetime <= 0 ||
    remainingLifetime > SITE_REQUEST_MAX_TTL_SECONDS ||
    claims.path !== actionPath(claims.action) ||
    !/^[0-9a-f]{64}$/u.test(claims.bodySha256) ||
    nonceRequired !== (claims.nonce !== undefined) ||
    (claims.nonce !== undefined && !validOpaqueValue(claims.nonce))
  ) {
    throw signingError();
  }
}

function sameSignedRequestBinding(
  signed: Readonly<BoardlyCommerceRequestClaims>,
  expected: Readonly<BoardlyCommerceRequestClaims>,
): boolean {
  return (
    signed.iss === expected.iss &&
    signed.aud === expected.aud &&
    signed.sub === expected.sub &&
    signed.schemaVersion === expected.schemaVersion &&
    signed.environment === expected.environment &&
    signed.origin === expected.origin &&
    signed.sessionId === expected.sessionId &&
    signed.authenticatedAt === expected.authenticatedAt &&
    sameJSONValue(
      signed.authenticationMethods,
      expected.authenticationMethods,
    ) &&
    signed.action === expected.action &&
    signed.method === expected.method &&
    signed.path === expected.path &&
    signed.operationId === expected.operationId &&
    signed.bodySha256 === expected.bodySha256 &&
    signed.nonce === expected.nonce
  );
}

function actionPath(action: BoardlyCommerceRequestClaims["action"]): string {
  if (action === "checkout") return CHECKOUT_PATH;
  if (action === "portal") return PORTAL_PATH;
  return ENTITLEMENT_PATH;
}

interface ParsedAssertion {
  assertion: string;
  signingInput: Uint8Array;
  signature: Uint8Array;
  header: { kid: string };
  claims: {
    audience: string;
    subject: string;
    siteId: string;
    environment: CommerceEnvironment;
    sessionId: string;
    expiresAt: number;
    issuedAt: number;
    notBefore: number;
    entitlements: Entitlement[];
  };
}

type VerificationKeyResolver = (
  keyId: string,
  refreshUnknown: boolean,
) => Promise<CryptoKey>;

async function verifyCommerceSession(
  assertion: string,
  siteOrigin: string,
  siteId: string,
  environment: CommerceEnvironment,
  context: CommerceCustomerContext,
  refreshUnknownKey: boolean,
  resolveKey: VerificationKeyResolver,
): Promise<CommerceSession> {
  const parsed = parseAssertion(assertion);
  const now = Math.floor(Date.now() / 1000);
  const claims = parsed.claims;
  if (
    claims.audience !== siteOrigin ||
    claims.siteId !== siteId ||
    claims.environment !== environment ||
    claims.subject !== context.subject ||
    claims.sessionId !== context.sessionId ||
    claims.notBefore > now + CLOCK_SKEW_SECONDS ||
    claims.issuedAt > now + CLOCK_SKEW_SECONDS ||
    claims.expiresAt <= now ||
    claims.notBefore !== claims.issuedAt ||
    claims.expiresAt <= claims.issuedAt ||
    claims.expiresAt - claims.issuedAt > 5 * 60
  ) {
    throw invalidAssertion();
  }
  let key: CryptoKey;
  try {
    key = await resolveKey(parsed.header.kid, refreshUnknownKey);
  } catch (error) {
    if (error instanceof BoardlyCommerceError) throw error;
    throw invalidAssertion();
  }
  let valid = false;
  try {
    valid = await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      key,
      parsed.signature,
      parsed.signingInput,
    );
  } catch {
    throw invalidAssertion();
  }
  if (!valid) throw invalidAssertion();
  return {
    assertion: parsed.assertion,
    expiresAt: new Date(claims.expiresAt * 1000).toISOString(),
    entitlements: claims.entitlements,
  };
}

function parseAssertion(value: string): ParsedAssertion {
  const jwt = parseJWT(requireAssertion(value), invalidAssertion);
  const audience = requireSingleAudience(jwt.claims.aud, invalidAssertion);
  if (
    !hasExactKeys(jwt.header, ["alg", "kid", "typ"]) ||
    jwt.header.alg !== "ES256" ||
    jwt.header.typ !== COMMERCE_SESSION_TYPE ||
    typeof jwt.header.kid !== "string" ||
    !validBoundedString(jwt.header.kid, 128) ||
    !hasExactKeys(jwt.claims, [
      "aud",
      "entitlements",
      "environment",
      "exp",
      "iat",
      "iss",
      "jti",
      "nbf",
      "schemaVersion",
      "sessionId",
      "siteId",
      "sub",
    ]) ||
    jwt.claims.schemaVersion !== 1 ||
    jwt.claims.iss !== COMMERCE_SESSION_ISSUER ||
    (jwt.claims.environment !== "sandbox" &&
      jwt.claims.environment !== "live") ||
    typeof jwt.claims.sub !== "string" ||
    typeof jwt.claims.sessionId !== "string" ||
    typeof jwt.claims.siteId !== "string" ||
    !isUUID(jwt.claims.siteId) ||
    typeof jwt.claims.jti !== "string" ||
    !isUUIDv7(jwt.claims.jti) ||
    !Number.isSafeInteger(jwt.claims.iat) ||
    !Number.isSafeInteger(jwt.claims.nbf) ||
    !Number.isSafeInteger(jwt.claims.exp) ||
    !Array.isArray(jwt.claims.entitlements) ||
    jwt.signature.length !== 64
  ) {
    throw invalidAssertion();
  }
  return {
    assertion: value,
    signingInput: jwt.signingInput,
    signature: jwt.signature,
    header: { kid: jwt.header.kid },
    claims: {
      audience,
      subject: jwt.claims.sub,
      siteId: jwt.claims.siteId,
      environment: jwt.claims.environment,
      sessionId: jwt.claims.sessionId,
      issuedAt: jwt.claims.iat as number,
      notBefore: jwt.claims.nbf as number,
      expiresAt: jwt.claims.exp as number,
      entitlements: jwt.claims.entitlements.map(parseEntitlement),
    },
  };
}

function requireSingleAudience(
  value: unknown,
  errorFactory: () => BoardlyCommerceError,
): string {
  if (typeof value === "string" && validBoundedString(value, 2048))
    return value;
  if (
    Array.isArray(value) &&
    value.length === 1 &&
    typeof value[0] === "string" &&
    validBoundedString(value[0], 2048)
  ) {
    return value[0];
  }
  throw errorFactory();
}

function createVerificationKeyResolver(
  apiBaseUrl: URL,
): VerificationKeyResolver {
  let cache: { expiresAt: number; keys: Map<string, CryptoKey> } | undefined;
  let loading: Promise<Map<string, CryptoKey>> | undefined;

  async function loadKeys(): Promise<Map<string, CryptoKey>> {
    const response = await fetch(new URL("site-commerce/jwks", apiBaseUrl), {
      method: "GET",
      headers: { Accept: "application/json" },
      cache: "no-store",
      credentials: "omit",
      redirect: "error",
      signal: upstreamRequestSignal(),
    }).catch(() => {
      throw keyUnavailable();
    });
    if (!response.ok) {
      await response.body?.cancel();
      throw keyUnavailable(
        response.status,
        optionalString(response.headers.get("X-Request-ID")),
        optionalString(response.headers.get("Retry-After")),
      );
    }
    const value = await readJSON(response);
    if (
      !isRecord(value) ||
      !hasExactKeys(value, ["keys"]) ||
      !Array.isArray(value.keys) ||
      value.keys.length === 0 ||
      value.keys.length > 8
    ) {
      throw invalidResponse();
    }
    const keys = new Map<string, CryptoKey>();
    for (const candidate of value.keys) {
      if (
        !isRecord(candidate) ||
        !hasExactKeys(candidate, [
          "alg",
          "crv",
          "kid",
          "kty",
          "use",
          "x",
          "y",
        ]) ||
        candidate.kty !== "EC" ||
        candidate.use !== "sig" ||
        candidate.crv !== "P-256" ||
        candidate.alg !== "ES256" ||
        typeof candidate.kid !== "string" ||
        typeof candidate.x !== "string" ||
        typeof candidate.y !== "string"
      ) {
        throw invalidResponse();
      }
      const imported = await crypto.subtle
        .importKey(
          "jwk",
          candidate as JsonWebKey,
          { name: "ECDSA", namedCurve: "P-256" },
          false,
          ["verify"],
        )
        .catch(() => {
          throw invalidResponse();
        });
      if (keys.has(candidate.kid)) throw invalidResponse();
      keys.set(candidate.kid, imported);
    }
    return keys;
  }

  async function currentKeys(
    forceRefresh: boolean,
  ): Promise<Map<string, CryptoKey>> {
    if (!forceRefresh && cache !== undefined && cache.expiresAt > Date.now()) {
      return cache.keys;
    }
    loading ??= loadKeys();
    try {
      const keys = await loading;
      cache = { keys, expiresAt: Date.now() + JWK_CACHE_MILLISECONDS };
      return keys;
    } finally {
      loading = undefined;
    }
  }

  return async (keyId: string, refreshUnknown: boolean): Promise<CryptoKey> => {
    const cached = await currentKeys(false);
    const existing = cached.get(keyId);
    if (existing !== undefined) return existing;
    if (!refreshUnknown) throw invalidAssertion();
    const refreshed = await currentKeys(true);
    const resolved = refreshed.get(keyId);
    if (resolved === undefined) throw invalidAssertion();
    return resolved;
  };
}

async function currentCustomerContext(
  getCustomerContext: () => Promise<CommerceCustomerContext>,
): Promise<CommerceCustomerContext> {
  let value: CommerceCustomerContext;
  try {
    value = await getCustomerContext();
  } catch {
    throw authUnavailable();
  }
  if (
    !isRecord(value) ||
    !hasExactKeys(value, [
      "authenticatedAt",
      "authenticationMethods",
      "sessionId",
      "subject",
    ]) ||
    !validBoundedString(value.subject, 255) ||
    !validBoundedString(value.sessionId, 255) ||
    !validTimestamp(value.authenticatedAt) ||
    Date.parse(value.authenticatedAt) >
      Date.now() + SITE_REQUEST_CLOCK_SKEW_SECONDS * 1000 ||
    !validAuthenticationMethods(value.authenticationMethods)
  ) {
    throw authUnavailable();
  }
  return {
    subject: value.subject,
    sessionId: value.sessionId,
    authenticatedAt: value.authenticatedAt,
    authenticationMethods: [...value.authenticationMethods],
  };
}

function parseChallenge(value: unknown): { nonce: string; expiresAt: string } {
  if (
    !isRecord(value) ||
    !hasExactKeys(value, ["expiresAt", "nonce", "schemaVersion"]) ||
    value.schemaVersion !== 1 ||
    typeof value.nonce !== "string" ||
    !validOpaqueValue(value.nonce) ||
    typeof value.expiresAt !== "string"
  ) {
    throw invalidResponse();
  }
  const expiresAt = requireTimestamp(value.expiresAt);
  const expiry = Date.parse(expiresAt);
  const now = Date.now();
  if (expiry <= now + 5_000) throw challengeExpired();
  if (expiry > now + 90_000) {
    throw invalidResponse();
  }
  return { nonce: value.nonce, expiresAt };
}

function parseHostedRedirect(value: unknown): { url: string } {
  if (
    !isRecord(value) ||
    !hasExactKeys(value, ["url"]) ||
    typeof value.url !== "string"
  ) {
    throw invalidResponse();
  }
  let url: URL;
  try {
    url = new URL(value.url);
  } catch {
    throw invalidResponse();
  }
  if (url.protocol !== "https:" || url.username !== "" || url.password !== "") {
    throw invalidResponse();
  }
  return { url: url.toString() };
}

function parseEntitlementEnvelope(value: unknown): CommerceSession {
  if (
    !isRecord(value) ||
    !hasExactKeys(value, ["assertion", "entitlements", "expiresAt"]) ||
    typeof value.assertion !== "string" ||
    typeof value.expiresAt !== "string" ||
    !Array.isArray(value.entitlements)
  ) {
    throw invalidResponse();
  }
  return {
    assertion: requireAssertion(value.assertion),
    expiresAt: requireTimestamp(value.expiresAt),
    entitlements: value.entitlements.map(parseEntitlement),
  };
}

function parseEntitlement(value: unknown): Entitlement {
  if (!isRecord(value)) throw invalidResponse();
  const allowed = [
    "expiresAt",
    "key",
    "revokedReason",
    "startsAt",
    "status",
    "updatedAt",
  ];
  if (Object.keys(value).some((key) => !allowed.includes(key)))
    throw invalidResponse();
  const status = requireResponseString(value.status);
  if (status !== "processing" && status !== "active" && status !== "denied") {
    throw invalidResponse();
  }
  const entitlement: Entitlement = {
    key: requireResponseString(value.key),
    status,
    updatedAt: requireTimestamp(value.updatedAt),
  };
  const startsAt = optionalTimestamp(value.startsAt);
  const expiresAt = optionalTimestamp(value.expiresAt);
  const revokedReason = optionalString(value.revokedReason);
  if (startsAt !== undefined) entitlement.startsAt = startsAt;
  if (expiresAt !== undefined) entitlement.expiresAt = expiresAt;
  if (revokedReason !== undefined) entitlement.revokedReason = revokedReason;
  return entitlement;
}

async function commerceError(
  response: Response,
): Promise<BoardlyCommerceError> {
  let payload: ErrorPayload = {};
  try {
    const value = await readJSON(response);
    if (isRecord(value)) payload = value;
  } catch {
    // The typed fallback intentionally excludes response bodies.
  }
  return new BoardlyCommerceError({
    status: response.status,
    code: optionalString(payload.code) ?? "commerce_request_failed",
    detail: optionalString(payload.detail) ?? "Boardly Commerce request failed",
    requestId:
      optionalString(payload.requestId) ??
      optionalString(response.headers.get("X-Request-ID")),
    retryAfter: optionalString(response.headers.get("Retry-After")),
  });
}

async function readJSON(response: Response): Promise<unknown> {
  try {
    const declaredLength = response.headers.get("Content-Length");
    if (
      declaredLength !== null &&
      /^\d+$/u.test(declaredLength) &&
      Number(declaredLength) > MAX_JSON_RESPONSE_BYTES
    ) {
      await response.body?.cancel();
      throw new TypeError("response body is too large");
    }
    if (response.body === null) throw new TypeError("response body is missing");
    const reader = response.body.getReader();
    const chunks: Uint8Array[] = [];
    let length = 0;
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        length += value.byteLength;
        if (length > MAX_JSON_RESPONSE_BYTES) {
          await reader.cancel();
          throw new TypeError("response body is too large");
        }
        chunks.push(value);
      }
    } finally {
      reader.releaseLock();
    }
    const bytes = new Uint8Array(length);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return JSON.parse(
      new TextDecoder("utf-8", { fatal: true }).decode(bytes),
    ) as unknown;
  } catch {
    throw invalidResponse(
      response.ok ? 502 : response.status,
      optionalString(response.headers.get("X-Request-ID")),
    );
  }
}

function upstreamRequestSignal(): AbortSignal {
  return AbortSignal.timeout(UPSTREAM_REQUEST_TIMEOUT_MILLISECONDS);
}

function normalizeSigner(signer: BoardlyCommerceSigner): BoardlyCommerceSigner {
  if (
    !isRecord(signer) ||
    (signer.environment !== "sandbox" && signer.environment !== "live") ||
    typeof signer.keyId !== "string" ||
    typeof signer.signRequest !== "function" ||
    !isRecord(signer.publicJwk)
  ) {
    throw new TypeError("Boardly Commerce signer is invalid");
  }
  requireKeyID(signer.keyId);
  const publicJwk = signer.publicJwk;
  if (
    !hasExactKeys(publicJwk, ["alg", "crv", "kid", "kty", "use", "x", "y"]) ||
    publicJwk.kty !== "EC" ||
    publicJwk.crv !== "P-256" ||
    publicJwk.use !== "sig" ||
    publicJwk.alg !== "ES256" ||
    publicJwk.kid !== signer.keyId
  ) {
    throw new TypeError("Boardly Commerce signer public JWK is invalid");
  }
  requireP256Coordinate(publicJwk.x);
  requireP256Coordinate(publicJwk.y);
  return signer;
}

async function validatePublicJWKThumbprint(
  jwk: BoardlyCommercePublicJWK,
): Promise<void> {
  const canonical = JSON.stringify({
    crv: jwk.crv,
    kty: jwk.kty,
    x: jwk.x,
    y: jwk.y,
  });
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(canonical),
  );
  if (encodeBase64URL(new Uint8Array(digest)) !== jwk.kid) {
    throw new TypeError(
      "Boardly Commerce signer key ID is not its RFC 7638 thumbprint",
    );
  }
}

async function importSiteVerificationKey(
  jwk: BoardlyCommercePublicJWK,
): Promise<CryptoKey> {
  await validatePublicJWKThumbprint(jwk);
  return crypto.subtle.importKey(
    "jwk",
    jwk,
    { name: "ECDSA", namedCurve: "P-256" },
    false,
    ["verify"],
  );
}

function lazySiteVerificationKey(
  jwk: BoardlyCommercePublicJWK,
): () => Promise<CryptoKey> {
  let verificationKey: Promise<CryptoKey> | undefined;
  return () => {
    verificationKey ??= importSiteVerificationKey(jwk);
    return verificationKey;
  };
}

function normalizeAPIBaseURL(value: string): URL {
  const raw = requireNonEmptyString(value, "apiBaseUrl");
  let url: URL;
  try {
    url = new URL(raw.endsWith("/") ? raw : `${raw}/`);
  } catch {
    throw new TypeError("apiBaseUrl must be an absolute URL");
  }
  if (
    !safeServerURL(url) ||
    url.pathname !== "/api/v1/" ||
    url.search !== "" ||
    url.hash !== ""
  ) {
    throw new TypeError(
      "apiBaseUrl must be an exact HTTPS or loopback /api/v1/ URL",
    );
  }
  return url;
}

function normalizeSiteOrigin(value: string): string {
  const raw = requireNonEmptyString(value, "siteOrigin");
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new TypeError(
      "siteOrigin must be an absolute HTTPS or loopback HTTP origin",
    );
  }
  if (
    !safeServerURL(url) ||
    url.pathname !== "/" ||
    url.search !== "" ||
    url.hash !== "" ||
    url.origin !== raw
  ) {
    throw new TypeError(
      "siteOrigin must be an exact HTTPS or loopback HTTP origin",
    );
  }
  return url.origin;
}

function normalizeLocalSignerURL(value: string): URL {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new TypeError("BOARDLY_COMMERCE_SIGNER_URL is invalid");
  }
  if (
    url.protocol !== "http:" ||
    !isLoopbackHostname(url.hostname) ||
    url.pathname !== LOCAL_SIGNER_PATH ||
    url.username !== "" ||
    url.password !== "" ||
    url.search !== "" ||
    url.hash !== ""
  ) {
    throw new TypeError(
      "BOARDLY_COMMERCE_SIGNER_URL must be the exact daemon loopback signer URL",
    );
  }
  return url;
}

function safeServerURL(url: URL): boolean {
  const loopbackHTTP =
    url.protocol === "http:" && isLoopbackHostname(url.hostname);
  return (
    (url.protocol === "https:" || loopbackHTTP) &&
    url.username === "" &&
    url.password === ""
  );
}

function isLoopbackHostname(hostname: string): boolean {
  if (
    hostname === "localhost" ||
    hostname.endsWith(".localhost") ||
    hostname === "[::1]"
  ) {
    return true;
  }
  const octets = hostname.split(".");
  return (
    octets.length === 4 &&
    octets[0] === "127" &&
    octets.every(
      (octet) => /^(0|[1-9][0-9]{0,2})$/u.test(octet) && Number(octet) <= 255,
    )
  );
}

function parseJWT(
  value: string,
  errorFactory: () => BoardlyCommerceError,
): {
  header: Record<string, unknown>;
  claims: Record<string, unknown>;
  signingInput: Uint8Array;
  signature: Uint8Array;
} {
  try {
    const segments = value.split(".");
    if (segments.length !== 3) throw new TypeError("JWT segment count");
    const [header, claims, signature] = segments;
    if (
      header === undefined ||
      claims === undefined ||
      signature === undefined
    ) {
      throw new TypeError("JWT segment missing");
    }
    return {
      header: decodeJSONObject(header),
      claims: decodeJSONObject(claims),
      signingInput: new TextEncoder().encode(`${header}.${claims}`),
      signature: decodeBase64URL(signature),
    };
  } catch {
    throw errorFactory();
  }
}

function decodeJSONObject(segment: string): Record<string, unknown> {
  const value: unknown = JSON.parse(
    new TextDecoder().decode(decodeBase64URL(segment)),
  );
  if (!isRecord(value)) throw new TypeError("JWT value is not an object");
  return value;
}

function decodeBase64URL(segment: string): Uint8Array {
  if (segment === "" || !/^[A-Za-z0-9_-]+$/u.test(segment)) {
    throw new TypeError("base64url value is invalid");
  }
  const standard = segment.replaceAll("-", "+").replaceAll("_", "/");
  const padding = "=".repeat((4 - (standard.length % 4)) % 4);
  const decoded = Uint8Array.from(atob(standard + padding), (character) =>
    character.charCodeAt(0),
  );
  if (encodeBase64URL(decoded) !== segment)
    throw new TypeError("base64url is not canonical");
  return decoded;
}

function encodeBase64URL(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) binary += String.fromCharCode(byte);
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/u, "");
}

async function sha256Hex(value: string): Promise<string> {
  const digest = new Uint8Array(
    await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value)),
  );
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join(
    "",
  );
}

function hasExactKeys(
  value: Record<string, unknown>,
  keys: readonly string[],
): boolean {
  return Object.keys(value).sort().join(",") === [...keys].sort().join(",");
}

function sameJSONValue(actual: unknown, expected: unknown): boolean {
  if (actual === expected) return true;
  if (Array.isArray(actual) && Array.isArray(expected)) {
    return (
      actual.length === expected.length &&
      actual.every((value, index) => sameJSONValue(value, expected[index]))
    );
  }
  if (isRecord(actual) && isRecord(expected)) {
    const actualKeys = Object.keys(actual).sort();
    const expectedKeys = Object.keys(expected).sort();
    return (
      actualKeys.join(",") === expectedKeys.join(",") &&
      actualKeys.every((key) => sameJSONValue(actual[key], expected[key]))
    );
  }
  return false;
}

function validAuthenticationMethods(value: unknown): value is string[] {
  return (
    Array.isArray(value) &&
    value.length >= 1 &&
    value.length <= 16 &&
    new Set(value).size === value.length &&
    value.every((method) => validBoundedString(method, 64))
  );
}

function validBoundedString(value: unknown, maximum: number): value is string {
  return (
    typeof value === "string" &&
    value.length >= 1 &&
    new TextEncoder().encode(value).byteLength <= maximum &&
    value.trim() === value &&
    !/\p{Cc}/u.test(value) &&
    !hasUnpairedSurrogate(value)
  );
}

function validTimestamp(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const match = RFC3339_TIMESTAMP_PATTERN.exec(value);
  if (match === null) return false;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  const offsetHour = match[7] === undefined ? 0 : Number(match[7]);
  const offsetMinute = match[8] === undefined ? 0 : Number(match[8]);
  return (
    month >= 1 &&
    month <= 12 &&
    day >= 1 &&
    day <= daysInMonth(year, month) &&
    hour <= 23 &&
    minute <= 59 &&
    second <= 59 &&
    offsetHour <= 23 &&
    offsetMinute <= 59 &&
    Number.isFinite(Date.parse(value))
  );
}

function daysInMonth(year: number, month: number): number {
  if (month === 2) {
    const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    return leapYear ? 29 : 28;
  }
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
}

function hasUnpairedSurrogate(value: string): boolean {
  for (const character of value) {
    const codePoint = character.codePointAt(0);
    if (codePoint !== undefined && codePoint >= 0xd800 && codePoint <= 0xdfff)
      return true;
  }
  return false;
}

function validOpaqueValue(value: string): boolean {
  try {
    return (
      /^[A-Za-z0-9_-]{43}$/u.test(value) && decodeBase64URL(value).length === 32
    );
  } catch {
    return false;
  }
}

function requireP256Coordinate(value: string): string {
  try {
    if (
      /^[A-Za-z0-9_-]{43}$/u.test(value) &&
      decodeBase64URL(value).length === 32
    ) {
      return value;
    }
  } catch {
    // Fall through to the typed configuration error.
  }
  throw new TypeError(
    "Boardly Commerce P-256 public key coordinate is invalid",
  );
}

function requireKeyID(value: string): string {
  return requireP256Coordinate(value);
}

function requireCredential(value: string): string {
  if (!validBoundedString(value, 4096)) {
    throw new TypeError("BOARDLY_COMMERCE_SIGNER_CAPABILITY is invalid");
  }
  return value;
}

function requireCommerceEnvironment(value: string): CommerceEnvironment {
  if (value !== "sandbox" && value !== "live") {
    throw new TypeError("BOARDLY_COMMERCE_ENVIRONMENT must be sandbox or live");
  }
  return value;
}

function requiredEnvironment(
  environment: ServerEnvironment,
  name: string,
): string {
  const value = environment[name];
  if (typeof value !== "string" || value === "") {
    throw new TypeError(`${name} is required`);
  }
  return value;
}

function requireUUID(value: string, name: string): string {
  if (!isUUID(value)) throw new TypeError(`${name} must be a canonical UUID`);
  return value;
}

function isUUID(value: unknown): value is string {
  return (
    typeof value === "string" &&
    UUID_PATTERN.test(value) &&
    value !== "00000000-0000-0000-0000-000000000000"
  );
}

function isUUIDv7(value: string): boolean {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
    value,
  );
}

function createUUIDv7(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  let timestamp = Date.now();
  for (let index = 5; index >= 0; index -= 1) {
    bytes[index] = timestamp & 0xff;
    timestamp = Math.floor(timestamp / 256);
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x70;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

function requireJWT(value: string): string {
  if (
    typeof value !== "string" ||
    value === "" ||
    value.length > MAX_ASSERTION_BYTES ||
    value.trim() !== value ||
    value.includes("\r") ||
    value.includes("\n")
  ) {
    throw signingError();
  }
  return value;
}

function requireAssertion(value: string): string {
  if (
    typeof value !== "string" ||
    value === "" ||
    value.length > MAX_ASSERTION_BYTES ||
    value.trim() !== value ||
    value.includes("\r") ||
    value.includes("\n")
  ) {
    throw invalidAssertion();
  }
  return value;
}

function requireNonEmptyString(value: string, name: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new TypeError(`${name} is required`);
  }
  return value;
}

function requireResponseString(value: unknown): string {
  const result = optionalString(value);
  if (result === undefined) throw invalidResponse();
  return result;
}

function requireTimestamp(value: unknown): string {
  const result = requireResponseString(value);
  if (!validTimestamp(result)) throw invalidResponse();
  return result;
}

function optionalTimestamp(value: unknown): string | undefined {
  if (value === undefined) return undefined;
  return requireTimestamp(value);
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function authUnavailable(): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status: 401,
    code: "commerce_auth_unavailable",
    detail: "A current authenticated customer session is required",
  });
}

function requireRecentPortalAuthentication(
  context: CommerceCustomerContext,
): void {
  if (
    Date.now() - Date.parse(context.authenticatedAt) >
    PORTAL_AUTHENTICATION_MAX_AGE_MILLISECONDS
  ) {
    throw reauthenticationRequired();
  }
}

function reauthenticationRequired(): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status: 401,
    code: "commerce_reauthentication_required",
    detail:
      "Recent customer authentication is required to open billing settings",
  });
}

function signingError(status: number = 502): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status,
    code: "commerce_signing_failed",
    detail: "The Boardly Commerce request could not be signed",
  });
}

function signingUnavailable(): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status: 503,
    code: "commerce_signing_unavailable",
    detail: "The local Boardly Commerce signer is unavailable",
  });
}

function networkError(_cause?: unknown): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status: 0,
    code: "commerce_network_error",
    detail: "Boardly Commerce is temporarily unreachable",
  });
}

function challengeExpired(): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status: 503,
    code: "commerce_challenge_expired",
    detail: "The Boardly Commerce challenge expired before it could be used",
  });
}

function keyUnavailable(
  status: number = 0,
  requestId?: string,
  retryAfter?: string,
): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status,
    code: "commerce_key_unavailable",
    detail: "Boardly Commerce verification key is temporarily unavailable",
    requestId,
    retryAfter,
  });
}

function invalidAssertion(): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status: 401,
    code: "commerce_invalid_assertion",
    detail: "The Boardly Commerce session is invalid or expired",
  });
}

function invalidResponse(
  status: number = 502,
  requestId?: string,
): BoardlyCommerceError {
  return new BoardlyCommerceError({
    status,
    code: "commerce_invalid_response",
    detail: "Boardly Commerce returned an invalid response",
    requestId,
  });
}
