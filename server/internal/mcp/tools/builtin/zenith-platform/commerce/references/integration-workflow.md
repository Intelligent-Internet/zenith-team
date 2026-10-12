# Integration workflow

## 1. Read readiness without blocking local work

Call `commerce_account_status` before asking setup questions. Local development targets `sandbox` unless the Task explicitly targets production. Record account readiness, terms, requirements, charges, and payouts. Continue building when `manifestReady` is false; it blocks publication and real Checkout, not the website implementation.

## 2. Bind Commerce to the Site's login

Commerce requires the site's existing authenticated customer session. Define one server-only mapper that returns:

```ts
interface CommerceCustomerContext {
  subject: string;
  sessionId: string;
  authenticatedAt: string;
  authenticationMethods: string[];
}
```

`subject` is the Site's stable customer identifier. `sessionId` identifies the current login session and must rotate on reauthentication or session replacement. `authenticatedAt` and `authenticationMethods` come from that session's trusted server record. Never derive any field from request JSON, query parameters, browser storage, or forwarded headers.

Before activation, test the BFF request shapes and denial behavior with server-side test doubles confined to tests. Do not add a fake paid state to browser runtime code.

## 3. Install the server-only kit and signer

Copy `commerce/templates/boardly-commerce.server.ts` into a server-only directory. It uses built-in Fetch and Web Crypto APIs and needs no payment SDK. Initialize it lazily:

```ts
let commerce: Promise<BoardlyCommerceClient> | undefined;

export function requireCommerce(): Promise<BoardlyCommerceClient> {
  commerce ??= (async () =>
    createBoardlyCommerce({
      apiBaseUrl: requiredServerEnv("BOARDLY_COMMERCE_API_URL"),
      siteOrigin: requiredServerEnv("BOARDLY_COMMERCE_SITE_ORIGIN"),
      siteId: requiredServerEnv("BOARDLY_COMMERCE_SITE_ID"),
      getCustomerContext: requireCommerceCustomerContext,
      signer: await createBoardlyCommerceSignerFromEnvironment(process.env),
    }))();
  return commerce;
}
```

Run the Site's existing validation, tests, and production build before starting local Commerce. Never run a framework production build against the same worktree and output cache while its development server is live; stop the development process first or build in a separate clean worktree with an isolated cache.

From the current Zenith Team Task's agent shell, run `zen commerce dev --manifest boardly-commerce.json -- npm run dev`. Keep this wrapper in the foreground and keep the Task Run active until the operator finishes the payment test. Never background or detach it, use `nohup`, or start its child Site command separately. The command intentionally fails closed from an ordinary operator shell or outside a Task. The daemon authorizes the command through a bootstrap bound to the current Run, Task, and Company in that Run's isolated `BOARDLY_HOME`; there is no global Commerce control file or standalone authority. It keeps the P-256 private key only in memory, registers the matching public key for the Task-authored Site, and issues a short-lived Site-and-origin-bound signer lease. The supervised Site receives the ephemeral loopback signer URL and lease capability but starts with a separate empty `BOARDLY_HOME`, so it cannot mint another lease. The helper verifies the daemon's public key and every returned JWT; capability values never enter request bodies, logs, or browser bundles. When the wrapper or Run exits, the signer lease is revoked. Stop any orphaned Site process and restart the wrapper from a new active Task Run; never describe the sandbox as testable after the wrapper or Run has ended.

In a hosted deployment, replace only signer construction with a `BoardlyCommerceSigner` adapter backed by a production KMS/HSM or secret manager. Prefer non-exportable P-256 keys. The adapter exposes the public JWK and RFC 7638 thumbprint and signs exact request claims with ES256; it never returns private material. For rotation, publish exactly the next public key in the well-known document, synchronize it through Zenith Team, and switch the signer only after synchronization succeeds. Zenith Team keeps the prior key accepted as `retiring` for the overlap window.

Missing configuration should produce an allowlisted `commerce_setup_required` response on Commerce routes only. It must not crash unrelated pages. Never use a random tunnel, private-network name, placeholder origin, caller-supplied key URL, shared secret, or direct Stripe SDK.

