#!/usr/bin/env node
// pnpm 9's JSON audit exits 1 for lower-severity findings too. Parse its
// complete report so the policy is explicit, without hiding registry failures.
import { spawnSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";

const [scope, output] = process.argv.slice(2);
if (!["all", "production"].includes(scope) || !output) {
  console.error("Usage: check_npm_dependencies.mjs <all|production> <report.json>");
  process.exit(2);
}

const args = ["--dir", "frontend", "audit", "--json"];
if (scope === "production") args.push("--prod");
const result = spawnSync("pnpm", args, {
  encoding: "utf8",
  timeout: 180_000,
  maxBuffer: 16 * 1024 * 1024,
});
mkdirSync(dirname(resolve(output)), { recursive: true });
writeFileSync(output, result.stdout || "");
if (result.stderr) process.stderr.write(result.stderr);

try {
  if (result.error || ![0, 1].includes(result.status)) {
    throw new Error(`pnpm audit did not complete (exit ${result.status})`);
  }
  const report = JSON.parse(result.stdout);
  const counts = report.metadata?.vulnerabilities;
  const advisories = report.advisories;
  const severities = ["info", "low", "moderate", "high", "critical"];
  if (
    report.error ||
    !counts ||
    severities.some((level) => !Number.isSafeInteger(counts[level]) || counts[level] < 0) ||
    !advisories ||
    typeof advisories !== "object" ||
    Array.isArray(advisories)
  ) {
    throw new Error("pnpm returned an incomplete or failed audit response");
  }
  const findings = Object.values(advisories);
  if (findings.some((finding) => !severities.includes(finding?.severity))) {
    throw new Error("pnpm returned an unknown advisory severity");
  }
  for (const level of severities) {
    if (findings.some((finding) => finding.severity === level) !== (counts[level] > 0)) {
      throw new Error("pnpm advisory records and severity totals disagree");
    }
  }
  if (result.status === 1 && findings.length === 0) {
    throw new Error("pnpm failed without a usable advisory report");
  }
  console.log(`${scope} dependency advisories: ${JSON.stringify(counts)}`);
  const blocking =
    counts.high + counts.critical > 0 ||
    findings.some((finding) => ["high", "critical"].includes(finding.severity));
  process.exitCode = blocking ? 1 : 0;
} catch (error) {
  console.error(`Dependency scan failed: ${error.message}`);
  process.exitCode = 2;
}
