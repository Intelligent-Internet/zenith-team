import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import type {
  ArtifactType,
  DeliverableStatus,
} from "@/features/run-deliverables/model/run-deliverable";

export type ConversationArtifactReference = {
  artifactId: string;
  runId: string | null;
  title: string;
  type: ArtifactType;
};

export function conversationArtifactReference(
  result: ConversationArtifact,
): ConversationArtifactReference {
  return {
    artifactId: result.artifact.id,
    runId: result.runId,
    title: result.artifact.title,
    type: result.artifact.type,
  };
}

export function artifactTypeLabel(type: ArtifactType) {
  return {
    app: "App",
    media: "Media",
    text: "Text",
    page: "Page",
    file: "File",
    whiteboard: "Whiteboard",
  }[type];
}

export function artifactReviewStatusLabel(status: DeliverableStatus) {
  return {
    ready_for_review: "Awaiting review",
    approved: "Result approved",
    changes_requested: "Changes requested",
    superseded: "Superseded",
  }[status];
}

export function artifactFeedbackReference(
  artifact: ConversationArtifactReference,
) {
  return `Request change to the ${artifact.type} artifact ${JSON.stringify(artifact.title)} ${artifact.runId ? `from Run ${artifact.runId}` : `from delivery ${artifact.artifactId}`}:`;
}
