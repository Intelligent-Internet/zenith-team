import { configureStore } from "@reduxjs/toolkit";
import { setupListeners } from "@reduxjs/toolkit/query";
import type { TypedUseSelectorHook } from "react-redux";
import { useDispatch, useSelector } from "react-redux";
import {
  FLUSH,
  PAUSE,
  PERSIST,
  PURGE,
  REGISTER,
  REHYDRATE,
  persistReducer,
  persistStore,
  type PersistedState,
} from "redux-persist";
import storage from "redux-persist/lib/storage";

import { zenithTeamApi } from "@/shared/api/zenith-team-api";
import rootReducer from "@/state/reducer";

function removeRetiredDashboardState(state: PersistedState): PersistedState {
  if (!state) {
    return state;
  }

  const persistedState = { ...(state as Record<string, unknown>) };
  delete persistedState.cockpit;
  return persistedState as PersistedState;
}

const persistConfig = {
  key: "root",
  whitelist: ["ui"],
  storage,
  version: 2,
  migrate: async (persistedState: PersistedState) =>
    removeRetiredDashboardState(persistedState),
};

const persistedReducer = persistReducer(persistConfig, rootReducer);

const store = configureStore({
  reducer: persistedReducer,
  middleware: (getDefaultMiddleware) =>
    getDefaultMiddleware({
      serializableCheck: {
        ignoredActions: [FLUSH, REHYDRATE, PAUSE, PERSIST, PURGE, REGISTER],
      },
    }).concat(zenithTeamApi.middleware),
  devTools: !import.meta.env.PROD,
});

export type RootState = ReturnType<typeof store.getState>;
export type AppDispatch = typeof store.dispatch;

export const useAppDispatch: () => AppDispatch = useDispatch;
export const useAppSelector: TypedUseSelectorHook<RootState> = useSelector;

export default store;

setupListeners(store.dispatch);

export const persistor = persistStore(store);
