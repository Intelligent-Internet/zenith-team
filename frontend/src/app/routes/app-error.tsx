import { useEffect, useRef } from "react";
import { Link } from "react-router";
import { Button } from "@/shared/ui/core/button";
import { ArrowLeft, RefreshCw } from "@/shared/ui/icons";

export function AppErrorPage() {
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    headingRef.current?.focus();
  }, []);

  return (
    <main
      className="grid min-h-dvh place-items-center bg-white px-6 py-12 text-[#0D0D0D] dark:bg-[#181818] dark:text-[#ECECEC]"
      aria-labelledby="app-error-title"
    >
      <section className="w-full max-w-md text-center">
        <p className="mb-3 text-sm text-[#6E6E6E] dark:text-[#A3A3A3]">
          Zenith Team
        </p>
        <h1
          id="app-error-title"
          ref={headingRef}
          tabIndex={-1}
          className="text-2xl font-semibold tracking-tight outline-none"
        >
          This page couldn’t open
        </h1>
        <p className="mt-3 text-sm leading-6 text-[#6E6E6E] dark:text-[#A3A3A3]">
          An unexpected problem interrupted the page. Reload to try again, or
          return to the start of your workspace.
        </p>
        <div className="mt-6 flex flex-wrap justify-center gap-3">
          <Button
            variant="default"
            className="rounded-lg bg-[#171717] text-white hover:bg-[#333333] focus-visible:bg-[#333333] active:bg-[#424242] dark:bg-white dark:text-[#171717] dark:hover:bg-[#E3E3E3] dark:focus-visible:bg-[#E3E3E3] dark:active:bg-[#CCCCCC]"
            onClick={() => window.location.reload()}
          >
            <RefreshCw size={16} aria-hidden="true" />
            Reload page
          </Button>
          <Button
            variant="outline"
            className="rounded-lg border-[#D9D9D9] text-[#0D0D0D] hover:bg-[#F5F5F5] focus-visible:bg-[#F5F5F5] dark:border-[#424242] dark:text-[#ECECEC] dark:hover:bg-[#2A2A2A] dark:focus-visible:bg-[#2A2A2A]"
            asChild
          >
            <Link
              to="/"
              reloadDocument
              onClick={(event) => {
                if (
                  event.button !== 0 ||
                  event.metaKey ||
                  event.ctrlKey ||
                  event.shiftKey ||
                  event.altKey
                )
                  return;
                event.preventDefault();
                // A hash-only navigation retains rejected lazy imports.
                window.history.replaceState(null, "", event.currentTarget.href);
                window.location.reload();
              }}
            >
              <ArrowLeft size={16} aria-hidden="true" />
              Return to start
            </Link>
          </Button>
        </div>
      </section>
    </main>
  );
}
