import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import type { TaskDto } from "@/shared/api/types";
import { hasProjectPermission } from "@/shared/permissions";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { toast } from "@/shared/ui/sonner";
import { useDecideTaskAssignmentMutation } from "./task-calendar-api";

export function TaskAssignmentResponse({ task }: { task: TaskDto }) {
  const session = useSessionSnapshot();
  const [decide, decision] = useDecideTaskAssignmentMutation();
  if (task.assignmentStatus === "accepted") return null;
  async function respond(accept: boolean) {
    try {
      await decide({
        companyId: task.companyId,
        taskId: task.id,
        input: { expectedRevision: task.revision, accept },
      }).unwrap();
      toast.success(accept ? "Assignment accepted" : "Assignment declined");
    } catch (error) {
      toast.error(getBoardlyQueryErrorMessage(error));
    }
  }
  return (
    <div className="task-assignment-response">
      <p>
        {task.assignmentStatus === "pending"
          ? "Waiting for the assignee to accept"
          : "Assignment declined"}
      </p>
      {task.assignee.id === session.userId && (
        <div>
          <button
            type="button"
            disabled={
              decision.isLoading ||
              !hasProjectPermission(task.access, "project.tasks.execute")
            }
            onClick={() => void respond(true)}
          >
            Accept task
          </button>
          {task.assignmentStatus !== "declined" && (
            <button
              type="button"
              disabled={decision.isLoading}
              onClick={() => void respond(false)}
            >
              Decline
            </button>
          )}
        </div>
      )}
    </div>
  );
}
