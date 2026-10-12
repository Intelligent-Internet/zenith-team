export async function copyTextToClipboard(text: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Electron can expose Clipboard API while denying renderer writes.
      // Continue with selection-based copy in the original click interaction.
    }
  }

  const textarea = document.createElement("textarea");
  const previouslyFocusedElement =
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  textarea.style.top = "0";
  // Keep the selection inside the active modal so its focus trap cannot
  // redirect focus before the browser copies the selected text.
  const container =
    previouslyFocusedElement?.closest(
      '[role="dialog"], [role="alertdialog"]',
    ) ?? document.body;
  container.appendChild(textarea);
  textarea.focus({ preventScroll: true });
  textarea.select();

  try {
    if (!document.execCommand("copy")) {
      throw new Error("Copy command failed");
    }
  } finally {
    textarea.remove();
    previouslyFocusedElement?.focus({ preventScroll: true });
  }
}
