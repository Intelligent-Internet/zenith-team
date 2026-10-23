import {
  Children,
  Fragment,
  isValidElement,
  useId,
  type ReactNode,
} from "react";
import { fromMarkdown } from "mdast-util-from-markdown";
import { gfmFromMarkdown } from "mdast-util-gfm";
import { gfm } from "micromark-extension-gfm";
import katex from "katex";
import { safeURL } from "./style";
import { InlineCodeBlock } from "./code-block";

// Parse prose and component slots together, so emphasis and lists may span native
// inline children. Slots carry React nodes; evaluated strings never become markup.
export function InlineMarkdown({
  children,
  inline = false,
}: {
  children: ReactNode;
  inline?: boolean;
}) {
  const prefix = `zenith-slot-${useId().replace(/[^a-z0-9]/gi, "")}-`;
  const slots: ReactNode[] = [];
  const slot = (node: ReactNode) => {
    const index = slots.push(node) - 1;
    return `[${prefix}${index}](#${prefix}${index})`;
  };
  const serialize = (nodes: ReactNode): string =>
    Children.toArray(nodes)
      .map((node) => {
        if (typeof node === "string" || typeof node === "number")
          return String(node);
        if (
          isValidElement<{ children?: ReactNode }>(node) &&
          node.type === Fragment
        )
          return serialize(node.props.children);
        return slot(node);
      })
      .join("");
  // Shield math from Markdown and keep code spans/fences literal. The component
  // parser also skips these delimiters, including LaTeX braces and angle brackets.
  // In inline-only components, indentation belongs to the component source,
  // not Markdown code blocks. Keep block-container Markdown whitespace intact.
  const prose = serialize(children);
  const source = (
    inline
      ? prose
          .replace(/\s+/g, (space) => (space.includes("\n") ? " " : space))
          .trim()
      : prose
  ).replace(
    /(`+)[\s\S]*?\1|(~{3,})[\s\S]*?\2|\\\(([\s\S]*?)\\\)|\\\[([\s\S]*?)\\\]/g,
    (match, code, fence, formula, display) => {
      if (code || fence) return match;
      return slot(
        <span
          className={
            display !== undefined ? "block overflow-x-auto" : undefined
          }
          dangerouslySetInnerHTML={{
            __html: katex.renderToString(formula ?? display, {
              displayMode: display !== undefined,
              throwOnError: false,
              trust: false,
              strict: "error",
              maxExpand: 100,
              maxSize: 10,
            }),
          }}
        />,
      );
    },
  );
  const tree = fromMarkdown(source, {
    extensions: [gfm()],
    mdastExtensions: [gfmFromMarkdown()],
  });
  if (
    inline &&
    tree.children.some(
      (node) => node.type !== "paragraph" && node.type !== "definition",
    )
  )
    throw new Error(
      "This component requires inline Markdown; put block Markdown in a box or other generic container.",
    );
  const render = (node: any, key: number): ReactNode => {
    const content = node.children?.map(render);
    switch (node.type) {
      case "root":
        return content;
      case "text":
        return node.value;
      case "paragraph":
        return inline ||
          node.children.every(
            (child: any) =>
              (child.type === "link" && child.url.startsWith(`#${prefix}`)) ||
              (child.type === "text" && !child.value.trim()),
          ) ? (
          <Fragment key={key}>{content}</Fragment>
        ) : (
          <div key={key} className="my-2 first:mt-0 last:mb-0">
            {content}
          </div>
        );
      case "strong":
        return <strong key={key}>{content}</strong>;
      case "emphasis":
        return <em key={key}>{content}</em>;
      case "delete":
        return <s key={key}>{content}</s>;
      case "inlineCode":
        return (
          <code
            key={key}
            className="rounded bg-workspace-control px-1 font-mono text-[0.9em]"
          >
            {node.value}
          </code>
        );
      case "code":
        if (node.lang === "mermaid")
          return (
            <InlineCodeBlock key={key} source={node.value} language="mermaid" />
          );
        return (
          <pre
            key={key}
            className="max-w-full overflow-auto rounded-workspace-control bg-workspace-control p-3 text-sm"
          >
            <code>{node.value}</code>
          </pre>
        );
      case "link": {
        const index = node.url.startsWith(`#${prefix}`)
          ? Number(node.url.slice(prefix.length + 1))
          : -1;
        if (Number.isInteger(index) && index >= 0 && index < slots.length)
          return <Fragment key={key}>{slots[index]}</Fragment>;
        const url = safeURL(node.url);
        return url ? (
          <a
            key={key}
            href={url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-workspace-accent underline"
          >
            {content}
          </a>
        ) : (
          <span key={key}>{content}</span>
        );
      }
      case "list":
        return node.ordered ? (
          <ol
            key={key}
            start={node.start}
            className="list-decimal space-y-1 pl-5"
          >
            {content}
          </ol>
        ) : (
          <ul key={key} className="list-disc space-y-1 pl-5">
            {content}
          </ul>
        );
      case "listItem":
        return (
          <li key={key}>
            {typeof node.checked === "boolean" && (
              <input
                type="checkbox"
                checked={node.checked}
                disabled
                aria-label={node.checked ? "Completed" : "Not completed"}
                className="mr-2"
              />
            )}
            {content}
          </li>
        );
      case "blockquote":
        return (
          <blockquote
            key={key}
            className="border-l-2 border-workspace-divider pl-3"
          >
            {content}
          </blockquote>
        );
      case "thematicBreak":
        return <hr key={key} className="my-3 border-workspace-divider" />;
      case "break":
        return "\n";
      case "heading":
        return (
          <div
            key={key}
            role="heading"
            aria-level={node.depth}
            className="my-2 font-semibold"
          >
            {content}
          </div>
        );
      case "table":
        return (
          <div key={key} className="max-w-full overflow-auto">
            <table className="w-full text-left text-sm">
              <thead>
                <tr>
                  {node.children[0].children.map((cell: any, index: number) => (
                    <th
                      key={index}
                      className="border-b border-workspace-divider p-2"
                      style={{ textAlign: node.align[index] ?? undefined }}
                    >
                      {cell.children.map(render)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {node.children.slice(1).map((row: any, index: number) => (
                  <tr key={index}>
                    {row.children.map((cell: any, i: number) => (
                      <td
                        key={i}
                        className="border-b border-workspace-divider p-2"
                        style={{ textAlign: node.align[i] ?? undefined }}
                      >
                        {cell.children.map(render)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
      case "html":
        return node.value; // HTML is never an executable Markdown escape hatch.
      case "image":
        return <span key={key}>{node.alt ?? "Image"}</span>;
      default:
        return <Fragment key={key}>{content ?? node.value}</Fragment>;
    }
  };
  return <>{render(tree, 0)}</>;
}
