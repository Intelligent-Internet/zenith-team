import type { CSSProperties, ReactNode } from "react";
import { readableColor } from "@/app/appearance/color";
import { detectDesktopPlatform } from "@/shared/desktop/desktop-download.service";
import { shouldUseDesktopHashRouter } from "@/shared/desktop/desktop-environment.service";
import {
  WorkspaceAppearanceProvider,
  useWorkspaceAppearance,
} from "@/app/appearance/workspace-appearance-provider";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { AppTitlebar } from "@/app/shell/app-titlebar";
import { Logo } from "@/shared/ui/logo";
import { AuthShowcasePanel } from "./auth-showcase-panel";
import "../styles/auth-page.css";
import "../styles/auth-forms.css";

export function AuthLayout({
  children,
  footer,
  label = "Zenith Team authentication",
}: {
  children: ReactNode;
  footer?: ReactNode;
  label?: string;
}) {
  const session = useSessionSnapshot();

  return (
    <WorkspaceAppearanceProvider
      userId={session.userId}
      companyId={session.company?.id}
      restoreLastAppearance
    >
      <AuthLayoutContent label={label} footer={footer}>
        {children}
      </AuthLayoutContent>
    </WorkspaceAppearanceProvider>
  );
}

function AuthLayoutContent({
  children,
  footer,
  label,
}: {
  children: ReactNode;
  footer?: ReactNode;
  label: string;
}) {
  const { palette } = useWorkspaceAppearance();
  const desktopMac =
    shouldUseDesktopHashRouter() && detectDesktopPlatform() === "macos";
  const artStyle = {
    // The illustration stays warm and light in every mode. Keep theme-colored
    // lettering legible against its lightest and darkest text backgrounds.
    "--auth-art-accent": readableColor(palette["--workspace-accent-fill"], [
      "#F4EBDF",
      "#E8DACA",
    ]),
  } as CSSProperties;

  return (
    <div
      className="auth-page"
      data-desktop-mac={desktopMac || undefined}
      style={artStyle}
    >
      <AppTitlebar
        className="!border-0 !bg-transparent !backdrop-blur-none"
        showProfileMenu={false}
      />
      <main className="auth-layout" aria-label={label}>
        <div className="auth-brand">
          <Logo alt="Zenith Team" />
        </div>
        <AuthShowcasePanel />
        <section
          className="auth-form-panel"
          aria-label="Zenith Team authentication form"
        >
          <div className="auth-panel-content">{children}</div>
          {footer ? (
            <footer className="auth-panel-footer">{footer}</footer>
          ) : null}
        </section>
      </main>
    </div>
  );
}
