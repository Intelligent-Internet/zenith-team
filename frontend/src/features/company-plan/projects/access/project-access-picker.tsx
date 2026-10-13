import * as Popover from "@radix-ui/react-popover";
import { useRef, useState, type ReactElement } from "react";

import { PROJECT_GRANT_EXCEEDS_OWN_COPY } from "@/lib/api-error-copy";
import {
  PROJECT_PERMISSION_GROUPS,
  PROJECT_PERMISSION_HELP,
  PROJECT_PERMISSION_LABELS,
  type ProjectPermission,
  type ProjectVisibility,
} from "@/shared/permissions";
import {
  Building2,
  Check,
  ChevronDown,
  LockKeyhole,
  LogOut,
  UserMinus,
  UsersRound,
} from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";

import {
  PROJECT_ACCESS_LEVELS,
  accessLevelFor,
  levelBlockedReason,
} from "./project-access-levels";
import {
  inheritedNote,
  readLocked,
  samePermissions,
  togglePermission,
  withinGiver,
  type AccessGiver,
  type InheritedAccess,
} from "./project-access-model";
import {
  ACCESS_TOOLTIP_CLASS,
  AccessLabel,
  PROJECT_ACCESS_LEVEL_ICONS,
  PROJECT_PERMISSION_ICONS,
  accessName,
  heldPermissions,
} from "./project-access-permission-icons";

const READ_NEEDED = "Needed for others";

// The picker's size at desktop sizes (project-access.css): 400 px wide, and
// about 460 px tall with all nine rows and the footer; in Add, with no
// footer, about 400 px.
const PICKER_WIDTH = 400;
const PICKER_HEIGHT = 470;
const DRAFT_PICKER_HEIGHT = 410;
// The gap to the label, and the margin kept from the window's edges.
const GAP = 6;
const EDGE = 12;

type PickerPlacement = {
  side: "top" | "right" | "bottom" | "left";
  align: "start" | "center" | "end";
  sideOffset: number;
  /** The margin kept from each edge of the window as it slides or flips. */
  collisionPadding:
    number | { top: number; right: number; bottom: number; left: number };
};

const BELOW: PickerPlacement = {
  side: "bottom",
  align: "end",
  sideOffset: GAP,
  collisionPadding: EDGE,
};

/**
 * Where a row's picker opens so all nine rows show: under the label, or
 * over it, when the whole picker fits there; otherwise beside it, where
 * the picker slides up or down within the window's full height. Only when
 * neither fits does it open where there is more room and scroll its list.
 */
function rowPlacement(label: DOMRect): PickerPlacement {
  const below = window.innerHeight - label.bottom - GAP - EDGE;
  const above = label.top - GAP - EDGE;
  if (below >= PICKER_HEIGHT) return BELOW;
  if (above >= PICKER_HEIGHT) return { ...BELOW, side: "top" };
  if (label.left - GAP - EDGE >= PICKER_WIDTH) {
    return { ...BELOW, side: "left", align: "center" };
  }
  if (window.innerWidth - label.right - GAP - EDGE >= PICKER_WIDTH) {
    return { ...BELOW, side: "right", align: "center" };
  }
  return below >= above ? BELOW : { ...BELOW, side: "top" };
}

/**
 * Where a picker in Add opens: beside the dialog, clear of its edge, so it
 * covers none of it (a window about 1400 px wide or more). Otherwise beside
 * the label, over the dialog's list, sliding up or down in the space above
 * Cancel and Add, so it never covers them. It stays below the title when
 * the space under the title holds the whole picker, else covers as little
 * of the title as it can. Only when the space above the footer is too
 * short does its list scroll.
 */
