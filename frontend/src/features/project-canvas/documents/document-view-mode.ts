import { createContext } from "react";

// Shared rendering policy, independent of native work blocks and capture state.
export const DocumentViewMode = createContext<
  "live" | "comparison" | "snapshot"
>("live");
