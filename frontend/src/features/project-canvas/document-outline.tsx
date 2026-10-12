import { useEffect, useId, useRef, useState } from "react";
import { useEditorState, type Editor } from "@tiptap/react";
import styles from "./document-outline.module.css";

export function DocumentOutline({ editor }: { editor: Editor | null }) {
  const panelId = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState<number>();
  const headings = useEditorState({
    editor,
    selector: ({ editor: current }) => {
      const result: { position: number; level: number; title: string }[] = [];
      current?.state.doc.descendants((node, position) => {
        if (node.type.name === "heading" && node.textContent.trim()) {
          result.push({
            position,
            level: Number(node.attrs.level),
            title: node.textContent.trim(),
          });
        }
      });
      return result;
    },
  });
  useEffect(() => {
    if (!editor || !headings?.length) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      let parent = editor.view.dom.parentElement;
      while (
        parent &&
        !/(auto|scroll)/.test(getComputedStyle(parent).overflowY)
      )
        parent = parent.parentElement;
      const threshold =
        Math.max(0, parent?.getBoundingClientRect().top ?? 0) + 96;
      let current = headings[0].position;
      for (const heading of headings) {
        const element = editor.view.nodeDOM(heading.position);
        if (
          element instanceof HTMLElement &&
          element.getBoundingClientRect().top <= threshold
        )
          current = heading.position;
      }
      setActive(current);
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(measure);
    };
    const observer = new ResizeObserver(schedule);
    observer.observe(editor.view.dom);
    document.addEventListener("scroll", schedule, true);
    window.addEventListener("resize", schedule);
    measure();
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      document.removeEventListener("scroll", schedule, true);
      window.removeEventListener("resize", schedule);
    };
  }, [editor, headings]);
  if (!editor || !headings?.length) return null;
  return (
    <div
      className={styles.outline}
      onPointerEnter={() => setOpen(true)}
      onPointerLeave={(event) => {
        if (!event.currentTarget.contains(document.activeElement))
          setOpen(false);
      }}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false);
      }}
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          event.preventDefault();
          event.stopPropagation();
          trigger.current?.focus({ preventScroll: true });
          setOpen(false);
        }
      }}
    >
      <button
        ref={trigger}
        type="button"
        className={styles.rail}
        aria-label="Table of contents"
        aria-controls={panelId}
        aria-expanded={open}
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
      >
        {headings.map((heading) => (
          <span
            key={heading.position}
            data-active={heading.position === active}
            data-level={heading.level}
          />
        ))}
      </button>
      <nav
        id={panelId}
        className={styles.panel}
        aria-label="Table of contents"
        hidden={!open}
      >
        <span className={styles.label}>On this page</span>
        {headings.map((heading) => (
          <button
            type="button"
            key={heading.position}
            aria-current={heading.position === active ? "location" : undefined}
            style={{ paddingLeft: 12 + Math.min(heading.level - 1, 3) * 12 }}
            onClick={() => {
              const element = editor.view.nodeDOM(heading.position);
              if (!(element instanceof HTMLElement)) return;
              trigger.current?.focus({ preventScroll: true });
              element.scrollIntoView({
                block: "start",
                behavior: window.matchMedia("(prefers-reduced-motion: reduce)")
                  .matches
                  ? "instant"
                  : "smooth",
              });
              setActive(heading.position);
              setOpen(false);
            }}
          >
            {heading.title}
          </button>
        ))}
      </nav>
    </div>
  );
}
