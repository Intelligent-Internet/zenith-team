import {
  iconsaxIcons,
  uiSymbols,
  type IconProps,
  type IconsaxIconName,
} from "@/shared/ui/icons";
import Brand0 from "@/assets/brands/facebook.svg?react";
import Brand1 from "@/assets/brands/gmail.svg?react";
import Brand2 from "@/assets/brands/google.svg?react";
import Brand3 from "@/assets/brands/google-calendar.svg?react";
import googleDriveUrl from "@/assets/brands/google-drive.svg?url";
import Brand5 from "@/assets/brands/google-map.svg?react";
import Brand6 from "@/assets/brands/linkedIn.svg?react";
import linuxUrl from "@/assets/brands/linux.svg?url";
import Brand8 from "@/assets/brands/logo.svg?react";
import Brand9 from "@/assets/brands/slack.svg?react";
import Brand10 from "@/assets/brands/x.svg?react";
import GitHubBrand from "@/assets/brands/github.svg?react";

const productIcons = {
  add: "add",
  "ai-magic": "magic-star",
  alert: "warning-2",
  "arrow-3": "swap-horizontal-01",
  "arrow-down": "arrow-down-02",
  "arrow-down-03-stroke-rounded": "import-arrow-01",
  "arrow-down-double": "double-chevron-down",
  "arrow-down-question": "message-question",
  "arrow-right": "arrow-right-01",
  "arrow-right-2": "arrow-right-02",
  artboard: "grid-1",
  award: "award",
  book: "book",
  "box-tick": "box-tick",
  brain: "cpu",
  brush: "brush",
  "bubble-chat": "message-circle",
  building: "building",
  "building-2": "buildings-2",
  "calendar-tick": "calendar-tick",
  category: "category",
  chart: "chart-2",
  "check-circle": "tick-circle",
  checkmark: "checkmark",
  clock: "clock",
  close: "close",
  code: "code",
  "code-2": "code-1",
  coin: "coin",
  "coin-2": "coin-1",
  compass: "discover",
  crown: "crown",
  cursor: "mouse-square",
  danger: "danger",
  database: "driver",
  delete: "trash",
  "delete-2": "trash",
  delivery: "box",
  design: "designtools",
  "dollar-square": "dollar-square",
  download: "import-arrow-01",
  edit: "edit",
  "edit-2": "edit-2",
  email: "sms",
  exchange: "swap-horizontal-01",
  expand: "maximize-4",
  eye: "eye",
  "eye-off": "eye-slash",
  flag: "flag",
  "flag-2": "flag-2",
  flash: "flash",
  "flash-2": "flash",
  folder: "folder",
  "folder-open": "folder-open",
  "folder-open-2": "folder-open",
  graph: "graph",
  hold: "pause-circle",
  home: "home-2",
  hourglass: "hourglass",
  idle: "timer-pause",
  "inbox-upload": "directbox-send",
  info: "info-circle",
  kanban: "grid-5",
  key: "key",
  layers: "layer",
  "layout-grid": "category",
  leaf: "tree",
  list: "task",
  lock: "lock",
  "lock-2": "lock",
  logout: "logout-01",
  marketing: "volume-high",
  "marketing-fill": "volume-high",
  mic: "microphone",
  "minimize-screen-stroke-rounded": "minimize",
  more: "3-dots-more",
  note: "note-text",
  notification: "notification",
  "open-external": "export-arrow-02",
  play: "play",
  power: "flash-circle",
  "power-circle": "flash-circle",
  product: "box",
  "receipt-2": "receipt-2",
  refresh: "refresh-arrow-01",
  rocket: "send-2",
  rotate: "rotate-left",
  "routing-2": "routing-2",
  save: "save-2",
  search: "search",
  security: "shield-security",
  sent: "send-2",
  setting: "setting-2",
  "setting-2": "setting-4",
  share: "hierarchy-2",
  "share-2": "share",
  "share-3": "share",
  "shield-tick": "shield-tick",
  shrink: "minimize",
  "sidebar-right-collapse": "sidebar-right",
  "sidebar-right-open": "sidebar-left",
  star: "star",
  "task-edit": "edit",
  "task-square": "task-square",
  tick: "checkmark",
  toolbox: "briefcase",
  "user-circle": "profile-circle",
  view: "eye",
  window: "monitor",
  workflow: "hierarchy-2",
} as const satisfies Record<string, IconsaxIconName>;

const brandIcons = {
  facebook: Brand0,
  gmail: Brand1,
  github: GitHubBrand,
  google: Brand2,
  "google-calendar": Brand3,
  "google-map": Brand5,
  linkedIn: Brand6,
  logo: Brand8,
  slack: Brand9,
  x: Brand10,
} as const;

// These large, multicolor marks keep their original artwork outside the JS bundle.
const brandImages = {
  "google-drive": googleDriveUrl,
  linux: linuxUrl,
} as const;

const iconComponents = { ...iconsaxIcons, ...uiSymbols };

export type IconName =
  | keyof typeof productIcons
  | keyof typeof iconComponents
  | keyof typeof brandIcons
  | keyof typeof brandImages;

export function Icon({
  name,
  size,
  className = size === undefined ? "size-6" : undefined,
  ...props
}: IconProps & { name: string }) {
  if (Object.hasOwn(brandImages, name)) {
    return (
      <svg
        width={size ?? 24}
        height={size ?? 24}
        viewBox="0 0 24 24"
        aria-hidden={
          props["aria-label"] || props["aria-labelledby"] ? undefined : true
        }
        focusable="false"
        className={className}
        {...props}
      >
        <image
          href={brandImages[name as keyof typeof brandImages]}
          width="24"
          height="24"
        />
      </svg>
    );
  }
  if (Object.hasOwn(brandIcons, name)) {
    const Brand = brandIcons[name as keyof typeof brandIcons];
    return (
      <Brand
        {...(size === undefined ? {} : { width: size, height: size })}
        aria-hidden={
          props["aria-label"] || props["aria-labelledby"] ? undefined : true
        }
        focusable="false"
        className={className}
        {...props}
      />
    );
  }
  const iconName = Object.hasOwn(productIcons, name)
    ? productIcons[name as keyof typeof productIcons]
    : name;
  const Component = Object.hasOwn(iconComponents, iconName)
    ? iconComponents[iconName as keyof typeof iconComponents]
    : iconsaxIcons.category;
  return <Component size={size} className={className} {...props} />;
}
