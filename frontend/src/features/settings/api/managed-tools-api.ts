import type {
  DesktopToolRequest,
  DesktopToolValue,
} from "../../../../electron/shared/desktop-bridge.types";
import { operateDesktopTool } from "@/shared/desktop/desktop-environment.service";
import { zenithTeamApi } from "@/shared/api/zenith-team-api";

export const managedToolsApi = zenithTeamApi.injectEndpoints({
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
