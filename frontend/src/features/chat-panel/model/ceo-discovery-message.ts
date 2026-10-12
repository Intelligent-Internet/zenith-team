import type {
  CeoDiscoveryBlock,
  CeoDiscoveryMessagePayload,
  CeoDiscoveryOptionGroupBlock,
  ChatMessage,
} from "@/shared/chat/types";
import type { JsonObject } from "@/shared/api/types";

export const CEO_DISCOVERY_OTHER_OPTION_ID = "other";
export const CEO_DISCOVERY_OTHER_OPTION_LABEL = "Other";

const DISCOVERY_FLOW_STEPS = new Set([
  "intro",
  "idea_capture",
  "audience_question",
  "launch_question",
  "stage_question",
  "assets_question",
  "mode_recommendation",
  "ready_to_generate_plan",
]);

export function parseCeoDiscoveryMessagePayload(
  payload: JsonObject,
): CeoDiscoveryMessagePayload | null {
  if (
    payload.type !== "ceo_discovery_message" ||
    typeof payload.message !== "string" ||
    typeof payload.flow_step !== "string" ||
    !DISCOVERY_FLOW_STEPS.has(payload.flow_step)
  ) {
    return null;
  }

  const blocks = Array.isArray(payload.blocks)
    ? payload.blocks.flatMap((block) => {
        const parsed = parseDiscoveryBlock(block);
        return parsed ? [parsed] : [];
      })
    : [];
  const flowStep = payload.flow_step as CeoDiscoveryMessagePayload["flow_step"];
  const productVisionSnapshot = parseProductVisionSnapshot(
    payload.product_vision_snapshot,
    flowStep,
  );

  if (flowStep === "ready_to_generate_plan" && productVisionSnapshot === undefined) {
    return null;
  }

  return {
    type: "ceo_discovery_message",
    message: payload.message,
    flow_step: flowStep,
    blocks,
    business_context_patch: asRecord(payload.business_context_patch),
    clarity_score: asOptionalNumber(payload.clarity_score),
    completeness_score: asOptionalNumber(payload.completeness_score),
    product_vision_snapshot: productVisionSnapshot,
    runtime_trace: parseRuntimeTrace(payload.runtime_trace),
  };
}

export function nextCeoDiscoveryOptionSelection(
  currentSelection: readonly string[],
  optionId: string,
  selectionMode: CeoDiscoveryOptionGroupBlock["selection_mode"],
  exclusiveOptionIds: readonly string[] = [],
): string[] {
  const exclusiveOptionIdSet = new Set(exclusiveOptionIds);

  if (exclusiveOptionIdSet.has(optionId)) {
    return currentSelection.includes(optionId) ? [] : [optionId];
  }

  const selectionWithoutExclusiveOptions = currentSelection.filter(
    (currentOptionId) => !exclusiveOptionIdSet.has(currentOptionId),
  );

  if (selectionMode === "single") {
    return selectionWithoutExclusiveOptions.includes(optionId) ? [] : [optionId];
  }

  return selectionWithoutExclusiveOptions.includes(optionId)
    ? selectionWithoutExclusiveOptions.filter(
        (currentOptionId) => currentOptionId !== optionId,
      )
    : [...selectionWithoutExclusiveOptions, optionId];
}

export function hasFollowingUserMessage(
  messages: readonly Pick<ChatMessage, "author">[],
  messageIndex: number,
): boolean {
  return messages.some(
    (message, index) => index > messageIndex && message.author === "user",
  );
}

export function normalizeCeoDiscoveryOptionGroupOptions(
  options: readonly CeoDiscoveryOptionGroupBlock["options"][number][],
): CeoDiscoveryOptionGroupBlock["options"] {
  if (options.some(isCeoDiscoveryOtherOption)) {
    return [...options];
  }

  return [
    ...options,
    {
      id: CEO_DISCOVERY_OTHER_OPTION_ID,
      label: CEO_DISCOVERY_OTHER_OPTION_LABEL,
    },
  ];
}

export function ceoDiscoveryOtherOptionIds(
  options: readonly CeoDiscoveryOptionGroupBlock["options"][number][],
): string[] {
  return options.filter(isCeoDiscoveryOtherOption).map((option) => option.id);
}

export function isCeoDiscoveryOtherOption(
  option: CeoDiscoveryOptionGroupBlock["options"][number],
) {
  return (
    option.id.trim().toLowerCase() === CEO_DISCOVERY_OTHER_OPTION_ID ||
    option.label.trim().toLowerCase() ===
      CEO_DISCOVERY_OTHER_OPTION_LABEL.toLowerCase()
  );
}

