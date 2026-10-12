import { createSlice, type PayloadAction } from "@reduxjs/toolkit";

type FileDraft = { path: string; content: string; baseline: string };
type SkillFileDraft = {
  revision: number;
  versionId: string;
  changes: FileDraft[];
  deletePaths: string[];
};

// Unsaved file buffers stay in the editor until saved or discarded.
const initialState: Record<string, SkillFileDraft> = {};
const slice = createSlice({
  name: "skillFileDrafts",
  initialState,
  reducers: {
    setSkillFileDraft(
      state,
      { payload }: PayloadAction<{ key: string; draft: SkillFileDraft }>,
    ) {
      if (!payload.draft.changes.length && !payload.draft.deletePaths.length) {
        delete state[payload.key];
      } else state[payload.key] = payload.draft;
    },
    clearSkillFileDraft(state, { payload }: PayloadAction<string>) {
      delete state[payload];
    },
    clearSkillFileDrafts() {
      return initialState;
    },
    advanceSkillFileDraftRevision(
      state,
      {
        payload,
      }: PayloadAction<{
        key: string;
        versionId: string;
        expectedRevision: number;
        revision: number;
      }>,
    ) {
      const draft = state[payload.key];
      if (
        draft?.versionId === payload.versionId &&
        draft.revision === payload.expectedRevision
      ) {
        draft.revision = payload.revision;
      }
    },
  },
});
export const {
  setSkillFileDraft,
  clearSkillFileDraft,
  clearSkillFileDrafts,
  advanceSkillFileDraftRevision,
} = slice.actions;
export const skillFileDraftsReducer = slice.reducer;
