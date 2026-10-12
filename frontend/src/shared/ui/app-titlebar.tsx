import type { CSSProperties, ReactNode } from "react";
import { ceoAvatarSrc } from "@/shared/ui/agent-avatar/avatar-assets";
import { cx } from "@/shared/lib/cx";
import { ProfileMenu } from "@/shared/ui/profile-menu";

type AppTitlebarProps = {
  center?: ReactNode;
  className?: string;
  leading?: ReactNode;
  onLogout?: () => Promise<void> | void;
  profileAvatarFallbackSrc?: string;
  profileAvatarSrc?: string;
  profileContentClassName?: string;
  profileSurfaceClassName?: string;
  showProfileFacts?: boolean;
  showProfileMenu?: boolean;
  showSettingsItems?: boolean;
  trailing?: ReactNode;
};

const titlebarDragStyle = {
  WebkitAppRegion: "drag",
  userSelect: "none",
} as CSSProperties;

const titlebarNoDragStyle = {
  WebkitAppRegion: "no-drag",
  userSelect: "auto",
} as CSSProperties;

export function AppTitlebar({
  center,
  className,
  leading,
  onLogout,
  profileAvatarFallbackSrc = ceoAvatarSrc,
  profileAvatarSrc,
  profileContentClassName,
  profileSurfaceClassName,
  showProfileFacts = true,
  showProfileMenu = true,
  showSettingsItems = true,
  trailing,
}: AppTitlebarProps) {
  return (
    <header
      className={cx(
        "app-titlebar fixed inset-x-0 top-0 z-40 flex h-10 w-screen shrink-0 items-center border-b border-white/12 bg-[#102d33]/20 px-3 text-white backdrop-blur-[40px]",
        className,
      )}
      style={titlebarDragStyle}
      aria-label="Zenith Team title bar"
    >
      <div
        className="flex h-full min-w-0 flex-1 items-center pl-[76px]"
        style={titlebarDragStyle}
      >
        {leading}
      </div>

      {center ? (
        <div
          className="absolute left-1/2 top-1/2 flex h-8 -translate-x-1/2 -translate-y-1/2 items-center"
          style={titlebarNoDragStyle}
        >
          {center}
        </div>
      ) : null}

      <div
        className="ml-auto flex h-full shrink-0 items-center justify-end gap-2"
        style={titlebarNoDragStyle}
      >
        {trailing}
        {showProfileMenu ? (
          <ProfileMenu
            avatarFallbackSrc={profileAvatarFallbackSrc}
            avatarSrc={profileAvatarSrc}
            onLogout={onLogout}
            className="!size-8 shrink-0 overflow-hidden !border-white/30 !bg-white/10 p-0 !text-white/70 !shadow-none hover:!translate-y-0 hover:!border-white/45 hover:!bg-white/14"
            avatarClassName="!size-6 bg-transparent [&_img]:object-top"
            contentClassName={profileContentClassName}
            surfaceClassName={profileSurfaceClassName}
            showProfileFacts={showProfileFacts}
            showSettingsItems={showSettingsItems}
          />
        ) : null}
      </div>
    </header>
  );
}
