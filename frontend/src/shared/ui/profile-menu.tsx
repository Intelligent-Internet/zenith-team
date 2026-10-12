import { useMemberPresence } from "@/features/discussions/model/use-member-presence";
import { discussionsApi } from "@/features/discussions/api/discussions-api";
import { toast } from "@/shared/ui/sonner";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import {
  Building2Icon,
  BotIcon,
  Clock3Icon,
  SettingsIcon,
  ShieldCheckIcon,
} from "@/shared/ui/icons";
import type { ReactNode } from "react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import {
  SESSION_CHANGE_EVENT,
  readSessionSnapshot,
  refreshSessionSnapshot,
  syncCrossTabSessionStorageEvent,
  type SessionSnapshot,
} from "@/app/session/session";
import { cx } from "@/shared/lib/cx";
import { ContentPanel } from "@/shared/ui/content-panel";
import { Icon } from "./icon";
import { personInitials } from "./person-initials";

const profileButtonClassName =
  "grid size-10 place-items-center rounded-full border border-grey-3/32 bg-grey-3/14 text-grey-3 shadow-[0_8px_20px_rgba(0,0,0,0.24),inset_0_1px_0_rgba(255,255,255,0.36)] transition duration-150 hover:-translate-y-px hover:border-accent-sky/60 hover:bg-grey-3/22 [&_svg]:size-[18px]";
const profileAvatarClassName =
  "grid size-7 place-items-center overflow-hidden rounded-full bg-grey-3 text-xs font-bold leading-none text-charcoal";
const menuPositionClassName =
  "z-[90] !border-0 !bg-transparent !p-0 !text-white !shadow-none";
const menuSurfaceClassName =
  "max-h-[var(--radix-dropdown-menu-content-available-height)] w-[min(340px,calc(100vw-24px))] overflow-hidden rounded-3xl";
const menuItemClassName =
  "flex h-11 w-full cursor-pointer items-center gap-3 rounded-[16px] px-4 text-left text-sm font-bold outline-none transition duration-150 [&_svg]:size-4";
const settingsMenuItemClassName =
  "text-white/80 hover:bg-white/10 hover:text-white focus:bg-white/12 focus:text-white";
const logoutMenuItemClassName =
  "workspace-profile-menu__logout text-rose-300 hover:bg-rose-300/12 hover:text-rose-200 focus:bg-rose-300/14 focus:text-rose-100";

export type ProfileMenuSummary = {
  companyName: string | null;
  displayName: string;
  email: string;
  role: string | null;
  timezone: string | null;
};

type ProfileMenuProps = {
  avatarClassName?: string;
  avatarFallbackSrc?: string;
  avatarSrc?: string;
  className?: string;
  contentClassName?: string;
  onLogout?: () => Promise<void> | void;
  showProfileFacts?: boolean;
  showSettingsItems?: boolean;
  surfaceClassName?: string;
};

