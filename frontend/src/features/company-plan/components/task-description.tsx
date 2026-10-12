import { Streamdown } from "streamdown";
import { chatMarkdownLinkComponents } from "@/features/chat-panel/components/chat-markdown-link";

export function TaskDescription({ description }: { description: string }) {
  return (
    <Streamdown
      className="task-description text-sm leading-6 text-[var(--workspace-native-text-muted)] [overflow-wrap:anywhere] [&_h1]:text-base [&_h2]:text-base [&_h3]:text-sm [&_pre]:max-w-full [&_pre]:overflow-x-auto [&_table]:text-xs"
      components={chatMarkdownLinkComponents}
      controls={false}
      isAnimating={false}
      parseIncompleteMarkdown={false}
    >
      {description || "No task description yet."}
    </Streamdown>
  );
}
