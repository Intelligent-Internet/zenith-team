export type ReportStyle = "editorial" | "executive" | "compact";

// Print a detached copy: exports never mutate the editor or rely on global print styles.
export async function printPageReport(
  root: HTMLElement,
  title: string,
  style: ReportStyle,
  paper: "A4" | "Letter",
) {
  if (root.querySelector('[role="alert"], [role="status"]')) {
    throw new Error(
      "Some report content is still loading or unavailable. Resolve it in the preview before exporting.",
    );
  }
  if (
    root.querySelector(".pc-block-editor") &&
    !root.querySelector(".pc-block-editor .tiptap")
  ) {
    throw new Error(
      "The document is still opening. Please try again in a moment.",
    );
  }
  const frame = document.createElement("iframe");
  frame.title = "Page PDF export";
  frame.setAttribute("sandbox", "allow-same-origin allow-modals");
  frame.style.cssText =
    "position:fixed;left:-10000px;top:0;width:1000px;height:800px;border:0";
  document.body.append(frame);
  try {
    const doc = frame.contentDocument;
    const win = frame.contentWindow;
    if (!doc || !win) throw new Error("The print preview could not be opened.");
    doc.title = title;
    const base = doc.createElement("base");
    base.href = document.baseURI;
    doc.head.append(base);
    const loads: Promise<void>[] = [];
    document
      .querySelectorAll('style, link[rel="stylesheet"]')
      .forEach((source) => {
        const copy = source.cloneNode(true) as HTMLElement;
        if (copy instanceof HTMLLinkElement) {
          loads.push(
            new Promise((resolve, reject) => {
              copy.onload = () => resolve();
              copy.onerror = () =>
                reject(new Error("A report stylesheet could not be loaded."));
            }),
          );
        }
        doc.head.append(copy);
      });
    const rules = doc.createElement("style");
    rules.textContent = `@page { size: ${paper}; margin: 17mm 16mm; } html,body { margin:0!important;padding:0!important;background:white!important;overflow:visible!important;height:auto!important; } .pc-pdf-report { width:auto!important;max-width:none!important;margin:0!important;padding:0!important;box-shadow:none!important; }`;
    doc.head.append(rules);
    const report = root.cloneNode(true) as HTMLElement;
    report.className = `pc-pdf-report pc-pdf-${style}`;
    const canvases = root.querySelectorAll("canvas");
    report.querySelectorAll("canvas").forEach((canvas, index) => {
      const source = canvases[index];
      if (!source.width || !source.height)
        throw new Error(
          "An embedded preview is still rendering. Please retry.",
        );
      const image = doc.createElement("img");
      try {
        image.src = source.toDataURL("image/png");
      } catch {
        throw new Error(
          "An embedded preview cannot be exported by this browser. Use a linked reference instead.",
        );
      }
      image.alt = source.getAttribute("aria-label") || "Embedded preview";
      image.style.cssText = canvas.style.cssText;
      canvas.replaceWith(image);
    });
    report
      .querySelectorAll(
        "script, iframe, object, embed, .pc-block-settings, .pc-reference-heading button, .pc-work-action, .pc-work-more, .pc-block-gutter, .pc-selection-toolbar, .collaboration-cursor__caret, .collaboration-cursor__label",
      )
      .forEach((node) => node.remove());
    report.querySelectorAll("button").forEach((button) => {
      const text = doc.createElement("span");
      text.className = button.className;
      text.append(...Array.from(button.childNodes));
      button.replaceWith(text);
    });
    report.querySelectorAll("[contenteditable], [tabindex]").forEach((node) => {
      node.removeAttribute("contenteditable");
      node.removeAttribute("tabindex");
    });
    report.querySelectorAll("a").forEach((link) => {
      const href = link.getAttribute("href");
      if (!href || href.startsWith("#")) return;
      try {
        const url = new URL(href, document.baseURI);
        if (["https:", "http:", "mailto:"].includes(url.protocol))
          link.href = url.href;
        else link.removeAttribute("href");
      } catch {
        link.removeAttribute("href");
      }
    });
    report.querySelectorAll("img").forEach((img) => {
      img.loading = "eager";
    });
    report.querySelectorAll("audio,video").forEach((media) => {
      const link = doc.createElement("a");
      link.textContent = "Open media";
      const src = media.getAttribute("src");
      if (src && /^https?:/i.test(src)) link.href = src;
      media.replaceWith(link);
    });
    doc.body.append(report);
    loads.push(
      ...Array.from(doc.images).map((img) =>
        img.decode().catch(() => {
          throw new Error(
            "An image could not be loaded. Retry once the image is available.",
          );
        }),
      ),
    );
    let timeout: ReturnType<typeof setTimeout> | undefined;
    try {
      await Promise.race([
        Promise.all(loads).then(() => doc.fonts.ready),
        new Promise((_, reject) => {
          timeout = setTimeout(
            () =>
              reject(
                new Error("Report assets took too long to load. Please retry."),
              ),
            20000,
          );
        }),
      ]);
    } finally {
      clearTimeout(timeout);
    }
    await new Promise<void>((resolve) =>
      win.requestAnimationFrame(() => resolve()),
    );
    win.addEventListener("afterprint", () => frame.remove(), { once: true });
    win.focus();
    win.print();
    // Some browsers dispatch afterprint asynchronously, others never dispatch it on cancel.
    setTimeout(() => frame.remove(), 300000);
  } catch (error) {
    frame.remove();
    throw error;
  }
}
