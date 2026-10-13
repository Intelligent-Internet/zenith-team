import { useGetWorkspaceTaskQuery } from "@/shared/api/task-api";
import { useState } from "react";

import type { TaskDto } from "@/shared/api/types";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { toast } from "@/shared/ui/sonner";
import {
  TaskScheduleFields,
  taskScheduleValues,
  taskScheduleTrigger,
} from "./task-schedule-fields";
import {
  useSaveTaskScheduleMutation,
  type TaskCalendarSchedule,
} from "./task-calendar-api";

export function TaskScheduleEditor({
  task,
  schedule,
  onClose,
  appearance = "workspace",
}: {
  task: TaskDto;
  schedule?: TaskCalendarSchedule;
  onClose: () => void;
  appearance?: "calendar" | "workspace";
}) {
  const templateId = schedule?.templateTaskId || task.id;
  const template = useGetWorkspaceTaskQuery(
    { companyId: task.companyId, taskId: templateId },
    { skip: templateId === task.id },
  );
  const target = templateId === task.id ? task : template.currentData;
  if (template.isError)
    return (
      <p
        className="text-workspace-danger text-[length:12px] leading-[1.5]"
        role="alert"
      >
        {getBoardlyQueryErrorMessage(template.error)}
      </p>
    );
  if (!target) return <p role="status">Loading task…</p>;
  return (
    <TaskScheduleForm
      appearance={appearance}
      task={target}
      schedule={schedule}
      onClose={onClose}
    />
  );
}

function TaskScheduleForm({
  task,
  schedule,
  onClose,
  appearance = "workspace",
}: {
  task: TaskDto;
  schedule?: TaskCalendarSchedule;
  onClose: () => void;
  appearance?: "calendar" | "workspace";
}) {
  const [target] = useState(task);
  const [initialSchedule] = useState(schedule);
  const [values, setValues] = useState(() =>
    taskScheduleValues(schedule?.trigger),
  );
  const [save, mutation] = useSaveTaskScheduleMutation();
  const [error, setError] = useState("");
  async function submit(paused: boolean, changeTrigger: boolean) {
    setError("");
    try {
      await save({
        companyId: task.companyId,
        taskId: target.id,
        input: {
          expectedRevision: target.revision,
          paused,
          ...(changeTrigger ? { trigger: taskScheduleTrigger(values) } : {}),
        },
      }).unwrap();
      toast.success(paused ? "Schedule paused" : "Schedule saved");
      onClose();
    } catch (caught) {
      setError(getBoardlyQueryErrorMessage(caught));
    }
  }
  return (
    <form
      className="task-schedule-form"
      onSubmit={(event) => {
        event.preventDefault();
        void submit(false, true);
      }}
    >
      <fieldset disabled={mutation.isLoading}>
        <TaskScheduleFields
          appearance={appearance}
          values={values}
          onChange={setValues}
        />
        {error ? (
          <p
            className="text-workspace-danger text-[length:12px] leading-[1.5]"
            role="alert"
          >
            {error}
          </p>
        ) : null}
        <div className="task-schedule-actions flex justify-end gap-[8px] pt-[14px] mt-[14px] [border-top:1px_solid_var(--workspace-divider)]">
          <button
            className="personal-button personal-button--primary"
            type="submit"
          >
            {mutation.isLoading ? "Saving…" : "Save schedule"}
          </button>
          {schedule ? (
            <button
              type="button"
              className="personal-button"
              onClick={() => void submit(!initialSchedule?.paused, false)}
            >
              {initialSchedule?.paused ? "Resume" : "Pause"}
            </button>
          ) : null}
        </div>
      </fieldset>
    </form>
  );
}
