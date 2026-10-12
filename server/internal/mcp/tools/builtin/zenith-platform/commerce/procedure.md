
# Zenith Team Commerce

Use this release-owned platform Skill only for customer-facing website commerce. Zenith Team's Commerce Gateway is the payment authority; generated websites never receive Stripe credentials or a shared Zenith Team Site secret.

This procedure (`zenith-platform`, path `commerce/procedure.md`) must already be loaded before any Commerce status call, repository inspection, implementation planning, setup question, or code change. If loading fails, stop the payment work and report that blocker.

## Site-signed boundary

Every Commerce Site must already have its own customer login. The Site maps that authenticated session to a stable `CommerceCustomerContext` and signs each server-to-server Commerce request with a Site-owned P-256 key. Zenith Team verifies the signature and exact approved Site, environment, origin, customer, session, method, path, operation, and body digest. Checkout and Portal additionally consume a short-lived one-use challenge.

The browser calls only the Site's authenticated BFF. It never calls Zenith Team or Stripe directly, never signs a request, and never receives a signing capability, private key, Commerce assertion, or payment-provider identifier.

For local development, run this command only from the current Zenith Team Task's
agent shell:

```sh
zen commerce dev --manifest boardly-commerce.json -- npm run dev
```

The command intentionally fails closed in an ordinary operator shell or any
other context outside a Zenith Team Task. The daemon gives the Task Run a scoped
bootstrap in its isolated `BOARDLY_HOME`, keeps the local P-256 private key
only in daemon memory, and creates a short-lived signer lease bound to that
Task, Company, Site, and origin. The supervised Site process receives the
seven server-only variables documented in `commerce/templates/.env.example` and a
separate empty `BOARDLY_HOME`; it never receives the Task bootstrap. There is
no global Commerce control file or standalone daemon authority. Do not copy or
persist either capability.

Keep `zen commerce dev` in the foreground and keep the current Task Run
active for the entire operator test. Never background, detach, run it through
`nohup`, or start its child Site command separately. The daemon revokes the
signer lease when the supervised command or Run exits. Stop any orphaned Site
process and start the wrapper again from a new active Task Run; never report a
local sandbox as ready after its wrapper or Run has ended.

Use `createBoardlyCommerceSignerFromEnvironment()` locally. For hosted
environments, implement `BoardlyCommerceSigner` with a production KMS, HSM, or secret-manager-backed key.
Keep private material server-only, rotate keys with an overlap window, and
publish the corresponding public JWK at the returned verification path.

## Sandbox-first workflow

1. Call `commerce_account_status` as an early read-only check. Treat Zenith Team's Company-scoped readiness projection as authoritative. An unready account blocks publication and real payment tests, not local implementation.
2. Default local development to `sandbox`; use `live` only when the Task explicitly targets production.
3. Inspect the repository and implement the payment UI, authenticated BFF, processing state, and protected-route denial behavior before asking setup questions.
4. Load `commerce/references/integration-workflow.md`, copy `commerce/templates/boardly-commerce.server.ts` into a server-only module, and use the adapter matching the site's stack.
5. Before activation, use only server-side test doubles confined to tests. After sandbox activation, use Stripe test mode for the real Checkout, processing, entitlement, and Portal flow. Never ship a runtime entitlement bypass.

Do not ask the user to install an identity provider, Stripe CLI, tunnel, webhook forwarder, or signing-secret tooling. Never ask the user whether the Company already has an active Stripe account, which country it uses, or whether charges and payouts are enabled; those facts come from `commerce_account_status`. Missing account readiness, deployment, or owner approval is a recoverable setup handoff, not a Task failure.

## Required workflow

1. Call `commerce_account_status`, record readiness, and continue independent local work.
2. Load `commerce/references/integration-workflow.md` and implement the local application.
3. Load `commerce/templates/boardly-commerce.server.ts`. Configure `BoardlyCommerceConfig` with the exact API URL, Site origin, Site ID, current customer context, and a server-only signer. Initialize it lazily so unrelated pages still run before Commerce is configured.
4. Load `commerce/templates/.env.example` and the stack adapter: `commerce/templates/nextjs-app-router.md` or `commerce/templates/fetch-serverless.md`.
5. Add authenticated, same-origin/CSRF-protected Site routes for Checkout, Customer Portal, and Commerce-session refresh. Require a customer authentication root no more than five minutes old for Portal, using the Site's existing step-up or reauthentication flow when stale. Store assertions only in the existing authenticated server session or a host-only Secure, HttpOnly, SameSite cookie.
6. Load `commerce/templates/processing-page.md`. A redirect means only “processing”; entitlement access starts only after a verified Zenith Team assertion reports an active entitlement.
7. Run the site's existing checks and validate request shapes, denial behavior, retry identity, and protected routes. For an operator payment test, keep the `zen commerce dev` wrapper in the foreground and keep the Task Run active until the operator finishes.
8. If the Task includes environment activation, load `commerce/references/manifest-v2.md`. Resolve exact price, currency, offer policy, entitlement, automatic-tax policy, paths, and canonical origin. When a real business decision remains, call `ask_human` exactly once with one consolidated question, then end the Run without reporting it complete or failed.
9. When the target account is ready and every value is exact, create `boardly-commerce.json` and call `commerce_manifest_submit`.
10. Persist the returned three-field `verificationDocument` unchanged. At the exact returned `verificationPath`, serve those proof fields plus `signingKeys: [signer.publicJwk]` using `createBoardlySiteVerificationDocument()`.
11. For a loopback sandbox manifest, the Approval cannot be published until the daemon registers its local signing key. Immediately start `zen commerce dev --manifest boardly-commerce.json -- <site command>` from this same Task Run after persisting the proof. Keep the wrapper in the foreground, keep the Run active while the owner publishes and tests, and do not end the Run while that signing key is missing. When resuming an unpublished loopback manifest, start the wrapper again before directing the owner to its Approval.
12. For a non-loopback manifest, call `commerce_manifest_status` exactly once. If it is not `active`, preserve the manifest ID, Site ID, digest, approval ID, verification path, and proof, then end the Run. The owner-facing **Review & publish payment setup** action owns that handoff.
13. Load and complete `commerce/references/security-checklist.md`, then report the environment, checks, remaining setup, and manifest identifiers when submitted.

