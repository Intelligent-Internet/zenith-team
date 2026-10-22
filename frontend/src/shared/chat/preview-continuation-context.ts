import { createContext } from "react";

// Preparing a draft grants no send authority. The ordinary composer owns review,
// editing and submission under the current conversation's existing permissions.
export const PreviewContinuationContext = createContext<{
  conversationId: string;
  prepareDraft: (text: string) => void;
  openEntity?: (entity: unknown) => Promise<void>;
  submit?: (text: string) => Promise<void>;
} | null>(null);
