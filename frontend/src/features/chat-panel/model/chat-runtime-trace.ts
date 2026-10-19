import type { ChatRuntimeActivity } from "@/shared/chat/types";

const HIDDEN_RUNTIME_TOOLS = new Set([
  "ask_select_option_question",
  "emit_discovery_payload",
  "load_skill_reference",
  "search_skills",
]);

const LEGACY_SUBAGENT_ACTIVITY_NAMES = new Set([
  "interacted",
  "interrupted",
  "started",
]);

const RUNTIME_TOOL_DISPLAY_LABELS: Record<string, string> = {
  emit_company_plan_payload: "Save Company Plan Draft",
  wiki_list: "Browse Company Wiki",
  wiki_read: "Read Company Wiki Page",
  wiki_search: "Search Company Wiki",
  wiki_write: "Write Company Wiki Pages",
};

const RUNTIME_SKILL_DISPLAY_LABELS: Record<string, string> = {
  "company-plan-revision@v1": "Company Plan Revision",
  "founder-discovery@v1": "Founder Discovery",
  "image-gen@v1": "Image Gen",
  "market-research@v1": "Market Research",
};

type RuntimeActivityPosition = ChatRuntimeActivity["position"];
type RuntimeToolActivityMetadata = {
  callId?: string;
  error?: string;
  exitCode?: number;
  input?: string;
  itemId?: string;
  output?: string;
  status?: string;
};

export function runtimeActivityIsInProgress(activity: ChatRuntimeActivity) {
  if (activity.toolName === "subagent_activity") {
    const input = parseRuntimeToolInput(activity.input);
    if (
      (stringValue(input?.activityKind) ?? stringValue(input?.name)) ===
      "interrupted"
    )
      return false;
  }
  const status = activity.status?.toLowerCase();
  if (status) {
    return (
      status === "running" ||
      status === "in_progress" ||
      status === "inprogress"
    );
  }
  return Boolean(
    (activity.callId || activity.itemId) && !activity.output?.trim(),
  );
}

export function visibleRuntimeTools(value: string[] | undefined) {
  return normalizeTraceItems(value).filter(
    (tool) => !HIDDEN_RUNTIME_TOOLS.has(tool),
  );
}

export function normalizeRuntimeToolName(
  toolName: string | undefined,
  category?: string,
  input?: string,
) {
  const normalizedToolName = stripMcpNamespace(toolName?.trim() ?? "");
  if (!normalizedToolName) {
    return undefined;
  }
  if (!LEGACY_SUBAGENT_ACTIVITY_NAMES.has(normalizedToolName)) {
    return normalizedToolName;
  }
  if (category === "subagent") {
    return "subagent_activity";
  }

  const parsedInput = parseRuntimeToolInput(input);
  return stringValue(parsedInput?.name) === normalizedToolName
    ? "subagent_activity"
    : normalizedToolName;
}

export function runtimeActivitiesFromTools(
  tools: string[] | undefined,
  activeSkills: string[] | undefined,
  position: RuntimeActivityPosition = "before_message",
) {
  return visibleRuntimeTools(tools).flatMap((toolName) => {
    const activity = runtimeActivityFromTool(toolName, activeSkills, position);
    return activity ? [activity] : [];
  });
}

export function appendRuntimeToolActivity(
  activities: ChatRuntimeActivity[] | undefined,
  {
    activeSkills,
    callId,
    error,
    exitCode,
    input,
    itemId,
    output,
    position,
    status,
    toolName,
  }: {
    activeSkills?: string[];
    callId?: string;
    error?: string;
    exitCode?: number;
    input?: string;
    itemId?: string;
    output?: string;
    position: RuntimeActivityPosition;
    status?: string;
    toolName: string;
  },
) {
  const activity = runtimeActivityFromTool(toolName, activeSkills, position, {
    callId,
    error,
    exitCode,
    input,
    itemId,
    output,
    status,
  });

  if (!activity) {
    return activities;
  }

  const current = activities ?? [];
  return normalizeRuntimeActivities(mergeRuntimeActivity(current, activity));
}

