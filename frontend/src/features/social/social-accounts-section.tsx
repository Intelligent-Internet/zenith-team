import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { toast } from "sonner";
import { shouldUseDesktopHashRouter } from "@/shared/desktop/desktop-environment.service";
import { Link2Off, Plus } from "@/shared/ui/icons";
import { Avatar, AvatarFallback, AvatarImage } from "@/shared/ui/core/avatar";
import { personInitials } from "@/shared/ui/person-initials";
import { attachmentUrl } from "@/shared/chat/attachment-preview";
import { PersonalToolsDialog } from "@/features/common/components/personal-tools-dialog";
import { SocialPlatformMark } from "./social-platform-mark";
import { getZenithTeamQueryErrorMessage } from "@/shared/api/zenith-team-api";
import {
  useDisconnectSocialMutation,
  useSocialConnectionsQuery,
  useSocialPlatformsQuery,
  useStartSocialOAuthMutation,
  type SocialConnection,
  type SocialPlatform,
} from "./social-api";
import "./social.css";

export function SocialAccountsSection({
  search = "",
  connectedOnly = false,
}: {
  search?: string;
  connectedOnly?: boolean;
}) {
  const isDesktop = shouldUseDesktopHashRouter();
  const [params, setParams] = useSearchParams();
  const platforms = useSocialPlatformsQuery();
  const connections = useSocialConnectionsQuery(undefined, {
    refetchOnMountOrArgChange: true,
    refetchOnFocus: true,
  });
  const [start] = useStartSocialOAuthMutation();
  const connecting = useRef(false);
  const [isConnecting, setIsConnecting] = useState(false);
  const [disconnect, disconnecting] = useDisconnectSocialMutation();
  const [selected, setSelected] = useState<SocialConnection | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const result = params.get("oauth");
    if (!result) return;
    if (result === "connected") {
      toast.success("Social account connected");
      void connections.refetch();
    } else toast.error("Connection wasn’t completed. Please try again.");
    const clean = new URLSearchParams(params);
    clean.delete("oauth");
    clean.delete("platform");
    setParams(clean, { replace: true });
  }, [params, setParams, connections.refetch]);
  async function connect(platform: string) {
    if (connecting.current) return;
    connecting.current = true;
    setIsConnecting(true);
    setError("");
    try {
      const bridge = window.zenithTeamDesktop;
      const loopback = isDesktop
        ? await bridge?.startSocialOAuthLoopback?.()
        : undefined;
      if (isDesktop && !loopback?.ok)
        throw new Error(
          loopback && !loopback.ok
            ? loopback.reason
            : "Please update the desktop app to connect social accounts.",
        );
      const result = await start({
        platform,
        desktop: loopback?.ok
          ? { redirectUri: loopback.redirectUri, state: loopback.state }
          : undefined,
      }).unwrap();
      if (isDesktop && bridge) {
        const opened = await bridge.openExternal(result.authorizationUrl);
        if (!opened.ok) throw new Error(opened.reason);
      } else window.location.assign(result.authorizationUrl);
    } catch (error) {
      if (isDesktop) {
        await window.zenithTeamDesktop
          ?.cancelSocialOAuthLoopback?.()
          .catch(() => {});
      }
      setError(getZenithTeamQueryErrorMessage(error));
    } finally {
      connecting.current = false;
      setIsConnecting(false);
    }
  }
  async function remove() {
    if (!selected) return;
    setError("");
    try {
      await disconnect({
        id: selected.id,
        revision: selected.revision,
      }).unwrap();
      setSelected(null);
      toast.success("Account disconnected");
    } catch (error) {
      setError(getZenithTeamQueryErrorMessage(error));
    }
  }
  const accounts = connections.currentData?.items ?? [];

  const term = search.trim().toLocaleLowerCase();
  const matchesSection = "social accounts publishing".includes(term);
  const groups = (platforms.currentData?.items ?? []).flatMap((platform) => {
    const matchesPlatform =
      matchesSection ||
      `${platform.name} ${platform.id}`.toLocaleLowerCase().includes(term);
    const connectedAccounts = accounts.filter(
      (account) =>
        account.platform === platform.id &&
        (matchesPlatform ||
          `${account.displayName} ${account.username}`
            .toLocaleLowerCase()
            .includes(term)),
    );
    if (connectedOnly && !connectedAccounts.length) return [];
    if (!matchesPlatform && !connectedAccounts.length) return [];
    return [{ platform, accounts: connectedAccounts }];
  });
  const loading = platforms.isLoading || connections.isLoading;
  const queryError = platforms.error || connections.error;
  if (!loading && !queryError && !groups.length) return null;

  return (
    <section
      className="plugins-section"
      id="social-accounts"
      aria-labelledby="social-accounts-heading"
    >
      <header className="plugins-section__header">
        <h2 id="social-accounts-heading">Social accounts</h2>
      </header>
      {queryError ? (
        <div
          className="social-load-state flex items-center gap-[12px] m-[16px_0_0] text-workspace-muted text-workspace-label"
          role="alert"
        >
          <p>{getZenithTeamQueryErrorMessage(queryError)}</p>
          <button
            className="plugins-text-button"
            type="button"
            onClick={() => {
              if (platforms.isError) void platforms.refetch();
              if (connections.isError) void connections.refetch();
            }}
          >
            Try again
          </button>
        </div>
      ) : null}
      {loading ? (
        <p
          className="social-load-state flex items-center gap-[12px] m-[16px_0_0] text-workspace-muted text-workspace-label"
          role="status"
        >
          Loading social accounts…
        </p>
      ) : (
        <ul className="plugins-grid">
          {groups.map(({ platform, accounts: platformAccounts }) => (
            <SocialPlatformRow
              key={platform.id}
              platform={platform}
              accounts={platformAccounts}
              connecting={isConnecting}
              onConnect={() => void connect(platform.id)}
              onDisconnect={(account) => {
                setError("");
                setSelected(account);
              }}
            />
          ))}
        </ul>
      )}
      {error && !selected && (
        <p
          role="alert"
          className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
        >
          {error}
        </p>
      )}
      {selected && (
        <PersonalToolsDialog
          title={`Disconnect ${selected.displayName}?`}
          description="This removes the account and its pages from Zenith Team."
          onClose={() => {
            if (!disconnecting.isLoading) setSelected(null);
          }}
        >
          <p className="personal-note">Existing posts stay unchanged.</p>
          {selected.platform === "linkedin" && (
            <p className="personal-note">
              LinkedIn access is removed from Zenith Team. To also revoke the
              app’s grant, remove it in{" "}
              <a
                href="https://www.linkedin.com/psettings/permitted-services"
                target="_blank"
                rel="noreferrer"
              >
                LinkedIn’s permitted services
              </a>
              .
            </p>
          )}
          {error && (
            <p
              role="alert"
              className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
            >
              {error}
            </p>
          )}
          <div className="personal-dialog-actions">
            <button
              className="personal-button"
              disabled={disconnecting.isLoading}
              onClick={() => setSelected(null)}
            >
              Keep connected
            </button>
            <button
              className="personal-button personal-button--danger"
              disabled={disconnecting.isLoading}
              onClick={() => void remove()}
            >
              {disconnecting.isLoading ? "Disconnecting…" : "Disconnect"}
            </button>
          </div>
        </PersonalToolsDialog>
      )}
    </section>
  );
}

