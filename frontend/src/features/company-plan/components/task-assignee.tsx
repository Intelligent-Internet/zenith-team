import * as Avatar from "@radix-ui/react-avatar";
import type { TaskDto } from "@/shared/api/types";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { personInitials } from "@/shared/ui/person-initials";
import "@/features/company-plan/styles/task-assignee.css";

// Who a Task belongs to, and who does the work (ADR 0014). The assignee is
// always a member and is drawn as one; the Agent doing the work, when there
// is one, follows under its own avatar, so a person never wears an Agent's
// face. Leave `executedBy` out where the Agent is shown elsewhere.
export function TaskAssignee({
  assignee,
  executedBy,
  variant = "names",
}: {
  assignee: TaskDto["assignee"];
  executedBy?: TaskDto["executedBy"];
  variant?: "names" | "avatars";
}) {
  return (
    <span className="project-task-assignee" data-variant={variant}>
      <span
        className="project-task-assignee__member"
        title={`Assigned to ${assignee.name}`}
      >
        <TaskMemberAvatar member={assignee} />
        <span className={variant === "avatars" ? "sr-only" : undefined}>
          {variant === "avatars"
            ? `Assigned to ${assignee.name}`
            : assignee.name}
        </span>
      </span>
      {executedBy?.kind === "agent" ? (
        <span
          className="project-task-assignee__agent"
          title={`${executedBy.name} does the work`}
        >
          <FoldAvatar paused agentId={executedBy.id} size={20} />
          <span className={variant === "avatars" ? "sr-only" : undefined}>
            {variant === "avatars"
              ? `Executed by ${executedBy.name}`
              : executedBy.name}
          </span>
        </span>
      ) : null}
    </span>
  );
}

// A member's face: their photo when they have one, their initials until it
// loads or when they do not. Sized by the caller's CSS.
export function TaskMemberAvatar({
  member,
}: {
  member: Pick<TaskDto["assignee"], "avatarUrl" | "name">;
}) {
  const initials = personInitials(member.name);
  return (
    <Avatar.Root aria-hidden="true" className="project-person-avatar">
      {member.avatarUrl ? (
        <Avatar.Image
          alt=""
          className="project-person-avatar__image"
          draggable={false}
          src={member.avatarUrl}
        />
      ) : null}
      <Avatar.Fallback>{initials}</Avatar.Fallback>
    </Avatar.Root>
  );
}