After production deployment, create a separate `live` manifest for its exact HTTPS origin and production signing key. Never reuse a sandbox manifest for live. A changed origin, return path, catalog value, or signing authority requires the Zenith Team review flow appropriate to that change.

## Error-directed recovery

Branch on the returned Commerce `code`, never on HTTP status or `detail`. Preserve the `requestId` when reporting a blocker. Retry only ambiguous transport failures; deterministic provider errors must not be replayed unchanged.

- `commerce_provider_credentials_invalid`: a configured Stripe Commerce restricted key is invalid, deleted, or expired. Stop retrying and ask the Zenith Team operator to rotate the affected key in Zenith Team; never request the key in chat or place it in Site code.
- `commerce_provider_permission_required`: the configured Stripe restricted key lacks authority for the attempted resource. Stop retrying and ask the Zenith Team operator to grant the named Commerce key the required connected-account permission; Checkout needs `Customers: Write` and `Checkout Sessions: Write`, catalog publication needs `Products: Write`, `Prices: Write`, and Customer Portal configuration write access, and Portal launch needs Billing Portal Session write access. Never replace the restricted key with a secret key in Site code.
- `commerce_tax_setup_required`: an approved Offer enables `automaticTax`, but Stripe Tax is not active for the connected account. Stop retrying. The owner must activate Stripe Tax or explicitly approve a revised manifest with a different automatic-tax policy; never silently disable tax.
- `commerce_checkout_tax_address_invalid`: Stripe rejected the Checkout customer tax location. Do not retry or change the operation ID, and do not silently disable automatic tax. Preserve the `requestId` and report the Zenith Team platform integration failure.
- `commerce_provider_request_invalid`: Stripe deterministically rejected Zenith Team's provider request. Do not retry, invent parameters, or bypass the Gateway. Preserve the `requestId` for platform diagnosis.
- `commerce_provider_rate_limited`: wait with backoff, then let the user retry; do not hot-loop.
- `commerce_operation_recovery_required`: Zenith Team cannot safely replay the provider write. Stop and preserve the operation ID and `requestId` for reconciliation.
- `commerce_provider_unavailable`: this is the transient provider class. The server kit may retry once with the same serialized body and operation ID, but with a fresh challenge and signed JWT; after that, surface the typed error.

## Non-negotiable boundaries

- Manifest schema V2 Site fields are exactly `alias`, `origin`, and `portalReturnPath`.
- Checkout accepts only a code-owned `offerAlias`; the website never chooses amount, currency, environment, Site ID, Stripe identifiers, or return URL at runtime.
- Each Checkout or Portal retry preserves the operation ID and exact body, but obtains a fresh challenge and fresh signed JWT because a nonce is consumed once.
- Entitlement refresh signs exact `{}` bytes without a challenge; each retry obtains a fresh signed JWT.
- `getCustomerContext()` reads only the current authenticated server session. Never accept `subject`, `sessionId`, `authenticatedAt`, or `authenticationMethods` from browser input.
- Portal rejects authentication roots older than five minutes with `commerce_reauthentication_required`; reauthentication rotates the Site session ID and updates trusted session metadata before retry.
- Site request JWTs use only ES256, `typ=boardly-site-request+jwt`, the registered RFC 7638 key thumbprint, and a maximum 60-second lifetime.
- A Commerce assertion is a five-minute bearer credential. Bind verification to the exact environment, Site ID, Site origin, customer subject, and Site session ID; never expose it to browser JavaScript, logs, analytics, or an unsigned store.
- Ordinary protected reads may verify an assertion offline. Refresh when missing or expired and immediately before high-risk or irreversible operations.
- Keep signer capabilities, KMS handles, private keys, hosted Checkout/Portal URLs, raw payment responses, and authorization headers out of logs and browser-visible state.
- Never replace the Gateway with direct Stripe code, Payment Links, a shared secret, or browser-only authorization.
