import { apiClient } from "@/lib/api-client";
import type { components } from "@/shared/api/generated/api.generated";

export type HuddleMedia = { url: string; token: string };
export type Huddle = {
  id: string;
  chatConversationId: string;
  state: string;
  deadlineAt: string;
  roomName?: string;
};
export type HuddleJoin = {
  huddle: Huddle;
  connection: { id: string; status: string };
  media?: HuddleMedia;
};
export type HuddleConnection = {
  id: string;
  huddleId: string;
  userId: string;
  status: "reserved" | "connected" | "ended" | "revoked";
  revision: number;
  createdAt: string;
  displayName: string;
};
export type HuddleInvitation = components["schemas"]["HuddleInvitation"];

export async function startHuddle(
  companyId: string,
  conversationId: string,
): Promise<Huddle> {
  return apiClient.post<Huddle>(
    `/companies/${companyId}/conversations/${conversationId}/huddles`,
    { requestKey: crypto.randomUUID() },
  );
}

export async function joinHuddle(
  companyId: string,
  huddleId: string,
): Promise<HuddleJoin> {
  return apiClient.post<HuddleJoin>(
    `/companies/${companyId}/huddles/${huddleId}/connections`,
    { requestKey: crypto.randomUUID() },
  );
}

export async function listHuddleConnections(
  companyId: string,
  huddleId: string,
): Promise<HuddleConnection[]> {
  return apiClient.get<HuddleConnection[]>(
    `/companies/${companyId}/huddles/${huddleId}/connections`,
  );
}

export async function leaveHuddle(
  companyId: string,
  huddleId: string,
  connectionId: string,
): Promise<void> {
  await apiClient.delete(
    `/companies/${companyId}/huddles/${huddleId}/connections/${connectionId}`,
  );
}

export async function inviteToHuddle(
  companyId: string,
  huddleId: string,
  inviteeUserId: string,
): Promise<HuddleInvitation> {
  return apiClient.post<HuddleInvitation>(
    `/companies/${companyId}/huddles/${huddleId}/invitations`,
    { inviteeUserId, requestKey: crypto.randomUUID() },
  );
}

export async function listPendingHuddleInvitations(
  companyId: string,
): Promise<HuddleInvitation[]> {
  return apiClient.get<HuddleInvitation[]>(
    `/companies/${companyId}/huddles/invitations`,
  );
}

export async function listHuddleInvitationHistory(
  companyId: string,
): Promise<HuddleInvitation[]> {
  return apiClient.get<HuddleInvitation[]>(
    `/companies/${companyId}/huddles/invitations/history`,
  );
}

export async function respondToHuddleInvitation(
  companyId: string,
  huddleId: string,
  invitationId: string,
  accept: boolean,
  expectedRevision: number,
): Promise<HuddleInvitation> {
  return apiClient.post<HuddleInvitation>(
    `/companies/${companyId}/huddles/${huddleId}/invitations/${invitationId}/respond`,
    { accept, expectedRevision },
  );
}
