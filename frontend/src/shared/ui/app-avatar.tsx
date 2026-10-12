import { useState } from "react";

import type { PluginItemV1, PluginProvider } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import {
  localPluginIconUrl,
  type LocalPluginIconContext,
  safePluginIconUrl,
} from "@/shared/lib/plugin-icons";
import { Icon, type IconName } from "@/shared/ui/icon";
import "./app-avatar.css";

export type AppAvatarSize = 20 | 24 | 28 | 32 | 40 | 52 | 56 | 60 | 72;

export type AppAvatarProps = {
  /** The monogram letter and, with `label`, the name the avatar stands for. */
  name: string;
  /** A remote logo; only a URL that passes safePluginIconUrl is loaded. */
  iconUrl?: string | null;
  /** A local plugin logo served by the desktop app's boardly-plugin-icon:// scheme. */
  local?: {
    provider: PluginProvider;
    nativeId: string;
    context?: LocalPluginIconContext;
  };
  /** A bundled brand mark shown when no logo loads. */
  brand?: IconName;
  /**
   * Renews with the data behind the logos (a plugin's `observedAt`), so a
   * logo that failed while it was unavailable is tried again.
   */
  revision?: string;
  size?: AppAvatarSize;
  /** When set the avatar is an image with this accessible name; otherwise it is decorative. */
  label?: string;
  className?: string;
};

/**
 * One app, plugin or connector logo on a Codex-style rounded tile. It tries the
 * remote logo, then the local plugin logo, then the bundled brand mark, then a
 * monogram; a logo that fails to load falls through to the next source, and
 * the failures are forgotten when the sources or the revision change.
 */
export function AppAvatar({
  name,
  iconUrl,
  local,
  brand,
  revision,
  size = 32,
  label,
  className,
}: AppAvatarProps) {
  const sources = [
    safePluginIconUrl(iconUrl ?? undefined),
    local ? localPluginIconUrl(local, local.context) : undefined,
  ].filter(
    (source, index, all): source is string =>
      Boolean(source) && all.indexOf(source) === index,
  );
  const failureKey = `${sources.join("\n")}\n${revision ?? ""}`;
  const [failures, setFailures] = useState({
    key: failureKey,
    urls: [] as string[],
  });
  const failed = failures.key === failureKey ? failures.urls : [];
  const logoUrl = sources.find((source) => !failed.includes(source));
  const monogram = Array.from(name.trim())[0]?.toLocaleUpperCase() ?? "";

  return (
    <span
      aria-hidden={label ? undefined : true}
      aria-label={label}
      className={cx("app-avatar", className)}
      data-size={size}
      data-source={logoUrl ? "logo" : brand ? "brand" : "monogram"}
      role={label ? "img" : undefined}
    >
      {logoUrl ? (
        <img
          alt=""
          className="app-avatar__logo"
          decoding="async"
          draggable={false}
          loading="lazy"
          referrerPolicy="no-referrer"
          src={logoUrl}
          onError={() =>
            setFailures((current) => {
              const urls = current.key === failureKey ? current.urls : [];
              return urls.includes(logoUrl)
                ? current
                : { key: failureKey, urls: [...urls, logoUrl] };
            })
          }
        />
      ) : brand ? (
        <Icon aria-hidden="true" className="app-avatar__logo" name={brand} />
      ) : (
        monogram
      )}
    </span>
  );
}

/**
 * The avatar of a discovered plugin: its remote logo, then its local one. The
 * local logo is unavailable until the computer has discovered its plugins,
 * so each new discovery tries a failed logo again.
 */
export function pluginAvatarProps(
  plugin: Pick<
    PluginItemV1,
    "iconUrl" | "name" | "nativeId" | "observedAt" | "provider"
  >,
  context?: LocalPluginIconContext,
): AppAvatarProps {
  return {
    name: plugin.name,
    iconUrl: plugin.iconUrl,
    local: { provider: plugin.provider, nativeId: plugin.nativeId, context },
    revision: plugin.observedAt,
  };
}
