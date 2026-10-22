import {
  createContext,
  createElement,
  useContext,
  type ComponentProps,
  type ReactNode,
  type HTMLAttributes,
} from "react";
import {
  NodeViewContent as EditorContent,
  NodeViewWrapper as EditorWrapper,
} from "@tiptap/react";
import "./render-surface.css";

// The same visual components serve ProseMirror node views and plain React
// documents. Only the authoring adapter needs a contentDOM.
export const ReadonlySurface = createContext<{ children: ReactNode } | null>(
  null,
);

export function NodeViewWrapper(props: ComponentProps<typeof EditorWrapper>) {
  const surface = useContext(ReadonlySurface);
  if (!surface) return <EditorWrapper {...props} />;
  const { as = "div", ...rest } = props;
  return createElement(as, rest);
}

export function NodeViewContent(props: HTMLAttributes<HTMLDivElement>) {
  const surface = useContext(ReadonlySurface);
  if (!surface) return <EditorContent {...props} />;
  return createElement(
    "div",
    { ...props, "data-node-view-content": "" },
    surface.children,
  );
}
