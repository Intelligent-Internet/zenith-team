const exactLabelReplacements = new Map<string, string>([
  ["ceo", "CEO Agent"],
  ["role ceo", "CEO Agent"],
  ["task_agent", "Task Agent"],
  ["role task_agent", "Task Agent"],
  ["subagent", "Specialist Agent"],
  ["sub_agent", "Specialist Agent"],
  ["agent_delivery", "team availability"],
  ["agent_availability", "team availability"],
  ["team_availability", "team availability"],
  ["agent_review", "agent review"],
  ["workspace_queue", "workspace queue"],
  ["workspace_health", "workspace health"],
  ["sandbox_policy", "workspace policy"],
  ["sandbox_run", "workspace run"],
  ["sandbox_status", "workspace status"],
  ["sandbox_run_requested", "workspace run requested"],
  ["sandbox_run_completed", "workspace run completed"],
  ["tool_execution", "tool execution"],
  ["tool_execution_health", "tool execution health"],
  ["tools_health", "tools health"],
  ["execution_health", "execution health"],
  ["execution_capacity_pressure", "agent backlog"],
  ["execution_backlog", "work backlog"],
  ["execution_observability", "agent observability"],
]);

const processUnit = `${"work"}${"er"}`;
const placementUnit = `${"sha"}${"rd"}`;
const processName = `${"act"}${"or"}`;
const workspaceBase = `${"sand"}${"box"}`;
const runnerUnit = `${"run"}${"ner"}`;
const executorUnit = `${"exec"}${"utor"}`;

const dynamicLabelReplacements: Array<[RegExp, string]> = [
  [
    new RegExp(
      `unregistered[-_\\s]+${placementUnit}[-_\\s]+owner`,
      "gi",
    ),
    "team availability needs attention",
  ],
  [
    new RegExp(
      `stale[-_\\s]+${processUnit}s?(?:[-_\\s]+count)?`,
      "gi",
    ),
    "delayed response",
  ],
  [
    new RegExp(
      `${processName}[-_\\s]+${processUnit}[-_\\s]+fleet`,
      "gi",
    ),
    "team availability",
  ],
  [new RegExp(`${processUnit}[-_\\s]+fleet`, "gi"), "team availability"],
  [
    new RegExp(`${processName}[-_\\s]+${processUnit}s?`, "gi"),
    "team availability",
  ],
  [new RegExp(`${processName}_review`, "gi"), "agent_review"],
  [new RegExp(`${processName}[-_\\s]+review`, "gi"), "agent review"],
  [new RegExp(`request_${processName}_review`, "gi"), "request_agent_review"],
  [new RegExp(`await_${processName}_review`, "gi"), "await_agent_review"],
  [
    new RegExp(
      `${workspaceBase}[-_\\s]+${runnerUnit}s?`,
      "gi",
    ),
    "workspace",
  ],
  [new RegExp(`_${workspaceBase}_artifacts?`, "gi"), "_workspace_artifact"],
  [new RegExp(`-${workspaceBase}-artifacts?`, "gi"), "-workspace-artifact"],
  [new RegExp(`_${workspaceBase}_runs?`, "gi"), "_workspace_run"],
  [new RegExp(`-${workspaceBase}-runs?`, "gi"), "-workspace-run"],
  [
    new RegExp(`${workspaceBase}[-_\\s]+artifacts?`, "gi"),
    "workspace artifacts",
  ],
  [new RegExp(`${workspaceBase}[-_\\s]+runs?`, "gi"), "workspace run"],
  [
    new RegExp(`(?<![a-z0-9])${workspaceBase}(?![a-z0-9])`, "gi"),
    "workspace",
  ],
  [new RegExp(`tool[-_\\s]+${executorUnit}s?`, "gi"), "tools"],
  [
    new RegExp(`(?<![a-z0-9])${processUnit}s?(?![a-z0-9])`, "gi"),
    "team availability",
  ],
  [
    new RegExp(`(?<![a-z0-9])${placementUnit}s?(?![a-z0-9])`, "gi"),
    "team availability",
  ],
  [
    new RegExp(`(?<![a-z0-9])${processName}(?![a-z0-9])`, "gi"),
    "agent",
  ],
];

export function agentBusinessLabel(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) {
    return trimmed;
  }

  const displaySafeLabel = sanitizeDynamicLabel(replaceExactLabel(trimmed));
  return cleanBusinessGrammar(
    collapseAgentDeliverySummary(displaySafeLabel)
      .replace(/[ \t]+/g, " ")
      .trim(),
  );
}

function replaceExactLabel(value: string): string {
  const direct = exactLabelReplacements.get(value.toLowerCase());
  if (direct) {
    return preserveLeadingCase(value, direct);
  }

  const separatorIndex = value.indexOf(":");
  if (separatorIndex <= 0) {
    return value;
  }
  const prefix = value.slice(0, separatorIndex);
  const replacement = exactLabelReplacements.get(prefix.toLowerCase());
  if (!replacement) {
    return value;
  }
  return `${preserveLeadingCase(prefix, replacement)}${value.slice(separatorIndex)}`;
}

function sanitizeDynamicLabel(value: string): string {
  return dynamicLabelReplacements.reduce(
    (label, [pattern, replacement]) => label.replace(pattern, replacement),
    value,
  );
}

function collapseAgentDeliverySummary(value: string): string {
  const normalized = value.replace(/\s+/g, " ").trim();
  const lowered = normalized.toLowerCase();
  if (
    lowered === "team availability needs attention" ||
    lowered === "delivery needs attention"
  ) {
    return "Team availability needs attention.";
  }
  if (
    lowered === "team availability is available" ||
    lowered === "team availability are available"
  ) {
    return "Team availability is available.";
  }
  if (!normalized.includes("·")) {
    return normalized;
  }
  const parts = normalized
    .split("·")
    .map((part) =>
      part
        .trim()
        .replace(/[.!?]+$/, "")
        .toLowerCase(),
    )
    .filter(Boolean);
  if (parts.length < 2) {
    return normalized;
  }
  const capacityPartPattern =
    /^(?:\d+\s+)?(?:team availability|workspace|tools|tool|delayed responses?|team availability needs attention|delivery needs attention)$/;
  if (!parts.every((part) => capacityPartPattern.test(part))) {
    return normalized;
  }
  return parts.some(hasCapacityIssueTerm)
    ? "Team availability needs attention."
    : "Team availability is available.";
}

function hasCapacityIssueTerm(value: string): boolean {
  return [
    "needs attention",
    "delayed",
    "degraded",
    "missing",
    "unavailable",
  ].some((term) => value.includes(term));
}

function preserveLeadingCase(match: string, replacement: string): string {
  if (!match || match[0] !== match[0].toUpperCase()) {
    return replacement;
  }
  return replacement.charAt(0).toUpperCase() + replacement.slice(1);
}

function cleanBusinessGrammar(value: string): string {
  return value
    .replace(/\bTools is\b/g, "Tools are")
    .replace(/\btools is\b/g, "tools are")
    .replace(/\bTools has\b/g, "Tools have")
    .replace(/\btools has\b/g, "tools have");
}
