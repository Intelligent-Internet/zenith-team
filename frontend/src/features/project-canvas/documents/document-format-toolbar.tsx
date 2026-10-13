import { useEditorState, type Editor } from "@tiptap/react";
import {
  Bold,
  Code,
  Italic,
  Link2,
  List,
  ListEnd,
  ListTodo,
  Package,
  RotateCw,
  Strikethrough,
  Table2,
  Text,
  TextCursorInput,
  Undo2,
  type IconComponent,
} from "@/shared/ui/icons";
import "./document-format-toolbar.css";

function ToolButton({
  label,
  icon: Icon,
  active = false,
  disabled = false,
  onClick,
}: {
  label: string;
  icon: IconComponent;
  active?: boolean;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      aria-pressed={active}
      className={active ? "is-active" : ""}
      disabled={disabled}
      // Keep the editor's selection: the command runs on what was selected.
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
    >
      <Icon size={18} />
    </button>
  );
}

/**
 * The shared document formatting bar: text style, inline marks, lists and
 * blocks, page links and Artifact citations, undo and redo. It drives the
 * shared draft's editor, so every change reaches collaborators like typing.
 */
export function DocumentFormatToolbar({ editor }: { editor: Editor | null }) {
  const state = useEditorState({
    editor,
    selector: ({ editor: current }) =>
      current
        ? {
            editable: current.isEditable,
            block: current.isActive("heading", { level: 1 })
              ? "h1"
              : current.isActive("heading", { level: 2 })
                ? "h2"
                : current.isActive("heading", { level: 3 })
                  ? "h3"
                  : "paragraph",
            bold: current.isActive("bold"),
            italic: current.isActive("italic"),
            underline: current.isActive("underline"),
            strike: current.isActive("strike"),
            bullet: current.isActive("bulletList"),
            ordered: current.isActive("orderedList"),
            task: current.isActive("taskList"),
            code: current.isActive("codeBlock"),
            quote: current.isActive("blockquote"),
            undo: current.can().undo(),
            redo: current.can().redo(),
          }
        : null,
  });
  const disabled = !editor || !state?.editable;
  const run = (command: (editor: Editor) => void) => () => {
    if (editor) command(editor);
  };
  return (
    <div
      className="pc-format-toolbar"
      role="toolbar"
      aria-label="Page formatting"
    >
      <select
        aria-label="Text style"
        value={state?.block ?? "paragraph"}
        disabled={disabled}
        onChange={(event) => {
          const level = Number(event.target.value.slice(1));
          if (event.target.value === "paragraph") {
            editor?.chain().focus().setParagraph().run();
          } else if (level === 1 || level === 2 || level === 3) {
            editor?.chain().focus().setHeading({ level }).run();
          }
        }}
      >
        <option value="paragraph">Text</option>
        <option value="h1">Heading 1</option>
        <option value="h2">Heading 2</option>
        <option value="h3">Heading 3</option>
      </select>
      <span className="[flex:0_0_1px] w-[1px] h-[20px] m-[0_8px] [background:var(--workspace-divider)]" />
      <ToolButton
        label="Bold"
        icon={Bold}
        active={state?.bold}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleBold().run())}
      />
      <ToolButton
        label="Italic"
        icon={Italic}
        active={state?.italic}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleItalic().run())}
      />
      <ToolButton
        label="Underline"
        icon={Text}
        active={state?.underline}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleUnderline().run())}
      />
      <ToolButton
        label="Strikethrough"
        icon={Strikethrough}
        active={state?.strike}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleStrike().run())}
      />
      <span className="[flex:0_0_1px] w-[1px] h-[20px] m-[0_8px] [background:var(--workspace-divider)]" />
      <ToolButton
        label="Bulleted list"
        icon={List}
        active={state?.bullet}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleBulletList().run())}
      />
      <ToolButton
        label="Numbered list"
        icon={ListEnd}
        active={state?.ordered}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleOrderedList().run())}
      />
      <ToolButton
        label="Checklist"
        icon={ListTodo}
        active={state?.task}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleTaskList().run())}
      />
      <ToolButton
        label="Quote"
        icon={TextCursorInput}
        active={state?.quote}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleBlockquote().run())}
      />
      <ToolButton
        label="Code block"
        icon={Code}
        active={state?.code}
        disabled={disabled}
        onClick={run((e) => e.chain().focus().toggleCodeBlock().run())}
      />
      <span className="[flex:0_0_1px] w-[1px] h-[20px] m-[0_8px] [background:var(--workspace-divider)]" />
      <ToolButton
        label="Insert table"
        icon={Table2}
        disabled={disabled}
        onClick={run((e) =>
          e
            .chain()
            .focus()
            .insertTable({ rows: 3, cols: 3, withHeaderRow: true })
            .run(),
        )}
      />
      <ToolButton
        label="Link page (type [[)"
        icon={Link2}
        disabled={disabled || state?.code}
        onClick={run((e) => e.chain().focus().insertContent("[[").run())}
      />
      <ToolButton
        label="Cite artifact (type [[a: or paste an artifact link)"
        icon={Package}
        disabled={disabled || state?.code}
        onClick={run((e) => e.chain().focus().insertContent("[[a:").run())}
      />
      <span className="[flex:0_0_1px] w-[1px] h-[20px] m-[0_8px] [background:var(--workspace-divider)]" />
      <ToolButton
        label="Undo"
        icon={Undo2}
        disabled={disabled || !state?.undo}
        onClick={run((e) => e.chain().focus().undo().run())}
      />
      <ToolButton
        label="Redo"
        icon={RotateCw}
        disabled={disabled || !state?.redo}
        onClick={run((e) => e.chain().focus().redo().run())}
      />
    </div>
  );
}