export function ProfileMenu({
  avatarClassName,
  avatarFallbackSrc,
  avatarSrc,
  className,
  contentClassName,
  onLogout,
  showProfileFacts = true,
  showSettingsItems = true,
  surfaceClassName,
}: ProfileMenuProps = {}) {
  const navigate = useNavigate();
  const presence = useMemberPresence([], true);
  const [setAway, awayMutation] = discussionsApi.useSetMemberAwayMutation();
  const selfPresence = presence.currentData?.items.find(
    (item) => item.userId === readSessionSnapshot().userId,
  );
  const [session, setSession] = useState<SessionSnapshot>(() =>
    readSessionSnapshot(),
  );
  const summary = createProfileMenuSummary(session);
  const profileInitials = personInitials(
    session.displayName?.trim() || session.email,
  );
  const sessionAvatarSrc = getSessionAvatarSrc(session);
  const resolvedAvatarSrc = avatarSrc ?? sessionAvatarSrc ?? avatarFallbackSrc;

  useEffect(() => {
    let cancelled = false;

    const syncSessionSnapshot = () => {
      if (!cancelled) {
        setSession(readSessionSnapshot());
      }
    };
    const handleStorageChange = (event: StorageEvent) => {
      if (syncCrossTabSessionStorageEvent(event)) {
        syncSessionSnapshot();
      }
    };

    window.addEventListener("storage", handleStorageChange);
    window.addEventListener(SESSION_CHANGE_EVENT, syncSessionSnapshot);

    if (session.status === "authenticated") {
      void refreshSessionSnapshot()
        .then((nextSession) => {
          if (!cancelled) {
            setSession(nextSession);
          }
        })
        .catch(syncSessionSnapshot);
    }

    return () => {
      cancelled = true;
      window.removeEventListener("storage", handleStorageChange);
      window.removeEventListener(SESSION_CHANGE_EVENT, syncSessionSnapshot);
    };
  }, [session.status]);

  const handleLogout = async () => {
    await onLogout?.();
    navigate("/login", { replace: true });
  };
  const openProfileSettings = () => {
    navigate("/settings/profile");
  };
  const openCompanySettings = () => {
    navigate("/settings/general");
  };
  const openAgentSettings = () => {
    navigate("/settings/agents");
  };

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger
        className={cx(profileButtonClassName, className)}
        type="button"
        aria-label="Open profile menu"
      >
        <span className={cx(profileAvatarClassName, avatarClassName)}>
          {resolvedAvatarSrc ? (
            <img
              src={resolvedAvatarSrc}
              alt=""
              className="h-full w-full object-cover"
            />
          ) : session.status === "authenticated" && profileInitials ? (
            profileInitials
          ) : (
            <Icon name="user-circle" aria-hidden="true" />
          )}
        </span>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          className={menuPositionClassName}
          collisionPadding={12}
          side="bottom"
          sideOffset={14}
        >
          <ContentPanel
            className={cx(
              menuSurfaceClassName,
              contentClassName,
              surfaceClassName,
            )}
            contentClassName={cx(
              "pointer-events-auto relative w-full flex-col overflow-y-auto rounded-3xl p-4 text-white",
              surfaceClassName,
            )}
          >
            <div className="relative z-10 border-b border-white/18 px-4 pb-3 pt-2">
              <p className="truncate text-lg font-extrabold leading-none text-white drop-shadow-[0_1px_8px_rgba(0,0,0,0.45)]">
                {summary.displayName}
              </p>
              {selfPresence && (
                <p className="mt-2 text-xs text-white/80">
                  {selfPresence.online ? "Active" : "Away"}
                </p>
              )}
              <p className="mt-2 truncate text-sm font-medium leading-none text-white/82 drop-shadow-[0_1px_8px_rgba(0,0,0,0.42)]">
                {summary.email}
              </p>
            </div>
            {session.company?.id && (
              <DropdownMenu.Item
                className={cx(menuItemClassName, settingsMenuItemClassName)}
                disabled={!selfPresence || awayMutation.isLoading}
                onSelect={() => {
                  void setAway({
                    companyId: session.company!.id!,
                    manualAway: !selfPresence?.manualAway,
                  })
                    .unwrap()
                    .catch(() => toast.error("Status could not be updated."));
                }}
              >
                <Clock3Icon aria-hidden="true" />
                {selfPresence?.manualAway
                  ? "Set yourself as active"
                  : "Set yourself as away"}
              </DropdownMenu.Item>
            )}
            {showProfileFacts ? (
              <div className="relative z-10 grid gap-2 border-b border-white/12 px-2 py-3 text-sm text-white/78">
                {summary.companyName ? (
                  <ProfileMenuFact
                    icon={<Building2Icon aria-hidden="true" />}
                    label="Company"
                    value={summary.companyName}
                  />
                ) : null}
                {summary.role ? (
                  <ProfileMenuFact
                    icon={<ShieldCheckIcon aria-hidden="true" />}
                    label="Role"
                    value={summary.role}
                  />
                ) : null}
                {summary.timezone ? (
                  <ProfileMenuFact
                    icon={<Clock3Icon aria-hidden="true" />}
                    label="Timezone"
                    value={summary.timezone}
                  />
                ) : null}
              </div>
            ) : null}
            <div className="relative z-10 pt-3">
              {showSettingsItems ? (
                <>
                  <DropdownMenu.Item
                    className={cx(menuItemClassName, settingsMenuItemClassName)}
                    onSelect={openProfileSettings}
                  >
                    <SettingsIcon aria-hidden="true" />
                    My Account
                  </DropdownMenu.Item>
                  <DropdownMenu.Item
                    className={cx(menuItemClassName, settingsMenuItemClassName)}
                    onSelect={openCompanySettings}
                  >
                    <Building2Icon aria-hidden="true" />
                    General settings
                  </DropdownMenu.Item>
                  <DropdownMenu.Item
                    className={cx(menuItemClassName, settingsMenuItemClassName)}
                    onSelect={openAgentSettings}
                  >
                    <BotIcon aria-hidden="true" />
                    Agent settings
                  </DropdownMenu.Item>
                </>
              ) : null}
              <DropdownMenu.Item
                className={cx(menuItemClassName, logoutMenuItemClassName)}
                onSelect={handleLogout}
              >
                <Icon name="logout" aria-hidden="true" />
                Logout
              </DropdownMenu.Item>
            </div>
          </ContentPanel>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function ProfileMenuFact({
  icon,
  label,
  value,
}: {
  icon: ReactNode;
  label: string;
  value: string;
}) {
  return (
    <div className="grid min-w-0 grid-cols-[18px_72px_minmax(0,1fr)] items-center gap-2 rounded-[14px] px-2 py-1.5 text-left">
      <span className="text-white/58 [&_svg]:size-4">{icon}</span>
      <span className="text-xs font-semibold text-white/44">{label}</span>
      <span className="truncate text-xs font-bold text-white/84">{value}</span>
    </div>
  );
}

export function createProfileMenuSummary(
  session: SessionSnapshot,
): ProfileMenuSummary {
  return {
    companyName: trimmedOrNull(session.company?.workspaceName),
    displayName: trimmedOrNull(session.displayName) ?? "Zenith Team user",
    email: trimmedOrNull(session.email) ?? "No email available",
    role: trimmedOrNull(session.user?.role),
    timezone: trimmedOrNull(session.user?.timezone),
  };
}

function getSessionAvatarSrc(session: SessionSnapshot) {
  const sessionWithAvatar = session as typeof session & {
    avatarUrl?: string;
    image?: string;
    picture?: string;
  };

  return (
    sessionWithAvatar.avatarUrl ??
    sessionWithAvatar.picture ??
    sessionWithAvatar.image
  );
}

function trimmedOrNull(value: string | undefined): string | null {
  const trimmed = value?.trim();
  return trimmed ? trimmed : null;
}
