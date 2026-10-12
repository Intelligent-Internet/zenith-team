import { createHash, webcrypto } from "node:crypto";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  BoardlyCommerceError,
  type BoardlyCommerceRequestClaims,
  type BoardlyCommerceSigner,
  type CommerceCustomerContext,
  createBoardlyCommerce,
  createBoardlyCommerceSignerFromEnvironment,
  createBoardlySiteVerificationDocument,
} from "../builtin/zenith-platform/commerce/templates/boardly-commerce.server";

const SITE_ID = "01900000-0000-7000-8000-000000000022";
const SITE_ORIGIN = "https://store.example";
const NONCE = base64URLBytes(new Uint8Array(32).fill(7));
const RETRY_NONCE = base64URLBytes(new Uint8Array(32).fill(8));
const UUID_V7_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u;

beforeEach(() => {
  // Keep expiry boundaries stable across asynchronous signing and verification.
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-09-29T00:00:00.000Z"));
  vi.stubGlobal("crypto", webcrypto);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("Boardly Commerce site-signed BFF kit", () => {
  it("gets a one-use challenge and signs the exact Checkout request", async () => {
    const fixture = await createSiteSigner();
    const requests: Array<{ input: RequestInfo | URL; init?: RequestInit }> =
      [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ input, init });
        if (String(input).endsWith("/site-commerce/challenges")) {
          return challengeResponse();
        }
        return Response.json({
          url: "https://checkout.stripe.com/c/pay/session",
        });
      }),
    );
    const client = createTestClient(fixture.signer);

    await expect(client.checkout("pro_monthly")).resolves.toEqual({
      url: "https://checkout.stripe.com/c/pay/session",
    });

    expect(requests).toHaveLength(2);
    for (const request of requests) {
      expect(request.init?.signal).toBeInstanceOf(AbortSignal);
      expect(request.init?.signal?.aborted).toBe(false);
    }
    expect(String(requests[0]?.input)).toBe(
      "https://api.boardly.example/api/v1/site-commerce/challenges",
    );
    expect(requests[0]?.init?.headers).not.toHaveProperty("Authorization");
    const challenge = JSON.parse(String(requests[0]?.init?.body)) as Record<
      string,
      unknown
    >;
    expect(challenge).toEqual({
      schemaVersion: 1,
      siteId: SITE_ID,
      action: "checkout",
      operationId: challenge.operationId,
    });
    expect(challenge.operationId).toMatch(UUID_V7_PATTERN);

    const request = requests[1];
    expect(String(request?.input)).toBe(
      "https://api.boardly.example/api/v1/site-commerce/checkout-sessions",
    );
    const body = String(request?.init?.body);
    expect(JSON.parse(body)).toEqual({
      offerAlias: "pro_monthly",
      operationId: challenge.operationId,
    });
    const authorization = String(
      (request?.init?.headers as Record<string, string>).Authorization,
    );
    expect(authorization.startsWith("BoardlySite ")).toBe(true);
    expect(fixture.claims).toHaveLength(1);
    expect(fixture.claims[0]).toMatchObject({
      iss: SITE_ID,
      aud: "boardly-site-commerce",
      sub: "customer-123",
      schemaVersion: 1,
      environment: "sandbox",
      origin: SITE_ORIGIN,
      sessionId: "session-abc",
      authenticationMethods: ["password"],
      action: "checkout",
      method: "POST",
      path: "/api/v1/site-commerce/checkout-sessions",
      operationId: challenge.operationId,
      bodySha256: createHash("sha256").update(body).digest("hex"),
      nonce: NONCE,
    });
    expect(
      Date.now() - Date.parse(String(fixture.claims[0]?.authenticatedAt)),
    ).toBeLessThan(2 * 60_000);
    expect(fixture.claims[0]?.nbf).toBe(fixture.claims[0]?.iat);
    expect((fixture.claims[0]?.exp ?? 0) - (fixture.claims[0]?.iat ?? 0)).toBe(
      55,
    );
  });

  it("retries an ambiguous Checkout with a fresh challenge and proof", async () => {
    const fixture = await createSiteSigner();
    const requests: Array<{ input: RequestInfo | URL; init?: RequestInit }> =
      [];
    let challengeCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ input, init });
        if (String(input).endsWith("/site-commerce/challenges")) {
          challengeCount += 1;
          return challengeResponse(challengeCount === 1 ? NONCE : RETRY_NONCE);
        }
        if (requests.length === 2) {
          throw new TypeError("response lost after send");
        }
        return Response.json({
          url: "https://checkout.stripe.com/c/pay/session",
        });
      }),
    );

    await expect(
      createTestClient(fixture.signer).checkout("pro_monthly"),
    ).resolves.toEqual({
      url: "https://checkout.stripe.com/c/pay/session",
    });

    expect(requests).toHaveLength(4);
    expect(requests[3]?.init?.body).toBe(requests[1]?.init?.body);
    expect(requests[3]?.init?.headers).not.toEqual(requests[1]?.init?.headers);
    expect(fixture.claims).toHaveLength(2);
    expect(fixture.claims[1]?.operationId).toBe(fixture.claims[0]?.operationId);
    expect(fixture.claims.map(({ nonce }) => nonce)).toEqual([
      NONCE,
      RETRY_NONCE,
    ]);
    expect(fixture.claims[1]?.jti).not.toBe(fixture.claims[0]?.jti);
  });

  it("retries a transient challenge before signing the Checkout request", async () => {
    const fixture = await createSiteSigner();
    const requests: Array<{ input: RequestInfo | URL; init?: RequestInit }> =
      [];
    let challengeCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ input, init });
        if (String(input).endsWith("/site-commerce/challenges")) {
          challengeCount += 1;
          if (challengeCount === 1) return new Response(null, { status: 503 });
          return challengeResponse();
        }
        return Response.json({
          url: "https://checkout.stripe.com/c/pay/session",
        });
      }),
    );

    await expect(
      createTestClient(fixture.signer).checkout("pro_monthly"),
    ).resolves.toEqual({
      url: "https://checkout.stripe.com/c/pay/session",
    });

    expect(requests).toHaveLength(3);
    expect(fixture.claims).toHaveLength(1);
    const firstChallenge = JSON.parse(String(requests[0]?.init?.body)) as {
      operationId: string;
    };
    const secondChallenge = JSON.parse(String(requests[1]?.init?.body)) as {
      operationId: string;
    };
    expect(secondChallenge.operationId).toBe(firstChallenge.operationId);
    expect(fixture.claims[0]?.operationId).toBe(firstChallenge.operationId);
  });

  it("does not retry a deterministic provider request rejection", async () => {
    const fixture = await createSiteSigner();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).endsWith("/site-commerce/challenges")) {
        return challengeResponse();
      }
      return Response.json(
        {
          code: "commerce_provider_request_invalid",
          detail: "Stripe rejected Boardly's Commerce request",
          requestId: "request-provider-invalid",
        },
        { status: 502 },
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      createTestClient(fixture.signer).checkout("pro_monthly"),
    ).rejects.toMatchObject({
      status: 502,
      code: "commerce_provider_request_invalid",
      requestId: "request-provider-invalid",
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fixture.claims).toHaveLength(1);
  });

  it("retries a near-expiry challenge with a fresh nonce before signing", async () => {
    const fixture = await createSiteSigner();
    const requests: Array<{ input: RequestInfo | URL; init?: RequestInit }> =
      [];
    let challengeCount = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ input, init });
        if (String(input).endsWith("/site-commerce/challenges")) {
          challengeCount += 1;
          return challengeResponse(
            challengeCount === 1 ? NONCE : RETRY_NONCE,
            challengeCount === 1 ? Date.now() + 4_000 : Date.now() + 60_000,
          );
        }
        return Response.json({
          url: "https://checkout.stripe.com/c/pay/session",
        });
      }),
    );

    await expect(
      createTestClient(fixture.signer).checkout("pro_monthly"),
    ).resolves.toEqual({
      url: "https://checkout.stripe.com/c/pay/session",
    });

    const challengeRequests = requests.filter(({ input }) =>
      String(input).endsWith("/site-commerce/challenges"),
    );
    expect(challengeRequests).toHaveLength(2);
    expect(fixture.claims).toHaveLength(1);
    expect(fixture.claims[0]?.nonce).toBe(RETRY_NONCE);
  });

  it("surfaces a safe typed error when the fresh challenge is also expired", async () => {
    const fixture = await createSiteSigner();
    const fetchMock = vi.fn(async () =>
      challengeResponse(NONCE, Date.now() - 1),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      createTestClient(fixture.signer).checkout("pro_monthly"),
    ).rejects.toMatchObject({
      status: 503,
      code: "commerce_challenge_expired",
      detail: "The Boardly Commerce challenge expired before it could be used",
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fixture.claims).toHaveLength(0);
  });

  it("uses a challenge for Portal and no challenge for entitlement refresh", async () => {
    const fixture = await createSiteSigner();
    const session = await createCommerceSessionFixture();
    const requests: Array<{ input: RequestInfo | URL; init?: RequestInit }> =
      [];
    let entitlementAttempts = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ input, init });
        const url = String(input);
        if (url.endsWith("/site-commerce/challenges"))
          return challengeResponse();
        if (url.endsWith("/site-commerce/portal-sessions")) {
          return Response.json({ url: "https://billing.stripe.com/p/session" });
        }
        if (url.endsWith("/site-commerce/jwks"))
          return Response.json(session.jwkSet);
        entitlementAttempts += 1;
        if (entitlementAttempts === 1)
          return new Response(null, { status: 503 });
        return Response.json(session.response);
      }),
    );
    const client = createTestClient(fixture.signer);

    await client.portal();
    await expect(client.refreshSession()).resolves.toMatchObject({
      entitlements: [{ key: "plan.pro", status: "active" }],
    });

    const challengeBodies = requests
      .filter(({ input }) =>
        String(input).endsWith("/site-commerce/challenges"),
      )
      .map(
        ({ init }) => JSON.parse(String(init?.body)) as Record<string, unknown>,
      );
    expect(challengeBodies).toHaveLength(1);
    expect(challengeBodies[0]?.action).toBe("portal");
    expect(fixture.claims.map(({ action }) => action)).toEqual([
      "portal",
      "entitlements",
      "entitlements",
    ]);
    const entitlementClaims = fixture.claims[1];
    expect(entitlementClaims?.path).toBe(
      "/api/v1/site-commerce/entitlements/verify",
    );
    expect(entitlementClaims?.operationId).toMatch(UUID_V7_PATTERN);
    expect(entitlementClaims?.jti).toMatch(UUID_V7_PATTERN);
    expect(entitlementClaims?.operationId).not.toBe(entitlementClaims?.jti);
    expect(entitlementClaims).not.toHaveProperty("nonce");
    expect(fixture.claims[2]?.operationId).toBe(entitlementClaims?.operationId);
    expect(fixture.claims[2]?.jti).not.toBe(entitlementClaims?.jti);
    expect(fixture.claims[2]).not.toHaveProperty("nonce");
    const entitlementRequest = requests.find(({ input }) =>
      String(input).endsWith("/site-commerce/entitlements/verify"),
    );
    expect(entitlementRequest?.init?.body).toBe("{}");
    for (const request of requests) {
      expect(request.init?.signal).toBeInstanceOf(AbortSignal);
    }
  });

  it("requires recent authentication before creating a Portal challenge", async () => {
    const fixture = await createSiteSigner();
    const fetchMock = vi.fn(async () => challengeResponse());
    vi.stubGlobal("fetch", fetchMock);
    const client = createBoardlyCommerce({
      apiBaseUrl: "https://api.boardly.example/api/v1/",
      siteOrigin: SITE_ORIGIN,
      siteId: SITE_ID,
      signer: fixture.signer,
      getCustomerContext: async () =>
        customerContext({
          authenticatedAt: new Date(Date.now() - 5 * 60_000 - 1).toISOString(),
        }),
    });

    await expect(client.portal()).rejects.toMatchObject({
      status: 401,
      code: "commerce_reauthentication_required",
      detail:
        "Recent customer authentication is required to open billing settings",
    });
    expect(fetchMock).not.toHaveBeenCalled();
    expect(fixture.claims).toHaveLength(0);
  });

  it("rejects customer context values that the Go verifier cannot accept", async () => {
    const fixture = await createSiteSigner();
    const fetchMock = vi.fn(async () => challengeResponse());
    vi.stubGlobal("fetch", fetchMock);
    const invalidContexts: CommerceCustomerContext[] = [
      customerContext({ subject: "é".repeat(128) }),
      customerContext({ subject: "customer\u0085control" }),
      customerContext({ authenticatedAt: "2026-02-30T01:02:03Z" }),
    ];

    for (const context of invalidContexts) {
      const client = createBoardlyCommerce({
        apiBaseUrl: "https://api.boardly.example/api/v1/",
        siteOrigin: SITE_ORIGIN,
        siteId: SITE_ID,
        signer: fixture.signer,
        getCustomerContext: async () => context,
      });
      await expect(client.getEntitlements()).rejects.toMatchObject({
        status: 401,
        code: "commerce_auth_unavailable",
      });
    }
    expect(fetchMock).not.toHaveBeenCalled();
    expect(fixture.claims).toHaveLength(0);
  });

  it("verifies Boardly assertions against the current Site customer session", async () => {
    const fixture = await createSiteSigner();
    const session = await createCommerceSessionFixture();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).endsWith("/site-commerce/jwks"))
          return Response.json(session.jwkSet);
        return Response.json(session.response);
      }),
    );
    const client = createTestClient(fixture.signer);

    const refreshed = await client.refreshSession();
    await expect(client.verifySession(refreshed.assertion)).resolves.toEqual(
      refreshed,
    );

    const wrongSessionClient = createBoardlyCommerce({
      apiBaseUrl: "https://api.boardly.example/api/v1/",
      siteOrigin: SITE_ORIGIN,
      siteId: SITE_ID,
      signer: fixture.signer,
      getCustomerContext: async () =>
        customerContext({ sessionId: "session-other" }),
    });
    await expect(
      wrongSessionClient.verifySession(refreshed.assertion),
    ).rejects.toMatchObject({
      code: "commerce_invalid_assertion",
      status: 401,
    });

    const wrongSiteClient = createBoardlyCommerce({
      apiBaseUrl: "https://api.boardly.example/api/v1/",
      siteOrigin: SITE_ORIGIN,
      siteId: "01900000-0000-7000-8000-000000000099",
      signer: fixture.signer,
      getCustomerContext: async () => customerContext(),
    });
    await expect(
      wrongSiteClient.verifySession(refreshed.assertion),
    ).rejects.toMatchObject({
      code: "commerce_invalid_assertion",
    });

    const wrongEnvironmentClient = createTestClient({
      ...fixture.signer,
      environment: "live",
    });
    await expect(
      wrongEnvironmentClient.verifySession(refreshed.assertion),
    ).rejects.toMatchObject({
      code: "commerce_invalid_assertion",
    });
  });

  it("rejects a JWKS key with fields outside the closed public-key contract", async () => {
    const fixture = await createSiteSigner();
    const session = await createCommerceSessionFixture();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).endsWith("/site-commerce/jwks")) {
          return Response.json({
            keys: [{ ...session.jwkSet.keys[0], unexpected: "field" }],
          });
        }
        return Response.json(session.response);
      }),
    );

    await expect(
      createTestClient(fixture.signer).refreshSession(),
    ).rejects.toMatchObject({
      status: 502,
      code: "commerce_invalid_response",
    });
  });

  it("rejects an oversized challenge response before signing", async () => {
    const fixture = await createSiteSigner();
    const payload =
      JSON.stringify({
        schemaVersion: 1,
        nonce: NONCE,
        expiresAt: new Date(Date.now() + 60_000).toISOString(),
      }) + " ".repeat(300_000);
    const fetchMock = vi.fn(
      async () =>
        new Response(payload, {
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      createTestClient(fixture.signer).checkout("pro_monthly"),
    ).rejects.toMatchObject({
      status: 502,
      code: "commerce_invalid_response",
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fixture.claims).toHaveLength(0);
  });

  it("rejects signer output with an algorithm-confusion header before network I/O", async () => {
    const fixture = await createSiteSigner();
    const fetchMock = vi.fn(async () => challengeResponse());
    vi.stubGlobal("fetch", fetchMock);
    const signer: BoardlyCommerceSigner = {
      ...fixture.signer,
      signRequest: async (claims) =>
        [
          base64URLJSON({
            alg: "none",
            typ: "boardly-site-request+jwt",
            kid: fixture.signer.keyId,
          }),
          base64URLJSON(claims),
          base64URLBytes(new Uint8Array(64).fill(1)),
        ].join("."),
    };

    await expect(
      createTestClient(signer).checkout("pro_monthly"),
    ).rejects.toMatchObject({
      code: "commerce_signing_failed",
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("rejects signer output that is expired or usable beyond sixty seconds", async () => {
    const cases: Array<{
      name: string;
      times(now: number): { iat: number; nbf: number; exp: number };
    }> = [
      {
        name: "expired",
        times: (now) => ({ iat: now - 60, nbf: now - 60, exp: now }),
      },
      {
        name: "future expiry",
        times: (now) => ({ iat: now + 1, nbf: now + 1, exp: now + 61 }),
      },
    ];

    for (const testCase of cases) {
      const fixture = await createSiteSigner();
      const fetchMock = vi.fn(async () => challengeResponse());
      vi.stubGlobal("fetch", fetchMock);
      const signer: BoardlyCommerceSigner = {
        ...fixture.signer,
        signRequest: async (claims) =>
          fixture.signer.signRequest({
            ...claims,
            ...testCase.times(Math.floor(Date.now() / 1000)),
          }),
      };

      await expect(
        createTestClient(signer).checkout("pro_monthly"),
        testCase.name,
      ).rejects.toMatchObject({
        code: "commerce_signing_failed",
      });
      expect(fetchMock, testCase.name).toHaveBeenCalledTimes(2);
      expect(
        fetchMock.mock.calls.every(([input]) =>
          String(input).endsWith("/site-commerce/challenges"),
        ),
        testCase.name,
      ).toBe(true);
    }
  });

  it("builds the public well-known document from Boardly proof and signer JWK", async () => {
    const fixture = await createSiteSigner();
    await expect(
      createBoardlySiteVerificationDocument(
        {
          schemaVersion: 2,
          siteAlias: "store",
          manifestDigest: "a".repeat(64),
        },
        fixture.signer,
      ),
    ).resolves.toEqual({
      schemaVersion: 2,
      siteAlias: "store",
      manifestDigest: "a".repeat(64),
      signingKeys: [fixture.signer.publicJwk],
    });
  });

  it.each([
    [
      "wrong schema version",
      {
        schemaVersion: 1,
        siteAlias: "store",
        manifestDigest: "a".repeat(64),
      },
    ],
    [
      "extra field",
      {
        schemaVersion: 2,
        siteAlias: "store",
        manifestDigest: "a".repeat(64),
        signingKeys: [],
      },
    ],
    [
      "malformed digest",
      {
        schemaVersion: 2,
        siteAlias: "store",
        manifestDigest: "not-a-digest",
      },
    ],
  ])("rejects a persisted proof with a %s", async (_name, proof) => {
    const fixture = await createSiteSigner();

    await expect(
      createBoardlySiteVerificationDocument(proof, fixture.signer),
    ).rejects.toThrow("Boardly Site verification proof is invalid");
  });

  it("loads the daemon signer lease without exposing its capability", async () => {
    const fixture = await createSiteSigner();
    const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push({ input, init });
        if (init?.method === "GET") {
          return Response.json({
            schemaVersion: 1,
            environment: "sandbox",
            keyId: fixture.signer.keyId,
            publicJwk: {
              kty: "EC",
              crv: "P-256",
              x: fixture.signer.publicJwk.x,
              y: fixture.signer.publicJwk.y,
            },
          });
        }
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        const claims = completeLocalSignerClaims(body, fixture.signer);
        return Response.json({
          schemaVersion: 1,
          token: await fixture.signer.signRequest({
            ...claims,
            aud: [claims.aud],
          } as unknown as BoardlyCommerceRequestClaims),
          expiresAt: new Date(claims.exp * 1000).toISOString(),
          keyId: fixture.signer.keyId,
        });
      }),
    );
    const environment = {
      BOARDLY_COMMERCE_ENVIRONMENT: "sandbox",
      BOARDLY_COMMERCE_SIGNER_URL: "http://127.0.0.1:7788/v1/commerce/dev/sign",
      BOARDLY_COMMERCE_SIGNER_CAPABILITY: "secret-lease-capability",
      BOARDLY_COMMERCE_SIGNER_KEY_ID: fixture.signer.keyId,
    };

    const signer =
      await createBoardlyCommerceSignerFromEnvironment(environment);
    const claims = requestClaims(fixture.signer);
    await expect(signer.signRequest(claims)).resolves.toContain(".");

    expect(calls).toHaveLength(2);
    for (const call of calls) {
      expect(call.init?.signal).toBeInstanceOf(AbortSignal);
      expect(call.init?.headers).toMatchObject({
        Accept: "application/json",
        Authorization: "BoardlyCommerceLease secret-lease-capability",
      });
      expect(String(call.init?.body ?? "")).not.toContain(
        "secret-lease-capability",
      );
    }
  });

  it("returns allowlisted errors without exposing customer or signing failures", async () => {
    const fixture = await createSiteSigner();
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              code: "commerce_rate_limited",
              detail: "Try again later",
              requestId: "request-123",
            }),
            {
              status: 429,
              headers: {
                "Content-Type": "application/json",
                "Retry-After": "45",
              },
            },
          ),
      ),
    );

    const error = await createTestClient(fixture.signer)
      .portal()
      .catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(BoardlyCommerceError);
    expect(error).toMatchObject({
      status: 429,
      code: "commerce_rate_limited",
      requestId: "request-123",
      retryAfter: "45",
    });

    const unavailableContext = createBoardlyCommerce({
      apiBaseUrl: "https://api.boardly.example/api/v1/",
      siteOrigin: SITE_ORIGIN,
      siteId: SITE_ID,
      signer: fixture.signer,
      getCustomerContext: async () => {
        throw new Error("session_cookie=must-not-leak");
      },
    });
    const contextError = await unavailableContext
      .getEntitlements()
      .catch((reason: unknown) => reason);
    expect(contextError).toMatchObject({
      status: 401,
      code: "commerce_auth_unavailable",
      detail: "A current authenticated customer session is required",
    });
    expect(String(contextError)).not.toContain("session_cookie");
  });
});

