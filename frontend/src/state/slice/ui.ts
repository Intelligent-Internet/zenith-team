import { createSlice, type PayloadAction } from "@reduxjs/toolkit";

type UiState = {
  activeTeam: string | null;
  chatOpen: boolean;
  sidebarCollapsedByCompany: Record<string, boolean>;
};

const initialState: UiState = {
  activeTeam: "ceo",
  chatOpen: false,
  sidebarCollapsedByCompany: {},
};

const uiSlice = createSlice({
  name: "ui",
  initialState,
  reducers: {
    setActiveTeam: (state, action: PayloadAction<string | null>) => {
      state.activeTeam = action.payload;
    },
    setChatOpen: (state, action: PayloadAction<boolean>) => {
      state.chatOpen = action.payload;
    },
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

export const { setActiveTeam, setChatOpen, setWorkspaceSidebarCollapsed } =
  uiSlice.actions;

export const uiReducer = uiSlice.reducer;

export const selectActiveTeam = (state: { ui: UiState }) => state.ui.activeTeam;
export const selectChatOpen = (state: { ui: UiState }) => state.ui.chatOpen;
