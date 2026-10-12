import { agentDraftsReducer } from "@/state/slice/agent-drafts";
import { skillFileDraftsReducer } from "@/state/slice/skill-file-drafts";
import { createAction } from "@reduxjs/toolkit";
import { combineReducers } from "redux";
import type { Reducer, UnknownAction } from "redux";
import { persistReducer, type PersistedState } from "redux-persist";
import storage from "redux-persist/lib/storage";

import { boardlyApi } from "@/shared/api/boardly-api";
import { appearanceReducer } from "@/state/slice/appearance";
import { chatReducer } from "@/state/slice/chat";
import {
  conversationDockReducer,
  normalizeConversationDockState,
} from "@/state/slice/conversation-dock";
import { uiReducer } from "@/state/slice/ui";
import { workReducer } from "@/state/slice/work";
import { workflowDraftsReducer } from "@/state/slice/workflow-drafts";
import { workflowPinsReducer } from "@/state/slice/workflow-pins";

const uiPersistConfig = {
  key: "ui",
  storage,
  whitelist: ["activeTeam", "sidebarCollapsedByCompany"],
  version: 2,
  migrate: async (persistedState: PersistedState) => {
    if (!persistedState) {
      return persistedState;
    }

    const uiState = { ...(persistedState as Record<string, unknown>) };
    delete uiState.chatOpen;
    return uiState as unknown as PersistedState;
  },
};

const conversationDockPersistConfig = {
  key: "conversationDock",
  storage,
  whitelist: ["companies"],
  version: 2,
  migrate: async (persistedState: PersistedState) => {
    if (!persistedState) {
      return persistedState;
    }

    return {
      ...(persistedState as Record<string, unknown>),
      ...normalizeConversationDockState(persistedState),
    } as unknown as PersistedState;
  },
};

const workPersistConfig = {
  key: "work",
  storage,
  whitelist: ["view", "projectsView", "projectTaskViews", "overviewViews"],
};

const workflowPinsPersistConfig = {
  key: "workflowPins",
  storage,
  whitelist: ["companies"],
  version: 1,
};

export const resetAppState = createAction("app/resetAppState");

const appReducer = combineReducers({
  skillFileDrafts: skillFileDraftsReducer,
  agentDrafts: persistReducer(
    { key: "agentDrafts", storage },
    agentDraftsReducer,
  ),
  [boardlyApi.reducerPath]: boardlyApi.reducer,
  appearance: appearanceReducer,
  chat: chatReducer,
  conversationDock: persistReducer(
    conversationDockPersistConfig,
    conversationDockReducer,
  ),
  ui: persistReducer(uiPersistConfig, uiReducer),
  work: persistReducer(workPersistConfig, workReducer),
  // In memory only: a half-filled launch form must not outlive the session.
  workflowDrafts: workflowDraftsReducer,
  workflowPins: persistReducer(workflowPinsPersistConfig, workflowPinsReducer),
});

export type AppState = ReturnType<typeof appReducer>;

const rootReducer: Reducer<AppState, UnknownAction> = (state, action) => {
  if (resetAppState.match(action)) {
    return appReducer(undefined, action);
  }

  return appReducer(state, action);
};

export default rootReducer;