function createTestClient(signer: BoardlyCommerceSigner) {
  return createBoardlyCommerce({
    apiBaseUrl: "https://api.boardly.example/api/v1/",
    siteOrigin: SITE_ORIGIN,
    siteId: SITE_ID,
    signer,
    getCustomerContext: async () => customerContext(),
  });
}

function customerContext(
  overrides: Partial<CommerceCustomerContext> = {},
): CommerceCustomerContext {
  return {
    subject: overrides.subject ?? "customer-123",
    sessionId: overrides.sessionId ?? "session-abc",
    authenticatedAt:
      overrides.authenticatedAt ?? new Date(Date.now() - 60_000).toISOString(),
    authenticationMethods: overrides.authenticationMethods ?? ["password"],
  };
}

function challengeResponse(
  nonce: string = NONCE,
  expiresAt: number = Date.now() + 60_000,
) {
  return Response.json({
    schemaVersion: 1,
    nonce,
    expiresAt: new Date(expiresAt).toISOString(),
  });
}

async function createSiteSigner(): Promise<{
  claims: BoardlyCommerceRequestClaims[];
  signer: BoardlyCommerceSigner;
}> {
  const keyPair = await crypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" },
    true,
    ["sign", "verify"],
  );
  const exported = await crypto.subtle.exportKey("jwk", keyPair.publicKey);
  const keyId = await publicJWKThumbprint(exported);
  const publicJwk = {
    kty: "EC" as const,
    crv: "P-256" as const,
    use: "sig" as const,
    alg: "ES256" as const,
    kid: keyId,
    x: String(exported.x),
    y: String(exported.y),
  };
  const claims: BoardlyCommerceRequestClaims[] = [];
  return {
    claims,
    signer: {
      environment: "sandbox",
      keyId,
      publicJwk,
      async signRequest(request) {
        claims.push(request);
        const header = base64URLJSON({
          alg: "ES256",
          typ: "boardly-site-request+jwt",
          kid: keyId,
        });
        const payload = base64URLJSON(request);
        const signingInput = `${header}.${payload}`;
        const signature = await crypto.subtle.sign(
          { name: "ECDSA", hash: "SHA-256" },
          keyPair.privateKey,
          new TextEncoder().encode(signingInput),
        );
        return `${signingInput}.${base64URLBytes(new Uint8Array(signature))}`;
      },
    },
  };
}

