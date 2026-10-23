#!/usr/bin/env python3
"""Query OSV for the exact voice lockfile versions without installing them."""

import argparse
import datetime
import json
from pathlib import Path
import sys
import tomllib
import urllib.request


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lockfile", type=Path, default=Path("voice-agent/uv.lock"))
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report = {
        "checkedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "endpoint": "https://api.osv.dev/v1/querybatch",
        "lockfile": str(args.lockfile),
    }
    try:
        with args.lockfile.open("rb") as handle:
            lock = tomllib.load(handle)
        unscannable = [
            package["name"]
            for package in lock["package"]
            if package.get("source") != {"registry": "https://pypi.org/simple"}
            and not (
                package["name"] == "zenith-helia"
                and package.get("source") == {"virtual": "."}
            )
        ]
        if unscannable:
            raise ValueError(
                "Lockfile has sources requiring a separate review: "
                + ", ".join(unscannable)
            )
        packages = [
            package
            for package in lock["package"]
            if "registry" in package.get("source", {})
        ]
        if not packages:
            raise ValueError("No registry packages found; refusing an empty scan")
        payload = {
            "queries": [
                {
                    "package": {"name": package["name"], "ecosystem": "PyPI"},
                    "version": package["version"],
                }
                for package in packages
            ]
        }
        request = urllib.request.Request(
            report["endpoint"],
            data=json.dumps(payload).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=45) as response:
            body = response.read(16 * 1024 * 1024 + 1)
        if len(body) > 16 * 1024 * 1024:
            raise ValueError("OSV response exceeded the 16 MiB limit")
        result = json.loads(body)
        results = result.get("results")
        if not isinstance(results, list) or len(results) != len(packages):
            raise ValueError("OSV returned an incomplete batch response")
        report["packagesQueried"] = len(packages)
        report["results"] = []
        for package, findings in zip(packages, results, strict=True):
            if not isinstance(findings, dict) or "error" in findings:
                raise ValueError("OSV returned a failed package query")
            page_token = findings.get("next_page_token", "")
            if not isinstance(page_token, str) or page_token:
                raise ValueError("OSV paginated a result; refusing an incomplete scan")
            vulnerabilities = findings.get("vulns", [])
            if not isinstance(vulnerabilities, list):
                raise ValueError("OSV returned an invalid vulnerability list")
            report["results"].append(
                {
                    "name": package["name"],
                    "version": package["version"],
                    "vulns": vulnerabilities,
                }
            )
        affected = [entry for entry in report["results"] if entry["vulns"]]
        report["affectedPackages"] = len(affected)
        for entry in affected:
            print(
                f"{entry['name']}=={entry['version']}: "
                + ", ".join(item["id"] for item in entry["vulns"])
            )
        print(f"Queried {len(packages)} locked packages; {len(affected)} affected")
        return 1 if affected else 0
    except Exception as error:
        report["error"] = str(error)
        print(f"Voice dependency scan failed: {error}", file=sys.stderr)
        return 2
    finally:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    sys.exit(main())
