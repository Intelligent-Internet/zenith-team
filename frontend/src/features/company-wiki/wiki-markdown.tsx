import { memo, useEffect, useMemo, useRef } from "react";
import {
  Streamdown,
  defaultRehypePlugins,
  type StreamdownProps,
} from "streamdown";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import { ChatMarkdownLink } from "@/features/chat-panel/components/chat-markdown-link";
import { Link2 } from "@/shared/ui/icons";
import type { WikiPageSummary } from "./wiki-api";
import {
  WikiArtifactChip,
  WikiArtifactsProvider,
  artifactRefSource,
} from "./wiki-artifacts";
import { encodeWikiLinkLabel } from "./wiki-link-markdown";

// Module-level so Streamdown's block memo keeps its cache across renders.
const wikiRehypePlugins: StreamdownProps["rehypePlugins"] = [
  defaultRehypePlugins.raw,
  defaultRehypePlugins.katex,
  [
    rehypeSanitize,
    {
      ...defaultSchema,
      tagNames: [...(defaultSchema.tagNames ?? []), "u"],
    },
  ],
  defaultRehypePlugins.harden,
];

const wikiLinkPattern = /\[\[([0-9a-fA-F-]{36})(?:\|([^\]]+))?\]\]/g;
const artifactRefPattern = new RegExp(artifactRefSource, "g");

// Stored labels are markdown source, so only their `|` needs encoding to keep a
// table cell whole; a page title fallback is plain text and is fully encoded.
// An Artifact citation becomes a `#artifact-<id>-<ordinal|all>` link that the
// `a` renderer turns into a chip resolved for the viewer.
function markdownWithLinks(body: string, pages: WikiPageSummary[]) {
  return outsideCode(body, (text) =>
    text
      .replace(
        artifactRefPattern,
        (_match, id: string, ordinal: string | undefined) =>
          `[Artifact](#artifact-${id}-${ordinal ?? "all"})`,
      )
      .replace(
        wikiLinkPattern,
        (_match, id: string, label: string | undefined) => {
          const text = label
            ? label.replace(/\\?\|/g, "&#124;")
            : encodeWikiLinkLabel(
                pages.find((page) => page.id === id)?.title || "Linked page",
              );
          return `[${text}](#wiki-${id})`;
        },
      ),
  );
}

// Fenced blocks and inline code spans stay literal, as they do in the editor,
// whose markdown tokenizers never run inside code.
const codeSegment = /^(`{3,}|~{3,})[^\n]*\n[\s\S]*?^\1[^\n]*$|(`+)[^`]+?\2/gm;

function outsideCode(body: string, transform: (text: string) => string) {
  let result = "";
  let last = 0;
  for (const match of body.matchAll(codeSegment)) {
    result += transform(body.slice(last, match.index)) + match[0];
    last = match.index + match[0].length;
  }
  return result + transform(body.slice(last));
}

type WikiMarkdownProps = {
  companyId: string;
  body: string;
  /** Pages used to label `[[id]]` links that carry no label. */
  pages: WikiPageSummary[];
  /** Omit for read-only previews, where links render as plain text. */
  onOpenPage?: (pageId: string) => void;
};

/** Renders stored Wiki markdown as static, sanitized content. */
export const WikiMarkdown = memo(function WikiMarkdown({
  companyId,
  body,
  pages,
  onOpenPage,
}: WikiMarkdownProps) {
  const openPageRef = useRef(onOpenPage);
  useEffect(() => {
    openPageRef.current = onOpenPage;
  }, [onOpenPage]);
  const interactive = Boolean(onOpenPage);
  const components = useMemo<StreamdownProps["components"]>(
    () => ({
      a: (props) => {
        const { href, children } = props;
        const artifact = /^#artifact-([0-9a-fA-F-]{36})-(\d{1,2}|all)$/.exec(
          href ?? "",
        );
        if (artifact)
          return (
            <WikiArtifactChip
              id={artifact[1]}
              ordinal={artifact[2] === "all" ? null : Number(artifact[2])}
              interactive={interactive}
            />
          );
        if (!interactive) return <span>{children}</span>;
        if (href?.startsWith("#wiki-")) {
          const pageId = href.slice("#wiki-".length);
          return (
            <button
              className="zen-wiki__inline-link"
              onClick={() => openPageRef.current?.(pageId)}
            >
              {children}
              <Link2 size={13} />
            </button>
          );
        }
        return <ChatMarkdownLink {...props} />;
      },
    }),
    [interactive],
  );
  const markdown = useMemo(() => markdownWithLinks(body, pages), [body, pages]);

  return (
    <WikiArtifactsProvider companyId={companyId} body={body}>
      <Streamdown
        controls={false}
        isAnimating={false}
        parseIncompleteMarkdown={false}
        rehypePlugins={wikiRehypePlugins}
        components={components}
      >
        {markdown}
      </Streamdown>
    </WikiArtifactsProvider>
  );
});
