import { useMessageConnectorsQuery } from "@/shared/api/message-connector-api";
import {
  useMessageSkillsQuery,
  type MessageSkillScope,
} from "@/shared/api/agent-skill-api";
import type { ChatMentionMember } from "../model/chat-mentions";

export function useResourceMentionMembers(
  scope: MessageSkillScope | undefined,
): ChatMentionMember[] {
  const options = {
    skip: !scope?.companyId || (!scope.conversation_id && !scope.agent_id),
    refetchOnMountOrArgChange: true,
  };
  const skills = useMessageSkillsQuery(scope ?? { companyId: "" }, options);
  const connectors = useMessageConnectorsQuery(scope ?? { companyId: "" }, {
    ...options,
    pollingInterval: 30_000,
    skipPollingIfUnfocused: true,
  });
  return [
    ...(skills.currentData?.items ?? []).map((skill): ChatMentionMember => ({
      id: skill.id,
      name: skill.name,
      description: `${skill.agentName} · ${skill.description}`,
      kind: "agent",
      skill: { skillId: skill.id, versionId: skill.versionId },
    })),
    ...(connectors.currentData?.items ?? []).map(
      (connector): ChatMentionMember => ({
        id: connector.id,
        name: connector.name,
        description: `${connector.agentNames.join(", ")} · ${connector.provider}`,
        kind: "agent",
        connector: {
          connectorId: connector.id,
          eligibleAgentIds: connector.eligibleAgentIds,
        },
      }),
    ),
  ];
}
