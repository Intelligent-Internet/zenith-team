import type { LibraryArtifact } from "@/features/chat-panel/api/artifact-library-api";
import { createSlice, type PayloadAction } from "@reduxjs/toolkit";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatMessage } from "@/shared/chat/types";

type TeamChatState = Partial<Record<ChatTeamId, ChatMessage[]>>;
type TeamDraftState = Partial<Record<ChatTeamId, string>>;
type TaskChatState = Record<string, ChatMessage[]>;
type TaskDraftState = Record<string, string>;
type ResourceChatState = Record<string, ChatMessage[]>;
type ResourceDraftState = Record<string, string>;
type ConversationChatState = Record<string, ChatMessage[]>;
type ChatMessageChanges = Partial<Omit<ChatMessage, "id" | "team">>;

type ChatState = {
  artifactDrafts: Record<string, LibraryArtifact[]>;
  draftsByResource: ResourceDraftState;
  draftsByTeam: TeamDraftState;
  draftsByTask: TaskDraftState;
  messagesByConversation: ConversationChatState;
  messagesByResource: ResourceChatState;
  messagesByTeam: TeamChatState;
  messagesByTask: TaskChatState;
};

const emptyChatMessages: ChatMessage[] = [];

const initialState: ChatState = {
  artifactDrafts: {},
  draftsByResource: {},
  draftsByTeam: {},
  draftsByTask: {},
  messagesByConversation: {},
  messagesByResource: {},
  messagesByTeam: {},
  messagesByTask: {},
};

const getTeamMessages = (state: ChatState, team: ChatTeamId) => {
  state.messagesByTeam[team] ??= [];
  return state.messagesByTeam[team];
};

const getConversationMessages = (state: ChatState, conversationKey: string) => {
  state.messagesByConversation[conversationKey] ??= [];
  return state.messagesByConversation[conversationKey];
};

const getTaskMessages = (state: ChatState, taskChatId: string) => {
  state.messagesByTask[taskChatId] ??= [];
  return state.messagesByTask[taskChatId];
};

const getResourceMessages = (state: ChatState, resourceChatId: string) => {
  state.messagesByResource[resourceChatId] ??= [];
  return state.messagesByResource[resourceChatId];
};

function findEquivalentMessageIndex(
  messages: ChatMessage[],
  nextMessage: ChatMessage,
) {
  return messages.findIndex((message) => {
    if (message.id === nextMessage.id) {
      return true;
    }
    if (
      message.author !== nextMessage.author ||
      message.team !== nextMessage.team
    ) {
      return false;
    }
    if (
      message.clientMessageId &&
      nextMessage.clientMessageId &&
      message.clientMessageId === nextMessage.clientMessageId &&
      hasSameAssistantSegment(message, nextMessage)
    ) {
      return true;
    }
    if (
      message.turnKey &&
      nextMessage.turnKey &&
      message.turnKey === nextMessage.turnKey &&
      hasSameAssistantSegment(message, nextMessage)
    ) {
      return true;
    }
    return false;
  });
}

function mergeEquivalentMessage(
  existing: ChatMessage,
  nextMessage: ChatMessage,
): ChatMessage {
  return {
    ...existing,
    ...nextMessage,
    id: existing.id,
    text: nextMessage.text.trim() ? nextMessage.text : existing.text,
  };
}

function findMessageUpdateTarget(
  messages: ChatMessage[],
  id: number,
  changes: ChatMessageChanges,
) {
  const byId = messages.find((item) => item.id === id);
  if (byId) {
    return byId;
  }

  return messages.find((item) => {
    if (
      changes.clientMessageId &&
      (!changes.author || item.author === changes.author) &&
      item.clientMessageId === changes.clientMessageId &&
      matchesAssistantSegmentChanges(item, changes)
    ) {
      return true;
    }
    if (
      changes.turnKey &&
      (!changes.author || item.author === changes.author) &&
      item.turnKey === changes.turnKey &&
      matchesAssistantSegmentChanges(item, changes)
    ) {
      return true;
    }
    return false;
  });
}

