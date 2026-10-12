import { createSlice, type PayloadAction } from "@reduxjs/toolkit";

/**
 * A Workflow launch form someone left half-filled to connect one of its apps
 * in Settings › Plugins, keyed `${companyId}:${workflowKey}`. The dialog
 * saves it when a chip link takes the person away, restores it when the
 * dialog opens again, and clears it on launch, Cancel and Schedule it.
 *
 * In memory only — deliberately not persisted: answers can hold client
 * details, and a reload starting the form afresh is acceptable.
 */
export type WorkflowDraft = {
  /** Field key → answer: text, or the chosen values of a multi_select. */
  answers: Record<string, string | string[]>;
  /** Field key → the files already uploaded for a field that takes them. */
  files?: Record<string, WorkflowDraftFile[]>;
  pageIndex: number;
  agentId?: string;
  projectId?: string;
};

/** An uploaded file a draft keeps, so the way back does not upload it again. */
export type WorkflowDraftFile = {
  attachmentId: string;
  filename: string;
  contentType: string;
  sizeBytes: number;
};

type WorkflowDraftsState = {
  drafts: Record<string, WorkflowDraft>;
};

type WorkflowDraftTarget = { companyId: string; workflowKey: string };

const initialState: WorkflowDraftsState = {
  drafts: {},
};

function draftKey({ companyId, workflowKey }: WorkflowDraftTarget) {
  return `${companyId}:${workflowKey}`;
}

const workflowDraftsSlice = createSlice({
  name: "workflowDrafts",
  initialState,
  reducers: {
    saveWorkflowDraft: (
      state,
      action: PayloadAction<WorkflowDraftTarget & { draft: WorkflowDraft }>,
    ) => {
      const { draft, ...target } = action.payload;
      if (!target.companyId || !target.workflowKey) return;
      state.drafts[draftKey(target)] = draft;
    },
    clearWorkflowDraft: (state, action: PayloadAction<WorkflowDraftTarget>) => {
      delete state.drafts[draftKey(action.payload)];
    },
  },
});

export const { clearWorkflowDraft, saveWorkflowDraft } =
  workflowDraftsSlice.actions;
export const workflowDraftsReducer = workflowDraftsSlice.reducer;

export function selectWorkflowDraft(
  state: { workflowDrafts: WorkflowDraftsState },
  companyId: string,
  workflowKey: string,
): WorkflowDraft | undefined {
  return state.workflowDrafts.drafts[draftKey({ companyId, workflowKey })];
}
