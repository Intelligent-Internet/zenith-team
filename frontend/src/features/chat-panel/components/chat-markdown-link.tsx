import {
  createContext,
  useContext,
  type ComponentProps,
  type ReactNode,
} from "react";
import { useNavigate } from "react-router";
import { defaultRehypePlugins, type StreamdownProps } from "streamdown";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import { openDesktopExternal } from "@/shared/desktop/desktop-environment.service";
import { cx } from "@/shared/lib/cx";
import { toast } from "@/shared/ui/sonner";

type ChatMarkdownLinkProps = ComponentProps<"a"> & {
  node?: unknown;
};

export type ChatWorkspaceFileLinkTarget =
  | { kind: "workspace-file"; path: string }
  | { kind: "workspace-file-link"; href: string };

type ChatMarkdownLinkTarget =
  | { kind: "external"; url: string }
  | { kind: "fragment"; id: string }
  | { kind: "internal"; route: string }
  | ChatWorkspaceFileLinkTarget
  | { kind: "unsupported" };

const linkClassName =
  "inline cursor-pointer border-0 bg-transparent p-0 align-baseline [color:inherit] [font:inherit] underline underline-offset-2";
const internalRouteRoots = new Set([
  "activity",
  "agents",
  "app",
  "approvals",
  "assets",
  "calendar",
  "inbox",
  "mailbox",
  "more",
  "notifications",
  "privacy",
  "projects",
  "resources",
  "runs",
  "scheduled",
  "settings",
  "social",
  "tasks",
  "team",
  "terms",
  "wiki",
]);
const ChatMarkdownLinkContext = createContext<
  ((target: ChatWorkspaceFileLinkTarget) => void) | undefined
>(undefined);

export function ChatMarkdownLinkProvider({
  children,
  onWorkspaceFileOpen,
}: {
  children: ReactNode;
  onWorkspaceFileOpen?: (target: ChatWorkspaceFileLinkTarget) => void;
}) {
  return (
    <ChatMarkdownLinkContext.Provider value={onWorkspaceFileOpen}>
      {children}
    </ChatMarkdownLinkContext.Provider>
  );
}

export function ChatMarkdownLink({
  children,
  className,
  href,
  node: _node,
  title,
}: ChatMarkdownLinkProps) {
  const navigate = useNavigate();
  const openWorkspaceFile = useContext(ChatMarkdownLinkContext);
  const target = resolveChatMarkdownLink(href);

  if (target.kind === "external") {
    return (
      <a
        className={className}
        href={target.url}
        rel="noopener noreferrer"
        target="_blank"
        title={title}
        onClick={(event) => {
          event.preventDefault();
          void openExternalLink(target.url);
        }}
      >
        {children}
      </a>
    );
  }

  return (
    <button
      className={cx(linkClassName, className)}
      title={title}
      type="button"
      onClick={() => {
        if (target.kind === "internal") {
          navigate(target.route);
          return;
        }
        if (target.kind === "fragment") {
          scrollToFragment(target.id);
          return;
        }
        if (
          (target.kind === "workspace-file" ||
            target.kind === "workspace-file-link") &&
          openWorkspaceFile
        ) {
          openWorkspaceFile(target);
          return;
        }

        toast.error(
          target.kind === "workspace-file" ||
            target.kind === "workspace-file-link"
            ? "This workspace file is no longer connected to its run."
            : "This link is not supported.",
        );
      }}
    >
      {children}
    </button>
  );
}

export const chatMarkdownLinkComponents = {
  a: ChatMarkdownLink,
} satisfies StreamdownProps["components"];

// ChatMarkdownLink routes local links through the workspace viewer. Streamdown's
// hardener rejects them before that component runs; retain HTML sanitization
// while allowing file hrefs (never image sources) to reach our link handler.
export const chatMarkdownRehypePlugins: StreamdownProps["rehypePlugins"] = [
  defaultRehypePlugins.raw,
  defaultRehypePlugins.katex,
  [
    rehypeSanitize,
    {
      ...defaultSchema,
      protocols: {
        ...defaultSchema.protocols,
        href: [...(defaultSchema.protocols?.href ?? []), "file", "streamdown"],
      },
    },
  ],
];

