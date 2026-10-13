/**
 * Who wrote a Wiki revision. An Agent writes on behalf of a member, so the
 * label names both ("Andrew for You"); `agentName` falls back to a generic
 * noun when the Agent no longer exists or is not in the roster.
 */
export function wikiAuthorLabel({
  agentId,
  agentName,
  authorUserId,
  viewerUserId,
}: {
  agentId: string | null;
  agentName: string | null | undefined;
  authorUserId: string;
  viewerUserId: string | undefined;
}) {
  const member = authorUserId === viewerUserId ? "You" : "Team member";
  return agentId ? `${agentName || "An agent"} for ${member}` : member;
}
