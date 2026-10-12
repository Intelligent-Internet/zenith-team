import type { ChatCompanyModeSelection } from "@/features/chat-panel/types";
import type { GeneratedImagePreview } from "@/features/chat-panel/model/generated-image-assets";
import { CEO_CHAT_TEAM_ID } from "@/shared/chat/chat-team";
import type {
  CeoDiscoveryMessagePayload,
  ChatMessage,
} from "@/shared/chat/types";

export function plainModeRecommendationFromMessage(
  message: ChatMessage,
  text: string,
  generatedImages: GeneratedImagePreview[],
): ChatCompanyModeSelection | undefined {
  if (message.author !== "team" || message.team !== CEO_CHAT_TEAM_ID) {
    return undefined;
  }

  const recommendedMode = recommendedModeFromText(text);
  if (!recommendedMode) {
    return undefined;
  }

  const hasDiscoveryTool = message.activeTools?.includes(
    "emit_discovery_payload",
  );
  const hasFounderDiscoverySkill = message.activeSkills?.some((skill) =>
    skill.startsWith("founder-discovery"),
  );

  return hasDiscoveryTool ||
    hasFounderDiscoverySkill ||
    generatedImages.length > 0
    ? recommendedMode
    : undefined;
}

function recommendedModeFromText(
  text: string,
): ChatCompanyModeSelection | undefined {
  const normalized = text.toLowerCase();

  if (/\blight\s+mode\b/.test(normalized)) {
    return "light";
  }

  if (/\b(?:company\s+full\s+mode|full\s+mode)\b/.test(normalized)) {
    return "full";
  }

  return undefined;
}

export function plainModeRecommendationPayloadFromMessage(
  message: ChatMessage,
  text: string,
  recommendedMode: ChatCompanyModeSelection,
): CeoDiscoveryMessagePayload {
  const runtimeTrace = runtimeTraceFromMessage(message);

  return {
    type: "ceo_discovery_message",
    message: text,
    flow_step: "mode_recommendation",
    blocks: [
      {
        kind: "paragraph",
        text,
      },
      {
        kind: "mode_selector",
        recommended_mode: recommendedMode,
      },
    ],
    ...(runtimeTrace ? { runtime_trace: runtimeTrace } : {}),
  };
}

function runtimeTraceFromMessage(
  message: ChatMessage,
): CeoDiscoveryMessagePayload["runtime_trace"] {
  const activeSkills = uniqueStrings(message.activeSkills);
  const usedTools = uniqueStrings(message.activeTools);
  const toolActivities = message.runtimeActivities?.map((activity) => ({
    ...(activity.callId ? { call_id: activity.callId } : {}),
    ...(activity.input ? { input: activity.input } : {}),
    ...(activity.itemId ? { item_id: activity.itemId } : {}),
    ...(activity.output ? { output: activity.output } : {}),
    ...(activity.status ? { status: activity.status } : {}),
    tool_name: activity.toolName,
  }));

  if (!activeSkills.length && !usedTools.length && !toolActivities?.length) {
    return undefined;
  }

  return {
    active_skills: activeSkills,
    ...(toolActivities?.length ? { tool_activities: toolActivities } : {}),
    used_tools: usedTools,
  };
}

function uniqueStrings(values: string[] | undefined) {
  return [...new Set(values?.filter(Boolean) ?? [])];
}
