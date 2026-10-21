import { createApi, fakeBaseQuery } from "@reduxjs/toolkit/query/react";

import {
  ApiClientError,
  type ApiErrorBody,
  getApiErrorBody,
  getApiErrorCode,
  getApiErrorMessage,
} from "@/lib/api-client";

export type ZenithTeamApiError = {
  body?: ApiErrorBody | null;
  code?: string | null;
  message: string;
  status: number | null;
};

/**
 * The message of a failed request: RTK's `unwrap()` rejects with the plain
 * `ZenithTeamApiError` object (not an `Error`), whose message already carries
 * the mapped copy for its code.
 */
export function getZenithTeamQueryErrorMessage(
  error: unknown,
  fallback = "Something went wrong. Please try again.",
): string {
  if (error && typeof error === "object" && "message" in error) {
    const message = (error as { message: unknown }).message;
    if (typeof message === "string" && message.trim()) return message;
  }
  return fallback;
}

export function isRetryableZenithTeamQueryError(error: unknown): boolean {
  if (!error || typeof error !== "object" || !("status" in error)) {
    return false;
  }
  return (
    error.status === null ||
    error.status === 429 ||
    (typeof error.status === "number" && error.status >= 500)
  );
}

export const zenithTeamApi = createApi({
  baseQuery: fakeBaseQuery<ZenithTeamApiError>(),
  endpoints: () => ({}),
  invalidationBehavior: "delayed",
  // Screen-level data is kept across normal workspace navigation. Realtime
  // events and mutation tags still converge active entries immediately.
  keepUnusedDataFor: 300,
  reducerPath: "zenithTeamApi",
  tagTypes: [
    "Canvas",
    "CanvasHistory",
    "CanvasThreads",
    "MemberPresence",
    "Account",
    "Analytics",
    "Calendar",
    "Social",
    "Studio",
    "Activity",
    "Approval",
    "Artifact",
    "Company",
    "Group",
    "Connector",
    "Conversation",
    "ChannelAgent",
    "ChannelAgentActivity",
    "Deliverable",
    "DiscussionDetail",
    "DiscussionList",
    "Message",
    "Inbox",
    "Invitation",
    "Member",
    "AgentMail",
    "Mailbox",
    "Plugin",
    "ProactiveMode",
    "ProjectResource",
    "Project",
    "Question",
    "Run",
    "Runtime",
    "Schedule",
    "Task",
    "TaskReport",
    "Agent",
    "AgentSkill",
    "AgentBinding",
    "Workflow",
    "WorkspaceAsset",
    "Wiki",
    "Whiteboard",
  ],
});

export async function executeZenithTeamQuery<T>(
  request: () => Promise<T>,
  fallback: string,
  options: { retry?: boolean } = {},
) {
  const attempts = options.retry ? 2 : 1;

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    try {
      return { data: await request() } as const;
    } catch (error) {
      if (attempt === attempts) {
        const status = error instanceof ApiClientError ? error.status : null;
        return {
          error: {
            body: getApiErrorBody(error),
            code: getApiErrorCode(error),
            message: getApiErrorMessage(error, fallback),
            status,
          },
        } as const;
      }
    }
  }

  return { error: { message: fallback, status: null } } as const;
}
