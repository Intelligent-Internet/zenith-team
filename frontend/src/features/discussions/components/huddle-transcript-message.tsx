import { ChevronDown, FileText } from "@/shared/ui/icons";
import type { DiscussionMessage } from "../api/discussions-api";
import "../styles/huddle-transcript.css";

export function HuddleTranscriptMessage({
  message,
}: {
  message: DiscussionMessage;
}) {
  const savedText =
    message.body && "markdown" in message.body ? message.body.markdown : "";
  // Previously saved cards include this generated introduction in their body.
  const introduction =
    "Automatically transcribed while the Agent was present; recognition errors, connection gaps and missing final words are possible. Human speakers are grouped as Participants. Generated Agent speech may have been interrupted. No audio recording is saved.\n";
  const text = savedText.startsWith(introduction)
    ? savedText.slice(introduction.length).trimStart()
    : savedText;
  return (
    <details className="huddle-transcript">
      <summary>
        <FileText size={18} aria-hidden="true" />
        <span>
          <strong>Transcript · {message.system?.agentName ?? "Agent"}</strong>
          <small>Captured during this Agent’s visit</small>
        </span>
        <time dateTime={message.createdAt}>
          {new Date(message.createdAt).toLocaleTimeString([], {
            hour: "2-digit",
            minute: "2-digit",
          })}
        </time>
        <ChevronDown
          className="huddle-transcript-chevron"
          size={16}
          aria-hidden="true"
        />
      </summary>
      <div className="p-[14px] [border-top:1px_solid_var(--border)] max-h-[440px] overflow-auto whitespace-pre-wrap wrap-anywhere text-[length:13px] leading-[1.65] [user-select:text]">
        {text}
      </div>
    </details>
  );
}
