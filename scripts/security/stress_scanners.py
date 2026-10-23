#!/usr/bin/env python3
"""Exercise scanner failure handling offline; never install application packages."""

import argparse
import contextlib
import copy
import datetime
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from unittest import mock
import urllib.error

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    checks = []

    def record(name, actual, expected, **details):
        checks.append({"name": name, "actual": actual, "expected": expected,
                       "passed": actual == expected, **details})

    with tempfile.TemporaryDirectory(prefix="zenith-security-stress-") as directory:
        temp = Path(directory)
        node = shutil.which("node")
        if not node:
            raise RuntimeError("Node.js is required")
        fake = temp / "pnpm"
        fake.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$AUDIT_ARGS"\n'
                        'if [ "$AUDIT_SIGNAL" = 1 ]; then kill -TERM $$; fi\n'
                        '/bin/cat "$AUDIT_FIXTURE"\nexit "$AUDIT_EXIT"\n')
        fake.chmod(0o755)
        clean = {"metadata": {"vulnerabilities": dict.fromkeys(
            ["info", "low", "moderate", "high", "critical"], 0)}, "advisories": {}}
        fixtures = [("clean", clean, 0, 0)]
        for severity in clean["metadata"]["vulnerabilities"]:
            report = copy.deepcopy(clean)
            report["metadata"]["vulnerabilities"][severity] = 1
            report["advisories"]["example"] = {"severity": severity}
            fixtures.append((severity, report, 1, 1 if severity in ["high", "critical"] else 0))
        for name, body in [("malformed-json", "{"), ("null", "null"),
                           ("array", []), ("missing-fields", {}),
                           ("registry-error", {"error": "offline", **clean})]:
            fixtures.append((name, body, 0, 2))
        for name, value in [("negative-count", -1), ("fractional-count", 0.5),
                            ("unsafe-count", 2**53), ("string-count", "0"),
                            ("count-without-record", 1)]:
            report = copy.deepcopy(clean)
            report["metadata"]["vulnerabilities"]["moderate"] = value
            fixtures.append((name, report, 0, 2))
        for name, advisory in [("record-without-count", {"severity": "high"}),
                               ("unknown-severity", {"severity": "urgent"}),
                               ("null-advisory", None)]:
            report = copy.deepcopy(clean)
            report["advisories"]["example"] = advisory
            fixtures.append((name, report, 1, 2))
        repeated = copy.deepcopy(fixtures[3][1])
        repeated["metadata"]["vulnerabilities"]["moderate"] = 2
        fixtures.extend([("repeated-affected-paths", repeated, 1, 0),
                         ("failed-without-findings", clean, 1, 2),
                         ("tool-failed", clean, 2, 2),
                         ("tool-signalled", clean, 0, 2),
                         ("oversized-output", "x" * (17 * 1024 * 1024), 0, 2),
                         ("missing-tool", clean, 0, 2)])
        for name, report, status, expected in fixtures:
            fixture = temp / "npm-response"
            fixture.write_text(report if isinstance(report, str) else json.dumps(report))
            env = {**os.environ, "PATH": str(temp), "AUDIT_FIXTURE": str(fixture),
                   "AUDIT_ARGS": str(temp / "npm-args"), "AUDIT_EXIT": str(status),
                   "AUDIT_SIGNAL": "1" if name == "tool-signalled" else "0"}
            if name == "missing-tool":
                env["PATH"] = str(temp / "missing")
            result = subprocess.run(
                [node, str(ROOT / "scripts/security/check_npm_dependencies.mjs"),
                 "production", str(temp / "npm-output.json")],
                env=env, capture_output=True, text=True, timeout=20, cwd=temp,
            )
            record("npm/" + name, result.returncode, expected)
        record("npm/production-scope", (temp / "npm-args").read_text().splitlines(),
               ["--dir", "frontend", "audit", "--json", "--prod"])
        env["PATH"] = str(temp)
        fixture.write_text(json.dumps(clean))
        result = subprocess.run(
            [node, str(ROOT / "scripts/security/check_npm_dependencies.mjs"),
             "all", str(temp / "npm-output.json")],
            env=env, capture_output=True, text=True, timeout=20, cwd=temp)
        record("npm/all-clean", result.returncode, 0)
        record("npm/all-scope", (temp / "npm-args").read_text().splitlines(),
               ["--dir", "frontend", "audit", "--json"])

        spec = importlib.util.spec_from_file_location(
            "voice_scanner", ROOT / "scripts/security/check_voice_dependencies.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        lock = '[[package]]\nname="sample"\nversion="1.0"\nsource={registry="https://pypi.org/simple"}\n'
        voice_cases = [
            ("clean", lock, {"results": [{}]}, None, 0),
            ("advisory", lock, {"results": [{"vulns": [{"id": "TEST-1"}]}]}, None, 1),
            ("malformed-json", lock, "{", None, 2),
            ("null", lock, "null", None, 2),
            ("array", lock, [], None, 2),
            ("missing-results", lock, {}, None, 2),
            ("wrong-result-count", lock, {"results": []}, None, 2),
            ("wrong-result-type", lock, {"results": {}}, None, 2),
            ("null-package-result", lock, {"results": [None]}, None, 2),
            ("query-error", lock, {"results": [{"error": "offline"}]}, None, 2),
            ("pagination", lock, {"results": [{"next_page_token": "more"}]}, None, 2),
            ("malformed-pagination", lock, {"results": [{"next_page_token": []}]}, None, 2),
            ("null-vulnerabilities", lock, {"results": [{"vulns": None}]}, None, 2),
            ("malformed-advisory", lock, {"results": [{"vulns": [{}]}]}, None, 2),
            ("oversized-response", lock, "x" * (17 * 1024 * 1024), None, 2),
            ("network-failure", lock, {}, urllib.error.URLError("fixture"), 2),
            ("timeout", lock, {}, TimeoutError("fixture"), 2),
            ("http-failure", lock, {}, urllib.error.HTTPError("https://api.osv.dev", 503, "fixture", {}, None), 2),
            ("empty-lock", "package=[]", {}, None, 2),
            ("malformed-lock", "[[package", {}, None, 2),
            ("custom-registry", lock.replace("https://pypi.org/simple", "https://private.example/simple"), {}, None, 2),
            ("git-source", lock.replace('registry="https://pypi.org/simple"', 'git="https://example.invalid/repo"'), {}, None, 2),
            ("local-source", lock.replace('registry="https://pypi.org/simple"', 'directory="../local"'), {}, None, 2),
            ("project-root", lock + '\n[[package]]\nname="zenith-helia"\nversion="0.1"\nsource={virtual="."}\n', {"results": [{}]}, None, 0),
            ("other-virtual-source", lock + '\n[[package]]\nname="other"\nversion="0.1"\nsource={virtual="."}\n', {}, None, 2),
        ]
        no_network = {"empty-lock", "malformed-lock", "custom-registry", "git-source",
                      "local-source", "other-virtual-source"}
        for name, lock_text, response, error, expected in voice_cases:
            lock_path = temp / "uv.lock"
            lock_path.write_text(lock_text)
            output = temp / "voice-output.json"
            body = response if isinstance(response, str) else json.dumps(response)
            with mock.patch.object(sys, "argv", ["scanner", "--lockfile", str(lock_path), "--output", str(output)]), \
                 mock.patch.object(module.urllib.request, "urlopen", side_effect=error,
                                   return_value=io.BytesIO(body.encode())) as network, \
                 contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                status = module.main()
            record("voice/" + name, status, expected)
            evidence = json.loads(output.read_text())
            record("voice/report/" + name, bool(evidence.get("error")) if expected == 2
                   else evidence.get("affectedPackages"), True if expected == 2 else expected)
            if name in no_network:
                record("voice/no-network/" + name, network.call_count, 0)

        # Execute the exact gate body, not a copy of its acceptance logic.
        ruby = shutil.which("ruby")
        if not ruby:
            raise RuntimeError("Ruby (standard YAML parser) is required for gate checks")
        config = json.loads(subprocess.check_output(
            [ruby, "-ryaml", "-rjson", "-e", 'puts JSON.generate(YAML.load_file(ARGV[0]))',
             str(ROOT / ".github/workflows/ci.yml")], text=True))
        jobs = config["jobs"]
        gate = jobs["ci-gate"]
        record("workflow/reusable-call", jobs["dependency-security"].get("uses"), "./.github/workflows/security.yml")
        record("workflow/unconditional-security", "if" in jobs["dependency-security"], False)
        record("workflow/gate-needs-security", "dependency-security" in gate["needs"], True)
        record("workflow/gate-always", gate["if"], "always()")
        record("workflow/gate-security-env", gate["steps"][0]["env"]["SECURITY_RESULT"],
               "${{ needs.dependency-security.result }}")
        record("workflow/gate-results-env", gate["steps"][0]["env"]["RESULTS"],
               "${{ join(needs.*.result, ' ') }}")
        security_config = json.loads(subprocess.check_output(
            [ruby, "-ryaml", "-rjson", "-e", 'puts JSON.generate(YAML.load_file(ARGV[0]))',
             str(ROOT / ".github/workflows/security.yml")], text=True))
        # Ruby's YAML 1.1 parser represents the YAML key `on` as true.
        triggers = security_config.get("on", security_config.get("true", {}))
        record("workflow/reusable-trigger", "workflow_call" in triggers, True)
        record("workflow/read-only-token", security_config["permissions"], {"contents": "read"})
        for job_name in ["go", "frontend"]:
            record(f"workflow/{job_name}-complete-matrix", security_config["jobs"][job_name]["strategy"]["fail-fast"], False)
        record("workflow/go-pipefail-shell", security_config["jobs"]["go"]["defaults"]["run"]["shell"], "bash")
        for security in ["success", "failure", "cancelled", "skipped", ""]:
            for other in ["success", "skipped", "failure", "cancelled"]:
                result = subprocess.run(
                    ["/bin/bash", "-e", "-o", "pipefail", "-c", gate["steps"][0]["run"]],
                    env={**os.environ, "SECURITY_RESULT": security, "RESULTS": f"success {other} {security}"},
                    capture_output=True, text=True, timeout=5)
                record(f"gate/security-{security or 'empty'}/other-{other}", result.returncode,
                       0 if security == "success" and other in ["success", "skipped"] else 1)
        record("go/pipeline-propagates-failure", subprocess.run(
            ["/bin/bash", "-e", "-o", "pipefail", "-c", "exit 3 | /usr/bin/tee /dev/null"],
            capture_output=True).returncode, 3)

    inputs = [".github/workflows/ci.yml", ".github/workflows/security.yml",
              "scripts/security/check_npm_dependencies.mjs",
              "scripts/security/check_voice_dependencies.py", "scripts/security/stress_scanners.py"]
    report = {"checkedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "inputSha256": {path: hashlib.sha256((ROOT / path).read_bytes()).hexdigest()
                              for path in inputs},
              "mode": "offline fixtures; actual scanner and gate code", "checks": checks,
              "passed": sum(check["passed"] for check in checks), "total": len(checks)}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(f"{report['passed']}/{report['total']} security gate stress checks passed")
    for check in checks:
        if not check["passed"]:
            print(json.dumps(check), file=sys.stderr)
    return 0 if report["passed"] == report["total"] else 1


if __name__ == "__main__":
    sys.exit(main())
