import { useEffect, useRef, useState } from "react";

import {
  previewDesktopWorkspaceAsset,
  type DesktopWorkspaceAssetItem,
  type DesktopWorkspaceAssetPreview,
} from "@/app/desktop/desktop-environment.service";

const workspaceAssetCardPreviewCharacterLimit = 4_096;
const workspaceAssetCardPreviewConcurrency = 4;
const workspaceAssetCardPreviewCacheLimit = 16;

let activePreviewRequests = 0;
const queuedPreviewRequests: Array<() => void> = [];
const previewCache = new Map<string, DesktopWorkspaceAssetPreview>();
const pendingPreviews = new Map<
  string,
  Promise<DesktopWorkspaceAssetPreview | null>
>();

export function useLazyWorkspaceAssetPreview<TElement extends HTMLElement>({
  companyId,
  item,
  scope,
}: {
  companyId: string;
  item: DesktopWorkspaceAssetItem;
  scope: string;
}) {
  const previewRef = useRef<TElement | null>(null);
  const [preview, setPreview] = useState<DesktopWorkspaceAssetPreview | null>(
    null,
  );
  const [shouldLoad, setShouldLoad] = useState(false);

  useEffect(() => {
    setPreview(null);
    setShouldLoad(false);
    if (item.previewKind === "none") return;

    const previewElement = previewRef.current;
    if (
      !previewElement ||
      typeof window === "undefined" ||
      !("IntersectionObserver" in window)
    ) {
      setShouldLoad(true);
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return;
        setShouldLoad(true);
        observer.disconnect();
      },
      { rootMargin: "160px" },
    );
    observer.observe(previewElement);
    return () => observer.disconnect();
  }, [companyId, item.modifiedAt, item.path, item.previewKind, scope]);

  useEffect(() => {
    if (!shouldLoad || item.previewKind === "none") return;
    let active = true;
    void loadWorkspaceAssetCardPreview(companyId, item, scope).then((value) => {
      if (active) setPreview(value);
    });
    return () => {
      active = false;
    };
  }, [companyId, item, scope, shouldLoad]);

  return { preview, previewRef };
}

function loadWorkspaceAssetCardPreview(
  companyId: string,
  item: DesktopWorkspaceAssetItem,
  scope: string,
) {
  const key = [companyId, scope, item.path, item.modifiedAt].join("\u0000");
  const cached = previewCache.get(key);
  if (cached) return Promise.resolve(cached);
  const pending = pendingPreviews.get(key);
  if (pending) return pending;

  const request = scheduleWorkspaceAssetCardPreview(async () => {
    const result = await previewDesktopWorkspaceAsset({
      companyId,
      path: item.path,
      scope,
    });
    if (!result.ok) return null;
    const preview = compactWorkspaceAssetCardPreview(result.value);
    if (!preview) return null;
    rememberWorkspaceAssetCardPreview(key, preview);
    return preview;
  })
    .catch(() => null)
    .finally(() => pendingPreviews.delete(key));
  pendingPreviews.set(key, request);
  return request;
}

function scheduleWorkspaceAssetCardPreview<T>(task: () => Promise<T>) {
  return new Promise<T>((resolve, reject) => {
    const run = () => {
      activePreviewRequests += 1;
      void task()
        .then(resolve, reject)
        .finally(() => {
          activePreviewRequests -= 1;
          queuedPreviewRequests.shift()?.();
        });
    };
    if (activePreviewRequests < workspaceAssetCardPreviewConcurrency) {
      run();
      return;
    }
    queuedPreviewRequests.push(run);
  });
}

function compactWorkspaceAssetCardPreview(
  preview: DesktopWorkspaceAssetPreview,
) {
  if (preview.format === "image") {
    return preview.contentBase64 ? preview : null;
  }
  if (!preview.content?.trim()) return null;
  return {
    ...preview,
    content: truncateWorkspaceAssetCardContent(preview.content),
  };
}

function rememberWorkspaceAssetCardPreview(
  key: string,
  preview: DesktopWorkspaceAssetPreview,
) {
  if (previewCache.size >= workspaceAssetCardPreviewCacheLimit) {
    const oldestKey = previewCache.keys().next().value;
    if (oldestKey !== undefined) previewCache.delete(oldestKey);
  }
  previewCache.set(key, preview);
}

function truncateWorkspaceAssetCardContent(content: string) {
  let characterCount = 0;
  let endIndex = 0;
  for (const character of content) {
    if (characterCount === workspaceAssetCardPreviewCharacterLimit) break;
    characterCount += 1;
    endIndex += character.length;
  }
  return content.slice(0, endIndex);
}
