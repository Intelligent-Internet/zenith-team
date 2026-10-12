import {
  operateDesktopTool,
  type DesktopToolRequest,
  type DesktopToolValue,
} from "@/app/desktop/desktop-environment.service";
import { boardlyApi } from "@/shared/api/boardly-api";

export const managedToolsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    operateManagedTool: builder.mutation<
      DesktopToolValue,
      Omit<DesktopToolRequest, "schemaVersion">
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Runtime" },
      ],
      queryFn: async (request) => {
        const result = await operateDesktopTool(request);
        return result.ok
          ? { data: result.value }
          : {
              error: {
                code: result.code,
                message: result.reason,
                status: null,
              },
            };
      },
    }),
  }),
});

export const { useOperateManagedToolMutation } = managedToolsApi;
