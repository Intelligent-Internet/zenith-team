import { createSlice, type PayloadAction } from "@reduxjs/toolkit";

/**
 * Workflows a person pinned on the Workflows page, per Company, newest first.
 * UI-only state: the server has no pin contract, so pins live in this
 * persisted slice and are cleared with the rest of the persisted state on
 * sign-out. A key the catalog no longer ships is simply not shown.
 */
type WorkflowPinsState = {
  companies: Record<string, string[]>;
};

const maxPinnedWorkflows = 100;
const noPinnedWorkflows: readonly string[] = [];

const initialState: WorkflowPinsState = {
  companies: {},
};

function pinnedKeys(state: WorkflowPinsState, companyId: string): string[] {
  const keys: unknown = state.companies?.[companyId];
  return Array.isArray(keys)
    ? keys.filter((key): key is string => typeof key === "string")
    : [];
}

const workflowPinsSlice = createSlice({
  name: "workflowPins",
  initialState,
  reducers: {
    toggleWorkflowPin: (
      state,
      action: PayloadAction<{ companyId: string; workflowKey: string }>,
    ) => {
      const { companyId, workflowKey } = action.payload;
      if (!companyId || !workflowKey) return;
      state.companies ??= {};
      const current = pinnedKeys(state, companyId);
      state.companies[companyId] = current.includes(workflowKey)
        ? current.filter((key) => key !== workflowKey)
        : [workflowKey, ...current].slice(0, maxPinnedWorkflows);
    },
  },
});

export const { toggleWorkflowPin } = workflowPinsSlice.actions;
export const workflowPinsReducer = workflowPinsSlice.reducer;

export function selectPinnedWorkflowKeys(
  state: { workflowPins: WorkflowPinsState },
  companyId: string,
): readonly string[] {
  const keys: unknown = state.workflowPins.companies?.[companyId];
  return Array.isArray(keys) ? (keys as string[]) : noPinnedWorkflows;
}
