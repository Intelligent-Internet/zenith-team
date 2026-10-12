import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ChevronDown, ChevronUp } from "@/shared/ui/icons";
import "@/features/chat-panel/styles/chat-user-message-content.css";

export function ChatUserMessageContent({ children }: { children: ReactNode }) {
  const contentId = useId();
  const contentRef = useRef<HTMLDivElement>(null);
  const [canExpand, setCanExpand] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const previewHeight = 176;

  useLayoutEffect(() => {
    const content = contentRef.current;
    if (!content) return;
    const measure = () => setCanExpand(content.scrollHeight > previewHeight);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(content);
    return () => observer.disconnect();
  }, []);

  const clipped = canExpand && !expanded;

  return (
    <>
      <div
        id={contentId}
        className="workspace-user-message-content"
        data-collapsed={clipped || undefined}
        style={clipped ? { maxHeight: previewHeight } : undefined}
        onFocusCapture={() => setExpanded(true)}
      >
        <div ref={contentRef}>{children}</div>
      </div>
      {canExpand ? (
        <button
          className="workspace-user-message-content__toggle"
          type="button"
          aria-controls={contentId}
          aria-expanded={expanded}
          onClick={() => setExpanded((current) => !current)}
        >
          {expanded ? "Show less" : "Show full message"}
          {expanded ? (
            <ChevronUp size={14} aria-hidden="true" />
          ) : (
            <ChevronDown size={14} aria-hidden="true" />
          )}
        </button>
      ) : null}
    </>
  );
}
