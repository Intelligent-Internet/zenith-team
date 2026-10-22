// Temporary previews may change viewer-local values, but cannot acquire a
// conversation host or mutate authored content by opting into local interaction.
export function allowsLocalInteraction(
  mode: "live" | "comparison" | "snapshot",
  preview: boolean,
) {
  return mode === "live" || (mode === "comparison" && preview);
}
