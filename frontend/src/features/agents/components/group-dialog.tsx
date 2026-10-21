import { useId, useMemo, useState } from "react";

import { NOTHING_INHERITED } from "@/features/company-plan/projects/access/project-access-model";
import {
  ACCESS_TOOLTIP_CLASS,
  StaticPermissions,
} from "@/features/company-plan/projects/access/project-access-permission-icons";
import {
  AccessLoading,
  LoadFailed,
} from "@/features/company-plan/projects/access/project-access-status";
import { TaskMemberAvatar } from "@/features/company-plan/components/task-assignee";
import { GROUP_CONFLICT_COPY, accessErrorCopy } from "@/lib/api-error-copy";
import type { ZenithTeamApiError } from "@/shared/api/zenith-team-api";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Folder, Search, UserPlus, Users, X } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import { Tooltip } from "@/shared/ui/tooltip";

import {
  useChangeGroupMembersMutation,
  useCreateGroupMutation,
  useGetGroupQuery,
  usePreviewGroupChangeMutation,
  useUpdateGroupMutation,
  type GroupSummary,
} from "../api/groups-api";
import type { ActiveCompanyMember } from "../api/team-directory-api";
import { changeAsksFirst } from "../model/group-change";
import {
  GroupChangeReviewDialog,
  type GroupReview,
  type GroupReviewNames,
} from "./group-change-review";
import "@/features/company-plan/projects/access/project-access.css";
import "../styles/groups.css";

const NAME_MAX = 80;
const DESCRIPTION_MAX = 500;
const NAME_TAKEN = "Name already taken";

// New group, or Edit group: a name, a description and
// the people in it, then the Projects it can see when it has any. The
// title never names the Group; its Name field does. Only
// Team members are offered; Agents never are. A new Group reaches no
// Project, so it is created straight away. Adding or removing people on
// an existing Group reads the server's preview first: it saves straight
// away unless the change asks (runs stop, someone loses a Project, or it
// moves Projects the viewer can't see), and only then asks "Change
// {Group}?" over the form and saves exactly what it showed. Cancel there
// goes back to the edits. The viewer is marked "you" among the people.
export function GroupDialog({
  companyId,
  group,
  groups,
  members,
  names,
  onClose,
  onCreated,
}: {
  companyId: string;
  /** The Group to edit, or null for a new one. */
  group: GroupSummary | null;
  /** Every Group of the Company, to catch a name already taken. */
  groups: readonly GroupSummary[];
  members: readonly ActiveCompanyMember[];
  names: GroupReviewNames;
  onClose: () => void;
  /** A new Group was created (before the dialog closes). */
  onCreated?: (group: { id: string; name: string }) => void;
}) {
  const [busy, setBusy] = useState(false);
  // The people picker, closed until Add people opens it: Escape there
  // closes only the picker, never the dialog with what was typed.
  const [adding, setAdding] = useState(false);
  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !busy) onClose();
      }}
    >
      <DialogContent
        aria-describedby={undefined}
        className="project-access-dialog group-dialog"
        overlayClassName="project-access-dialog__overlay"
        onEscapeKeyDown={(event) => {
          if (!adding) return;
          event.preventDefault();
          setAdding(false);
        }}
      >
        <GroupForm
          adding={adding}
          companyId={companyId}
          group={group}
          groups={groups}
          members={members}
          names={names}
          onAdding={setAdding}
          onBusy={setBusy}
          onClose={onClose}
          onCreated={onCreated}
        />
      </DialogContent>
    </Dialog>
  );
}

