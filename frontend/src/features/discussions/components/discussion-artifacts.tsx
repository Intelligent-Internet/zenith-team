import { ConversationArtifactsPanel } from "@/features/chat-panel/components/artifacts/conversation-artifacts-panel";
import { useConversationArtifacts } from "@/features/chat-panel/hooks/use-conversation-artifacts";
import { useDiscussionPreview } from "./discussion-preview";

export function DiscussionArtifacts({
  companyId,
  conversationId,
  onCreateWhiteboard,
}: {
  companyId: string;
  conversationId: string;
  onCreateWhiteboard?: () => void;
}) {
  const artifacts = useConversationArtifacts({ companyId, conversationId });
  const preview = useDiscussionPreview();
  return (
    <section
      id={`discussion-artifacts-${conversationId}`}
      className="discussion-artifacts-page"
    >
      <ConversationArtifactsPanel
        companyId={companyId}
        artifacts={artifacts}
        layout="page"
        onSelect={(artifact) => preview.openArtifact(artifact.artifactId)}
        onCreateWhiteboard={onCreateWhiteboard}
      />
    </section>
  );
}
