import { useEditorState, type Editor } from "@tiptap/react";
import * as Popover from "@radix-ui/react-popover";
import { type ReactNode } from "react";
import { Icon } from "@/shared/ui/icon";
import "./block-settings.css";

/** Contextual controls shared by authored blocks and personal Overview views. */
export function BlockSettings({
  label,
  children,
  description = "Changes apply to this Page.",
  needed = false,
}: {
  label: string;
  children: ReactNode;
  description?: string;
  needed?: boolean;
}) {
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          type="button"
          className="pc-block-settings"
          data-needed={needed}
          aria-label={label}
          title={label}
          contentEditable={false}
        >
          <Icon name="setting-2" className="size-4" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="pc-block-settings-popup"
          side="bottom"
          align="end"
          sideOffset={6}
          collisionPadding={16}
          aria-label={label}
          contentEditable={false}
          onEscapeKeyDown={(event) => event.stopPropagation()}
        >
          <header>
            <strong>{label}</strong>
            <Popover.Close asChild>
              <button type="button" aria-label="Close block settings">
                <Icon name="close" className="size-4" />
              </button>
            </Popover.Close>
          </header>
          {children}
          <p>{description}</p>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

export function useBlockEditable(editor: Editor) {
  return useEditorState({
    editor,
    selector: ({ editor }) => editor.isEditable,
  });
}
