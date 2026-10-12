# Fetch-compatible serverless adapter

Create authenticated POST handlers in the Site's existing serverless router. Cache signer and client promises in the server module:

```ts
import {
  type BoardlyCommerceClient,
  type BoardlyCommerceSigner,
  createBoardlyCommerce,
  createBoardlyCommerceSignerFromEnvironment,
  hasActiveEntitlement,
} from "./boardly-commerce.server";
import {
  requireCommerceCustomerContext,
  requireCustomerSession,
  requireSameOrigin,
} from "./auth";
import { requiredServerEnv } from "./config";

let signer: Promise<BoardlyCommerceSigner> | undefined;
let commerce: Promise<BoardlyCommerceClient> | undefined;

function requireCommerceSigner(): Promise<BoardlyCommerceSigner> {
  signer ??= createBoardlyCommerceSignerFromEnvironment(process.env);
  return signer;
}

function requireCommerce(): Promise<BoardlyCommerceClient> {
  commerce ??= (async () =>
    createBoardlyCommerce({
      apiBaseUrl: requiredServerEnv("BOARDLY_COMMERCE_API_URL"),
      siteOrigin: requiredServerEnv("BOARDLY_COMMERCE_SITE_ORIGIN"),
      siteId: requiredServerEnv("BOARDLY_COMMERCE_SITE_ID"),
      getCustomerContext: requireCommerceCustomerContext,
      signer: await requireCommerceSigner(),
    }))();
  return commerce;
}

const OFFERS = new Set(["pro_monthly", "pro_yearly"]);

export async function checkout(request: Request): Promise<Response> {
  await requireCustomerSession(request);
  requireSameOrigin(request);
  const input: unknown = await request.json();
  const offerAlias =
    typeof input === "object" &&
    input !== null &&
    "offerAlias" in input &&
    typeof input.offerAlias === "string"
      ? input.offerAlias
      : "";
  if (!OFFERS.has(offerAlias)) {
    return Response.json({ code: "invalid_offer" }, { status: 400 });
  }
  return Response.json(await (await requireCommerce()).checkout(offerAlias), {
    headers: { "Cache-Control": "private, no-store" },
  });
}
```

Use the daemon signer factory only under `zen commerce dev --manifest boardly-commerce.json -- npm run dev`, launched from the current Zenith Team Task's agent shell. Standalone and operator-shell invocations are unsupported and fail closed. The supervised process receives an isolated `BOARDLY_HOME` without the Task bootstrap, and the daemon keeps its local private key only in memory. Hosted code supplies a production KMS/HSM or secret-manager-backed signer with the same interface. Missing setup returns `commerce_setup_required` only from Commerce routes and never crashes unrelated pages.

- Portal: require auth and same-origin, reject non-empty input, and require an authentication root no more than five minutes old before `portal()`. On `commerce_reauthentication_required`, run the Site's existing step-up or reauthentication flow, rotate the Site session ID, update its trusted authentication time and methods, and retry; never accept those values from request input.
- Entitlement refresh: require auth and same-origin, require `{}`, then call `refreshSession()`. Store its assertion in the authenticated server session or a host-only Secure, HttpOnly, SameSite cookie; return only `{ entitlements }`.
- Protected routes load that assertion, call `verifySession()`, and authorize with `hasActiveEntitlement()`.
- Refresh a missing or expired assertion and refresh immediately before high-risk or irreversible mutations.
- The returned verification-path handler combines the exact Zenith Team proof with exactly one current or next signer public JWK using `createBoardlySiteVerificationDocument()`. During rotation, select the next-key signer for only this verification handler while `BoardlyCommerceConfig` keeps the current request signer, synchronize the next key through Zenith Team, then switch the request signer. Zenith Team retains the prior key as `retiring` for the overlap window.

`requireCommerceCustomerContext` reads only trusted server session state. Browser input never selects customer identity, session ID, Site ID, environment, origin, key, operation path, or return URL.

Map `BoardlyCommerceError` to an allowlisted status, code, detail, request ID, and Retry-After response. Never serialize original causes, authorization headers, Commerce assertions, signer capabilities, private keys, payment responses, or hosted URLs.
