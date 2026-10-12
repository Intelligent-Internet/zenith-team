import { useEffect, useRef, useState } from "react";

import { TaskMemberAvatar } from "@/features/company-plan/components/task-assignee";
import {
  AccessChip,
  RunsChip,
} from "@/features/company-plan/projects/access/project-access-counts";
import { NOTHING_INHERITED } from "@/features/company-plan/projects/access/project-access-model";
import {
  AccessLabel,
  accessLabel,
} from "@/features/company-plan/projects/access/project-access-permission-icons";
import { RoleMark } from "@/features/company-plan/projects/access/project-access-row";
import {
  AccessLoading,
  LoadFailed,
} from "@/features/company-plan/projects/access/project-access-status";
import { accessErrorCopy } from "@/lib/api-error-copy";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type { CompanyRole } from "@/shared/permissions";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";
import {
  AlertCircle,
  ArrowDown,
  Bell,
  Eye,
  EyeOff,
  Folder,
  Minus,
  Plus,
  type IconComponent,
} from "@/shared/ui/icons";

import type { GroupChangePreview } from "../api/groups-api";
import {
  counted,
  firstAndMore,
  firstAndMoreText,
  personRows,
  type PersonChange,
  type PersonRow,
} from "../model/group-change";
// The confirm shell every access question is drawn with.
import "@/features/settings/styles/settings.css";
import "@/features/company-plan/projects/access/project-access.css";
import "../styles/groups.css";

/** How a preview names the Projects and people it lists. */
export type GroupReviewNames = {
  project: (projectId: string) => { title: string } | null;
  person: (userId: string) => {
    name: string;
    avatarUrl: string | null;
    isSelf: boolean;
    /** Their Company role: an owner or admin keeps managing every Project. */
    role?: CompanyRole;
  };
};

/**
 * One Group change to confirm, then save exactly as previewed. A change
 * asks only when `changeAsksFirst` says so; anything else saves straight
 * away and never gets here.
 */
export type GroupReview = {
  /** A short question: "Change Finance?". */
  title: string;
  /** A change of a Group's members, or one Project's Group grant. */
  scope: "group" | "project";
  /** The preview already read, shown as it is; `load` reads it again. */
  preview?: GroupChangePreview;
  load: () => Promise<GroupChangePreview>;
  /** Saves with the revision the preview answered. */
  save: (revision: number) => Promise<void>;
  /** The error codes that mean the preview went stale. */
  staleCodes: readonly string[];
  /** Said when it did: "Access changed", "Group changed". */
  staleCopy: string;
};

// The mark after a person's name: a plus when they gain, a neutral arrow
// when they keep every Project with less, a minus only when they lose
// one. The label is what a screen reader says there.
const CHANGE_SIGNS: Record<
  PersonChange,
  { Icon: IconComponent; label: string }
> = {
  gain: { Icon: Plus, label: "Gains" },
  lowered: { Icon: ArrowDown, label: "Lowered" },
  loss: { Icon: Minus, label: "Loses" },
};

type Phase =
  | { kind: "loading" }
  | { kind: "failed"; message: string }
  | { kind: "ready"; preview: GroupChangePreview }
  | { kind: "stale" };

/**
 * A preview row as the person reads it: an owner or admin who loses a
 * Project's content keeps managing it through their role, so their row is
 * lowered, never a loss.
 */
function shownChange(row: PersonRow, role?: CompanyRole): PersonChange {
  return row.change === "loss" && (role === "owner" || role === "admin")
    ? "lowered"
    : row.change;
}

