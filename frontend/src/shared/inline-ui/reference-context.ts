import { createContext, type CSSProperties, type ReactNode } from "react";

// Reference reads are independent of permission to send a chat continuation.
export type InlineReferenceRenderer = (
  name: string,
  props: Record<string, unknown>,
  interactive: boolean,
) => ReactNode;
export const InlineReferenceContext =
  createContext<InlineReferenceRenderer | null>(null);
export const InlineInteractionContext = createContext(true);

// The app supplies its existing palettes; shared rendering never imports app code.
export const InlineThemeContext = createContext<
  Record<"light" | "dark", CSSProperties>
>({ light: {}, dark: {} });
