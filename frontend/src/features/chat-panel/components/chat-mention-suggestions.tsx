import { useLayoutEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { AtSign } from "@/shared/ui/icons";
import type { ChatMentionMember } from "@/features/chat-panel/model/chat-mentions";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { personInitials } from "@/shared/ui/person-initials";
import { useChatMemberProfile } from "@/shared/chat/member-profile";
import "@/features/chat-panel/styles/chat-mentions.css";

export function ChatMentionSuggestions({
  activeIndex,
  listId,
  members,
  onActiveIndexChange,
  onSelect,
  textareaRef,
}: {
  activeIndex: number;
  listId: string;
  members: readonly ChatMentionMember[];
  onActiveIndexChange: (index: number) => void;
  onSelect: (member: ChatMentionMember) => void;
  textareaRef: RefObject<HTMLElement | null>;
}) {
  const popupRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({
    left: 0,
    bottom: 0,
    width: 0,
    maxHeight: 320,
  });

  useLayoutEffect(() => {
    const composer = textareaRef.current?.closest("form");
    if (!composer) return;
    const updatePosition = () => {
      const bounds = composer.getBoundingClientRect();
      const width = Math.min(420, bounds.width, window.innerWidth - 24);
      setPosition({
        left: Math.max(
          12,
          Math.min(bounds.left, window.innerWidth - width - 12),
        ),
        bottom: window.innerHeight - bounds.top + 8,
        width,
        maxHeight: Math.max(100, Math.min(320, bounds.top - 20)),
      });
    };
    updatePosition();
    const observer = new ResizeObserver(updatePosition);
    observer.observe(composer);
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  }, [textareaRef]);

  useLayoutEffect(() => {
    popupRef.current
      ?.querySelector('[aria-selected="true"]')
      ?.scrollIntoView({ block: "nearest" });
  }, [activeIndex, members.length]);

  return createPortal(
    <div className="chat-mention-suggestions" ref={popupRef} style={position}>
      <div className="chat-mention-suggestions__heading">
        <AtSign size={15} aria-hidden />
        <span>Mention someone</span>
      </div>
      <div
        aria-label="Members"
        className="chat-mention-suggestions__list"
        id={listId}
        role="listbox"
      >
        {members.map((member, index) => (
          <div
            aria-selected={activeIndex === index}
            className="chat-mention-suggestions__member"
            data-unavailable={member.unavailable ? true : undefined}
            id={`${listId}-${index}`}
            key={`${member.kind}:${member.id}`}
            onMouseDown={(event) => event.preventDefault()}
            onMouseMove={() => onActiveIndexChange(index)}
            onClick={() => onSelect(member)}
            role="option"
          >
            <span className="chat-mention-suggestions__avatar" aria-hidden>
              <MentionAvatar member={member} />
            </span>
            <span className="chat-mention-suggestions__identity">
              <span className="chat-mention-suggestions__name">
                {member.name}
              </span>
              <span className="chat-mention-suggestions__description">
                {member.description}
              </span>
            </span>
            <span className="chat-mention-suggestions__kind">
              {member.unavailable ??
                (member.broadcastScope
                  ? "Channel"
                  : member.kind === "agent"
                    ? "Agent"
                    : "Member")}
            </span>
          </div>
        ))}
      </div>
      {members.length === 0 ? (
        <p className="chat-mention-suggestions__empty" role="status">
          No matching members
        </p>
      ) : null}
      <div className="chat-mention-suggestions__help" aria-hidden>
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> to navigate
        </span>
        <span>
          <kbd>↵</kbd> to select
        </span>
        <span>
          <kbd>esc</kbd> to close
        </span>
      </div>
    </div>,
    document.body,
  );
}

function MentionAvatar({ member }: { member: ChatMentionMember }) {
  const profile = useChatMemberProfile({
    id: member.id,
    kind: member.kind,
    name: member.name,
    avatarSrc: member.avatarSrc,
    title: member.description ?? "",
  });
  if (member.broadcastScope) return <AtSign size={20} />;
  if (member.kind === "agent")
    return <FoldAvatar agentId={member.id} size={34} motion="still" />;
  return profile.avatarSrc ? (
    <img src={profile.avatarSrc} alt="" />
  ) : (
    personInitials(profile.name)
  );
}
