import { lazy } from "react";

export const workspaceSidebarPages = [
  {
    id: "chat",
    label: "Chat",
    matches: (pathname: string) => pathname === "/app",
    Preview: lazy(() =>
      import("@/features/chat-workspace/routes/chat-workspace-page").then(
        (module) => ({ default: module.ChatWorkspacePage }),
      ),
    ),
  },
  {
    id: "wiki",
    label: "Wiki",
    matches: (pathname: string) =>
      pathname === "/wiki" || pathname.startsWith("/wiki/"),
    Preview: lazy(() =>
      import("@/features/company-wiki/wiki-page").then((module) => ({
        default: module.WikiPage,
      })),
    ),
  },
] as const;
