import { useCallback, useRef } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "@/shared/ui/icons";
import { ArtifactViewer } from "./artifact-viewer";
import type { CanvasScope, CanvasVersion } from "./canvas-api";
import styles from "./artifact-focus-preview.module.css";

export type PreviewOrigin = Pick<DOMRect, "x" | "y" | "width" | "height">;

export default function ArtifactFocusPreview({
  scope,
  version,
  title,
  origin,
  canvas,
  canvasZoom,
  itemId,
  onClose,
}: {
  scope: CanvasScope;
  version: CanvasVersion;
  title: string;
  origin?: PreviewOrigin;
  canvas: HTMLElement | null;
  canvasZoom: number;
  itemId: string;
  onClose: () => void;
}) {
  const contentRef = useRef<HTMLDivElement | null>(null);
  const backdropRef = useRef<HTMLDivElement>(null);
  const motionRef = useRef<Animation | null>(null);
  const backdropMotionRef = useRef<Animation | null>(null);
  const closingRef = useRef(false);
  const sceneMotions = useRef<
    { element: Element; animation: Animation; rest: Keyframe }[]
  >([]);
  const collapsedTransform = useRef("translate(-50%, -50%) scale(0.88)");
  const closePreview = () => {
    if (closingRef.current) return;
    const element = contentRef.current;
    if (
      !element ||
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    ) {
      onClose();
      return;
    }
    closingRef.current = true;
    const current = getComputedStyle(element);
    const from = { transform: current.transform, opacity: current.opacity };
    motionRef.current?.cancel();
    const motion = element.animate(
      [from, { transform: collapsedTransform.current, opacity: 0 }],
      {
        duration: 280,
        easing: "cubic-bezier(0.4, 0, 0.2, 1)",
        fill: "forwards",
      },
    );
    motionRef.current = motion;
    for (const entry of sceneMotions.current) {
      const current = getComputedStyle(entry.element);
      const from = { translate: current.translate, opacity: current.opacity };
      entry.animation.cancel();
      entry.animation = entry.element.animate([from, entry.rest], {
        duration: 280,
        easing: "cubic-bezier(0.4, 0, 0.2, 1)",
        fill: "forwards",
      });
    }
    const backdrop = backdropRef.current;
    if (backdrop) {
      const currentBackdrop = getComputedStyle(backdrop);
      backdropMotionRef.current = backdrop.animate(
        [
          {
            opacity: currentBackdrop.opacity,
            backdropFilter: currentBackdrop.backdropFilter,
          },
          { opacity: 0, backdropFilter: "blur(0px) saturate(1)" },
        ],
        { duration: 280, easing: "ease-out", fill: "forwards" },
      );
    }
    void motion.finished.then(onClose, () => undefined);
  };
  const previewRef = useCallback(
    (element: HTMLDivElement | null) => {
      contentRef.current = element;
      if (
        !element ||
        window.matchMedia("(prefers-reduced-motion: reduce)").matches
      )
        return;
      const bounds = element.getBoundingClientRect();
      const dx = origin
        ? origin.x + origin.width / 2 - (bounds.x + bounds.width / 2)
        : 0;
      const dy = origin
        ? origin.y + origin.height / 2 - (bounds.y + bounds.height / 2)
        : 24;
      const scale = origin
        ? Math.min(
            origin.width / bounds.width,
            origin.height / bounds.height,
            1,
          )
        : 0.88;
      collapsedTransform.current = `translate(-50%, -50%) translate(${dx}px, ${dy}px) scale(${scale})`;
      const animation = element.animate(
        [
          {
            transform: collapsedTransform.current,
            opacity: 0.25,
          },
          {
            transform: "translate(-50%, -50%) translate(0, 0) scale(1)",
            opacity: 1,
          },
        ],
        { duration: 420, easing: "cubic-bezier(0.22, 1, 0.36, 1)" },
      );
      motionRef.current = animation;
      if (canvas && origin) {
        const centerX = origin.x + origin.width / 2;
        const centerY = origin.y + origin.height / 2;
        const neighbors = Array.from(
          canvas.querySelectorAll<HTMLElement>(".react-flow__node-artifact"),
        )
          .filter((node) => node.dataset.id !== itemId)
          .map((node) => ({
            node,
            bounds: node.getBoundingClientRect(),
            rest: { translate: getComputedStyle(node).translate },
          }));
        for (const { node, bounds, rest } of neighbors) {
          const dx = bounds.x + bounds.width / 2 - centerX;
          const dy = bounds.y + bounds.height / 2 - centerY;
          const distance = Math.hypot(dx, dy);
          const shift =
            Math.max(0, 56 * (1 - distance / 1000)) / Math.max(canvasZoom, 0.1);
          if (!shift) continue;
          const x = distance ? dx / distance : 1;
          const y = distance ? dy / distance : 0;
          sceneMotions.current.push({
            element: node,
            rest,
            animation: node.animate(
              [rest, { translate: `${x * shift}px ${y * shift}px` }],
              {
                duration: 420,
                easing: "cubic-bezier(0.22, 1, 0.36, 1)",
                fill: "forwards",
              },
            ),
          });
        }
        const edges = canvas.querySelector(".react-flow__edges");
        if (edges) {
          const rest = { opacity: getComputedStyle(edges).opacity };
          sceneMotions.current.push({
            element: edges,
            rest,
            animation: edges.animate([rest, { opacity: 0.15 }], {
              duration: 360,
              fill: "forwards",
            }),
          });
        }
      }
      return () => {
        motionRef.current?.cancel();
        backdropMotionRef.current?.cancel();
        for (const entry of sceneMotions.current) entry.animation.cancel();
        sceneMotions.current = [];
        contentRef.current = null;
      };
    },
    [origin, canvas, canvasZoom, itemId],
  );
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) closePreview();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay ref={backdropRef} className={styles.backdrop} />
        <Dialog.Content
          ref={previewRef}
          className={`pc-dialog ${styles.preview}`}
          aria-describedby={undefined}
        >
          <Dialog.Close className={styles.close} aria-label="Close preview">
            <X size={20} />
          </Dialog.Close>
          <div className={styles.stage}>
            <ArtifactViewer scope={scope} version={version} />
          </div>
          <Dialog.Title className={styles.title}>{title}</Dialog.Title>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
