import type {
  DiscussionDetail,
  DiscussionMessage,
} from "../api/discussions-api";
import { DiscussionAvatar } from "./discussion-identity";
import { HuddleTranscriptMessage } from "./huddle-transcript-message";
import { HuddleCallMessage } from "./huddle-call-message";

export function DiscussionSystemMessage({
  message,
  detail,
}: {
  message: DiscussionMessage;
  detail: DiscussionDetail;
}) {
  const system = message.system;
  if (system?.action === "huddle_call")
    return (
      <HuddleCallMessage
        message={message}
        companyId={detail.conversation.companyId}
      />
    );
  if (system?.action === "huddle_transcript")
    return <HuddleTranscriptMessage message={message} />;
  const users =
    system?.subjectUserIds ??
    (system?.subjectUserId ? [system.subjectUserId] : []);
  const agents =
    system?.subjectAgentIds ??
    (system?.subjectAgentId ? [system.subjectAgentId] : []);
  const subjects = [
    ...users.map((id) => {
      const member = detail.members.find((item) => item.userId === id);
      return {
        key: `human:${id}`,
        name: member?.displayName ?? "A member",
        src: member?.avatarUrl,
        userId: id,
        agentId: undefined,
      };
    }),
    ...agents.map((id) => ({
      key: `agent:${id}`,
      name:
        detail.agents.find((item) => item.agentId === id)?.name ?? "An agent",
      src: undefined,
      userId: undefined,
      agentId: id,
    })),
  ];
  const names = new Intl.ListFormat("en", {
    style: "long",
    type: "conjunction",
  }).format(subjects.map((subject) => subject.name));
  const actor = detail.members.find(
    (member) => member.userId === system?.actorUserId,
  )?.displayName;
  const added =
    system?.action === "members_added" ||
    system?.action === "member_added" ||
    system?.action === "agent_added";
  const invitation = `${names || "Members"} ${subjects.length === 1 ? "was" : "were"} added ${system?.actorUserId ? `by ${actor ?? "a member"}` : "to the channel"}`;
  const text = {
    members_added: invitation,
    member_added: invitation,
    agent_added: invitation,
    member_removed: `${names || "A member"} is no longer in the channel`,
    agent_removed: `${names || "An agent"} was removed from the channel`,
    channel_updated: "Channel details were updated",
    channel_archived: "This channel was archived",
    channel_restored: "This channel was restored",
  }[system?.action ?? "channel_updated"];
  return (
    <div
      className="flex items-center justify-center gap-[9px] p-[10px_22px] text-[length:12px] text-[var(--discussion-muted)]"
      aria-label={text}
      title={new Date(message.createdAt).toLocaleString()}
    >
      {added && subjects.length > 0 ? (
        <span
          className="inline-flex shrink-0 items-center pl-[6px]"
          aria-hidden="true"
        >
          {subjects.slice(0, 3).map((subject) => (
            <span className="discussion-system-avatar" key={subject.key}>
              <DiscussionAvatar
                name={subject.name}
                src={subject.src}
                userId={subject.userId}
                agentId={subject.agentId}
              />
            </span>
          ))}
          {subjects.length > 3 ? (
            <span className="discussion-system-overflow">
              +{subjects.length - 3}
            </span>
          ) : null}
        </span>
      ) : null}
      <span className="min-w-0 wrap-anywhere text-center leading-[1.5]">
        {text}
      </span>
    </div>
  );
}
