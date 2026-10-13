import { memo } from "react";
import { Streamdown } from "streamdown";
export const MarkdownView = memo(function MarkdownView({
  children,
}: {
  children: string;
}) {
  return (
    <Streamdown
      className="pc-document-prose"
      mode="static"
      controls={false}
      rehypePlugins={[]}
      remarkRehypeOptions={{ allowDangerousHtml: false }}
      components={{
        img: ({ alt }) => (
          <span className="p-[8px_14px] text-[var(--pc-muted)] text-[length:11px]">
            {alt ? `Image: ${alt}` : "Embedded image"}
          </span>
        ),
        a: ({ href, children }) =>
          /^https?:\/\//i.test(href ?? "") ? (
            <a href={href} target="_blank" rel="noopener noreferrer">
              {children}
            </a>
          ) : (
            <span>{children}</span>
          ),
      }}
    >
      {children}
    </Streamdown>
  );
});
