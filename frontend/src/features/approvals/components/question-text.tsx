import { Streamdown } from "streamdown";

import { chatMarkdownLinkComponents } from "@/features/chat-panel/components/chat-markdown-link";

export function QuestionText({ children }: { children: string }) {
  return (
    <Streamdown
      className="agent-question-text"
      components={chatMarkdownLinkComponents}
      controls={false}
      mode="static"
      parseIncompleteMarkdown={false}
    >
      {children}
    </Streamdown>
  );
}