export function mergeRuntimeToolActivityMetadata(
  activities: ChatRuntimeActivity[] | undefined,
  metadata: RuntimeToolActivityMetadata,
) {
  if (!activities?.length) {
    return activities;
  }

  const activityIndex = activities.findIndex((activity) =>
    isSameRuntimeToolCall(activity, metadata),
  );

  if (activityIndex < 0) {
    return normalizeRuntimeActivities(activities);
  }

  const nextActivities = [...activities];
  nextActivities[activityIndex] = mergeRuntimeActivityFields(
    nextActivities[activityIndex],
    metadata,
  );
  return normalizeRuntimeActivities(nextActivities);
}

export function mergeRuntimeActivities(
  current: ChatRuntimeActivity[] | undefined,
  next: ChatRuntimeActivity[] | undefined,
) {
  if (!current?.length) {
    return normalizeRuntimeActivities(next);
  }
  if (!next?.length) {
    return normalizeRuntimeActivities(current);
  }

  return normalizeRuntimeActivities(
    next.reduce<ChatRuntimeActivity[]>(
      mergeRuntimeActivity,
      normalizeRuntimeActivities(current) ?? [],
    ),
  );
}

export function normalizeRuntimeActivities(
  activities: ChatRuntimeActivity[] | undefined,
) {
  if (!activities?.length) {
    return undefined;
  }

  const normalized = activities.reduce<ChatRuntimeActivity[]>(
    mergeRuntimeActivity,
    [],
  );
  return normalized.length ? normalized : undefined;
}

function mergeRuntimeActivity(
  current: ChatRuntimeActivity[],
  activity: ChatRuntimeActivity,
) {
  const matchingIndex = current.findIndex(
    (item) =>
      item.id === activity.id ||
      isSameRuntimeToolCall(item, {
        callId: activity.callId,
        itemId: activity.itemId,
      }),
  );

  if (matchingIndex >= 0) {
    const next = [...current];
    next[matchingIndex] = mergeRuntimeActivityFields(
      next[matchingIndex],
      activity,
    );
    return next;
  }

  if (isGenericLoadSkillActivity(activity)) {
    return current.some(isNamedLoadSkillActivity)
      ? current
      : [...current, activity];
  }

  if (isNamedLoadSkillActivity(activity)) {
    const genericIndex = current.findIndex(isGenericLoadSkillActivity);
    if (genericIndex >= 0) {
      const next = [...current];
      next[genericIndex] = {
        ...activity,
        position: current[genericIndex].position,
      };
      return next;
    }
  }

  if (isUnidentifiedToolDetailActivity(activity)) {
    const detailIndex = findBestDetailedToolActivityIndex(current, activity);
    if (detailIndex >= 0) {
      const currentActivity = current[detailIndex];
      if (!currentActivity) {
        return current;
      }
      const next = [...current];
      next[detailIndex] = mergeRuntimeActivityFields(currentActivity, activity);
      return next;
    }
  }

  if (isGenericToolActivity(activity)) {
    return current.some((item) => isDetailedSameToolActivity(item, activity))
      ? current
      : [...current, activity];
  }

  if (isDetailedToolActivity(activity)) {
    const genericIndex = current.findIndex(
      (item) =>
        isGenericToolActivity(item) && item.toolName === activity.toolName,
    );
    if (genericIndex >= 0) {
      const next = [...current];
      next[genericIndex] = {
        ...activity,
        position: current[genericIndex].position,
      };
      return next;
    }
  }

  return [...current, activity];
}

function normalizeTraceItems(value: string[] | undefined) {
  if (!value?.length) {
    return [];
  }

  const seen = new Set<string>();
  return value.flatMap((item) => {
    const text = item.trim();
    if (!text || seen.has(text)) {
      return [];
    }
    seen.add(text);
    return [text];
  });
}

