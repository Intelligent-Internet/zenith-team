import { shouldUseDesktopHashRouter } from "@/shared/desktop/desktop-environment.service";
import { getStudioHeroPoster } from "@/features/public-site/lib/studio-assets";

async function render() {
  if (window.location.pathname === "/" && !shouldUseDesktopHashRouter()) {
    // Discover the hero before downloading and rendering the landing component.
    const poster = document.createElement("link");
    poster.rel = "preload";
    poster.as = "image";
    poster.href = getStudioHeroPoster();
    poster.fetchPriority = "high";
    document.head.append(poster);

    const { renderPublicLandingPage } =
      await import("@/features/public-site/public-entry");
    renderPublicLandingPage();
    return;
  }

  const { renderApp } = await import("@/app/bootstrap");
  await renderApp();
}

void render();