function hasSameAssistantSegment(left: ChatMessage, right: ChatMessage) {
  return (
    left.author !== "team" ||
    right.author !== "team" ||
    (left.afterUserInputRequestId === right.afterUserInputRequestId &&
      Boolean(left.pausedForUserInput) === Boolean(right.pausedForUserInput))
  );
}

function matchesAssistantSegmentChanges(
  message: ChatMessage,
  changes: ChatMessageChanges,
) {
  return (
    message.author !== "team" ||
    changes.author !== "team" ||
    message.afterUserInputRequestId === changes.afterUserInputRequestId
  );
}

const chatSlice = createSlice({
  name: "chat",
  initialState,
  reducers: {
    setArtifactDraft: (
      state,
      action: PayloadAction<{ key: string; artifacts: LibraryArtifact[] }>,
    ) => {
      state.artifactDrafts ??= {};
      if (action.payload.artifacts.length)
        state.artifactDrafts[action.payload.key] = action.payload.artifacts;
      else delete state.artifactDrafts[action.payload.key];
    },
    appendConversationChatMessage: (
      state,
      action: PayloadAction<{
        conversationKey: string;
        message: ChatMessage;
      }>,
    ) => {
      const messages = getConversationMessages(
        state,
        action.payload.conversationKey,
      );
      const existingIndex = findEquivalentMessageIndex(
        messages,
        action.payload.message,
      );
      if (existingIndex >= 0) {
        messages[existingIndex] = mergeEquivalentMessage(
          messages[existingIndex],
          action.payload.message,
        );
        return;
      }
      messages.push(action.payload.message);
    },
    appendChatMessage: (state, action: PayloadAction<ChatMessage>) => {
      const messages = getTeamMessages(state, action.payload.team);
      const existingIndex = findEquivalentMessageIndex(
        messages,
        action.payload,
      );

      if (existingIndex >= 0) {
        messages[existingIndex] = mergeEquivalentMessage(
          messages[existingIndex],
          action.payload,
        );
        return;
      }

      messages.push(action.payload);
    },
    setTeamChatMessages: (
      state,
      action: PayloadAction<{
        messages: ChatMessage[];
        team: ChatTeamId;
      }>,
    ) => {
      state.messagesByTeam[action.payload.team] = action.payload.messages;
    },
    setConversationChatMessages: (
      state,
      action: PayloadAction<{
        conversationKey: string;
        messages: ChatMessage[];
      }>,
    ) => {
      state.messagesByConversation[action.payload.conversationKey] =
        action.payload.messages;
    },
    setTeamDraft: (
      state,
      action: PayloadAction<{ team: ChatTeamId; message: string }>,
    ) => {
      state.draftsByTeam[action.payload.team] = action.payload.message;
    },
    appendTaskChatMessage: (
      state,
      action: PayloadAction<{ message: ChatMessage; taskChatId: string }>,
    ) => {
      getTaskMessages(state, action.payload.taskChatId).push(
        action.payload.message,
      );
    },
    setTaskChatMessages: (
      state,
      action: PayloadAction<{ messages: ChatMessage[]; taskChatId: string }>,
    ) => {
      state.messagesByTask[action.payload.taskChatId] = action.payload.messages;
    },
    setTaskDraft: (
      state,
      action: PayloadAction<{ message: string; taskChatId: string }>,
    ) => {
      state.draftsByTask[action.payload.taskChatId] = action.payload.message;
    },
    updateTaskChatMessage: (
      state,
      action: PayloadAction<{
        changes: ChatMessageChanges;
        id: number;
        taskChatId: string;
      }>,
    ) => {
      const message = getTaskMessages(state, action.payload.taskChatId).find(
        (item) => item.id === action.payload.id,
      );

      if (message) {
        Object.assign(message, action.payload.changes);
      }
    },
    appendResourceChatMessage: (
      state,
      action: PayloadAction<{
        message: ChatMessage;
        resourceChatId: string;
      }>,
    ) => {
      getResourceMessages(state, action.payload.resourceChatId).push(
        action.payload.message,
      );
    },
    setResourceChatMessages: (
      state,
      action: PayloadAction<{
        messages: ChatMessage[];
        resourceChatId: string;
      }>,
    ) => {
      state.messagesByResource[action.payload.resourceChatId] =
        action.payload.messages;
    },
    setResourceDraft: (
      state,
      action: PayloadAction<{ message: string; resourceChatId: string }>,
    ) => {
      state.draftsByResource[action.payload.resourceChatId] =
        action.payload.message;
    },
    updateResourceChatMessage: (
      state,
      action: PayloadAction<{
        changes: ChatMessageChanges;
        id: number;
        resourceChatId: string;
      }>,
    ) => {
      const message = getResourceMessages(
        state,
        action.payload.resourceChatId,
      ).find((item) => item.id === action.payload.id);

      if (message) {
        Object.assign(message, action.payload.changes);
      }
    },
    updateChatMessage: (
      state,
      action: PayloadAction<{
        id: number;
        team: ChatTeamId;
        changes: ChatMessageChanges;
      }>,
    ) => {
      const messages = getTeamMessages(state, action.payload.team);
      const message = findMessageUpdateTarget(
        messages,
        action.payload.id,
        action.payload.changes,
      );

      if (message) {
        Object.assign(message, action.payload.changes);
      }
    },
    updateConversationChatMessage: (
      state,
      action: PayloadAction<{
        changes: ChatMessageChanges;
        conversationKey: string;
        id: number;
      }>,
    ) => {
      const messages = getConversationMessages(
        state,
        action.payload.conversationKey,
      );
      const message = findMessageUpdateTarget(
        messages,
        action.payload.id,
        action.payload.changes,
      );
      if (message) Object.assign(message, action.payload.changes);
    },
    /** Drops what a viewer kept of Task chats they can no longer read. */
    forgetTaskChats: (state, action: PayloadAction<readonly string[]>) => {
      for (const taskChatId of action.payload) {
        delete state.messagesByTask[taskChatId];
        delete state.draftsByTask[taskChatId];
      }
    },
    upsertChatMessage: (state, action: PayloadAction<ChatMessage>) => {
      const messages = getTeamMessages(state, action.payload.team);
      const nextMessage = action.payload;
      const existingIndex = findEquivalentMessageIndex(messages, nextMessage);

      if (existingIndex >= 0) {
        messages.splice(
          existingIndex,
          1,
          mergeEquivalentMessage(messages[existingIndex], nextMessage),
        );
        return;
      }

      messages.push(nextMessage);
    },
  },
});

