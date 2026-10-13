import type { SessionSnapshot } from "@/shared/session/session";
import { pendingMemberAgentSetupPath } from "@/features/company-invitations/model/member-agent-setup";

export function getNextRoute(session: SessionSnapshot): string {
  if (session.status !== "authenticated") {
    return "/login";
  }

  const memberSetup = pendingMemberAgentSetupPath(
    session.userId,
    session.company?.id,
  );
  if (memberSetup) return memberSetup;

  if (session.nextStep === "company" || session.nextStep === "agent_setup") {
    return "/onboarding";
  }

  return "/app";
}
