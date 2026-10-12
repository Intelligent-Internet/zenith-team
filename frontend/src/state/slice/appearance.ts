import { createSlice, type PayloadAction } from "@reduxjs/toolkit";

import {
  defaultAppearancePreferences,
  type AppearancePreferences,
} from "@/app/appearance/preferences";

type AppearanceState = {
  scopeKey: string | null;
  preferences: AppearancePreferences;
  storageError: boolean;
};

const initialState: AppearanceState = {
  scopeKey: null,
  preferences: defaultAppearancePreferences(),
  storageError: false,
};

const appearanceSlice = createSlice({
  name: "appearance",
  initialState,
  reducers: {
    setActiveAppearance: (_state, action: PayloadAction<AppearanceState>) =>
      action.payload,
    clearActiveAppearance: (state, action: PayloadAction<string | null>) =>
      state.scopeKey === action.payload ? initialState : state,
  },
});

export const { setActiveAppearance, clearActiveAppearance } =
  appearanceSlice.actions;
export const appearanceReducer = appearanceSlice.reducer;
export const selectAppearance = (state: { appearance: AppearanceState }) =>
  state.appearance;
