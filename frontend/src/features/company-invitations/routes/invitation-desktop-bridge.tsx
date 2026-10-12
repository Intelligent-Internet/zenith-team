import { useEffect, useRef, useState } from "react";
import { useParams } from "react-router";
import { AuthLayout } from "@/features/auth/components/auth-layout";
import { StudioDownloadDialog } from "@/features/public-site/components/studio-dialogs";
import { Button } from "@/shared/ui/button";
import { desktopAppUrl } from "@/shared/lib/desktop-app-url";
import "@/features/public-site/styles/studio-page.css";

export function InvitationDesktopBridge() {
  const { token = "" } = useParams();
  const [showDownloads, setShowDownloads] = useState(false);
  const attempted = useRef("");
  const validToken = /^[A-Za-z0-9_-]{43}$/.test(token);
  const desktopUrl = desktopAppUrl(`/invite/${token}`);

  useEffect(() => {
    if (!validToken || attempted.current === desktopUrl) return;
    attempted.current = desktopUrl;
    // Browsers may require a user gesture; the button remains available below.
    try {
      window.location.assign(desktopUrl);
    } catch {
      // Stay on the bridge when no protocol handler is available.
    }
  }, [desktopUrl, validToken]);

  return (
    <AuthLayout label="Open invitation in Zenith Team">
      <header className="auth-panel-heading">
        <p className="auth-panel-kicker">YOU’RE INVITED</p>
        <h1>{validToken ? "Continue in Zenith Team" : "Invalid invitation"}</h1>
        <p>
          {validToken
            ? "Open the desktop app to view your invitation and join your team."
            : "Ask the sender for a new invitation link."}
        </p>
      </header>
      {validToken ? (
        <div className="grid gap-4">
          <p>
            If your browser asks, allow it to open Zenith Team. If the app
            doesn’t open, use the button below.
          </p>
          <Button variant="default" asChild>
            <a href={desktopUrl}>Open Zenith Team</a>
          </Button>
          <Button variant="outline" onClick={() => setShowDownloads(true)}>
            Download desktop app
          </Button>
          <p>
            After installing, return here and select Open Zenith Team. Sign in
            or create an account in the app with the email that received this
            invitation.
          </p>
        </div>
      ) : null}
      {showDownloads ? (
        <StudioDownloadDialog onClose={() => setShowDownloads(false)} />
      ) : null}
    </AuthLayout>
  );
}
