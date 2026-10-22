import { createContext } from "react";
import type { DocumentView } from "./editor-runtime";
import type { DocumentScope } from "../document-scope";

export const IntelligentDocumentContext = createContext<DocumentScope | null>(
  null,
);

import type { ScopeRuntime } from "./scope-runtime";

export type InstanceContext = {
  registerView?: (view: DocumentView) => () => void;
  runtime: ScopeRuntime;
  depth: number;
  budget: number;
};
export const IntelligentInstanceContext = createContext<InstanceContext | null>(
  null,
);
// Opt-in for local controls in a validated temporary preview, never saved writes.
export const IntelligentPreviewInteractionContext = createContext(false);

export const InlineStreamingContext = createContext(false);
