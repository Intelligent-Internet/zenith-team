import type { components as ApiComponents } from "@/shared/api/generated/api.generated";

export type AgentMailDraftStatus =
  ApiComponents["schemas"]["AgentMailDraftStatus"];

export type AgentMailDraftDeliveryInput =
  ApiComponents["schemas"]["AgentMailDraftDeliveryInput"];

export type AgentMailDraftDelivery =
  ApiComponents["schemas"]["AgentMailDraftDelivery"];

export type AgentMailDraft = ApiComponents["schemas"]["AgentMailDraft"];

export type AgentMailDraftGroup = Omit<
  ApiComponents["schemas"]["AgentMailDraftGroup"],
  "drafts" | "sourceRunId"
> & {
  drafts: AgentMailDraft[];
  sourceRunId: string;
};

export type UpdateAgentMailDraftInput =
  ApiComponents["schemas"]["UpdateAgentMailDraftRequest"];

export type AgentMailDraftVersion =
  ApiComponents["schemas"]["AgentMailDraftVersion"];

// The conversation dock persists only this selection, never email content.
export type AgentMailDraftSelection = {
  groupId: string;
  sourceRunId: string;
};
