import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import {
  conversationArtifactReference,
  type ConversationArtifactReference,
} from "@/features/chat-panel/model/conversation-artifact";
import { TurnArtifactCard } from "./turn-artifact-card";
import { Link } from "react-router";

export function ChatRunArtifacts({
  items,
  onSelect,
}: {
  items: ConversationArtifact[];
  onSelect: (artifact: ConversationArtifactReference) => void;
}) {
  if (!items.length) return null;
  return (
    <div
      className="my-3 flex w-full min-w-0 flex-wrap gap-2"
      aria-label="Turn outputs"
    >
      {items.map((item) => {
        const files = item.artifact.items.filter(
          (file) => file.role !== "supporting",
        );
        return (
          <div key={item.artifact.id} className="min-w-0 max-w-full">
            <TurnArtifactCard
              title={item.artifact.title}
              stale={item.artifact.freshness === "stale"}
              type={item.artifact.type}
              files={files.map((file) => ({
                fileName: file.displayName,
                contentType: file.contentType,
              }))}
              onClick={() => onSelect(conversationArtifactReference(item))}
            />
            {item.artifact.freshness === "stale" && item.artifact.liveUrl && (
              <Link
                className="mt-1 block text-xs underline"
                to={item.artifact.liveUrl}
              >
                Open latest
              </Link>
            )}
          </div>
        );
      })}
    </div>
  );
}
