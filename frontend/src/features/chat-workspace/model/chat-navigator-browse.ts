import type { AgentDto } from "@/shared/api/types";

export type ChatNavigatorBrowseState = {
  view: "recent" | "agents" | "archived";
  agentId: string | null;
  query: string;
};

export const chatNavigatorSections = [
  "pinned",
  "channels",
  "directMessages",
  "tasks",
] as const;

export type ChatNavigatorSection = (typeof chatNavigatorSections)[number];

export type ChatNavigatorPreferences = {
  collapsedSections: ChatNavigatorSection[];
};

export const initialChatNavigatorPreferences: ChatNavigatorPreferences = {
  collapsedSections: [],
};

export type ChatNavigatorAgent = Pick<AgentDto, "id" | "name" | "title"> & {
  archived: boolean;
  canStartChat: boolean;
};

export const initialChatNavigatorBrowse: ChatNavigatorBrowseState = {
  view: "recent",
  agentId: null,
  query: "",
};

export function matchesChatNavigatorSearch(text: string, query: string) {
  const normalize = (value: string) =>
    value
      .normalize("NFKD")
      .replace(/[\u0300-\u036f]/g, "")
      .toLocaleLowerCase()
      .replaceAll("đ", "d")
      .trim();
  return normalize(text).includes(normalize(query));
}

export function directConversationTitle(
  title: string | null | undefined,
  agentName: string,
) {
  return title?.trim() ? title : "Chat with " + agentName;
}
