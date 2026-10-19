import { createSlice, type PayloadAction } from "@reduxjs/toolkit";

type UiState = {
  sidebarCollapsedByCompany: Record<string, boolean>;
};

const initialState: UiState = {
  sidebarCollapsedByCompany: {},
};

const uiSlice = createSlice({
  name: "ui",
  initialState,
  reducers: {
    setWorkspaceSidebarCollapsed: (
      state,
      action: PayloadAction<{ companyId: string; collapsed: boolean }>,
    ) => {
      state.sidebarCollapsedByCompany ??= {};
      state.sidebarCollapsedByCompany[action.payload.companyId] =
        action.payload.collapsed;
    },
  },
});

export const { setWorkspaceSidebarCollapsed } = uiSlice.actions;

export const uiReducer = uiSlice.reducer;
