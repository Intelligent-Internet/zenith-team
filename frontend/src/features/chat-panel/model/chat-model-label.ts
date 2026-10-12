export function chatModelLabel(model: string | null | undefined) {
  if (!model) return "Provider default";
  return model.replace(
    /^gpt-(\d+(?:\.\d+)?)(.*)$/i,
    (_match, version: string, suffix: string) =>
      `GPT-${version}${suffix.replace(/-([a-z])/gi, (_separator, letter: string) => ` ${letter.toUpperCase()}`)}`,
  );
}