function draftPlacement(
  label: DOMRect,
  dialog: Element | null,
): PickerPlacement {
  const beside = { ...BELOW, align: "start" as const };
  if (!dialog) return { ...beside, side: "right" };
  const box = dialog.getBoundingClientRect();
  if (window.innerWidth - box.right - GAP - EDGE >= PICKER_WIDTH) {
    return {
      ...beside,
      side: "right",
      sideOffset: box.right - label.right + GAP,
    };
  }
  if (box.left - GAP - EDGE >= PICKER_WIDTH) {
    return {
      ...beside,
      side: "left",
      sideOffset: label.left - box.left + GAP,
    };
  }
  const footerTop =
    dialog.querySelector('[data-slot="dialog-footer"]')?.getBoundingClientRect()
      .top ?? window.innerHeight;
  const titleBottom =
    dialog.querySelector('[data-slot="dialog-header"]')?.getBoundingClientRect()
      .bottom ?? 0;
  // Under the title when there is room; else as low as the whole picker
  // can start and still end above the footer.
  const top = Math.max(
    EDGE,
    Math.min(titleBottom + GAP, footerTop - GAP - DRAFT_PICKER_HEIGHT),
  );
  return {
    side: "left",
    align: "center",
    sideOffset: GAP,
    collisionPadding: {
      top,
      right: EDGE,
      bottom: Math.max(EDGE, window.innerHeight - footerTop + GAP),
      left: EDGE,
    },
  };
}

/** A picker that saves: the row's own permissions, then Save. */
export type PickerSaveMode = {
  kind: "save";
  /**
   * Saves the selection. Resolves true once the picker may close: saved,
   * or handed to a confirmation or a review.
   */
  onSave: (permissions: ProjectPermission[]) => Promise<boolean>;
  /** Removes their access; left out when there is nothing of their own. */
  onRemove?: () => void;
  /** Why Remove access is not offered here, said on the button. */
  removeBlocked?: string | null;
  /** The row is the viewer's own: Remove access reads "Leave". */
  self?: boolean;
};

/** A picker inside a form that saves later (Add): every click applies. */
export type PickerDraftMode = {
  kind: "draft";
  onChange: (permissions: ProjectPermission[]) => void;
};

