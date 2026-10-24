import { Extension } from "@tiptap/react";
import { Plugin } from "@tiptap/pm/state";

// Resolve older stored Canvas links without rewriting authored documents.
export function documentLocation(href: string): string | null {
  let url: URL;
  try {
    url = new URL(href, window.location.href);
  } catch {
    return null;
  }
  if (url.origin !== window.location.origin) return null;
  if (/^\/projects\/[^/]+\/canvas$/.test(url.pathname)) {
    url.pathname = url.pathname.replace(
      /\/canvas$/,
      url.searchParams.has("versionId") ? "/artifacts" : "/pages",
    );
    url.searchParams.delete("mode");
  }
  if (
    !/^\/projects\/[^/]+\/(pages|artifacts)$/.test(url.pathname) &&
    !/^\/wiki\/pages\/[^/]+$/.test(url.pathname)
  )
    return null;
  return `${url.pathname}${url.search}${url.hash}`;
}

export const DocumentLinks = Extension.create<{
  navigate: (location: string) => void;
}>({
  name: "documentLinks",
  priority: 1100,
  addOptions() {
    return { navigate: () => {} };
  },
  addProseMirrorPlugins() {
    const navigate = this.options.navigate;
    return [
      new Plugin({
        props: {
          handleClick: (_view, _pos, event) => {
            const anchor =
              event.target instanceof Element
                ? event.target.closest("a[href]")
                : null;
            if (!anchor || !(anchor instanceof HTMLAnchorElement)) return false;
            const location = documentLocation(anchor.href);
            if (!location) return false;
            event.preventDefault();
            if (event.metaKey || event.ctrlKey || event.shiftKey) {
              window.open(location, "_blank", "noopener,noreferrer");
            } else {
              navigate(location);
            }
            return true;
          },
        },
      }),
    ];
  },
});
