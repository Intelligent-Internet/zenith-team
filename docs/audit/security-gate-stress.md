# Dependency security gate stress review

Reviewed on 2026-10-23. The final offline harness passed **116/116 checks** against the actual scanner scripts and the shell body extracted from `ci-gate`. Actionlint 1.7.12 accepted both changed workflows; zizmor 1.30.1 reported no medium/high findings in offline mode. These checks establish failure handling and workflow structure, not absence of vulnerabilities in the product or its images.

## Findings fixed

| Failure condition | Previous behavior | Result after change |
| --- | --- | --- |
| A Python lockfile package uses a private or custom registry | It was queried as a public PyPI package; an empty public result could pass | Only the exact public PyPI source is admitted. Other sources fail before any network request; the named local project root is the sole virtual-source exception. |
| OSV returns an unbounded body or a malformed empty pagination field | Body size was unbounded; a non-string false-like page token could pass | Read at most 16 MiB plus one byte, reject overflow, and reject non-string or nonempty pagination tokens. |
| npm advisory records disagree with severity totals, or counts exceed the safe integer range | Some invalid reports could pass the high/critical policy | Require nonnegative safe integer counts and agreement on presence of each severity. Multiple affected paths per advisory remain valid. |
| The reusable dependency security workflow is skipped | The generic gate rejected failures/cancellations but allowed skips | `ci-gate` explicitly requires `needs.dependency-security.result == success`. Existing optional lanes may still skip. |

The Python custom-registry case was reproduced before the fix with a one-package private registry lockfile and a mocked empty public OSV result: the scanner exited zero. The regression harness now verifies both the nonzero result and **zero network calls** for that source.

## Coverage and workflow compatibility

[The repeatable harness](../../scripts/security/stress_scanners.py) runs offline with temporary fixtures, a fake `pnpm` executable, mocked OSV transport, and the actual gate shell extracted using Ruby's standard YAML parser. It covers clean and advisory-bearing reports; every npm severity; duplicate affected paths; absent tools; subprocess failures and signals; oversized output; malformed JSON, schemas, counts and advisory records; incomplete OSV batches; pagination; HTTP/network/timeouts; unsupported Python sources; and retained error reports. It tests both npm scopes and all 20 combinations of security status and representative other-lane status, including skipped and cancelled jobs. It also checks the real gate environment expressions, reusable caller dependency, `always()` condition, matrix fail-fast settings, and Go pipeline failure propagation.

The local workflow call is unconditional on pull requests, including changes outside the normal path filters. The caller lists the reusable workflow in `ci-gate.needs`; the callee defines `workflow_call`, has `contents: read`, requests no secrets, and uses unique artifact names for each matrix variant. Both matrices use `fail-fast: false`, so one failed variant does not prevent evidence collection from the others. The explicit Bash shell supplies `pipefail` for the Go scan piped through `tee`. These choices match GitHub's [reusable workflow contract](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows) and [workflow syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax).

Fresh-checkout Go scan viability was verified separately in a copy without ignored/generated files: full and minimal `cmd/server` scans passed without the daemon's generated tool assets. The broader minimal test step invokes `make minimal-check MINIMAL_GOTOOLCHAIN=auto`, so its `tools-prepare` prerequisite resolves the verified assets first. [Clean-checkout evidence](evidence/clean-checkout-server-scans.json).

Live smoke checks after the scanner changes queried all 76 locked Python packages with no returned advisories and completed the full npm audit with the previously reviewed moderate uuid advisory and no high/critical findings. The explicit npm policy still permits low/moderate findings; this is not a zero-advisory gate. [npm report](evidence/security-stress-live-npm-all.json), [Python report](evidence/security-stress-live-voice.json).

The full npm matrix variant also runs this offline harness in CI before its live scan and retains its report with the npm artifact, so scanner failure handling is checked on future pull requests and daily runs.

## Reproduce

From the repository root, with Python 3.11+, Node.js and Ruby installed:

```sh
python3 -B scripts/security/stress_scanners.py \
  --output docs/audit/evidence/security-gate-stress.json
uvx zizmor@1.30.1 --offline --strict-collection --min-severity medium \
  --format json .github/workflows/security.yml .github/workflows/ci.yml
docker run --rm -v "$PWD:/repo:ro" -w /repo \
  rhysd/actionlint:1.7.12@sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667 \
  -color .github/workflows/security.yml .github/workflows/ci.yml
```

[Stress evidence](evidence/security-gate-stress.json), [zizmor result](evidence/security-gate-zizmor.json), [actionlint output](evidence/security-workflow-actionlint.txt).

No application packages or lifecycle scripts are installed by the harness, and it writes no bytecode cache into the repository. Local execution does not emulate the GitHub scheduler or prove branch protection settings. Hosted runs and the required-check rule must be verified on the pull request. Zizmor's offline mode does not refresh action advisory metadata. The real 180-second npm timeout and platform-specific resource exhaustion were not waited out; process failure, termination, buffer overflow and job bounds were checked instead. No container image scans or builds were repeated for this gate review; their findings remain in the separate [container assessment](container-images.md).
