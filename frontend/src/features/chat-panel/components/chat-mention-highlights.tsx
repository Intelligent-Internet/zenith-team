import { Fragment, useLayoutEffect, useRef, type RefObject } from "react";

type MentionRange = { start: number; end: number };

export function ChatMentionHighlights({
  ranges,
  textareaRef,
  value,
}: {
  ranges: readonly MentionRange[];
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  value: string;
}) {
  const mirrorRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const textarea = textareaRef.current;
    const mirror = mirrorRef.current;
    if (!textarea || !mirror) return;

    // Match the native editor's metrics so highlights do not move its caret or wrapping.
    const syncLayout = () => {
      const style = window.getComputedStyle(textarea);
      Object.assign(mirror.style, {
        left: `${textarea.offsetLeft}px`,
        top: `${textarea.offsetTop}px`,
        width: `${textarea.clientWidth}px`,
        height: `${textarea.clientHeight}px`,
        // Computed font shorthand can be empty when font variants are set.
        fontFamily: style.fontFamily,
        fontSize: style.fontSize,
        fontWeight: style.fontWeight,
        fontStyle: style.fontStyle,
        fontStretch: style.fontStretch,
        fontVariant: style.fontVariant,
        fontKerning: style.fontKerning,
        fontFeatureSettings: style.fontFeatureSettings,
        fontVariationSettings: style.fontVariationSettings,
        lineHeight: style.lineHeight,
        letterSpacing: style.letterSpacing,
        wordSpacing: style.wordSpacing,
        padding: style.padding,
        textAlign: style.textAlign,
        textIndent: style.textIndent,
        textTransform: style.textTransform,
        tabSize: style.tabSize,
        wordBreak: style.wordBreak,
        color: style.color,
      });
      syncScroll();
    };
    const syncScroll = () => {
      mirror.scrollTop = textarea.scrollTop;
      mirror.scrollLeft = textarea.scrollLeft;
    };

    syncLayout();
    const observer = new ResizeObserver(syncLayout);
    observer.observe(textarea);
    if (textarea.parentElement) observer.observe(textarea.parentElement);
    textarea.addEventListener("scroll", syncScroll);
    window.addEventListener("resize", syncLayout);
    return () => {
      observer.disconnect();
      textarea.removeEventListener("scroll", syncScroll);
      window.removeEventListener("resize", syncLayout);
    };
  }, [textareaRef, value]);

  return (
    <div aria-hidden="true" className="chat-mention-highlights" ref={mirrorRef}>
      {ranges.map((range, index) => (
        <Fragment key={range.start}>
          {value.slice(index ? ranges[index - 1].end : 0, range.start)}
          <mark className="chat-mention-highlights__tag">
            {value.slice(range.start, range.end)}
          </mark>
        </Fragment>
      ))}
      {value.slice(ranges.at(-1)?.end ?? 0)}
      {"\u200b"}
    </div>
  );
}
