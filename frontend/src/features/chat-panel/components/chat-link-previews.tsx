import { ArrowUpRight, Globe } from "@/shared/ui/icons";
import { useEffect, useMemo, useRef, useState } from "react";
import { ChatMarkdownLink } from "./chat-markdown-link";
import {
  loadChatLinkPreview,
  messagePreviewLinks,
  type ChatLinkPreview,
} from "../model/chat-link-preview";
import "../styles/chat-link-previews.css";

export function ChatLinkPreviews({ text }: { text: string }) {
  const links = useMemo(() => messagePreviewLinks(text), [text]);
  if (!links.length) return null;
  return (
    <div
      aria-label="Shared links"
      className="chat-link-previews flex max-w-[560px] flex-col gap-[8px] mt-[9px]"
    >
      {links.map((url) => (
        <ChatLinkCard key={url} url={url} />
      ))}
    </div>
  );
}

function ChatLinkCard({ url }: { url: string }) {
  const [preview, setPreview] = useState<ChatLinkPreview>({});
  const [imageFailed, setImageFailed] = useState(false);
  const cardRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let active = true;
    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return;
        observer.disconnect();
        void loadChatLinkPreview(url).then((result) => {
          if (active) setPreview(result);
        });
      },
      { rootMargin: "120px" },
    );
    if (cardRef.current) observer.observe(cardRef.current);
    return () => {
      active = false;
      observer.disconnect();
    };
  }, [url]);
  const domain = new URL(url).hostname.replace(/^www\./, "");
  return (
    <div ref={cardRef} className="chat-link-preview-wrapper">
      <ChatMarkdownLink
        className="chat-link-preview"
        href={url}
        title={`Open ${url}`}
      >
        {preview.image && !imageFailed ? (
          <img
            alt=""
            src={preview.image}
            loading="lazy"
            referrerPolicy="no-referrer"
            crossOrigin="anonymous"
            onError={() => setImageFailed(true)}
          />
        ) : (
          <span className="grid w-[64px] h-[64px] shrink-0 place-items-center rounded-[6px] [background:var(--workspace-control-bg)] text-workspace-muted">
            <Globe aria-hidden="true" size={24} />
          </span>
        )}
        <span className="chat-link-preview__copy flex min-w-0 flex-1 flex-col gap-[6px] wrap-anywhere">
          <span className="flex items-center gap-[5px] text-workspace-muted text-[length:var(--workspace-text-caption,_12px)] leading-[18px]">
            {domain}
            <ArrowUpRight aria-hidden="true" size={12} />
          </span>
          <strong>{preview.title || domain}</strong>
          {preview.description ? (
            <span className="[display:-webkit-box] overflow-hidden text-workspace-muted text-[length:var(--workspace-text-chat,_15px)] leading-[var(--workspace-leading-chat,_24px)] [-webkit-line-clamp:2] [-webkit-box-orient:vertical]">
              {preview.description}
            </span>
          ) : (
            <span className="overflow-hidden text-workspace-muted text-[length:var(--workspace-text-caption,_12px)] text-ellipsis whitespace-nowrap">
              {url}
            </span>
          )}
        </span>
      </ChatMarkdownLink>
    </div>
  );
}
