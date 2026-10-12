import { useEffect, useState, type CSSProperties } from "react";
import type { ThemedToken } from "shiki";
import { attachmentLanguage } from "./attachment-language";
import { attachmentUrl, type ChatAttachment } from "./attachment-preview";
import "./chat-source-preview.css";

export function ChatSourceCode({
  text,
  file,
  compact = false,
}: {
  text: string;
  file: ChatAttachment;
  compact?: boolean;
}) {
  const language = attachmentLanguage(file.name, file.mediaType);
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const shown = lines.slice(0, compact ? 5 : 2000);
  const code = shown.join("\n");
  const [highlight, setHighlight] = useState<{
    code: string;
    language: string;
    tokens: ThemedToken[][];
  }>();
  useEffect(() => {
    let cancelled = false;
    if (language && code.length <= 100_000) {
      void import("shiki")
        .then(async ({ codeToTokens, bundledLanguages }) => {
          if (!(language in bundledLanguages)) return;
          const result = await codeToTokens(code, {
            lang: language as keyof typeof bundledLanguages,
            defaultColor: false,
            themes: { light: "github-light", dark: "github-dark" },
          });
          if (!cancelled)
            setHighlight({ code, language, tokens: result.tokens });
        })
        .catch(() => {
          /* Plain text remains readable when highlighting fails. */
        });
    }
    return () => {
      cancelled = true;
    };
  }, [code, language]);
  const tokens =
    highlight?.code === code && highlight.language === language
      ? highlight.tokens
      : undefined;
  return (
    <>
      <pre
        className="chat-source-code"
        tabIndex={0}
        aria-label={`${file.name} source code`}
      >
        <code>
          {shown.map((line, index) => (
            <span className="chat-source-code__line" key={index}>
              <span className="chat-source-code__number" aria-hidden="true">
                {index + 1}
              </span>
              <span>
                {tokens?.[index]?.map((token, tokenIndex) => (
                  <span
                    key={tokenIndex}
                    style={token.htmlStyle as CSSProperties}
                  >
                    {token.content}
                  </span>
                )) ??
                  (line || " ")}
              </span>
            </span>
          ))}
        </code>
      </pre>
      {!compact && lines.length > shown.length ? (
        <p className="chat-source-preview__status">
          Showing the first 2,000 lines. Download the file for the full content.
        </p>
      ) : null}
    </>
  );
}

export function ChatSourcePreview({
  file,
  onOpen,
}: {
  file: ChatAttachment;
  onOpen: () => void;
}) {
  const [preview, setPreview] = useState<{
    key?: string;
    text?: string;
    error?: boolean;
    truncated?: boolean;
  }>();
  const key = file.url ?? file.content;
  const tooLarge = (file.sizeBytes ?? 0) > 1_000_000;
  useEffect(() => {
    if (tooLarge || key === undefined) return;
    const controller = new AbortController();
    const load = async () => {
      if (file.content !== undefined)
        return {
          text: file.content.slice(0, 1_000_000),
          truncated: file.content.length > 1_000_000,
        };
      const url = attachmentUrl(file.url);
      if (!url) throw new Error("Unavailable");
      const response = await fetch(url, {
        signal: controller.signal,
        credentials: "omit",
      });
      if (!response.ok) throw new Error("Unavailable");
      const reader = response.body?.getReader();
      if (!reader) throw new Error("Unavailable");
      const decoder = new TextDecoder();
      let text = "";
      let bytes = 0;
      try {
        while (true) {
          const { value, done } = await reader.read();
          if (done) return { text: text + decoder.decode(), truncated: false };
          const remaining = 1_000_000 - bytes;
          text += decoder.decode(value.subarray(0, remaining), {
            stream: true,
          });
          bytes += value.length;
          if (bytes > 1_000_000)
            return { text: text + decoder.decode(), truncated: true };
        }
      } finally {
        await reader.cancel();
        reader.releaseLock();
      }
    };
    void load()
      .then((result) => {
        if (!controller.signal.aborted) setPreview({ key, ...result });
      })
      .catch(() => {
        if (!controller.signal.aborted) setPreview({ key, error: true });
      });
    return () => controller.abort();
  }, [key, tooLarge, file.url, file.content]);
  const current = preview?.key === key ? preview : undefined;
  const count = current?.text?.split(/\r\n?|\n/).length;
  return (
    <div className="chat-source-preview">
      {current?.text !== undefined ? (
        <ChatSourceCode text={current.text} file={file} compact />
      ) : (
        <p className="chat-source-preview__status" role="status">
          {tooLarge
            ? "Open to preview this file"
            : current?.error
              ? "Preview unavailable. You can still download this file."
              : "Loading preview…"}
        </p>
      )}
      <button
        className="chat-source-preview__expand"
        type="button"
        onClick={onOpen}
      >
        {count
          ? `${count > 5 ? "Show more" : "Open file"} (${count.toLocaleString()}${current?.truncated ? "+" : ""} ${count === 1 ? "line" : "lines"})`
          : "Open file"}
      </button>
    </div>
  );
}