function GroupForm({
  adding,
  companyId,
  group,
  groups,
  members,
  names,
  onAdding: setAdding,
  onBusy,
  onClose,
  onCreated,
}: {
  adding: boolean;
  companyId: string;
  group: GroupSummary | null;
  groups: readonly GroupSummary[];
  members: readonly ActiveCompanyMember[];
  names: GroupReviewNames;
  onAdding: (adding: boolean | ((current: boolean) => boolean)) => void;
  onBusy: (busy: boolean) => void;
  onClose: () => void;
  onCreated?: (group: { id: string; name: string }) => void;
}) {
  const nameId = useId();
  const descriptionId = useId();
  const errorId = useId();
  const detail = useGetGroupQuery(
    { companyId, id: group?.id ?? "" },
    { skip: !group, refetchOnMountOrArgChange: true },
  );
  const [createGroup] = useCreateGroupMutation();
  const [updateGroup] = useUpdateGroupMutation();
  const [changeMembers] = useChangeGroupMembersMutation();
  const [previewChange] = usePreviewGroupChangeMutation();
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  // The people as edited; null until someone is added or removed, so the
  // list follows the Group as loaded until then.
  const [draft, setDraft] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [review, setReview] = useState<GroupReview | null>(null);

  const loaded = useMemo(
    () => detail.currentData?.members.map((member) => member.userId) ?? null,
    [detail.currentData],
  );
  const people = draft ?? loaded ?? [];
  const memberById = useMemo(
    () => new Map(members.map((member) => [member.actor.id, member])),
    [members],
  );
  const before = new Set(loaded ?? []);
  const addUserIds = people.filter((userId) => !before.has(userId));
  const removeUserIds = (loaded ?? []).filter(
    (userId) => !people.includes(userId),
  );
  const peopleChanged = addUserIds.length > 0 || removeUserIds.length > 0;
  const trimmedName = name.trim();
  const trimmedDescription = description.trim();
  const detailsChanged =
    group !== null &&
    (trimmedName !== group.name || trimmedDescription !== group.description);
  const nameTaken = groups.some(
    (other) =>
      other.id !== group?.id &&
      other.name.toLocaleLowerCase() === trimmedName.toLocaleLowerCase(),
  );
  const nameError =
    trimmedName.length === 0
      ? null
      : nameTaken
        ? NAME_TAKEN
        : trimmedName.length > NAME_MAX
          ? `Too long (max ${NAME_MAX})`
          : null;
  const descriptionError =
    trimmedDescription.length > DESCRIPTION_MAX
      ? `Too long (max ${DESCRIPTION_MAX})`
      : null;
  const loading = group !== null && !detail.currentData;
  const invalid =
    trimmedName.length === 0 || nameError !== null || descriptionError !== null;
  const unchanged = group !== null && !peopleChanged && !detailsChanged;

  const working = (value: boolean) => {
    setBusy(value);
    onBusy(value);
  };
  const failed = (reason: unknown, fallback: string) =>
    setFormError(
      accessErrorCopy((reason as Partial<ZenithTeamApiError>).code) ?? fallback,
    );

  const saveDetails = async (id: string, revision: number) => {
    if (!detailsChanged) return;
    await updateGroup({
      companyId,
      id,
      expectedRevision: revision,
      ...(trimmedName === group?.name ? {} : { name: trimmedName }),
      ...(trimmedDescription === group?.description
        ? {}
        : { description: trimmedDescription }),
    }).unwrap();
  };

  const create = async () => {
    working(true);
    setFormError(null);
    try {
      const created = await createGroup({
        companyId,
        name: trimmedName,
        description: trimmedDescription,
      }).unwrap();
      if (people.length > 0) {
        try {
          await changeMembers({
            companyId,
            id: created.id,
            addUserIds: people,
            removeUserIds: [],
            expectedRevision: created.revision,
          }).unwrap();
        } catch {
          toast.error("Created, but people not added");
          onCreated?.(created);
          onClose();
          return;
        }
      }
      onCreated?.(created);
      onClose();
    } catch (reason) {
      const failure = reason as Partial<ZenithTeamApiError>;
      setFormError(
        failure.code === "group_conflict"
          ? NAME_TAKEN
          : (accessErrorCopy(failure.code) ?? "Couldn't create group"),
      );
    } finally {
      working(false);
    }
  };

  // The people, then the name and description, against the Group's
  // revision the preview read.
  const saveChange = async (id: string, revision: number) => {
    const changed = await changeMembers({
      companyId,
      id,
      addUserIds,
      removeUserIds,
      expectedRevision: revision,
    }).unwrap();
    // The people are saved now. A name or description that fails after
    // them is said once and not retried: saving again would resend a
    // change of people that already happened.
    try {
      await saveDetails(id, changed.revision);
    } catch {
      toast.error("People saved, details not saved");
    }
  };

  const save = async () => {
    const current = detail.currentData;
    if (!group || !current) return;
    working(true);
    setFormError(null);
    try {
      if (peopleChanged) {
        const load = () =>
          previewChange({
            companyId,
            id: group.id,
            addUserIds,
            removeUserIds,
          }).unwrap();
        const preview = await load();
        if (changeAsksFirst(preview)) {
          setReview({
            title: `Change ${trimmedName || group.name}?`,
            scope: "group",
            preview,
            load,
            save: (revision) => saveChange(group.id, revision),
            staleCodes: ["group_conflict"],
            staleCopy: "Group changed",
          });
          return;
        }
        await saveChange(group.id, preview.revision);
      } else {
        await saveDetails(group.id, current.revision);
      }
      onClose();
    } catch (reason) {
      const failure = reason as Partial<ZenithTeamApiError>;
      if (failure.code === "group_conflict") {
        setFormError(GROUP_CONFLICT_COPY);
        void detail.refetch();
      } else failed(reason, "Couldn't save group");
    } finally {
      working(false);
    }
  };

  const groupProjects = detail.currentData?.projects ?? group?.projects ?? [];
  // The confirm of a change that asks opens over the form, beside it and
  // not in it, so Cancel there goes back to the edits.
  return (
    <>
      <form
        aria-describedby={formError ? errorId : undefined}
        className="flex min-h-0 flex-1 flex-col"
        onSubmit={(event) => {
          event.preventDefault();
          if (busy || invalid || loading || unchanged) return;
          void (group ? save() : create());
        }}
      >
        <DialogHeader className="project-access-dialog__header">
          <DialogTitle>{group ? "Edit group" : "New group"}</DialogTitle>
        </DialogHeader>
        <div className="project-access-dialog__body">
          <div className="flex flex-col gap-[6px]">
            <label className="sr-only" htmlFor={nameId}>
              Name
            </label>
            <input
              aria-invalid={nameError ? true : undefined}
              autoComplete="off"
              autoFocus
              className="group-dialog__input"
              id={nameId}
              maxLength={NAME_MAX + 20}
              placeholder="Name"
              required
              value={name}
              onChange={(event) => setName(event.currentTarget.value)}
            />
            {nameError ? (
              <small
                className="text-[var(--workspace-native-danger,_#ff8a7a)] text-[length:11.5px] leading-[1.35]"
                role="alert"
              >
                {nameError}
              </small>
            ) : null}
          </div>
          <div className="flex flex-col gap-[6px]">
            <label className="sr-only" htmlFor={descriptionId}>
              Description
            </label>
            <textarea
              aria-invalid={descriptionError ? true : undefined}
              className="group-dialog__input"
              id={descriptionId}
              placeholder="Description"
              rows={2}
              value={description}
              onChange={(event) => setDescription(event.currentTarget.value)}
            />
            {descriptionError ? (
              <small
                className="text-[var(--workspace-native-danger,_#ff8a7a)] text-[length:11.5px] leading-[1.35]"
                role="alert"
              >
                {descriptionError}
              </small>
            ) : null}
          </div>
          <section className="flex flex-col gap-[8px]" aria-label="People">
            <div className="group-dialog__section-heading flex min-h-[30px] items-center justify-start gap-[8px]">
              <Users aria-hidden="true" size={15} />
              <Tooltip
                contentClassName={ACCESS_TOOLTIP_CLASS}
                label="Add people"
              >
                <button
                  aria-expanded={adding}
                  aria-label="Add people"
                  className="dashboard-button group-dialog__icon-button"
                  disabled={busy || loading}
                  type="button"
                  onClick={() => setAdding((value) => !value)}
                >
                  <UserPlus size={15} aria-hidden="true" />
                </button>
              </Tooltip>
              {!loading && !adding && people.length === 0 ? (
                <span className="m-0 text-workspace-muted text-[length:12px] leading-[1.4]">
                  Nobody yet
                </span>
              ) : null}
            </div>
            {adding ? (
              <PeoplePicker
                members={members}
                picked={people}
                onPick={(userId) => setDraft([...people, userId])}
              />
            ) : null}
            {loading ? (
              detail.isError ? (
                <LoadFailed
                  message="Couldn't load group"
                  onRetry={() => void detail.refetch()}
                />
              ) : (
                <AccessLoading />
              )
            ) : people.length === 0 ? null : (
              <ul className="group-dialog__people">
                {people.map((userId) => {
                  const member = memberById.get(userId);
                  const personName = member?.actor.name ?? "Former member";
                  const self = names.person(userId).isSelf;
                  return (
                    <li key={userId} className="group-dialog__person">
                      <TaskMemberAvatar
                        member={{
                          name: personName,
                          avatarUrl: member?.actor.avatarUrl ?? null,
                        }}
                      />
                      <span className="project-access-person__text">
                        <span className="group-dialog__name">
                          <strong>{personName}</strong>
                          {self ? <small>you</small> : null}
                        </span>
                        {member?.email ? <small>{member.email}</small> : null}
                      </span>
                      <Tooltip
                        contentClassName={ACCESS_TOOLTIP_CLASS}
                        label="Remove"
                      >
                        <button
                          aria-label={`Remove ${personName}`}
                          className="group-dialog__remove"
                          disabled={busy}
                          type="button"
                          onClick={() =>
                            setDraft(people.filter((other) => other !== userId))
                          }
                        >
                          <X size={14} aria-hidden="true" />
                        </button>
                      </Tooltip>
                    </li>
                  );
                })}
              </ul>
            )}
          </section>
          {group && groupProjects.length > 0 ? (
            <section className="flex flex-col gap-[8px]" aria-label="Projects">
              <ul className="group-dialog__projects">
                {groupProjects.map((entry) => {
                  const project = names.project(entry.projectId);
                  const title = project?.title ?? "A project";
                  return (
                    <li key={entry.projectId}>
                      <Folder size={15} aria-hidden="true" />
                      <span>{title}</span>
                      <StaticPermissions
                        inherited={NOTHING_INHERITED}
                        own={entry.permissions}
                        subjectName={title}
                      />
                    </li>
                  );
                })}
              </ul>
            </section>
          ) : null}
          {formError ? (
            <p
              className="project-access-notice"
              data-tone="error"
              id={errorId}
              role="alert"
            >
              {formError}
            </p>
          ) : null}
        </div>
        <DialogFooter className="project-access-dialog__footer">
          <span className="project-access-dialog__match" />
          <button
            className="dashboard-button"
            disabled={busy}
            type="button"
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            className="dashboard-button dashboard-button-primary"
            disabled={busy || invalid || loading || unchanged}
            type="submit"
          >
            {busy ? "Saving…" : group ? "Save" : "Create"}
          </button>
        </DialogFooter>
      </form>
      <GroupChangeReviewDialog
        names={names}
        review={review}
        onClose={() => setReview(null)}
        onSaved={onClose}
      />
    </>
  );
}