function parseDiscoveryBlock(value: unknown): CeoDiscoveryBlock | null {
  if (!isRecord(value) || typeof value.kind !== "string") {
    return null;
  }

  if (value.kind === "paragraph" || value.kind === "question") {
    return typeof value.text === "string"
      ? { kind: value.kind, text: value.text }
      : null;
  }

  if (value.kind === "summary_cards" || value.kind === "ranked_cards") {
    const cards = Array.isArray(value.cards)
      ? value.cards.filter((card): card is string => typeof card === "string")
      : [];
    return cards.length > 0 ? { kind: value.kind, cards } : null;
  }

  if (value.kind === "option_group") {
    const options = Array.isArray(value.options)
      ? value.options.flatMap((option) => {
          if (!isRecord(option)) {
            return [];
          }
          return typeof option.id === "string" && typeof option.label === "string"
            ? [
                {
                  id: option.id,
                  label: option.label,
                  ...(option.recommended === true ? { recommended: true } : {}),
                },
              ]
            : [];
        })
      : [];

    if (
      typeof value.id !== "string" ||
      typeof value.label !== "string" ||
      (value.selection_mode !== "single" && value.selection_mode !== "multi") ||
      value.send_behavior !== "manual" ||
      options.length === 0
    ) {
      return null;
    }

    return {
      kind: "option_group",
      id: value.id,
      label: value.label,
      selection_mode: value.selection_mode,
      send_behavior: "manual",
      initial_selected_option_ids: [],
      options: normalizeCeoDiscoveryOptionGroupOptions(options),
    };
  }

  if (
    value.kind === "mode_selector" &&
    (value.recommended_mode === "light" || value.recommended_mode === "full")
  ) {
    return {
      kind: "mode_selector",
      recommended_mode: value.recommended_mode,
    };
  }

  return null;
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return isRecord(value) ? value : undefined;
}

function asRecordOrNull(value: unknown): Record<string, unknown> | null | undefined {
  if (value === null) {
    return null;
  }
  return asRecord(value);
}

function parseProductVisionSnapshot(
  value: unknown,
  flowStep: CeoDiscoveryMessagePayload["flow_step"],
): Record<string, unknown> | null | undefined {
  const snapshot = asRecordOrNull(value);

  if (flowStep !== "ready_to_generate_plan") {
    return snapshot;
  }

  if (!snapshot || !Array.isArray(snapshot.open_questions)) {
    return undefined;
  }

  const hasOnlyNonBlankStringQuestions = snapshot.open_questions.every(
    (question) => typeof question === "string" && question.trim().length > 0,
  );

  return hasOnlyNonBlankStringQuestions ? snapshot : undefined;
}

function asOptionalNumber(value: unknown): number | null | undefined {
  return typeof value === "number" || value === null ? value : undefined;
}

export function parseRuntimeTrace(
  value: unknown,
): CeoDiscoveryMessagePayload["runtime_trace"] {
  if (!isRecord(value)) {
    return undefined;
  }

  return {
    active_skills: asStringList(value.active_skills),
    tool_activities: asRuntimeToolActivities(value.tool_activities),
    used_tools: asStringList(value.used_tools),
  };
}

function asRuntimeToolActivities(
  value: unknown,
): NonNullable<CeoDiscoveryMessagePayload["runtime_trace"]>["tool_activities"] {
  if (!Array.isArray(value)) {
    return undefined;
  }

  const activities = value.flatMap((item) => {
    if (!isRecord(item)) {
      return [];
    }
    const toolName = optionalString(item.tool_name);
    if (!toolName) {
      return [];
    }
    return [
      {
        call_id: optionalString(item.call_id),
        input: optionalString(item.input),
        item_id: optionalString(item.item_id),
        output: optionalString(item.output),
        status: optionalString(item.status),
        tool_name: toolName,
      },
    ];
  });

  return activities.length ? activities : undefined;
}

function optionalString(value: unknown) {
  if (typeof value !== "string") {
    return undefined;
  }
  const normalized = value.trim();
  return normalized || undefined;
}

function asStringList(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }

  const seen = new Set<string>();
  const normalized: string[] = [];
  value.forEach((item) => {
    if (typeof item !== "string") {
      return;
    }
    const text = item.trim();
    if (!text || seen.has(text)) {
      return;
    }
    normalized.push(text);
    seen.add(text);
  });
  return normalized;
}
