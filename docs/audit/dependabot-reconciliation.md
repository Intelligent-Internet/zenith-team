# Dependabot and current-lockfile reconciliation

**The 46 open GitHub alerts do not represent 46 affected dependencies in the current lockfile.** At 2026-10-23 11:55 UTC, 40 alert records named packages whose locked versions are outside their affected ranges, five concerned packages absent from the lockfile, and one still applied by version: uuid 8.3.2. This agrees with the live npm audit's one moderate finding. No alert was dismissed and no dependency or lockfile was changed during this reconciliation.

GitHub's remote `main` was `cdce9db946b739c0068bdd2faff7192bca48931a`. Its lockfile was downloaded directly through the GitHub contents API and was byte-identical to the assessed local file: SHA-256 `1e5995d6402c702fca5ebff43c4abbf116030ed667e00c2930f8690c175b7905`. This is not a comparison of an unmerged dependency update against an older default branch.

## What the alert records actually contain

The refreshed read-only API query still returned 46 open records: one critical, 35 high, nine medium and one low. These are **41 unique GHSA advisories**, because five brace-expansion advisories each have separate records for its 1.x and 2.x version lines. Only one advisory is withdrawn: [sprintf-js / CVE-2026-97058](https://github.com/advisories/GHSA-hp3w-g68c-fv3c), withdrawn on October 22; that package is also absent. Mass withdrawal does not explain the discrepancy.

The API's `dependency.version_requirement` fields name older exact versions, including follow-redirects 1.16.0 and tar 6.2.1. All 46 alert records were created on October 21, 07:25–07:26 UTC, and their `updated_at` fields still have those values. The table compares every record with the packages actually present in the current lockfile.

| Package | Version recorded by GitHub alerts | Current locked versions | Alert records | Current version result |
| --- | --- | --- | ---: | --- |
| baseline-browser-mapping | 2.10.42 | 2.11.31 | 1 | Outside affected range |
| brace-expansion | 1.1.16, 2.1.2 | 1.1.21, 2.1.7, 5.0.12 | 10 | Outside affected ranges |
| braces | 3.0.3 | Absent | 1 | Package absent |
| browserslist | 4.28.5 | 4.29.4 | 2 | Outside affected ranges |
| extract-zip | 2.0.1 | Absent | 2 | Package absent |
| fast-uri | 3.1.3 | 3.1.9 | 8 | Outside affected ranges |
| follow-redirects | 1.16.0 | 1.16.1 | 2 | Outside affected ranges |
| http-cache-semantics | 4.2.0 | Absent | 1 | Package absent |
| image-size | 0.7.5 | 2.0.4 | 1 | Outside affected range |
| js-yaml | 4.3.1 | 4.3.2 | 1 | Outside affected range |
| source-map-js | 1.2.1 | 1.2.2 | 1 | Outside affected range |
| sprintf-js | 1.1.3 | Absent | 1 | Package absent; advisory withdrawn |
| tar | 6.2.1 | 7.5.22 | 12 | Outside affected ranges |
| tmp | 0.0.33 | 0.2.7 | 2 | Outside affected ranges |
| uuid | 8.3.2 | 8.3.2, 14.0.3 | 1 | 8.3.2 remains in affected range |

In particular, the critical alert is [node-tar decompression/parse denial of service, CVE-2026-59873](https://github.com/advisories/GHSA-23hp-3jrh-7fpw), fixed in 7.5.19; the current lock has 7.5.22. The recent [follow-redirects alert](https://github.com/advisories/GHSA-q3jh-wrc6-jgq8) is fixed in the locked 1.16.1, and the [source-map-js alert](https://github.com/advisories/GHSA-68fv-2mgg-jv7q) is fixed in the locked 1.2.2.

[Per-alert evidence](evidence/dependabot-reconciliation.json) includes public advisory identifiers, affected ranges, patched versions, withdrawal status, reported and locked versions, and classification. Ranges were evaluated using the already-installed npm `semver` 7.8.5, not an improvised numeric comparison. No credentials, account data or raw authenticated API responses are included in that evidence.

## Cross-check and gate coverage

A separate OSV batch request tested both sets of versions:

| Query set | Exact package/version pairs | Pairs with advisories | Unique returned advisories |
| --- | ---: | ---: | ---: |
| Current lock versions for the alert package names | 14 | 1 | 1, uuid |
| Older versions named in GitHub alert records | 16 | 15 | 40 |

The 40 advisories returned for the older versions match **exactly** the 40 nonwithdrawn unique GHSAs in GitHub's alert response: none missing and none extra. The one withdrawn sprintf-js advisory was excluded by OSV. OSV consumes GitHub advisory data, so this is an independent version-query cross-check, not an independent vulnerability investigation. [OSV evidence](evidence/dependabot-osv-crosscheck.json).

The evidence supports stale repository alert/dependency-graph state as the explanation. The API does not establish why refresh has lagged; no claim is made about a specific GitHub caching defect. These alerts should be rechecked after GitHub refreshes the dependency graph, with unresolved records investigated against the saved per-alert comparison. Bulk dismissal would conceal the discrepancy and is not justified by this review.

The new CI gate scans the lockfile checked out for that run, covering both all-dependency and production npm scopes. It does not consume this stale list of repository alerts or suppress these advisory IDs. Its explicit policy blocks high/critical findings and retains lower severities. The surviving uuid finding remains documented with the specific ExcelJS v4-only call path in the [dependency assessment](dependencies.md); version matching alone does not establish exploitability or justify declaring it harmless.

This reconciliation requires no new gate exemption or lockfile edit. It does not erase the separate findings in the [container assessment](container-images.md), establish a zero-CVE product, or prove that every vulnerability source has complete coverage.

## Reproduce the comparison

1. Read `repos/Intelligent-Internet/zenith-team/git/ref/heads/main` and download `contents/frontend/pnpm-lock.yaml?ref=main` with GitHub's raw content media type. Compare SHA-256 with the local lockfile.
2. Read `repos/Intelligent-Internet/zenith-team/dependabot/alerts?state=open&per_page=100`; use pagination if there are more than 100 results. Retain only package/version, public advisory metadata and timestamps in shared evidence.
3. Parse the lockfile's `packages` keys. For each alert, test every locked version of its package against `security_vulnerability.vulnerable_version_range` using npm semver, treating comma-separated comparator clauses as conjunctions. Count unique GHSA IDs separately from alert records.
4. Query `https://api.osv.dev/v1/querybatch` with ecosystem `npm` and each exact current and reported package/version pair; reject incomplete or paginated responses. Compare returned IDs with nonwithdrawn GHSA IDs.
5. Run the [existing npm audit wrapper](../../scripts/security/check_npm_dependencies.mjs) for both scopes. The [post-fix live full audit](evidence/security-stress-live-npm-all.json) records the current one-moderate result.
