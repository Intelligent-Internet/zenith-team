# Security checklist

Complete every item before committing:

- [ ] Missing Commerce configuration fails only Commerce routes with `commerce_setup_required`; unrelated pages still run.
- [ ] Checkout, Portal, and entitlement routes require the Site's current customer session and same-origin/CSRF protections.
- [ ] Portal requires an authentication root no more than five minutes old; stale sessions use the Site's own step-up or reauthentication flow and rotate `sessionId` before retrying.
- [ ] `getCustomerContext()` reads trusted server session state; browser input cannot select subject, session, authentication time, or methods.
- [ ] Checkout accepts only a code-owned `offerAlias`; Portal and entitlement refresh accept no caller authority.
- [ ] Checkout and Portal obtain a one-use challenge; an ambiguous retry preserves the body and operation ID but obtains a fresh challenge and fresh JWT.
- [ ] Entitlement refresh signs exact `{}` bytes without a challenge and obtains a fresh JWT for a retry.
- [ ] Site JWTs pin ES256, `typ=boardly-site-request+jwt`, the registered RFC 7638 key ID, Site ID, environment, origin, customer, session, method, path, operation, and body digest.
- [ ] Site JWT lifetime is at most 60 seconds; challenges are short-lived and consumed atomically by Zenith Team.
- [ ] Local development invokes `zen commerce dev --manifest boardly-commerce.json -- npm run dev` only from the current Zenith Team Task's agent shell; an operator-shell or standalone invocation fails closed.
- [ ] The `zen commerce dev` wrapper stays in the foreground and the Task Run stays active for the whole operator test; it is never backgrounded, detached, run with `nohup`, or replaced by its child Site command, and an orphaned Site is stopped instead of reported ready.
- [ ] The Task bootstrap exists only in that Run's isolated `BOARDLY_HOME`, is mode `0600` where supported, is never copied into the supervised Site's separate `BOARDLY_HOME`, and is revoked and removed when the Run ends. No global Commerce control or master capability exists.
- [ ] The local P-256 private key exists only in daemon memory. The short-lived signer lease capability is absent from files, logs, errors, request bodies, and browser bundles and is revoked when its supervised Site process ends.
- [ ] Hosted signing uses a production KMS/HSM or secret-manager adapter; private key material is server-only, the next key is synchronized before use, and Zenith Team retains the prior key for overlap.
- [ ] The exact returned proof is served at the exact verification path together with exactly one current or next public JWK.
- [ ] The browser never calls Zenith Team or Stripe directly and never receives a private key, signer capability, Commerce assertion, or provider identifier.
- [ ] Redirects lead to a processing page and never grant an entitlement.
- [ ] Assertions are stored only in the authenticated server session or a host-only Secure, HttpOnly, SameSite cookie.
- [ ] `verifySession()` binds assertions to the exact environment, Site ID, origin, customer subject, and Site session ID before `hasActiveEntitlement()` authorizes access.
- [ ] Missing or expired assertions and high-risk mutations refresh online; ordinary reads accept no more than five minutes of revocation staleness.
- [ ] Public JWKS comes only from `BOARDLY_COMMERCE_API_URL`; JWT-selected algorithms or key URLs are rejected.
- [ ] Sandbox and live use separate manifests, exact canonical origins, and distinct environment-appropriate signing keys.
- [ ] Hosted Checkout or Portal URLs, authorization headers, raw payment responses, and bearer credentials are absent from logs, analytics, exceptions, snapshots, and rendered HTML.
- [ ] Status was checked once and an immutable draft is not described as active.

Scan tracked and generated text for payment secrets and direct-provider authority. At minimum, review every match for:

```text
sk_
rk_
whsec_
STRIPE_SECRET
STRIPE_SECRET_KEY
stripe.checkout.sessions.create
new Stripe(
priceId
customerId
returnUrl
BOARDLY_COMMERCE_SIGNER_CAPABILITY
```

The capability name in the server-only environment template and adapter is expected; populated values, browser imports, logging, direct Stripe calls, and caller-controlled provider IDs are not.
