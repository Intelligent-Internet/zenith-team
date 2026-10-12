# Next.js App Router adapter

Keep the copied kit and this wrapper server-only:

```ts
import "server-only";
import {
  type BoardlyCommerceClient,
  type BoardlyCommerceSigner,
  createBoardlyCommerce,
  createBoardlyCommerceSignerFromEnvironment,
  createBoardlySiteVerificationDocument,
  hasActiveEntitlement,
} from "@/server/boardly-commerce.server";
import { requireCommerceCustomerContext } from "@/server/auth";
import { requiredServerEnv } from "@/server/config";
import verificationProof from "@/server/boardly-commerce.verification.json";

let signer: Promise<BoardlyCommerceSigner> | undefined;
let commerce: Promise<BoardlyCommerceClient> | undefined;

function requireCommerceSigner(): Promise<BoardlyCommerceSigner> {
  signer ??= createBoardlyCommerceSignerFromEnvironment(process.env);
  return signer;
}

export function requireCommerce(): Promise<BoardlyCommerceClient> {
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
```

Run the Site's existing validation and production build before starting `zen commerce dev --manifest boardly-commerce.json -- npm run dev` from the current Zenith Team Task's agent shell. Never run `next build` against the same worktree and `.next` cache while `next dev` is live; stop the development process first or build in a separate clean worktree with an isolated cache. Use this local signer factory only under that Task-scoped, daemon-supervised command; standalone and operator-shell invocations fail closed. The supervised process receives an isolated `BOARDLY_HOME` without the Task bootstrap, and the daemon keeps its local private key only in memory. In hosted environments, make `requireCommerceSigner()` return a production KMS/HSM or secret-manager-backed `BoardlyCommerceSigner`. Keep the key handle and all private material in server infrastructure. Preserve lazy initialization so unrelated pages run before Commerce configuration exists.

`requireCommerceCustomerContext` reads the Site's current trusted login session and returns its stable subject, current session ID, authentication time, and methods. It must reject missing sessions and must not accept those values from request input.

An authenticated Checkout route accepts only an allowlisted alias owned by Site code:

```ts
import { NextResponse } from "next/server";
import { requireCommerce } from "@/server/commerce";
import { requireCustomerSession, requireSameOrigin } from "@/server/auth";

const OFFERS = new Set(["pro_monthly", "pro_yearly"]);

export async function POST(request: Request) {
  await requireCustomerSession();
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
    return NextResponse.json({ code: "invalid_offer" }, { status: 400 });
  }
  return NextResponse.json(
    await (await requireCommerce()).checkout(offerAlias),
    {
      headers: { "Cache-Control": "private, no-store" },
    },
  );
}
```

Portal accepts no body authority. Entitlement refresh accepts `{}` only. Both require the same customer session and same-origin/CSRF policy. Before `portal()`, require an authentication root no more than five minutes old. If the kit returns `commerce_reauthentication_required`, run the Site's existing step-up or reauthentication flow, rotate the Site session ID, update its trusted authentication time and methods, then retry from that server session. Never accept those values from the Portal request.

On refresh, store `session.assertion` in the existing authenticated server session or a host-only Secure, HttpOnly, SameSite cookie, and return only `{ entitlements }` to browser code. A protected route loads that assertion and authorizes offline:

```ts
const commerce = await requireCommerce();
const session = await commerce.verifySession(requireCommerceAssertion());
if (!hasActiveEntitlement(session, "plan.pro")) {
  return NextResponse.json({ code: "payment_required" }, { status: 402 });
}
```

Persist the exact three-field proof returned by manifest submission as `boardly-commerce.verification.json`. Import that JSON directly as shown and pass it to `createBoardlySiteVerificationDocument()` without a type assertion or `as const`; the helper validates the untrusted persisted value's exact keys and values. The exact returned well-known route serves it with the active signer public key:

```ts
export async function GET() {
  const document = await createBoardlySiteVerificationDocument(
    verificationProof,
    await requireCommerceSigner(),
  );
  return NextResponse.json(document, {
    headers: { "Cache-Control": "public, max-age=60" },
  });
}
```

During a key rotation, the hosted route publishes exactly the current or next public key. Select the next-key signer for this verification route while `BoardlyCommerceConfig` continues using the current signer, synchronize the next key through Zenith Team, and only then switch the request signer. Zenith Team keeps the prior key accepted as `retiring` for the overlap window. Never publish private JWK fields. Map `BoardlyCommerceError` to an allowlisted status, code, detail, request ID, and Retry-After response; do not serialize its cause, authorization header, assertion, signer capability, or hosted URL.
