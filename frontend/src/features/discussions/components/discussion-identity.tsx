import { Avatar, AvatarFallback, AvatarImage } from "@/shared/ui/core/avatar";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { personInitials } from "@/shared/ui/person-initials";
import { useChatMemberProfile } from "@/shared/chat/member-profile";

export function DiscussionAvatar({
  name,
  src,
  userId,
  agentId,
  size,
}: {
  name: string;
  src?: string | null;
  userId?: string | null;
  agentId?: string;
  size?: number;
}) {
  // Discussion payloads omit large avatars. The profile cache already has the
  // full image, so resolving by identity needs no request per message.
  const profile = useChatMemberProfile({
    id: agentId ?? userId ?? "",
    kind: agentId ? "agent" : "human",
    name,
    avatarSrc: src,
    title: "",
  });
  if (agentId) return <FoldAvatar agentId={agentId} size={size ?? 32} />;
  return (
    <Avatar
      className="discussion-avatar"
      style={size === undefined ? undefined : { width: size, height: size }}
    >
      {profile.avatarSrc ? (
        <AvatarImage src={profile.avatarSrc} alt="" />
      ) : null}
      <AvatarFallback>{personInitials(profile.name) || "?"}</AvatarFallback>
    </Avatar>
  );
}
