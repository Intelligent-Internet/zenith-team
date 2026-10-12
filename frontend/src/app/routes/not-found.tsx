import { useEffect, useRef } from "react";
import { Link } from "react-router";
import { FolderOpen, MessagesSquare } from "@/shared/ui/icons";

export function NotFoundPage() {
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    headingRef.current?.focus();
  }, []);

  return (
    <main
      className="grid min-h-0 flex-1 place-items-center overflow-y-auto px-6 py-10 text-center"
      aria-labelledby="not-found-title"
    >
      <div className="flex w-full max-w-md flex-col items-center">
        <img
          src={`${import.meta.env.BASE_URL}images/boardly-404.png`}
          alt=""
          className="mb-6 w-full max-w-[280px] object-contain"
          draggable={false}
        />
        <h1
          id="not-found-title"
          ref={headingRef}
          tabIndex={-1}
          className="text-[length:var(--workspace-text-page)] font-semibold leading-[var(--workspace-leading-page)] text-[var(--workspace-native-text)] outline-none"
        >
          Page not found
        </h1>
        <p className="mt-3 text-sm leading-6 text-[var(--workspace-native-text-muted)]">
          This link may be outdated or the page may have moved. Continue from
          Chat or browse your projects.
        </p>
        <nav
          aria-label="Continue in your workspace"
          className="mt-6 flex flex-wrap justify-center gap-3"
        >
          <Link className="dashboard-button dashboard-button-primary" to="/app">
            <MessagesSquare size={16} />
            Open Chat
          </Link>
          <Link className="dashboard-button" to="/projects">
            <FolderOpen size={16} />
            View projects
          </Link>
        </nav>
      </div>
    </main>
  );
}

export const Component = NotFoundPage;
