import { taskReportService } from "@/shared/api/task-report.service";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import type {
  SendTaskReportsResponse,
  TaskReportPreviewDto,
} from "@/shared/api/types";

type TaskReportArgument = {
  companyId: string;
  conversationId: string;
};

type SendTaskReportsArgument = TaskReportArgument & {
  requestKey: string;
};

const taskReportTag = ({ companyId, conversationId }: TaskReportArgument) =>
  `${companyId}:${conversationId}:reports`;

export const taskReportApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getTaskReportPreview: builder.query<
      TaskReportPreviewDto,
      TaskReportArgument
    >({
      providesTags: (_result, _error, argument) => [
        { id: argument.companyId, type: "TaskReport" },
        { id: taskReportTag(argument), type: "TaskReport" },
      ],
      queryFn: ({ companyId, conversationId }) =>
        executeZenithTeamQuery(
          () => taskReportService.preview(companyId, conversationId),
          "Task updates could not be loaded.",
        ),
    }),
    sendTaskReports: builder.mutation<
      SendTaskReportsResponse,
      SendTaskReportsArgument
    >({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.companyId, type: "TaskReport" },
        { id: taskReportTag(argument), type: "TaskReport" },
      ],
      queryFn: ({ companyId, conversationId, requestKey }) =>
        executeZenithTeamQuery(
          () =>
            taskReportService.send(companyId, conversationId, { requestKey }),
          "Task updates could not be sent.",
        ),
    }),
  }),
  overrideExisting: false,
});

export const { useGetTaskReportPreviewQuery, useSendTaskReportsMutation } =
  taskReportApi;
