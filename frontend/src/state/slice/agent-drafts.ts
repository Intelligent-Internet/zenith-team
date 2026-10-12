import { createSlice, type PayloadAction } from "@reduxjs/toolkit";
import type { AgentDto } from "@/shared/api/types";

// User-authored drafts are UI state; saved instructions remain in the API cache.
type InstructionDraft = {
  value: string;
  baseline: string;
  revision: AgentDto["revision"];
};
const initialState: Record<string, InstructionDraft> = {};
const slice = createSlice({
  name: "agentDrafts",
  initialState,
  reducers: {
    setInstructionDraft(
      state,
      { payload }: PayloadAction<{ key: string; draft: InstructionDraft }>,
    ) {
      state[payload.key] = payload.draft;
    },
    acknowledgeInstructionSave(
      state,
      {
        payload,
      }: PayloadAction<{
        key: string;
        submitted: string;
        instructions: string;
        revision: AgentDto["revision"];
      }>,
    ) {
      const draft = state[payload.key];
      if (!draft) return;
      if (draft.value === payload.submitted) delete state[payload.key];
      else {
        draft.baseline = payload.instructions;
        draft.revision = payload.revision;
      }
    },
    clearInstructionDraft(state, { payload }: PayloadAction<string>) {
      delete state[payload];
    },
  },
});
export const {
  setInstructionDraft,
  clearInstructionDraft,
  acknowledgeInstructionSave,
} = slice.actions;
export const agentDraftsReducer = slice.reducer;
