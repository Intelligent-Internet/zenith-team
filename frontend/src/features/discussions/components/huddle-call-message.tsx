import { useState } from "react";
import { Headphones } from "@/shared/ui/icons";
import type { DiscussionMessage } from "../api/discussions-api";
import { HuddleHistoryDialog } from "./huddle-history-dialog";
import "../styles/huddle-call.css";

export function HuddleCallMessage({
  message,
  companyId,
}: {
  message: DiscussionMessage;
  companyId: string;
}) {
  const [open, setOpen] = useState(false);
  const call = message.huddle;
  if (!call)
    return (
      <p className="huddle-call-unavailable">
        Huddle history is no longer available.
      </p>
    );
  const ended = call.state === "ended";
  const title = ended
    ? "A huddle happened"
    : call.state === "ending"
      ? "Huddle is ending"
      : "Huddle started";
  const minutes = Math.floor(call.durationSeconds / 60);
  const duration = minutes > 0 ? `${minutes}m` : `${call.durationSeconds}s`;
  return (
    <article className="huddle-call-card">
      <span className="huddle-call-icon">
        <Headphones size={23} aria-hidden="true" />
      </span>
      <div className="huddle-call-copy">
        <header>
          <strong>{title}</strong>
          <time
            dateTime={call.startedAt}
            title={new Date(call.startedAt).toLocaleString()}
          >
            {new Date(call.startedAt).toLocaleTimeString([], {
              hour: "2-digit",
              minute: "2-digit",
            })}
          </time>
        </header>
        <p>
          {call.participantCount === 0
            ? ended
              ? "No one connected."
              : "Waiting for people to join."
            : `${call.participantCount} ${call.participantCount === 1 ? "person" : "people"} joined${ended ? ` · ${duration}` : ""}`}
        </p>
        {call.chatConversationId ? (
          <button
            type="button"
            className="huddle-call-link"
            onClick={() => setOpen(true)}
          >
            View conversation
          </button>
        ) : call.participantCount > 0 ? (
          <small>Conversation available to participants.</small>
        ) : null}
      </div>
      {open && call.chatConversationId && (
        <HuddleHistoryDialog
          companyId={companyId}
          call={call}
          conversationId={call.chatConversationId}
          onClose={() => setOpen(false)}
        />
      )}
    </article>
  );
}