function runtimeActivityFromTool(
  toolName: string,
  activeSkills: string[] | undefined,
  position: RuntimeActivityPosition,
  metadata: RuntimeToolActivityMetadata = {},
): ChatRuntimeActivity | undefined {
  const normalizedToolName = normalizeRuntimeToolName(
    toolName,
    undefined,
    metadata.input,
  );

  if (!normalizedToolName || HIDDEN_RUNTIME_TOOLS.has(normalizedToolName)) {
    return undefined;
  }

  const skillName =
    latestSkillName(activeSkills) ??
    (normalizedToolName === "load_skill"
      ? skillNameFromLoadSkillInput(metadata.input)
      : undefined);

  if (normalizedToolName === "load_skill" && !skillName) {
    return undefined;
  }

  return {
    id: runtimeActivityId(normalizedToolName, skillName, metadata),
    label: runtimeToolLabel(normalizedToolName, skillName),
    position,
    toolName: normalizedToolName,
    type: "tool",
    ...(metadata.callId ? { callId: metadata.callId } : {}),
    ...(normalizedToolName !== "load_skill" && metadata.error
      ? { error: metadata.error }
      : {}),
    ...(normalizedToolName !== "load_skill" && metadata.exitCode !== undefined
      ? { exitCode: metadata.exitCode }
      : {}),
    ...(metadata.input ? { input: metadata.input } : {}),
    ...(metadata.itemId ? { itemId: metadata.itemId } : {}),
    ...(normalizedToolName !== "load_skill" && metadata.output
      ? { output: metadata.output }
      : {}),
    ...(metadata.status ? { status: metadata.status } : {}),
  };
}

function runtimeActivityId(
  toolName: string,
  skillName: string | undefined,
  metadata: RuntimeToolActivityMetadata,
) {
  if (toolName === "load_skill" && skillName) {
    return ["tool", toolName, skillName].join(":");
  }
  if (metadata.callId) {
    return ["tool", toolName, "call", metadata.callId].join(":");
  }
  if (metadata.itemId) {
    return ["tool", toolName, "item", metadata.itemId].join(":");
  }
  return ["tool", toolName, skillName ?? "none"].join(":");
}

function runtimeToolLabel(toolName: string, skillName: string | undefined) {
  if (toolName === "load_skill") {
    return skillName
      ? `Load skill ${runtimeSkillDisplayLabel(skillName)}`
      : "Load skill";
  }

  return `Use tool ${humanizeToolName(toolName)}`;
}

export function runtimeSkillDisplayLabel(skillName: string) {
  const normalizedSkillName = skillName.trim();
  if (!normalizedSkillName) {
    return "";
  }

  return (
    RUNTIME_SKILL_DISPLAY_LABELS[normalizedSkillName] ??
    humanizeRuntimeName(normalizedSkillName.replace(/@v\d+$/i, ""))
  );
}

function humanizeToolName(toolName: string) {
  return RUNTIME_TOOL_DISPLAY_LABELS[toolName] ?? humanizeRuntimeName(toolName);
}

function humanizeRuntimeName(value: string) {
  return value
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((part) => `${part.slice(0, 1).toUpperCase()}${part.slice(1)}`)
    .join(" ");
}

// The MCP server namespace is a technical identifier, not display prose. Drop
// the Claude form (`mcp__boardly__ask_human`) and the Codex forms
// (`boardly/ask_human`, `boardly.ask_human`) so the hidden set, the display
// labels and the humanizer all see the bare tool name.
function stripMcpNamespace(toolName: string) {
  return toolName
    .replace(/^mcp__[a-z0-9-]+__/i, "")
    .replace(/^boardly[/.]/i, "");
}

function latestSkillName(activeSkills: string[] | undefined) {
  const normalizedSkills = activeSkills?.filter(Boolean) ?? [];
  return normalizedSkills[normalizedSkills.length - 1];
}

function skillNameFromLoadSkillInput(input: string | undefined) {
  const payload = parseRuntimeToolInput(input);
  return stringValue(payload?.skill_name) ?? stringValue(payload?.skillName);
}

function parseRuntimeToolInput(input: string | undefined) {
  if (!input) {
    return undefined;
  }

  try {
    const payload = JSON.parse(input) as unknown;
    return isRecord(payload) ? payload : undefined;
  } catch {
    return undefined;
  }
}

function isGenericLoadSkillActivity(activity: ChatRuntimeActivity) {
  return (
    activity.toolName === "load_skill" && activity.id === "tool:load_skill:none"
  );
}