function resolveChatMarkdownLink(
  href: string | undefined,
): ChatMarkdownLinkTarget {
  const value = href?.trim();
  if (!value || value === "streamdown:incomplete-link") {
    return { kind: "unsupported" };
  }

  const workspacePath = workspaceFilePathFromHref(value);
  if (workspacePath) {
    return { kind: "workspace-file", path: workspacePath };
  }

  if (value.startsWith("file:")) {
    return { kind: "workspace-file-link", href: value };
  }

  if (value.startsWith("#/")) {
    return isInternalRoute(value.slice(1))
      ? { kind: "internal", route: value.slice(1) }
      : { kind: "unsupported" };
  }
  if (value.startsWith("#")) {
    const id = decodeFragment(value.slice(1));
    return id ? { id, kind: "fragment" } : { kind: "unsupported" };
  }

  // Resolve workspace-relative links before URL resolution can turn assets/...
  // into an application route. Root-relative application routes stay below.
  if (!value.startsWith("/") && !/^[a-z][a-z\d+.-]*:/i.test(value)) {
    const path = decodeFragment(value.split(/[?#]/, 1)[0]);
    const segments = path.replaceAll("\\", "/").split("/");
    if (segments[0] === ".") segments.shift();
    if (
      segments.length > 0 &&
      segments.every(
        (segment) =>
          segment &&
          segment !== "." &&
          segment !== ".." &&
          ![...segment].some((character) => character.charCodeAt(0) < 32),
      )
    ) {
      return { kind: "workspace-file", path: segments.join("/") };
    }
    return { kind: "unsupported" };
  }

  try {
    const currentUrl = new URL(window.location.href);
    const targetUrl = new URL(value, currentUrl);

    if (isSameAppUrl(targetUrl, currentUrl)) {
      const route = internalRouteFromUrl(targetUrl, currentUrl);
      if (!isInternalRoute(route)) {
        return value.startsWith("/") && !value.startsWith("//")
          ? { kind: "workspace-file-link", href: value }
          : { kind: "unsupported" };
      }
      return {
        kind: "internal",
        route,
      };
    }
    if (targetUrl.protocol === "http:" || targetUrl.protocol === "https:") {
      return { kind: "external", url: targetUrl.href };
    }
  } catch {
    return { kind: "unsupported" };
  }

  return { kind: "unsupported" };
}

function workspaceFilePathFromHref(value: string) {
  const isFileUrl = value.startsWith("file:");
  const isAbsolutePath = value.startsWith("/") && !value.startsWith("//");
  if (!isFileUrl && !isAbsolutePath) {
    return "";
  }

  let pathname = value;
  if (isFileUrl) {
    try {
      pathname = new URL(value).pathname;
    } catch {
      return "";
    }
  }

  const decodedPath = decodeFragment(pathname).replaceAll("\\", "/");
  const workspaceMarker = "/workdir/";
  const markerIndex = decodedPath.lastIndexOf(workspaceMarker);
  if (markerIndex < 0) {
    return "";
  }

  const relativePath = decodedPath.slice(markerIndex + workspaceMarker.length);
  const segments = relativePath.split("/");
  if (
    !relativePath ||
    segments.some(
      (segment) =>
        !segment ||
        segment === "." ||
        segment === ".." ||
        [...segment].some((character) => character.charCodeAt(0) < 32),
    )
  ) {
    return "";
  }

  return segments.join("/");
}

function isSameAppUrl(targetUrl: URL, currentUrl: URL) {
  if (currentUrl.protocol === "http:" || currentUrl.protocol === "https:") {
    return targetUrl.origin === currentUrl.origin;
  }

  return (
    targetUrl.protocol === currentUrl.protocol &&
    targetUrl.hostname === currentUrl.hostname
  );
}

function internalRouteFromUrl(targetUrl: URL, currentUrl: URL) {
  if (
    targetUrl.pathname === currentUrl.pathname &&
    targetUrl.hash.startsWith("#/")
  ) {
    return targetUrl.hash.slice(1);
  }

  return `${targetUrl.pathname}${targetUrl.search}${targetUrl.hash}`;
}

function isInternalRoute(route: string) {
  const pathname = route.split(/[?#]/, 1)[0];
  if (pathname === "/") {
    return true;
  }

  const root = pathname.split("/").filter(Boolean)[0];
  return Boolean(root && internalRouteRoots.has(root));
}

function decodeFragment(value: string) {
  try {
    return decodeURIComponent(value);
  } catch {
    return "";
  }
}

function scrollToFragment(id: string) {
  const target = document.getElementById(id);
  if (!target) {
    toast.error("This section is not available.");
    return;
  }

  target.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

async function openExternalLink(url: string) {
  try {
    const result = await openDesktopExternal(url);
    if (!result.ok) {
      toast.error(result.reason);
    }
  } catch {
    toast.error("The link could not be opened.");
  }
}
