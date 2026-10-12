import type { ConversationTranscriptEntryDto } from "@/shared/api/types";

export type ToolActivity = {
  id: string;
  name: string;
  detail: string;
  url?: string;
  status: string;
  label: string;
  category: "website" | "search" | "document" | "command" | "email" | "tool";
  count: number;
};

function describeTool(name: string, input?: Record<string, unknown>) {
  const tool = name.replace(/^mcp__[\w-]+__/, "").toLowerCase();
  const action = input?.action;
  const operation =
    action && typeof action === "object" && "type" in action
      ? String(action.type)
      : "";
  const description = `${name.toLowerCase()} ${operation}`;
  if (
    /^(exec_command|bash|shell|shell_command|command_execution)$/.test(tool)
  ) {
    const command = input?.command ?? input?.cmd;
    const executable =
      typeof command === "string" ? command.trim().split(/\s+/)[0] : "";
    if (executable === "curl" || executable === "wget")
      return { label: "Fetching web content", category: "website" as const };
    if (executable === "cat" || executable === "head" || executable === "tail")
      return { label: "Reading files", category: "document" as const };
    if (executable === "rg" || executable === "grep" || executable === "find")
      return { label: "Searching files", category: "search" as const };
    if (executable === "ls" || executable === "pwd")
      return {
        label: "Exploring your workspace",
        category: "document" as const,
      };
    return { label: "Running a command", category: "command" as const };
  }
  if (
    /gmail|email|mail_message/.test(description) &&
    /search|list|get|read/.test(description)
  )
    return { label: "Reviewing business emails", category: "email" as const };
  if (/drive.*search|search.*drive/.test(description))
    return { label: "Finding relevant documents", category: "search" as const };
  if (
    /fetch|browse|web_fetch|webfetch|open_url/.test(description) ||
    operation === "open"
  )
    return { label: "Reading your website", category: "website" as const };
  if (/search|websearch/.test(description))
    return { label: "Searching for information", category: "search" as const };
  if (
    /github/.test(description) &&
    /read|get_file|list_files/.test(description)
  )
    return { label: "Exploring your codebase", category: "document" as const };
  if (/read|document|wiki/.test(description))
    return { label: "Reading documents", category: "document" as const };
  if (/update_plan/.test(tool))
    return { label: "Organizing the research", category: "tool" as const };
  return {
    label: `Using ${tool.replaceAll("_", " ") || "a tool"}`,
    category: "tool" as const,
  };
}

function toolTarget(input: Record<string, unknown> | undefined) {
  const action = input?.action;
  const source =
    action && typeof action === "object" && !Array.isArray(action)
      ? (action as Record<string, unknown>)
      : input;
  const rawUrl = source?.url;
  if (typeof rawUrl === "string") {
    try {
      const url = new URL(rawUrl);
      if (url.protocol === "https:" || url.protocol === "http:") {
        // Do not expose credentials, query tokens or fragment data in activity links.
        const safeUrl = `${url.origin}${url.pathname}`;
        return { detail: safeUrl, url: safeUrl };
      }
    } catch {
      /* A provider may supply a reference id instead of a URL. */
    }
  }
  const query =
    source?.query ??
    source?.q ??
    (Array.isArray(source?.queries)
      ? source.queries.find((item) => typeof item === "string")
      : undefined);
  return { detail: typeof query === "string" ? query.slice(0, 240) : "" };
}

export function toolActivities(entries: ConversationTranscriptEntryDto[]) {
  const activities = new Map<string, ToolActivity>();
  for (const entry of entries) {
    for (const part of entry.parts) {
      if (part.type !== "toolCall" && part.type !== "toolResult") continue;
      const previous = activities.get(part.callId);
      if (part.type === "toolCall") {
        activities.set(part.callId, {
          id: part.callId,
          name: part.name,
          ...describeTool(part.name, part.input),
          ...toolTarget(part.input),
          status: previous?.status ?? "in_progress",
          count: 1,
        });
      } else {
        activities.set(part.callId, {
          id: part.callId,
          name: previous?.name || part.name || "Tool",
          ...describeTool(previous?.name || part.name || "Tool"),
          ...(previous && {
            label: previous.label,
            category: previous.category,
          }),
          detail: previous?.detail ?? "",
          url: previous?.url,
          status: part.status ?? "completed",
          count: 1,
        });
      }
    }
  }
  const grouped: ToolActivity[] = [];
  for (const activity of activities.values()) {
    const previous = grouped.at(-1);
    if (
      previous?.status === "completed" &&
      activity.status === "completed" &&
      previous.name === activity.name &&
      previous.label === activity.label &&
      previous.detail === activity.detail
    ) {
      previous.count += 1;
    } else grouped.push({ ...activity });
  }
  return grouped;
}