## 4. Expose only Site-owned BFF routes

- Checkout requires the existing customer session and same-origin/CSRF policy, accepts one code-allowlisted `offerAlias`, and calls `checkout(offerAlias)`.
- Portal requires the same protections and an authentication root no more than five minutes old, accepts no runtime authority, and calls `portal()`. On `commerce_reauthentication_required`, use the Site's own step-up or reauthentication flow, rotate `sessionId`, update the trusted authentication time and methods, and retry from the new server session.
- Entitlement refresh requires the same protections, accepts `{}` only, and calls `refreshSession()`.

The kit obtains an unsigned, one-use challenge before Checkout and Portal, then signs the exact outgoing body. An automatic retry preserves the serialized body and operation ID but obtains a fresh challenge and fresh JWT. Entitlement refresh signs an exact `{}` body without a challenge and obtains a fresh JWT for a retry.

Store the returned Commerce assertion in the Site's authenticated server session or a host-only Secure, HttpOnly, SameSite cookie. Return only the safe entitlement projection to browser code. Hosted URLs may be returned to the authenticated caller for immediate navigation but must never be logged or persisted.

## 5. Publish Site proof and public keys

Manifest submission always returns an exact `verificationPath` and three-field `verificationDocument`:

```json
{
  "schemaVersion": 2,
  "siteAlias": "store",
  "manifestDigest": "<64 lowercase hex characters>"
}
```

Persist that proof unchanged. Import the persisted JSON directly and pass it to `createBoardlySiteVerificationDocument(proof, signer)` without a type assertion or `as const`; the helper validates its exact keys and values at runtime. The route returns the proof plus exactly one public JWK. During rotation this is the next key being synchronized; Zenith Team, not the well-known response, retains the previous retiring key for overlap. The public JWK is not secret; the private key and local lease capability must never be published. Remote activation requires an exact HTTPS origin and a usable public signing key. For loopback testing, the local daemon registers its in-memory development public key only through the current Task's scoped authority.

## 6. Protect content with bounded offline verification

The success path is a processing page. It polls the Site's entitlement BFF for a bounded period; timeout remains “Payment processing” and never grants access.

Ordinary protected server routes load the stored assertion, call `verifySession()`, and require `hasActiveEntitlement()`. Verification pins Zenith Team's ES256 key, exact environment, Site origin, Site ID, customer subject, Site session ID, and five-minute lifetime. It fetches Zenith Team JWKS only on a cold cache or key rotation.

Refresh a missing or expired assertion once. Refresh immediately before high-risk or irreversible mutations so refunds or disputes are not hidden by the bounded cache. Never reuse an assertion across Site sessions.

## 7. Activate sandbox, then live

Derive the exact canonical local origin from the repository. Do not ask the user for a gateway URL, identity vendor, webhook, tunnel, or signer capability. If price, currency, offer type or interval, entitlement, or automatic-tax policy cannot be derived from approved Product work, finish independent work and call `ask_human` once with one consolidated business question.

When the sandbox account is ready, submit a schema V2 manifest and persist the returned proof. Submission creates the owner Approval and **Review & publish payment setup** action. For a loopback sandbox manifest, that Approval cannot be published until the daemon registers its local signing key. Immediately start `zen commerce dev --manifest boardly-commerce.json -- <site command>` from the same Task Run, keep the wrapper in the foreground, and keep the Run active while the owner publishes and tests. Do not end the Run while the signing key is missing. When resuming an unpublished loopback manifest, restart the wrapper before directing the owner to the Approval.

For a non-loopback manifest, serve the returned proof plus the production signer public JWK and call status once. If status is not `active`, preserve the handoff and end the Run without asking the user to type “done”.

Sandbox event reconciliation is Zenith Team-owned. Do not install Stripe CLI, create a tunnel, expose a website webhook, or copy a signing secret. A redirect is never payment proof.

After deployment, create a separate live manifest with the exact production HTTPS origin, catalog, paths, and production signer. Never reuse a sandbox manifest for live.