// A Group change that asks first (Access DESIGN 6.3), as a confirm like
// every other access question: the question beside a red alert when
// someone loses a Project (else a calm pause), one compact row per person
// (avatar, name, a plus, a neutral arrow when they keep every Project with
// less or a minus when they lose one, the Projects it moves on with the
// label they end with, runs that stop), a mark when Projects the viewer
// can't see are left out, then Cancel and Change. Change saves exactly
// that change against the revision the preview answered. A save that lost
// a race says so and offers to review again; nothing is retried silently.
export function GroupChangeReview({
  names,
  review,
  onCancel,
  onSaved,
  onSavingChange,
}: {
  names: GroupReviewNames;
  review: GroupReview;
  /** Leaves without saving. */
  onCancel: () => void;
  onSaved: () => void;
  /** Told when a save starts and ends, so the box stays open meanwhile. */
  onSavingChange?: (saving: boolean) => void;
}) {
  const surfaceRef = useRef<HTMLDivElement>(null);
  const [phase, setPhase] = useState<Phase>(() =>
    review.preview
      ? { kind: "ready", preview: review.preview }
      : { kind: "loading" },
  );
  const [attempt, setAttempt] = useState(0);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  // Opens focused on itself, so no button starts with a focus ring.
  useEffect(() => {
    surfaceRef.current?.focus({ preventScroll: true });
  }, []);

  useEffect(() => {
    // The first preview came with the review; Review again reads anew.
    if (attempt === 0 && review.preview) return;
    let current = true;
    review.load().then(
      (preview) => {
        if (current) setPhase({ kind: "ready", preview });
      },
      (reason: unknown) => {
        if (!current) return;
        setPhase({
          kind: "failed",
          message:
            accessErrorCopy((reason as Partial<BoardlyApiError>).code) ??
            "Couldn't check change",
        });
      },
    );
    return () => {
      current = false;
    };
  }, [review, attempt]);

  const reload = () => {
    setSaveError(null);
    setPhase({ kind: "loading" });
    setAttempt((value) => value + 1);
  };
  const save = async (preview: GroupChangePreview) => {
    setSaving(true);
    onSavingChange?.(true);
    setSaveError(null);
    try {
      await review.save(preview.revision);
      onSavingChange?.(false);
      onSaved();
    } catch (reason) {
      const failure = reason as Partial<BoardlyApiError>;
      if (failure.code && review.staleCodes.includes(failure.code)) {
        setPhase({ kind: "stale" });
      } else {
        setSaveError(accessErrorCopy(failure.code) ?? "Couldn't save change");
      }
    } finally {
      setSaving(false);
      onSavingChange?.(false);
    }
  };

  const preview = phase.kind === "ready" ? phase.preview : null;
  // Only when nothing moves anywhere: a change that moves access only on
  // Projects the viewer can't see shows the hidden mark instead.
  const nothing =
    preview !== null &&
    preview.projects.length === 0 &&
    !preview.projectsHidden;
  const rows = (preview ? personRows(preview.projects) : []).map((row) => {
    const who = names.person(row.userId);
    return { row, who, change: shownChange(row, who.role) };
  });
  const losesProject = rows.some(({ change }) => change === "loss");
  const titles = (projectIds: readonly string[]) =>
    firstAndMore(
      projectIds.flatMap((projectId) => {
        const project = names.project(projectId);
        return project ? [project.title] : [];
      }),
      projectIds.length,
    );
  const opened = preview ? titles(preview.openedForCaller) : null;

  return (
    <div
      ref={surfaceRef}
      className="profile-delete-dialog__surface group-review"
      tabIndex={-1}
    >
      <DialogHeader className="profile-delete-dialog__header">
        <span
          className="profile-delete-dialog__icon"
          data-tone={losesProject ? "danger" : "neutral"}
        >
          <Icon name={losesProject ? "alert" : "hold"} />
        </span>
        <DialogTitle>{review.title}</DialogTitle>
        {preview?.projectsHidden ? (
          <div className="group-review__summary">
            <AccessChip
              icon={EyeOff}
              label="Some projects hidden"
              tooltip="Some projects hidden"
            />
          </div>
        ) : nothing ? (
          <p className="group-review__summary">No access changes</p>
        ) : null}
      </DialogHeader>
      <div className="group-review__body">
        {phase.kind === "loading" ? <AccessLoading label="Checking" /> : null}
        {phase.kind === "failed" ? (
          <LoadFailed message={phase.message} onRetry={reload} />
        ) : null}
        {phase.kind === "stale" ? (
          <p className="group-review__warning" role="alert">
            <AlertCircle size={16} aria-hidden="true" />
            {review.staleCopy}
          </p>
        ) : null}
        {rows.length > 0 ? (
          <ul className="group-review__people" aria-label="People">
            {rows.map(({ row, who, change }) => {
              const where = titles(row.projectIds);
              const sign = CHANGE_SIGNS[change];
              // What they end with there, as the list labels it ("View
              // +7"); an owner or admin left with nothing there keeps
              // their role mark instead; nothing when they lose it.
              const keepsManaging = row.after.length === 0 && change !== "loss";
              return (
                <li
                  key={`${row.userId}:${row.change}`}
                  className="group-review__person"
                  data-change={change}
                >
                  <TaskMemberAvatar
                    member={{ name: who.name, avatarUrl: who.avatarUrl }}
                  />
                  <span className="group-review__name">
                    <span className="group-review__name-text">{who.name}</span>
                    {who.isSelf ? <small>you</small> : null}
                  </span>
                  <span className="group-review__sign">
                    <sign.Icon size={13} aria-hidden="true" />
                    <span className="sr-only">{sign.label}</span>
                  </span>
                  <span className="group-review__where">
                    <Folder size={13} aria-hidden="true" />
                    <span className="group-review__project">
                      {where ? firstAndMoreText(where) : null}
                    </span>
                    {row.after.length > 0 ? (
                      <span
                        aria-label={accessLabel(row.after)}
                        className="group-review__level"
                        role="img"
                      >
                        <AccessLabel
                          inherited={NOTHING_INHERITED}
                          own={row.after}
                        />
                      </span>
                    ) : keepsManaging ? (
                      <RoleMark focusable={false} role={who.role} />
                    ) : null}
                  </span>
                  {row.runsStopping > 0 ? (
                    <RunsChip count={row.runsStopping} />
                  ) : (
                    <span aria-hidden="true" />
                  )}
                </li>
              );
            })}
          </ul>
        ) : null}
        {opened ? (
          <div className="group-review__opened" role="note">
            <AccessChip
              icon={Eye}
              label={`Opens for you: ${
                opened.first
                  ? firstAndMoreText(opened)
                  : counted(opened.more, "project", "projects")
              }`}
              tone="warn"
              tooltip="Opens for you"
            >
              {firstAndMoreText(opened)}
            </AccessChip>
            {review.scope === "group" ? (
              <AccessChip
                icon={Bell}
                label="Managers notified"
                tone="warn"
                tooltip="Managers notified"
              />
            ) : null}
          </div>
        ) : null}
        {saveError ? (
          <p className="project-access-notice" data-tone="error" role="alert">
            {saveError}
          </p>
        ) : null}
      </div>
      <DialogFooter className="profile-delete-dialog__footer">
        <button
          className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
          disabled={saving}
          type="button"
          onClick={onCancel}
        >
          Cancel
        </button>
        {phase.kind === "stale" ? (
          <button
            className="profile-delete-dialog__button runtime-dialog__submit"
            type="button"
            onClick={reload}
          >
            Review again
          </button>
        ) : (
          <button
            className={
              losesProject
                ? "profile-delete-dialog__button profile-delete-dialog__button--delete"
                : "profile-delete-dialog__button runtime-dialog__submit"
            }
            disabled={!preview || saving}
            type="button"
            onClick={() => {
              if (preview) void save(preview);
            }}
          >
            {saving ? "Saving…" : "Change"}
          </button>
        )}
      </DialogFooter>
    </div>
  );
}