// Team members not in the group yet, with a search (the Add people
// pattern). Picking someone adds them to the draft straight away.
function PeoplePicker({
  members,
  picked,
  onPick,
}: {
  members: readonly ActiveCompanyMember[];
  picked: readonly string[];
  onPick: (userId: string) => void;
}) {
  const [query, setQuery] = useState("");
  const candidates = members.filter(
    (member) => !member.accountDisabled && !picked.includes(member.actor.id),
  );
  const needle = query.trim().toLocaleLowerCase();
  const shown = candidates.filter((member) =>
    `${member.actor.name} ${member.email}`.toLocaleLowerCase().includes(needle),
  );
  return (
    <div className="group-dialog__picker flex flex-col gap-[8px] p-[10px] rounded-[12px] [background:var(--workspace-control-bg)]">
      <label className="project-access-search">
        <Search size={14} aria-hidden="true" />
        <input
          aria-label="Search people"
          autoFocus
          placeholder="Search"
          type="search"
          value={query}
          onChange={(event) => setQuery(event.currentTarget.value)}
        />
      </label>
      <ul className="project-access-people" aria-label="Team members">
        {shown.map((member) => (
          <li key={member.actor.id} className="project-access-people__row">
            <button
              className="project-access-people__option group-dialog__option"
              type="button"
              onClick={() => onPick(member.actor.id)}
            >
              <TaskMemberAvatar member={member.actor} />
              <span className="project-access-person__text">
                <strong>{member.actor.name}</strong>
                <small>{member.email}</small>
              </span>
              <UserPlus size={15} aria-hidden="true" />
            </button>
          </li>
        ))}
        {shown.length === 0 ? (
          <li className="p-[22px_12px] text-workspace-muted text-[length:12px] text-center [list-style:none]">
            {candidates.length === 0 ? "Everyone's already in" : "No match"}
          </li>
        ) : null}
      </ul>
    </div>
  );
}
