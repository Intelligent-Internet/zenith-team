import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { ArrowRight, Menu, X } from "@/shared/ui/icons";
import { Logo } from "@/shared/ui/logo";
import { PublicPageMetadata } from "../lib/public-page-metadata";
import {
  StudioMotionProvider,
  useStudioMotion,
  useStudioReveals,
} from "../lib/studio-motion";
import { StudioHero } from "../components/studio-hero";
import { StudioShowcase } from "../components/studio-showcase";
import { StudioSpaces } from "../components/studio-spaces";
import { StudioWorkflow } from "../components/studio-workflow";
import { StudioWorkboard } from "../components/studio-workboard";
import {
  StudioContext,
  StudioReview,
} from "../components/studio-context-review";
import { StudioTools } from "../components/studio-tools";
import { StudioArt } from "../components/studio-primitives";
import {
  StudioDownloadDialog,
  StudioHelpDialog,
} from "../components/studio-dialogs";
import "../styles/studio-page.css";

export const publicLandingDescription =
  "A shared plan, specialist AI agents, and space to build what comes next. Give your idea room to grow with Zenith Team.";

export function PublicLandingPage() {
  return (
    <StudioMotionProvider>
      <CompanyStudioPage />
    </StudioMotionProvider>
  );
}

function CompanyStudioPage() {
  const root = useRef<HTMLDivElement>(null);
  const header = useRef<HTMLElement>(null);
  const menuButton = useRef<HTMLButtonElement>(null);
  const { enabled } = useStudioMotion();
  const [dialog, setDialog] = useState<"download" | "help" | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  useEffect(() => {
    const target = document.getElementById(window.location.hash.slice(1));
    if (target && root.current?.contains(target)) {
      target.scrollIntoView({ behavior: "instant", block: "start" });
    }
  }, []);
  useEffect(() => {
    if (!menuOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setMenuOpen(false);
      menuButton.current?.focus();
    };
    const onOutside = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !header.current?.contains(event.target)
      )
        setMenuOpen(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onOutside);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onOutside);
    };
  }, [menuOpen]);
  useStudioReveals(root);
  const getStarted = () => {
    setMenuOpen(false);
    setDialog("download");
  };

  return (
    <div
      ref={root}
      className="studio-page"
      data-studio-scroll
      data-motion={enabled ? "on" : "off"}
      data-testid="public-scroll-root"
    >
      <PublicPageMetadata
        title="Zenith Team — Give your idea room to grow"
        description={publicLandingDescription}
        path="/"
      />
      <a className="studio-skip" href="#public-main">
        Skip to content
      </a>
      <header className="absolute inset-[0_0_auto] z-[20]" ref={header}>
        <div className="studio-container studio-header-inner">
          <Link
            className="studio-wordmark"
            to="/"
            aria-label="Zenith Team home"
          >
            <Logo />
          </Link>
          <nav
            className={menuOpen ? "studio-nav is-open" : "studio-nav"}
            aria-label="Main navigation"
            id="studio-navigation"
            onClick={() => setMenuOpen(false)}
          >
            <a href="#product">Product</a>
            <a href="#team">The team</a>
            <a href="#how-it-works">How it works</a>
          </nav>
          <div className="studio-header-actions">
            <button
              className="studio-button studio-header-cta"
              onClick={getStarted}
              type="button"
            >
              Get Zenith Team
            </button>
            <button
              className="studio-menu-button"
              ref={menuButton}
              type="button"
              onClick={() => setMenuOpen(!menuOpen)}
              aria-label={menuOpen ? "Close navigation" : "Open navigation"}
              aria-expanded={menuOpen}
              aria-controls="studio-navigation"
            >
              {menuOpen ? <X size={23} /> : <Menu size={23} />}
            </button>
          </div>
        </div>
      </header>
      <main id="public-main" tabIndex={-1}>
        <StudioHero onGetStarted={getStarted} />
        <StudioShowcase />
        <StudioSpaces />
        <StudioWorkflow />
        <StudioWorkboard />
        <StudioContext />
        <StudioReview />
        <StudioTools />
        <section
          className="studio-next"
          id="download"
          aria-labelledby="studio-next-title"
        >
          <StudioArt name="next-chapter" className="studio-next-art" />
          <div className="studio-container" data-reveal>
            <h2 id="studio-next-title">
              Your next chapter
              <br />
              has <em>room to grow.</em>
            </h2>
            <button
              className="studio-button"
              onClick={getStarted}
              type="button"
            >
              Get Zenith Team <ArrowRight size={17} />
            </button>
          </div>
        </section>
      </main>
      <footer className="studio-footer">
        <div className="studio-container">
          <Link
            className="studio-wordmark studio-wordmark--footer"
            to="/"
            aria-label="Zenith Team home"
          >
            <Logo />
          </Link>
          <nav aria-label="Footer navigation">
            <a href="#product">Product</a>
            <button type="button" onClick={() => setDialog("help")}>
              Help
            </button>
            <Link to="/privacy">Privacy</Link>
            <Link to="/terms">Terms</Link>
          </nav>
          <div className="studio-footer-note">
            <span>Agent work runs on your connected, online computer.</span>
          </div>
        </div>
      </footer>
      {dialog === "download" && (
        <StudioDownloadDialog onClose={() => setDialog(null)} />
      )}
      {dialog === "help" && (
        <StudioHelpDialog onClose={() => setDialog(null)} />
      )}
    </div>
  );
}