function requestClaims(
  signer: BoardlyCommerceSigner,
): BoardlyCommerceRequestClaims {
  const issuedAt = Math.floor(Date.now() / 1000);
  return {
    iss: SITE_ID,
    aud: "boardly-site-commerce",
    sub: "customer-123",
    iat: issuedAt,
    nbf: issuedAt,
    exp: issuedAt + 60,
    jti: "01991d2f-cd80-7901-9f96-b6b88ca60092",
    schemaVersion: 1,
    environment: signer.environment,
    origin: SITE_ORIGIN,
    sessionId: "session-abc",
    authenticatedAt: "2026-08-29T01:02:03.000Z",
    authenticationMethods: ["password"],
    action: "checkout",
    method: "POST",
    path: "/api/v1/site-commerce/checkout-sessions",
    operationId: "01991d2f-cd80-7901-9f96-b6b88ca60091",
    bodySha256: "a".repeat(64),
    nonce: NONCE,
  };
}

function completeLocalSignerClaims(
  body: Record<string, unknown>,
  signer: BoardlyCommerceSigner,
): BoardlyCommerceRequestClaims {
  const issuedAt = Math.floor(Date.now() / 1000);
  const path = body.path as BoardlyCommerceRequestClaims["path"];
  return {
    iss: SITE_ID,
    aud: "boardly-site-commerce",
    sub: String(body.subject),
    iat: issuedAt,
    nbf: issuedAt,
    exp: issuedAt + 60,
    jti: "01991d2f-cd80-7901-9f96-b6b88ca60093",
    schemaVersion: 1,
    environment: signer.environment,
    origin: SITE_ORIGIN,
    sessionId: String(body.sessionId),
    authenticatedAt: String(body.authenticatedAt),
    authenticationMethods: body.authenticationMethods as string[],
    action: path.endsWith("checkout-sessions")
      ? "checkout"
      : path.endsWith("portal-sessions")
        ? "portal"
        : "entitlements",
    method: "POST",
    path,
    operationId: String(body.operationId),
    bodySha256: String(body.bodySha256),
    ...(typeof body.nonce === "string" ? { nonce: body.nonce } : {}),
  };
}