export const {
  setArtifactDraft,
  appendChatMessage,
  appendConversationChatMessage,
  appendResourceChatMessage,
  appendTaskChatMessage,
  forgetTaskChats,
  setResourceChatMessages,
  setResourceDraft,
  setConversationChatMessages,
  setTeamChatMessages,
  setTaskChatMessages,
  setTaskDraft,
  setTeamDraft,
  updateChatMessage,
  updateConversationChatMessage,
  updateResourceChatMessage,
  updateTaskChatMessage,
  upsertChatMessage,
} = chatSlice.actions;

export const chatReducer = chatSlice.reducer;

export const selectChatMessagesByTeam = (
  state: { chat: ChatState },
  team: ChatTeamId,
) => state.chat.messagesByTeam[team] ?? emptyChatMessages;

export const selectConversationChatMessages = (
  state: { chat: ChatState },
  conversationKey: string,
) => state.chat.messagesByConversation[conversationKey] ?? emptyChatMessages;

export const selectChatDraftByTeam = (
  state: { chat: ChatState },
  team: ChatTeamId,
) => state.chat.draftsByTeam[team] ?? "";

export const selectTaskChatMessages = (
  state: { chat: ChatState },
  taskChatId: string,
) => state.chat.messagesByTask[taskChatId] ?? emptyChatMessages;

export const selectTaskChatDraft = (
  state: { chat: ChatState },
  taskChatId: string,
) => state.chat.draftsByTask[taskChatId] ?? "";

export const selectResourceChatMessages = (
  state: { chat: ChatState },
  resourceChatId: string,
) => state.chat.messagesByResource[resourceChatId] ?? emptyChatMessages;

export const selectResourceChatDraft = (
  state: { chat: ChatState },
  resourceChatId: string,
) => state.chat.draftsByResource[resourceChatId] ?? "";

export type { ChatState };
