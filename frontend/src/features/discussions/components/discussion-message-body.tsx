import { Streamdown, defaultRehypePlugins } from "streamdown";
import type { DiscussionMessage } from "../api/discussions-api";
import { discussionMentionHighlightPlugin } from "../model/discussion-mentions";

export function DiscussionMessageBody({
  body,
}: {
  body: DiscussionMessage["body"];
}) {
  return (
    <Streamdown
      key={JSON.stringify(body?.references)}
      className="discussion-markdown"
      rehypePlugins={[
        ...Object.values(defaultRehypePlugins),
        [discussionMentionHighlightPlugin, body],
      ]}
      mode="static"
      controls={false}
      parseIncompleteMarkdown={false}
      remarkRehypeOptions={{ allowDangerousHtml: false }}
      components={{
        a: ({ href, children }) =>
          /^https?:\/\//i.test(href ?? "") ? (
            <a
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              referrerPolicy="no-referrer"
            >
              {children}
            </a>
          ) : (
            <span>{children}</span>
          ),
        img: () => null,
        pre: ({ children }) => <pre>{children}</pre>,
        code: ({ children }) => <code>{children}</code>,
      }}
    >
      {body?.markdown ?? ""}
    </Streamdown>
  );
}
