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
    <div aria-label="Shared links" className="chat-link-previews">
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
          <span className="chat-link-preview__icon">
            <Globe aria-hidden="true" size={24} />
          </span>
        )}
        <span className="chat-link-preview__copy">
          <span className="chat-link-preview__domain">
            {domain}
            <ArrowUpRight aria-hidden="true" size={12} />
          </span>
          <strong>{preview.title || domain}</strong>
          {preview.description ? (
            <span className="chat-link-preview__description">
              {preview.description}
            </span>
          ) : (
            <span className="chat-link-preview__url">{url}</span>
          )}
        </span>
      </ChatMarkdownLink>
    </div>
  );
}
