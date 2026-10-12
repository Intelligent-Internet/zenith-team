const desktopCommandPaletteOpenEvent = "boardly:desktop-command-palette-open";

export function openDesktopCommandPalette(): void {
  window.dispatchEvent(new Event(desktopCommandPaletteOpenEvent));
}

export function subscribeToDesktopCommandPaletteOpen(
  listener: () => void,
): () => void {
  window.addEventListener(desktopCommandPaletteOpenEvent, listener);

  return () => {
    window.removeEventListener(desktopCommandPaletteOpenEvent, listener);
  };
}
