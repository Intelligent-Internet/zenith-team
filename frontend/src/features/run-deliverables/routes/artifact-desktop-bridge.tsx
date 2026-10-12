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
          <div className="artifact-bridge-icon">
            <Monitor size={28} aria-hidden />
          </div>
          <span className="artifact-eyebrow">SHARED WITH YOU</span>
          <h1 id="artifact-open-title">
            Your artifact is ready
            <br />
            to open.
          </h1>
          <p className="artifact-bridge-description">
            Continue in Zenith Team to view the files
            <br className="hidden sm:block" /> and pick up the conversation.
          </p>
          <div className="artifact-bridge-actions">
            <Button variant="default" asChild>
              <a href={desktopUrl}>
                Open Zenith Team <ArrowUpRight size={18} aria-hidden />
              </a>
            </Button>
            <Button variant="outline" onClick={onContinueInBrowser}>
              Continue in browser
            </Button>
          </div>
          <p className="artifact-bridge-hint">
            If prompted, allow your browser to open the app.
          </p>
          <div className="artifact-bridge-privacy">
            <LockKeyhole size={16} aria-hidden />
            <span>Private artifact. Only people with access can view it.</span>
          </div>
        </section>
        <p className="artifact-bridge-download">
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
