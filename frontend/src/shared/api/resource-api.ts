import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import { companyResourceService } from "@/shared/api/company-resource.service";
import type {
  EntityRevisionRequest,
  CompanyResourceCreateRequest,
  CompanyResourceDto,
  CompanyResourceUpdateRequest,
} from "@/shared/api/types";

export const resourceApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listWorkspaceResources: builder.query<
      Awaited<ReturnType<typeof companyResourceService.listResources>>,
      string
    >({
      providesTags: (_result, _error, companyId: string) => [
        { id: companyId, type: "ProjectResource" },
      ],
      queryFn: (companyId: string) =>
        executeZenithTeamQuery(
          () => companyResourceService.listResources(companyId),
          "Resources could not be loaded.",
        ),
    }),
    createWorkspaceResource: builder.mutation<
      CompanyResourceDto,
      {
        companyId: string;
        request: CompanyResourceCreateRequest;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "ProjectResource" },
      ],
      queryFn: (argument: {
        companyId: string;
        request: CompanyResourceCreateRequest;
      }) =>
        executeZenithTeamQuery(
          () =>
            companyResourceService.createResource(
              argument.companyId,
              argument.request,
            ),
          "Resource could not be created.",
        ),
    }),
    updateWorkspaceResource: builder.mutation<
      Awaited<ReturnType<typeof companyResourceService.updateResource>>,
      {
        companyId: string;
        request: CompanyResourceUpdateRequest;
        resourceId: string;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "ProjectResource" },
      ],
      queryFn: (argument: {
        companyId: string;
        request: CompanyResourceUpdateRequest;
        resourceId: string;
      }) =>
        executeZenithTeamQuery(
          () =>
            companyResourceService.updateResource(
              argument.companyId,
              argument.resourceId,
              argument.request,
            ),
          "Resource could not be updated.",
        ),
    }),
    deleteWorkspaceResource: builder.mutation<
      Awaited<ReturnType<typeof companyResourceService.deleteResource>>,
      {
        companyId: string;
        request: EntityRevisionRequest;
        resourceId: string;
      }
    >({
      invalidatesTags: (_result, _error, argument: { companyId: string }) => [
        { id: argument.companyId, type: "ProjectResource" },
      ],
      queryFn: (argument: {
        companyId: string;
        request: EntityRevisionRequest;
        resourceId: string;
      }) =>
        executeZenithTeamQuery(
          () =>
            companyResourceService.deleteResource(
              argument.companyId,
              argument.resourceId,
              argument.request,
            ),
          "Resource could not be deleted.",
        ),
    }),
  }),
});

export const {
  useCreateWorkspaceResourceMutation,
  useDeleteWorkspaceResourceMutation,
  useLazyListWorkspaceResourcesQuery,
  useListWorkspaceResourcesQuery,
  useUpdateWorkspaceResourceMutation,
} = resourceApi;