function SocialPlatformRow({
  platform,
  accounts,
  connecting,
  onConnect,
  onDisconnect,
}: {
  platform: SocialPlatform;
  accounts: SocialConnection[];
  connecting: boolean;
  onConnect: () => void;
  onDisconnect: (account: SocialConnection) => void;
}) {
  const description = accounts.length
    ? `${accounts.length} connected ${accounts.length === 1 ? "account" : "accounts"}`
    : platform.available
      ? "Connect your account"
      : platform.comingSoon
        ? "Coming soon"
        : "Not available";

  return (
    <li className="min-w-0 [align-self:start]">
      <div className="plugins-row social-platform-row">
        <SocialPlatformMark platform={platform.id} />
        <div className="flex min-w-0 flex-1 flex-col gap-[2px]">
          <h3 className="m-0 text-workspace-text text-workspace-body font-[600] leading-workspace-body">
            {platform.name}
          </h3>
          <span className="overflow-hidden text-workspace-muted text-workspace-label leading-workspace-label text-ellipsis whitespace-nowrap">
            {description}
          </span>
        </div>
        <div className="relative z-[1] flex min-w-[56px] flex-none flex-col items-center">
          {platform.available && (
            <button
              type="button"
              className="plugins-icon-button"
              disabled={connecting}
              aria-label={`Connect ${accounts.length ? "another " : ""}${platform.name} account`}
              title={`Connect ${accounts.length ? "another " : ""}${platform.name} account`}
              onClick={onConnect}
            >
              <Plus size={20} aria-hidden />
            </button>
          )}
        </div>
      </div>
      {accounts.length > 0 && (
        <ul
          className="grid gap-[4px] m-[0_12px_8px_54px] [border-top:1px_solid_var(--workspace-divider)] p-[4px_0_0] [list-style:none]"
          aria-label={`${platform.name} accounts`}
        >
          {accounts.map((account) => (
            <SocialAccountRow
              key={account.id}
              account={account}
              platformName={platform.name}
              reconnectDisabled={connecting || !platform.available}
              onReconnect={onConnect}
              onDisconnect={() => onDisconnect(account)}
            />
          ))}
        </ul>
      )}
    </li>
  );
}

function SocialAccountRow({
  account,
  platformName,
  reconnectDisabled,
  onReconnect,
  onDisconnect,
}: {
  account: SocialConnection;
  platformName: string;
  reconnectDisabled: boolean;
  onReconnect: () => void;
  onDisconnect: () => void;
}) {
  const username = account.username
    ? `@${account.username.replace(/^@/, "")}`
    : "";

  return (
    <li className="social-platform-account flex min-w-0 min-h-[52px] items-center gap-[10px] p-[6px_0]">
      <Avatar
        className="social-account-avatar w-[28px] h-[28px] text-workspace-caption font-[500]"
        aria-hidden="true"
      >
        <AvatarImage
          src={attachmentUrl(account.avatarUrl)}
          alt=""
          referrerPolicy="no-referrer"
        />
        <AvatarFallback>{personInitials(account.displayName)}</AvatarFallback>
      </Avatar>
      <div className="flex-1 min-w-0">
        <p className="social-account-name" title={account.displayName}>
          {account.displayName}
        </p>
        {username && (
          <p className="social-account-username" title={username}>
            {username}
          </p>
        )}
        {account.targets.length > 1 && (
          <p className="social-account-targets">
            {account.targets.map((target) => target.name).join(" · ")}
          </p>
        )}
        {account.status === "needs_reconnect" && (
          <button
            type="button"
            className="plugins-text-button social-account-reconnect"
            disabled={reconnectDisabled}
            onClick={onReconnect}
          >
            Reconnect
          </button>
        )}
      </div>
      <button
        type="button"
        className="plugins-icon-button social-account-disconnect"
        aria-label={`Disconnect ${account.displayName} from ${platformName}`}
        title={`Disconnect ${account.displayName}`}
        onClick={onDisconnect}
      >
        <Link2Off size={16} aria-hidden />
      </button>
    </li>
  );
}
