import type { ConversationModelSettings } from "@/shared/api/types";
import type { StableChatMessage } from "@/features/chat-panel/model/chat-message-convergence";
import { Info } from "@/shared/ui/icons";
import {
  FoldAvatar,
  FoldAvatarMotionContext,
} from "@/shared/ui/agent-avatar/fold-avatar";
import { foldPaletteIndexForKey } from "@/shared/ui/agent-avatar/fold-variants";
import { Tooltip } from "@/shared/ui/tooltip";
import { chatModelLabel } from "@/features/chat-panel/model/chat-model-label";
import "@/features/chat-panel/styles/chat-model-settings.css";

export function createModelChangeTimeline({
  changes,
  messages,
  hasOlderMessages,
}: {
  changes: ConversationModelSettings["changes"];
  messages: StableChatMessage[];
  hasOlderMessages: boolean;
}) {
  const firstSequence = messages.find(
    (message) => message.messageSequence !== undefined,
  )?.messageSequence;
  return changes.flatMap((change) => {
    // Notices outside the loaded history enter with their surrounding messages.
    if (
      hasOlderMessages &&
      (firstSequence === undefined || change.afterSeq < firstSequence)
    )
      return [];
    // Keep the notice between turns: an in-flight reply still uses the old model.
    const nextMessage = messages.find(
      (message) =>
        message.author === "user" &&
        (message.messageSequence !== undefined
          ? message.messageSequence > change.afterSeq
          : message.createdAt &&
            Date.parse(message.createdAt) > Date.parse(change.createdAt)),
    );
    return [
      {
        beforeMessageId: nextMessage?.id,
        className: "workspace-chat-model-change",
        key: `model-change:${change.id}`,
        createdAt: change.createdAt,
        content: <ChatModelChange change={change} />,
      },
    ];
  });
}

function ChatModelChange({
  change,
}: {
  change: ConversationModelSettings["changes"][number];
}) {
  const previousModel = chatModelLabel(change.previousModel);
  const model = chatModelLabel(change.model);
  return (
    <div className="chat-model-change">
      <div className="inline-flex items-center gap-[8px] min-w-0 max-w-full p-[5px_20px] [background:linear-gradient(_90deg,_transparent,_light-dark(#f6f3fa,_rgb(160_131_200_/_9%))_12%,_light-dark(#f6f3fa,_rgb(160_131_200_/_9%))_88%,_transparent_)]">
        <FoldAvatarMotionContext.Provider value={true}>
          <FoldAvatar
            agentId={`model-change:${change.id}`}
            paletteIndex={foldPaletteIndexForKey("fold-tangerine")}
            size={20}
            compactMotion="blink"
          />
        </FoldAvatarMotionContext.Provider>
        <span
          className="min-w-0 overflow-hidden text-center text-ellipsis whitespace-nowrap"
          title={`Model changed from ${previousModel} to ${model}.`}
        >
          Model changed from {previousModel} to{" "}
          <span className="text-workspace-text">{model}</span>.
        </span>
        <Tooltip label="Applies to the next run. A run already in progress keeps its model.">
          <button
            type="button"
            className="chat-model-change-info"
            aria-label="About this model change"
          >
            <Info size={14} aria-hidden />
          </button>
        </Tooltip>
      </div>
    </div>
  );
}