async function createCommerceSessionFixture(): Promise<{
  response: { assertion: string; expiresAt: string; entitlements: unknown[] };
  jwkSet: { keys: JsonWebKey[] };
}> {
  const keyPair = await crypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" },
    true,
    ["sign", "verify"],
  );
  const exportedPublicKey = await crypto.subtle.exportKey(
    "jwk",
    keyPair.publicKey,
  );
  const publicKey: JsonWebKey = {
    kty: "EC",
    use: "sig",
    crv: "P-256",
    kid: "bcs1_test_key",
    alg: "ES256",
    x: exportedPublicKey.x,
    y: exportedPublicKey.y,
  };
  const issuedAt = Math.floor(Date.now() / 1000);
  const expiresAt = issuedAt + 300;
  const entitlements = [
    {
      key: "plan.pro",
      status: "active",
      updatedAt: "2026-08-29T01:02:03.000Z",
    },
  ];
  const header = base64URLJSON({
    alg: "ES256",
    kid: publicKey.kid,
    typ: "boardly-commerce+jwt",
  });
  const payload = base64URLJSON({
    schemaVersion: 1,
    iss: "boardly-commerce",
    aud: [SITE_ORIGIN],
    sub: "customer-123",
    siteId: SITE_ID,
    environment: "sandbox",
    sessionId: "session-abc",
    jti: "01991d2f-cd80-7901-9f96-b6b88ca60099",
    iat: issuedAt,
    nbf: issuedAt,
    exp: expiresAt,
    entitlements,
  });
  const signingInput = `${header}.${payload}`;
  const signature = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    keyPair.privateKey,
    new TextEncoder().encode(signingInput),
  );
  const assertion = `${signingInput}.${base64URLBytes(new Uint8Array(signature))}`;
  return {
    response: {
      assertion,
      expiresAt: new Date(expiresAt * 1000).toISOString(),
      entitlements,
    },
    jwkSet: { keys: [publicKey] },
  };
}

async function publicJWKThumbprint(jwk: JsonWebKey): Promise<string> {
  const canonical = JSON.stringify({
    crv: "P-256",
    kty: "EC",
    x: jwk.x,
    y: jwk.y,
  });
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(canonical),
  );
  return base64URLBytes(new Uint8Array(digest));
}

function base64URLJSON(value: unknown): string {
  return base64URLBytes(new TextEncoder().encode(JSON.stringify(value)));
}

function base64URLBytes(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) binary += String.fromCharCode(byte);
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/u, "");
}