// Each review gets its own key, so the next one in a queue starts afresh.
const reviewKeys = new WeakMap<GroupReview, number>();
let nextReviewKey = 0;
function reviewKey(review: GroupReview): number {
  let key = reviewKeys.get(review);
  if (key === undefined) {
    nextReviewKey += 1;
    key = nextReviewKey;
    reviewKeys.set(review, key);
  }
  return key;
}

// The confirm on its own, in the compact box of every access question,
// with no close X: Cancel and Escape leave everything as it was, and
// neither closes it while a save runs. On the Project access page it
// stands alone; in the Group dialog it opens over the form, so Cancel goes
// back to the edits and a save closes both.
export function GroupChangeReviewDialog({
  names,
  review,
  onClose,
  onSaved,
}: {
  names: GroupReviewNames;
  review: GroupReview | null;
  onClose: () => void;
  /**
   * Runs after a save instead of `onClose` (the default); it must close
   * this dialog itself, as the Group dialog does by closing with it.
   */
  onSaved?: () => void;
}) {
  const [saving, setSaving] = useState(false);
  return (
    <Dialog
      open={review !== null}
      onOpenChange={(next) => {
        if (!next && !saving) onClose();
      }}
    >
      <DialogContent
        aria-describedby={undefined}
        className="profile-delete-dialog project-access-confirm group-review-confirm"
        overlayClassName="profile-delete-dialog__overlay group-review-confirm__overlay"
        showCloseButton={false}
        onOpenAutoFocus={(event) => event.preventDefault()}
      >
        {review ? (
          <GroupChangeReview
            key={reviewKey(review)}
            names={names}
            review={review}
            onCancel={onClose}
            onSaved={onSaved ?? onClose}
            onSavingChange={setSaving}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
