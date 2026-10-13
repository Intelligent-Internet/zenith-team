import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { Logo } from "@/shared/ui/logo";
import { desktopAppUrl } from "@/shared/lib/desktop-app-url";
import {
  ArrowLeft,
  ArrowUpRight,
  LockKeyhole,
  Monitor,
} from "@/shared/ui/icons";
import "../styles/artifact-sharing.css";
import { StudioDownloadDialog } from "@/features/public-site/components/studio-dialogs";
import { Button } from "@/shared/ui/button";
import "@/features/public-site/styles/studio-page.css";

export function ArtifactDesktopBridge({
  path,
  onContinueInBrowser,
}: {
  path: string;
  onContinueInBrowser: () => void;
}) {
  const [showDownloads, setShowDownloads] = useState(false);
  const attempted = useRef("");
  const desktopUrl = desktopAppUrl(path);

  useEffect(() => {
    if (attempted.current === desktopUrl) return;
    attempted.current = desktopUrl;
    // Browsers may require a user gesture; the button remains available.
    try {
      window.location.assign(desktopUrl);
    } catch {
      // Keep the bridge visible if no desktop protocol handler is installed.
    }
  }, [desktopUrl]);

  return (
    <div className="artifact-bridge">
      <header className="artifact-bridge-brand">
        <Logo className="artifact-share-logo" />
        <Button variant="outline" asChild>
          <Link to="/">
            <ArrowLeft size={16} aria-hidden /> Back to home
          </Link>
        </Button>
      </header>
      <main className="artifact-bridge-main">
        <section
          className="artifact-bridge-card"
          aria-labelledby="artifact-open-title"
        >
          <div className="grid place-items-center w-[64px] h-[64px] m-[0_auto_28px] rounded-[18px] [background:color-mix(in_srgb,_var(--accent-sky)_45%,_var(--surface-primary))]">
            <Monitor size={28} aria-hidden />
          </div>
          <span className="block text-[length:10px] font-[650] tracking-[0.14em] text-[var(--text-secondary)]">
            SHARED WITH YOU
          </span>
          <h1 id="artifact-open-title">
            Your artifact is ready
            <br />
            to open.
          </h1>
          <p className="text-[var(--text-secondary)] text-[length:14px] leading-[1.7]">
            Continue in Zenith Team to view the files
            <br className="hidden sm:block" /> and pick up the conversation.
          </p>
          <div className="artifact-bridge-actions grid gap-[10px] mt-[30px]">
            <Button variant="default" asChild>
              <a href={desktopUrl}>
                Open Zenith Team <ArrowUpRight size={18} aria-hidden />
              </a>
            </Button>
            <Button variant="outline" onClick={onContinueInBrowser}>
              Continue in browser
            </Button>
          </div>
          <p className="m-[18px_0_28px] text-[var(--text-tertiary)] text-[length:11px]">
            If prompted, allow your browser to open the app.
          </p>
          <div className="artifact-bridge-privacy">
            <LockKeyhole size={16} aria-hidden />
            <span>Private artifact. Only people with access can view it.</span>
          </div>
        </section>
        <p className="artifact-bridge-download mt-[24px] text-center text-[var(--text-secondary)] text-[length:12px]">
          New to Zenith Team?{" "}
          <button type="button" onClick={() => setShowDownloads(true)}>
            Get the desktop app <ArrowUpRight size={14} aria-hidden />
          </button>
        </p>
      </main>
      {showDownloads ? (
        <StudioDownloadDialog onClose={() => setShowDownloads(false)} />
      ) : null}
    </div>
  );
}
