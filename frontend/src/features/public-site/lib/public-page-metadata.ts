import { useEffect } from "react";

const PUBLIC_SITE_ORIGIN = "https://app.example.com";
const PUBLIC_SOCIAL_IMAGE_PATH = "/images/og/zenith-team-social-card.png";

export type PublicPageMetadataInput = {
  title: string;
  description: string;
  path: string;
  robots?: "index, follow" | "noindex, nofollow";
};

export type PublicPageMetadataValue = {
  title: string;
  description: string;
  canonicalUrl: string;
  imageUrl: string;
  robots: "index, follow" | "noindex, nofollow";
};

export function buildPublicPageMetadata({
  description,
  path,
  robots = "index, follow",
  title,
}: PublicPageMetadataInput): PublicPageMetadataValue {
  return {
    canonicalUrl: new URL(path, `${PUBLIC_SITE_ORIGIN}/`).toString(),
    description,
    imageUrl: new URL(
      PUBLIC_SOCIAL_IMAGE_PATH,
      `${PUBLIC_SITE_ORIGIN}/`,
    ).toString(),
    robots,
    title,
  };
}

export function PublicPageMetadata(props: PublicPageMetadataInput) {
  useEffect(
    () => applyPublicPageMetadata(buildPublicPageMetadata(props)),
    [props.description, props.path, props.robots, props.title],
  );

  return null;
}

function applyPublicPageMetadata(metadata: PublicPageMetadataValue) {
  const previousTitle = document.title;
  const cleanups = [
    setMeta("name", "description", metadata.description),
    setMeta("property", "og:title", metadata.title),
    setMeta("property", "og:description", metadata.description),
    setMeta("property", "og:url", metadata.canonicalUrl),
    setMeta("property", "og:image", metadata.imageUrl),
    setMeta("name", "twitter:title", metadata.title),
    setMeta("name", "twitter:description", metadata.description),
    setMeta("name", "twitter:image", metadata.imageUrl),
    setMeta("name", "robots", metadata.robots),
    setCanonical(metadata.canonicalUrl),
  ];

  document.title = metadata.title;

  return () => {
    document.title = previousTitle;
    for (const cleanup of cleanups.reverse()) {
      cleanup();
    }
  };
}

function setMeta(attribute: "name" | "property", key: string, content: string) {
  const selector = `meta[${attribute}="${key}"]`;
  const existing = document.head.querySelector<HTMLMetaElement>(selector);
  const element = existing ?? document.createElement("meta");
  const previousContent = existing?.getAttribute("content");

  if (!existing) {
    element.setAttribute(attribute, key);
    document.head.append(element);
  }

  element.setAttribute("content", content);

  return () => {
    if (!existing) {
      element.remove();
      return;
    }

    if (previousContent === null || previousContent === undefined) {
      existing.removeAttribute("content");
    } else {
      existing.setAttribute("content", previousContent);
    }
  };
}

function setCanonical(href: string) {
  const existing = document.head.querySelector<HTMLLinkElement>(
    'link[rel="canonical"]',
  );
  const element = existing ?? document.createElement("link");
  const previousHref = existing?.getAttribute("href");

  if (!existing) {
    element.setAttribute("rel", "canonical");
    document.head.append(element);
  }

  element.setAttribute("href", href);

  return () => {
    if (!existing) {
      element.remove();
      return;
    }

    if (previousHref === null || previousHref === undefined) {
      existing.removeAttribute("href");
    } else {
      existing.setAttribute("href", previousHref);
    }
  };
}