function isNamedLoadSkillActivity(activity: ChatRuntimeActivity) {
  return (
    activity.toolName === "load_skill" && activity.id !== "tool:load_skill:none"
  );
}

function isGenericToolActivity(activity: ChatRuntimeActivity) {
  return !isDetailedToolActivity(activity);
}

function isDetailedToolActivity(activity: ChatRuntimeActivity) {
  return Boolean(
    activity.callId ||
    activity.itemId ||
    activity.input ||
    activity.output ||
    activity.error ||
    activity.exitCode !== undefined,
  );
}

function isDetailedSameToolActivity(
  current: ChatRuntimeActivity,
  next: ChatRuntimeActivity,
) {
  return isDetailedToolActivity(current) && current.toolName === next.toolName;
}

function isUnidentifiedToolDetailActivity(activity: ChatRuntimeActivity) {
  return Boolean(
    !activity.callId && !activity.itemId && isDetailedToolActivity(activity),
  );
}

function findBestDetailedToolActivityIndex(
  current: ChatRuntimeActivity[],
  activity: ChatRuntimeActivity,
) {
  const sameToolDetailedIndexes: number[] = [];
  current.forEach((item, index) => {
    if (isDetailedSameToolActivity(item, activity)) {
      sameToolDetailedIndexes.push(index);
    }
  });

  if (!sameToolDetailedIndexes.length) {
    return -1;
  }

  if (activity.output) {
    const withoutOutputIndex = firstMatchingActivityIndex(
      sameToolDetailedIndexes,
      current,
      (item) => !item.output,
    );
    if (withoutOutputIndex !== undefined) {
      return withoutOutputIndex;
    }
  }

  if (activity.input) {
    const withoutInputIndex = firstMatchingActivityIndex(
      sameToolDetailedIndexes,
      current,
      (item) => !item.input,
    );
    if (withoutInputIndex !== undefined) {
      return withoutInputIndex;
    }
  }

  return sameToolDetailedIndexes.length === 1
    ? (sameToolDetailedIndexes[0] ?? -1)
    : -1;
}

function firstMatchingActivityIndex(
  indexes: number[],
  activities: ChatRuntimeActivity[],
  predicate: (activity: ChatRuntimeActivity) => boolean,
) {
  for (const index of indexes) {
    const activity = activities[index];
    if (activity && predicate(activity)) {
      return index;
    }
  }
  return undefined;
}

function mergeRuntimeActivityFields(
  current: ChatRuntimeActivity,
  next: RuntimeToolActivityMetadata & Partial<ChatRuntimeActivity>,
) {
  if (current.toolName === "load_skill") {
    return {
      ...current,
      ...(next.label ? { label: next.label } : {}),
      ...(next.callId ? { callId: next.callId } : {}),
      ...(next.input ? { input: next.input } : {}),
      ...(next.itemId ? { itemId: next.itemId } : {}),
      ...(next.status ? { status: next.status } : {}),
    };
  }

  const merged = {
    ...current,
    ...(next.label ? { label: next.label } : {}),
    ...(next.callId ? { callId: next.callId } : {}),
    ...(next.error ? { error: next.error } : {}),
    ...(next.exitCode !== undefined ? { exitCode: next.exitCode } : {}),
    ...(next.input ? { input: next.input } : {}),
    ...(next.itemId ? { itemId: next.itemId } : {}),
    ...(next.output ? { output: next.output } : {}),
    ...(next.status ? { status: next.status } : {}),
  };
  return {
    ...merged,
    id: runtimeActivityId(merged.toolName, undefined, {
      callId: merged.callId,
      itemId: merged.itemId,
    }),
  };
}

function isSameRuntimeToolCall(
  activity: ChatRuntimeActivity,
  metadata: RuntimeToolActivityMetadata,
) {
  return Boolean(
    (metadata.callId && activity.callId === metadata.callId) ||
    (metadata.itemId && activity.itemId === metadata.itemId),
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringValue(value: unknown) {
  if (typeof value !== "string") {
    return undefined;
  }
  const normalized = value.trim();
  return normalized || undefined;
}
