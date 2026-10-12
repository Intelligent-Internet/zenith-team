# Processing page and protected backend

After Stripe redirects to the manifest's `successPath`, show a neutral processing state and poll the site's authenticated entitlement endpoint. A typical policy is every two seconds for at most sixty seconds.

```ts
type ProcessingResult = "active" | "processing";

export async function waitForEntitlement(
  entitlementKey: string,
  signal: AbortSignal,
): Promise<ProcessingResult> {
  const deadline = Date.now() + 60_000;
  while (!signal.aborted && Date.now() < deadline) {
    const response = await fetch("/api/commerce/entitlements", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
      cache: "no-store",
      credentials: "same-origin",
      signal,
    });
    if (response.ok) {
      const result = await response.json() as {
        entitlements: Array<{ key: string; status: string }>;
      };
      if (result.entitlements.some((item) =>
        item.key === entitlementKey && item.status === "active")) {
        return "active";
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 2_000));
  }
  return "processing";
}
```

The site's entitlement endpoint calls `refreshSession()`, stores the new assertion in the authenticated server session, and returns only the safe entitlement list shown above. On `processing`, display “Payment processing” and offer a safe retry/status refresh. Do not infer denial solely from timeout because asynchronous payment methods and webhook delivery can take longer.

The protected backend is authoritative. Ordinary protected reads load the server-held assertion, call `verifySession()`, and require `hasActiveEntitlement()`; the kit checks ES256, exact environment, Site ID, Site origin, customer subject, Site session ID, and five-minute expiry offline. If the assertion is missing or expired, refresh it once. Refresh immediately before high-risk or irreversible mutations to avoid relying on the bounded five-minute refund/dispute revocation cache. Never trust the processing page, URL parameters, local storage, or a browser-readable client claim.
