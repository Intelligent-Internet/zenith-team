import { Streamdown, defaultRehypePlugins } from "streamdown";
import { MemberProfileTrigger } from "@/shared/chat/member-profile";
import type {
  DiscussionDetail,
  DiscussionMessage,
} from "../api/discussions-api";
import { discussionMentionHighlightPlugin } from "../model/discussion-mentions";

export function DiscussionMessageBody({
  body,
  members = [],
}: {
  body: DiscussionMessage["body"];
  members?: DiscussionDetail["members"];
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
        mark: ({ node, children }) => {
          const start = node?.properties?.dataMentionStart;
          const reference = body?.references.find(
            (item) => start !== undefined && item.start === Number(start),
          );
          const id =
            reference?.type === "user"
              ? reference.userId
              : reference?.type === "agent"
                ? reference.agentId
                : undefined;
          if (!id || !reference || !body)
            return <mark className="discussion-mention">{children}</mark>;
          const member =
            reference.type === "user"
              ? members.find((item) => item.userId === id)
              : undefined;
          return (
            <mark className="discussion-mention">
              <MemberProfileTrigger
                profile={{
                  id,
                  kind: reference.type === "agent" ? "agent" : "human",
                  name:
                    member?.displayName ??
                    body.markdown
                      .slice(reference.start, reference.end)
                      .replace(/^@/, ""),
                  avatarSrc: member?.avatarUrl,
                  title:
                    member?.active === false
                      ? "No longer active"
                      : reference.type === "agent"
                        ? "Agent"
                        : "Team member",
                  archived: member?.active === false,
                }}
              >
                {children}
              </MemberProfileTrigger>
            </mark>
          );
        },
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
