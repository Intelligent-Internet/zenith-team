import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useLocation } from "react-router";
import { PublicSiteFooter } from "@/features/public-site/components/public-site-footer";
import { PublicSiteHeader } from "@/features/public-site/components/public-site-header";
import { findActiveLegalSectionId } from "@/features/public-site/lib/legal-scroll-spy";
import { PublicPageMetadata } from "@/features/public-site/lib/public-page-metadata";
import type {
  LegalDocument,
  LegalParagraph,
} from "@/features/public-site/model/legal-document";
import { cx } from "@/shared/lib/cx";
import "@/features/public-site/styles/public-site.css";

export function PublicLegalDocument({
  document,
  path,
}: {
  document: LegalDocument;
  path: string;
}) {
  const scrollRootRef = useRef<HTMLDivElement>(null);
  const location = useLocation();
  const [activeSectionId, setActiveSectionId] = useState(
    document.sections[0]?.id ?? "",
  );
  const scrollToSection = useCallback((sectionId: string) => {
    const section = scrollRootRef.current?.querySelector<HTMLElement>(
      `#${CSS.escape(sectionId)}`,
    );
    section?.scrollIntoView({ block: "start" });
    section?.focus({ preventScroll: true });
  }, []);

  useEffect(() => {
    if (location.hash) scrollToSection(location.hash.slice(1));
    else scrollRootRef.current?.scrollTo({ top: 0 });
  }, [location.hash, path, scrollToSection]);

  useEffect(() => {
    const scrollRoot = scrollRootRef.current;

    if (!scrollRoot || document.sections.length === 0) {
      return;
    }

    let animationFrame: number | null = null;

    const updateActiveSection = () => {
      animationFrame = null;
      const rootTop = scrollRoot.getBoundingClientRect().top;
      const readingLine =
        rootTop + Math.min(scrollRoot.clientHeight * 0.22, 180);
      const sectionPositions = Array.from(
        scrollRoot.querySelectorAll<HTMLElement>("[data-legal-section]"),
      ).map((section) => ({
        id: section.id,
        top: section.getBoundingClientRect().top,
      }));
      const nextSectionId = findActiveLegalSectionId(
        sectionPositions,
        readingLine,
      );

      if (nextSectionId) {
        setActiveSectionId((currentSectionId) =>
          currentSectionId === nextSectionId ? currentSectionId : nextSectionId,
        );
      }
    };

    const scheduleUpdate = () => {
      if (animationFrame === null) {
        animationFrame = window.requestAnimationFrame(updateActiveSection);
      }
    };

    setActiveSectionId(document.sections[0].id);
    updateActiveSection();
    scrollRoot.addEventListener("scroll", scheduleUpdate, { passive: true });
    window.addEventListener("resize", scheduleUpdate);

    return () => {
      scrollRoot.removeEventListener("scroll", scheduleUpdate);
      window.removeEventListener("resize", scheduleUpdate);

      if (animationFrame !== null) {
        window.cancelAnimationFrame(animationFrame);
      }
    };
  }, [document.sections]);

  return (
    <div
      className="h-full min-h-0 overflow-x-hidden overflow-y-auto bg-public-site-canvas text-white"
      data-testid="public-scroll-root"
      ref={scrollRootRef}
    >
      <PublicPageMetadata
        description={document.description}
        path={path}
        title={`${document.title} | Zenith Team`}
      />
      <Link
        className="sr-only z-[100] rounded-md bg-yellow px-4 py-2 font-semibold text-charcoal focus:not-sr-only focus:fixed focus:left-4 focus:top-4"
        to={{ pathname: path, hash: "#public-main" }}
        onClick={() => scrollToSection("public-main")}
      >
        Skip to content
      </Link>
      <PublicSiteHeader />
      <main
        className="relative z-10 mx-auto grid w-full max-w-[1220px] grid-cols-[240px_minmax(0,760px)] justify-center gap-16 px-10 pb-24 pt-10 max-[980px]:grid-cols-1 max-[760px]:px-5 max-[760px]:pb-16 max-[760px]:pt-6"
        id="public-main"
        tabIndex={-1}
      >
        <aside className="max-[980px]:hidden">
          <nav
            aria-label={`${document.title} contents`}
            className="sticky top-8 rounded-2xl border border-white/10 bg-white/[0.035] p-5"
          >
            <p className="text-xs font-semibold uppercase tracking-[0.14em] text-white/44">
              On this page
            </p>
            <ol className="mt-4 space-y-2.5">
              {document.sections.map((section) => {
                const isActive = section.id === activeSectionId;

                return (
                  <li key={section.id}>
                    <Link
                      aria-current={isActive ? "location" : undefined}
                      className={cx(
                        "block rounded-sm text-xs leading-5 transition-colors",
                        isActive
                          ? "text-white"
                          : "text-white/58 hover:text-white",
                      )}
                      to={{ pathname: path, hash: `#${section.id}` }}
                      onClick={() => {
                        setActiveSectionId(section.id);
                        scrollToSection(section.id);
                      }}
                    >
                      {section.title}
                    </Link>
                  </li>
                );
              })}
            </ol>
          </nav>
        </aside>
        <article className="public-legal-copy min-w-0">
          <header className="border-b border-white/10 pb-10">
            <p className="text-xs font-semibold uppercase tracking-[0.16em] text-yellow/78">
              Zenith Team legal
            </p>
            <h1 className="mt-4 text-[clamp(2.5rem,7vw,4.75rem)] font-semibold leading-[0.98] tracking-[-0.055em] text-white">
              {document.title}
            </h1>
            <p className="mt-5 text-sm text-white/48">
              Last updated: {document.lastUpdated}
            </p>
            <p className="mt-6 max-w-[680px] text-base leading-7 text-white/66">
              {document.description}
            </p>
          </header>
          <div className="space-y-12 pt-12">
            {document.sections.map((section) => (
              <section
                className="scroll-mt-8"
                data-legal-section=""
                id={section.id}
                key={section.id}
                tabIndex={-1}
              >
                <h2 className="text-xl font-semibold tracking-[-0.02em] text-white">
                  {section.title}
                </h2>
                <div className="mt-4 space-y-4">
                  {section.paragraphs.map((paragraph, index) => (
                    <LegalParagraphBlock
                      key={`${section.id}-${index}`}
                      paragraph={paragraph}
                    />
                  ))}
                </div>
              </section>
            ))}
          </div>
        </article>
      </main>
      <PublicSiteFooter />
    </div>
  );
}

function LegalParagraphBlock({ paragraph }: { paragraph: LegalParagraph }) {
  if (typeof paragraph === "string") {
    return <p className="text-[15px] leading-7 text-white/66">{paragraph}</p>;
  }

  return (
    <ul className="list-disc space-y-2.5 pl-6 text-[15px] leading-7 text-white/66 marker:text-yellow/64">
      {paragraph.map((item) => (
        <li key={item}>{item}</li>
      ))}
    </ul>
  );
}
