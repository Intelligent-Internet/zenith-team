import { createContext, useContext } from "react";

/**
 * A compact chat, such as a Board's: text and tags, without the formatting
 * bar, emoji picker, mention button and help, @here, or who manages an Agent.
 */
const CompactDiscussion = createContext(false);

export const CompactDiscussionProvider = CompactDiscussion.Provider;

export function useCompactDiscussion() {
  return useContext(CompactDiscussion);
}
