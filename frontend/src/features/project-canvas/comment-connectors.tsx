import type { RefObject } from "react";
import "./page-comments.css";

export function CommentConnectors({
  layer,
  paths,
  activeId,
  open,
  className = "",
}: {
  layer: RefObject<SVGSVGElement | null>;
  paths: { id: string; path: string }[];
  activeId: string;
  open: boolean;
  className?: string;
}) {
  return (
    <svg
      ref={layer}
      className={`po-comment-connectors ${className}`}
      aria-hidden="true"
      focusable="false"
      data-open={open}
    >
      {paths.map(({ id, path }) => (
        <path key={id} d={path} data-active={id === activeId} />
      ))}
    </svg>
  );
}
