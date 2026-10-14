import {
  useMessageSkillsQuery,
  type MessageSkillScope,
} from "@/shared/api/agent-skill-api";
import type { ChatMentionMember } from "../model/chat-mentions";

export function useSkillMentionMembers(
  scope: MessageSkillScope | undefined,
): ChatMentionMember[] {
  const catalog = useMessageSkillsQuery(scope ?? { companyId: "" }, {
    skip: !scope?.companyId || (!scope.conversation_id && !scope.agent_id),
    refetchOnMountOrArgChange: true,
  });
  return (catalog.currentData?.items ?? []).map((skill) => ({
    id: skill.id,
    name: skill.name,
    description: `${skill.agentName} · ${skill.description}`,
    kind: "agent",
    skill: { skillId: skill.id, versionId: skill.versionId },
  }));
}
