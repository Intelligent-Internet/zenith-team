import { useLayoutEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { AtSign, BookOpen } from "@/shared/ui/icons";
import type { ChatMentionMember } from "@/features/chat-panel/model/chat-mentions";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { personInitials } from "@/shared/ui/person-initials";
import { useChatMemberProfile } from "@/shared/chat/member-profile";
import "@/features/chat-panel/styles/chat-mentions.css";

export function ChatMentionSuggestions({
  trigger,
  activeIndex,
  listId,
  members,
  onActiveIndexChange,
  onSelect,
  textareaRef,
}: {
  trigger?: "@" | "$";
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
    <div
      className="chat-mention-suggestions fixed z-[210] flex flex-col overflow-hidden [border:1px_solid_var(--workspace-divider-strong,_#d4cbdc)] rounded-[12px] [background:var(--workspace-overlay-bg,_#fff)] text-[var(--workspace-native-text,_#2d2631)] [box-shadow:0_8px_28px_rgb(37_28_45_/_12%)] [font-family:var(--workspace-font-body,_sans-serif)] text-workspace-label leading-workspace-label"
      ref={popupRef}
      style={position}
    >
      <div className="chat-mention-suggestions__heading flex flex-none items-center gap-[8px] p-[12px_14px_10px] text-[var(--workspace-native-text-muted,_#706875)] font-[600]">
        {trigger === "$" ? (
          <BookOpen size={15} aria-hidden />
        ) : (
          <AtSign size={15} aria-hidden />
        )}
        <span>{trigger === "$" ? "Use a skill" : "Mention someone"}</span>
      </div>
      <div
        aria-label="Mention suggestions"
        className="min-h-0 overflow-y-auto overscroll-contain p-[0_6px_6px]"
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
            <span
              className="chat-mention-suggestions__avatar grid w-[34px] h-[34px] flex-none overflow-hidden place-items-center rounded-[8px] [background:var(--workspace-control-bg,_#f1edf4)] text-[var(--workspace-native-accent,_#7556b9)] font-[700]"
              aria-hidden
            >
              {member.skill ? (
                <BookOpen size={18} />
              ) : (
                <MentionAvatar member={member} />
              )}
            </span>
            <span className="grid min-w-0 flex-1 gap-[2px]">
              <span className="chat-mention-suggestions__name">
                {member.name}
              </span>
              <span className="chat-mention-suggestions__description">
                {member.description}
              </span>
            </span>
            <span className="flex-none [border:1px_solid_var(--workspace-divider,_#e5dfe8)] rounded-[5px] p-[2px_5px] text-[var(--workspace-native-text-muted,_#706875)] text-workspace-caption leading-workspace-caption">
              {member.unavailable ??
                (member.skill
                  ? "Skill"
                  : member.broadcastScope
                    ? "Channel"
                    : member.kind === "agent"
                      ? "Agent"
                      : "Member")}
            </span>
          </div>
        ))}
      </div>
      {members.length === 0 ? (
        <p
          className="m-0 p-[12px_14px_18px] text-[var(--workspace-native-text-muted,_#706875)]"
          role="status"
        >
          No matching results
        </p>
      ) : null}
      <div
        className="chat-mention-suggestions__help flex flex-none flex-wrap gap-[10px] [border-top:1px_solid_var(--workspace-divider,_#e5dfe8)] p-[8px_14px] text-[var(--workspace-native-text-muted,_#706875)] text-workspace-caption leading-workspace-caption"
        aria-hidden
      >
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
  if (member.skill ? "Skill" : member.broadcastScope)
    return <AtSign size={20} />;
  if (member.kind === "agent")
    return <FoldAvatar agentId={member.id} size={34} motion="still" />;
  return profile.avatarSrc ? (
    <img src={profile.avatarSrc} alt="" />
  ) : (
    personInitials(profile.name)
  );
}
