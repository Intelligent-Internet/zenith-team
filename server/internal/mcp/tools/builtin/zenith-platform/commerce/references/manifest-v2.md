# Zenith Team Commerce manifest V2

A manifest is immutable reviewed catalog policy. It contains no customer identity, secret, private key, provider identifier, or runtime-selected return URL.

For `sandbox`, use the exact canonical development origin. An HTTP origin is allowed only for an exact loopback hostname. A stable HTTPS preview may also be used. For `live`, use the exact production HTTPS origin. Temporary tunnels, private-network origins, and placeholders are invalid.

```json
{
  "schemaVersion": 2,
  "site": {
    "alias": "store",
    "origin": "http://localhost:3000",
    "portalReturnPath": "/account/billing"
  },
  "offers": [
    {
      "alias": "pro_monthly",
      "displayName": "Pro",
      "type": "subscription",
      "planGroup": "pro",
      "entitlement": "plan.pro",
      "amount": 99000,
      "currency": "vnd",
      "interval": "month",
      "automaticTax": false,
      "successPath": "/payment-processing",
      "cancelPath": "/pricing"
    }
  ]
}
```

Rules:

- `schemaVersion` is exactly `2`.
- `site` contains exactly `alias`, `origin`, and `portalReturnPath`.
- Aliases are stable lowercase identifiers. Runtime callers choose only an approved offer alias.
- Amount is a positive integer in currency minor units; currency is exactly three lowercase letters.
- `subscription` requires `planGroup` and `interval`; `one_time` forbids both.
- `interval` is `month` or `year`. Quantity is always one.
- `portalReturnPath`, `successPath`, and `cancelPath` are relative Site paths.
- `successPath` renders processing state. It never grants access from redirect parameters.
- One manifest contains the complete intended catalog for one environment. Sandbox and live use separate submissions.
- A loopback sandbox signing key is registered only from the Zenith Team Task that authored the manifest. Running `zen commerce dev` from an operator shell or another Task is not an activation path.
- A loopback sandbox Approval is not actionable until that Task Run starts `zen commerce dev` and the daemon registers its local public key. Persist the returned proof first, then start the wrapper in the same Run; do not end the Run while the key is missing.

When exact offer policy cannot be derived from approved Product work, ask one consolidated business question after finishing all independent implementation. Never invent price, currency, interval, entitlement, or tax policy.

Submission returns `siteId`, `verificationPath`, and the exact three-field `verificationDocument`. Persist the proof unchanged and serve it at that path with exactly one public signing key: the key being activated, or the next key being synchronized during rotation. Zenith Team retains the prior active public key as `retiring` for the overlap window. Never place private key material or a signer capability in the manifest or verification document.
