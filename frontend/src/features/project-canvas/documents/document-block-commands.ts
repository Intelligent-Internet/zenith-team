import type { Editor } from "@tiptap/react";

export const paragraph = () => ({ type: "paragraph" });
export const blocks: {
  type: string;
  label: string;
  keywords: string;
  run: (editor: Editor) => boolean;
}[] = [
  {
    type: "workflowReference",
    label: "Workflow",
    keywords: "process template launch",
    run: (e) =>
      e
        .chain()
        .focus()
        .insertContent({
          type: "workflowReference",
          attrs: { display: "card" },
        })
        .run(),
  },
  {
    type: "taskReference",
    label: "Task",
    keywords: "work run action",
    run: (e) =>
      e.chain().focus().insertContent({ type: "taskReference" }).run(),
  },
  {
    type: "taskCollection",
    label: "Project work",
    keywords: "tasks collection status board",
    run: (e) =>
      e
        .chain()
        .focus()
        .insertContent({
          type: "taskCollection",
          attrs: { filter: "active", display: "list" },
        })
        .run(),
  },
  {
    type: "outputCollection",
    label: "Project results",
    keywords: "artifacts outputs gallery collection",
    run: (e) =>
      e
        .chain()
        .focus()
        .insertContent({
          type: "outputCollection",
          attrs: { filter: "all", display: "list" },
        })
        .run(),
  },
  {
    type: "paragraph",
    label: "Text",
    keywords: "paragraph",
    run: (e) => e.chain().focus().setParagraph().run(),
  },
  ...([1, 2, 3] as const).map((level) => ({
    type: "heading",
    label: `Heading ${level}`,
    keywords: "title",
    run: (e: Editor) => e.chain().focus().toggleHeading({ level }).run(),
  })),
  {
    type: "bulletList",
    label: "Bulleted list",
    keywords: "bullet",
    run: (e) => e.chain().focus().toggleBulletList().run(),
  },
  {
    type: "orderedList",
    label: "Numbered list",
    keywords: "ordered",
    run: (e) => e.chain().focus().toggleOrderedList().run(),
  },
  {
    type: "taskList",
    label: "Checklist",
    keywords: "todo task",
    run: (e) => e.chain().focus().toggleTaskList().run(),
  },
  {
    type: "blockquote",
    label: "Quote",
    keywords: "blockquote",
    run: (e) => e.chain().focus().toggleBlockquote().run(),
  },
  {
    type: "codeBlock",
    label: "Code",
    keywords: "codeblock",
    run: (e) => e.chain().focus().toggleCodeBlock().run(),
  },
  {
    type: "mermaid",
    label: "Mermaid diagram",
    keywords: "flowchart sequence",
    run: (e) =>
      e
        .chain()
        .focus()
        .insertContent({
          type: "mermaid",
          content: [
            { type: "text", text: "flowchart LR\n  Idea --> Draft --> Review" },
          ],
        })
        .run(),
  },
  {
    type: "table",
    label: "Table",
    keywords: "rows columns",
    run: (e) =>
      e
        .chain()
        .focus()
        .insertTable({ rows: 3, cols: 3, withHeaderRow: true })
        .run(),
  },
  {
    type: "callout",
    label: "Callout",
    keywords: "notice",
    run: (e) =>
      e
        .chain()
        .focus()
        .insertContent({ type: "callout", content: [paragraph()] })
        .run(),
  },
  {
    type: "toggle",
    label: "Toggle",
    keywords: "collapse",
    run: (e) =>
      e
        .chain()
        .focus()
        .insertContent({
          type: "toggle",
          content: [
            {
              type: "toggleSummary",
              content: [{ type: "text", text: "Details" }],
            },
            { type: "toggleContent", content: [paragraph()] },
          ],
        })
        .run(),
  },
  {
    type: "columns",
    label: "Two columns",
    keywords: "layout",
    run: (e) =>
      e
        .chain()
        .focus()
        .insertContent({
          type: "columns",
          content: [
            { type: "column", content: [paragraph()] },
            { type: "column", content: [paragraph()] },
          ],
        })
        .run(),
  },
  {
    type: "horizontalRule",
    label: "Divider",
    keywords: "rule",
    run: (e) => e.chain().focus().setHorizontalRule().run(),
  },
  {
    type: "equation",
    label: "Equation",
    keywords: "math latex",
    run: (e) => e.chain().focus().insertContent({ type: "equation" }).run(),
  },
  {
    type: "tableOfContents",
    label: "Table of contents",
    keywords: "outline",
    run: (e) =>
      e.chain().focus().insertContent({ type: "tableOfContents" }).run(),
  },
  {
    type: "artifactReference",
    label: "Document reference",
    keywords: "embed source synced",
    run: (e) =>
      e.chain().focus().insertContent({ type: "artifactReference" }).run(),
  },
  {
    type: "fileBlock",
    label: "File link",
    keywords: "pdf attachment",
    run: (e) => e.chain().focus().insertContent({ type: "fileBlock" }).run(),
  },
  {
    type: "audioBlock",
    label: "Audio",
    keywords: "sound",
    run: (e) => e.chain().focus().insertContent({ type: "audioBlock" }).run(),
  },
  {
    type: "videoBlock",
    label: "Video",
    keywords: "movie",
    run: (e) => e.chain().focus().insertContent({ type: "videoBlock" }).run(),
  },
];
