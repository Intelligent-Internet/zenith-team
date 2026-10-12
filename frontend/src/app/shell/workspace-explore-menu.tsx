import * as Popover from "@radix-ui/react-popover";
import { useEffect, useRef, useState } from "react";
import { NavLink } from "react-router";
import {
  Calendar,
  CreditCard,
  Ellipsis,
  FolderOpen,
  Mail,
  Zap,
} from "@/shared/ui/icons";
import { useCompanyPermissions } from "@/shared/permissions";
import { cx } from "@/shared/lib/cx";
import "./workspace-explore-menu.css";

// A Project's Files open on the Assets page under its `projects/<id>` scope,
// but they belong to the Project: the rail marks Projects there, not Explore.
export function isProjectFilesPath(pathname: string) {
  return /^\/assets\/projects(\/|%2f)/i.test(pathname);
}

export function WorkspaceExploreMenu({
  pathname,
  mobile,
}: {
  pathname: string;
  mobile: boolean;
}) {
  const { can } = useCompanyPermissions();
  const canViewUsage = can("usage.read");
  const groups = [
    {
      id: "work",
      items: [
        { label: "Workflows", to: "/workflows", icon: Zap },
        { label: "Calendar", to: "/calendar", icon: Calendar },
        ...(can("mailbox.manage")
          ? [{ label: "Mailbox", to: "/mailbox", icon: Mail }]
          : []),
      ],
    },
    {
      id: "resources",
      items: [{ label: "Assets", to: "/assets", icon: FolderOpen }],
    },
    {
      id: "utilities",
      items: [
        ...(canViewUsage
          ? [
              {
                label: "Subscription",
                to: "/settings/subscription",
                icon: CreditCard,
              },
            ]
          : []),
      ],
    },
  ].filter(({ items }) => items.length > 0);
  const [open, setOpen] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const trigger = useRef<HTMLButtonElement>(null);
  const hoverOpened = useRef(false);
  const active =
    !isProjectFilesPath(pathname) &&
    groups.some(({ items }) =>
      items.some(({ to }) => pathname === to || pathname.startsWith(`${to}/`)),
    );

  function cancelTimer() {
    clearTimeout(timer.current);
  }

  function scheduleClose() {
    cancelTimer();
    if (hoverOpened.current)
      timer.current = setTimeout(() => setOpen(false), 220);
  }

  useEffect(() => () => clearTimeout(timer.current), []);

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Anchor asChild>
        <button
          ref={trigger}
          type="button"
          className={cx(
            "workspace-primary-nav-item",
            (active || open) && "is-active",
          )}
          data-tour-id="workspace-navigation-explore"
          aria-haspopup="dialog"
          aria-expanded={open}
          onPointerEnter={(event) => {
            cancelTimer();
            if (event.pointerType !== "mouse" || open) return;
            timer.current = setTimeout(() => {
              hoverOpened.current = true;
              setOpen(true);
            }, 120);
          }}
          onPointerLeave={scheduleClose}
          onClick={() => {
            cancelTimer();
            hoverOpened.current = false;
            setOpen(true);
          }}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              cancelTimer();
              setOpen(false);
            }
          }}
        >
          <span className="workspace-primary-nav-icon" aria-hidden="true">
            <Ellipsis size={20} />
          </span>
          <span>Explore</span>
        </button>
      </Popover.Anchor>
      <Popover.Portal>
        <Popover.Content
          className="workspace-explore-menu"
          aria-label="Explore"
          side={mobile ? "top" : "right"}
          align={mobile ? "end" : "start"}
          sideOffset={8}
          collisionPadding={12}
          onPointerEnter={cancelTimer}
          onPointerLeave={scheduleClose}
          onFocusCapture={() => {
            cancelTimer();
            hoverOpened.current = false;
          }}
          onOpenAutoFocus={(event) => {
            if (hoverOpened.current) event.preventDefault();
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (!hoverOpened.current) trigger.current?.focus();
          }}
        >
          <nav aria-label="Explore tools">
            {groups.map(({ id, items }) => (
              <div key={id} className="workspace-explore-group">
                {items.map(({ label, to, icon: Icon }) => (
                  <NavLink key={to} to={to} onClick={() => setOpen(false)}>
                    <Icon size={20} aria-hidden="true" />
                    <span>{label}</span>
                  </NavLink>
                ))}
              </div>
            ))}
          </nav>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