// What a person or Group can do on one Project, as one short label
// ("View +1") that opens a popover (Access DESIGN 7.0.1; a bottom sheet
// when narrow): four presets on top, then every permission as a compact
// row with its icon, name and a check, in three unlabelled groups split
// by a hairline. The picker is the only place that lists the permissions.
// View is needed for everything else, so it stays checked (greyed) while
// anything else is. What a Group or the Company floor gives shows checked
// and locked with where it comes from; what the viewer may not give (the
// self-grant cap) shows a lock in place of its check box.
export function ProjectAccessPicker({
  disabled = false,
  giver,
  inherited,
  mode,
  own,
  subjectName,
  visibility,
}: {
  disabled?: boolean;
  /** The viewer's own standing, for the self-grant cap. */
  giver: AccessGiver;
  /** What they hold from Groups or everyone, beside their own rows. */
  inherited: InheritedAccess;
  mode: PickerSaveMode | PickerDraftMode;
  /** Their own stored permissions (or the draft, in a form). */
  own: readonly ProjectPermission[];
  subjectName: string;
  visibility: ProjectVisibility;
}) {
  const [open, setOpen] = useState(false);
  const [placement, setPlacement] = useState<PickerPlacement>(BELOW);
  const contentRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  // Placed once as it opens, from where the label and the Add dialog sit.
  const toggle = (next: boolean) => {
    const label = triggerRef.current?.getBoundingClientRect();
    if (next && label) {
      setPlacement(
        mode.kind === "draft"
          ? draftPlacement(
              label,
              triggerRef.current?.closest('[data-slot="dialog-content"]') ??
                null,
            )
          : rowPlacement(label),
      );
    }
    setOpen(next);
  };
  return (
    <Popover.Root open={open} onOpenChange={toggle}>
      <Popover.Trigger asChild>
        <button
          ref={triggerRef}
          aria-label={accessName(subjectName, own, inherited)}
          className="project-access-perms"
          disabled={disabled}
          type="button"
        >
          <AccessLabel inherited={inherited} own={own} />
          <ChevronDown
            aria-hidden="true"
            className="flex-none text-workspace-muted"
            size={12}
          />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          ref={contentRef}
          aria-label={`Permissions for ${subjectName}`}
          align={placement.align}
          className="project-access-picker"
          collisionPadding={placement.collisionPadding}
          side={placement.side}
          sideOffset={placement.sideOffset}
          // Focus the picker itself, not its first preset: a focused
          // blocked preset opens its tooltip over the list.
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            contentRef.current?.focus({ preventScroll: true });
          }}
        >
          {mode.kind === "draft" ? (
            <PermissionChecklist
              draft={own}
              giver={giver}
              inherited={inherited}
              visibility={visibility}
              onChange={mode.onChange}
            />
          ) : (
            <SavingPicker
              giver={giver}
              inherited={inherited}
              mode={mode}
              own={own}
              visibility={visibility}
              onClose={() => setOpen(false)}
            />
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

// The picker on a row: a draft of their own permissions until Save.
function SavingPicker({
  giver,
  inherited,
  mode,
  own,
  visibility,
  onClose,
}: {
  giver: AccessGiver;
  inherited: InheritedAccess;
  mode: PickerSaveMode;
  own: readonly ProjectPermission[];
  visibility: ProjectVisibility;
  onClose: () => void;
}) {
  const [draft, setDraft] = useState<ProjectPermission[]>(() => [...own]);
  const [busy, setBusy] = useState(false);
  const changed = draft.length > 0 && !samePermissions(draft, own);
  const { onRemove, removeBlocked, self = false } = mode;
  const RemoveIcon = self ? LogOut : UserMinus;
  return (
    <>
      <PermissionChecklist
        disabled={busy}
        draft={draft}
        giver={giver}
        inherited={inherited}
        visibility={visibility}
        onChange={setDraft}
      />
      <div className="flex items-center gap-[8px] p-[10px_12px] [border-top:1px_solid_var(--workspace-divider)]">
        {onRemove ? (
          <MaybeTooltip label={removeBlocked ?? null}>
            <button
              aria-disabled={removeBlocked ? true : undefined}
              className="project-access-picker__remove"
              disabled={busy}
              type="button"
              onClick={() => {
                if (removeBlocked) return;
                onClose();
                onRemove();
              }}
            >
              <RemoveIcon aria-hidden="true" size={15} />
              {self ? "Leave" : "Remove access"}
            </button>
          </MaybeTooltip>
        ) : null}
        <button
          className="dashboard-button dashboard-button-primary ml-auto"
          disabled={busy || !changed}
          type="button"
          onClick={() => {
            setBusy(true);
            void mode.onSave(draft).then(
              (close) => {
                setBusy(false);
                if (close) onClose();
              },
              () => setBusy(false),
            );
          }}
        >
          {busy ? "Saving…" : "Save"}
        </button>
      </div>
    </>
  );
}

function PermissionChecklist({
  disabled = false,
  draft,
  giver,
  inherited,
  visibility,
  onChange,
}: {
  disabled?: boolean;
  draft: readonly ProjectPermission[];
  giver: AccessGiver;
  inherited: InheritedAccess;
  visibility: ProjectVisibility;
  onChange: (permissions: ProjectPermission[]) => void;
}) {
  const current = accessLevelFor(heldPermissions(draft, inherited));
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto overscroll-contain">
      <div
        aria-label="Presets"
        className="sticky z-[1] top-0 grid gap-[4px] grid-cols-[repeat(4,_minmax(0,_1fr))] p-[8px] [border-bottom:1px_solid_var(--workspace-divider)] [background:var(--workspace-overlay-bg)]"
        role="group"
      >
        {PROJECT_ACCESS_LEVELS.map((level) => {
          const Icon = PROJECT_ACCESS_LEVEL_ICONS[level.id];
          const blocked = levelBlockedReason(level, visibility, giver);
          const chosen = current?.id === level.id;
          // Only a preset that can't be picked has a tooltip: why.
          return (
            <MaybeTooltip key={level.id} label={blocked}>
              <button
                aria-disabled={blocked ? true : undefined}
                aria-label={`${level.shortLabel}: ${blocked ?? level.description}`}
                aria-pressed={chosen}
                className="project-access-preset"
                disabled={disabled}
                type="button"
                onClick={() => {
                  if (blocked || chosen) return;
                  onChange([...level.permissions]);
                }}
              >
                <Icon aria-hidden="true" size={16} />
                <span>{level.shortLabel}</span>
              </button>
            </MaybeTooltip>
          );
        })}
      </div>
      <div className="flex flex-col p-[4px_6px_6px]">
        {PROJECT_PERMISSION_GROUPS.map((group) => (
          <div
            key={group.label}
            aria-label={group.label}
            className="project-access-picker__group flex flex-col"
            role="group"
          >
            {group.permissions.map((permission) => (
              <PermissionOption
                key={permission}
                disabled={disabled}
                draft={draft}
                giver={giver}
                inherited={inherited}
                permission={permission}
                onChange={onChange}
              />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

function PermissionOption({
  disabled,
  draft,
  giver,
  inherited,
  permission,
  onChange,
}: {
  disabled: boolean;
  draft: readonly ProjectPermission[];
  giver: AccessGiver;
  inherited: InheritedAccess;
  permission: ProjectPermission;
  onChange: (permissions: ProjectPermission[]) => void;
}) {
  const Icon = PROJECT_PERMISSION_ICONS[permission];
  const mine = draft.includes(permission);
  const from = mine ? undefined : inherited.get(permission);
  const readNeeded = mine && permission === "project.read" && readLocked(draft);
  const capped = !mine && !from && !withinGiver([permission], giver);
  const locked = readNeeded || Boolean(from) || capped;
  const checked = mine || Boolean(from);
  // What the permission does, or where it comes from, for screen readers
  // only; on screen the grey tick says View is needed, the Group mark
  // where it comes from, and the lock that it is beyond the viewer.
  const note = from ? inheritedNote(from) : PROJECT_PERMISSION_HELP[permission];
  // The marker column is always there, so every check lines up. The
  // tooltips open to the side, never over the next row.
  const marker = from ? (
    <Tooltip
      contentClassName={ACCESS_TOOLTIP_CLASS}
      label={inheritedNote(from)}
      side="right"
    >
      <span className="inline-grid place-items-center text-workspace-muted">
        {from.via === "everyone" ? (
          <Building2 aria-hidden="true" size={13} />
        ) : (
          <UsersRound aria-hidden="true" size={13} />
        )}
      </span>
    </Tooltip>
  ) : (
    <span
      aria-hidden="true"
      className="inline-grid place-items-center text-workspace-muted"
    />
  );
  // Beyond the viewer: a lock where the check box would be.
  const check = capped ? (
    <Tooltip
      contentClassName={ACCESS_TOOLTIP_CLASS}
      label={PROJECT_GRANT_EXCEEDS_OWN_COPY}
      side="right"
    >
      <span className="project-access-option__check" data-capped="">
        <LockKeyhole aria-hidden="true" size={12} />
      </span>
    </Tooltip>
  ) : (
    <span
      aria-hidden="true"
      className="project-access-option__check"
      data-on={checked ? "" : undefined}
    >
      {checked ? <Check size={12} /> : null}
    </span>
  );
  return (
    <button
      aria-checked={checked}
      aria-description={
        readNeeded
          ? READ_NEEDED
          : capped
            ? PROJECT_GRANT_EXCEEDS_OWN_COPY
            : note
      }
      aria-label={PROJECT_PERMISSION_LABELS[permission]}
      aria-disabled={locked ? true : undefined}
      className="project-access-option"
      data-locked={locked ? "" : undefined}
      disabled={disabled}
      role="checkbox"
      type="button"
      onClick={() => {
        if (locked) return;
        onChange(togglePermission(draft, permission, !mine));
      }}
    >
      <span
        className="project-access-option__icon"
        data-on={checked ? "" : undefined}
      >
        <Icon aria-hidden="true" size={15} />
      </span>
      <span className="project-access-option__text">
        <strong>{PROJECT_PERMISSION_LABELS[permission]}</strong>
      </span>
      {marker}
      {check}
    </button>
  );
}

function MaybeTooltip({
  children,
  label,
}: {
  children: ReactElement;
  label: string | null;
}) {
  if (!label) return children;
  return (
    <Tooltip contentClassName={ACCESS_TOOLTIP_CLASS} label={label}>
      {children}
    </Tooltip>
  );
}
