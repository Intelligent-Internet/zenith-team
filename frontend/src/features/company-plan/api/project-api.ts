import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type {
  components,
  operations,
} from "@/shared/api/generated/api.generated";
export type Project =
  operations["getProject"]["responses"][200]["content"]["application/json"];
export type ProjectGrant = components["schemas"]["ProjectGrant"];
export type ProjectGrantSource = components["schemas"]["ProjectGrantSource"];
export type ProjectGrantCollection =
  components["schemas"]["ProjectGrantCollection"];
export type ProjectGroupGrant = components["schemas"]["ProjectGroupGrant"];
export type ProjectGrantResult = components["schemas"]["ProjectGrantResult"];
export type ProjectGroupGrantResult =
  components["schemas"]["ProjectGroupGrantResult"];
type GroupChangePreview = components["schemas"]["GroupChangePreview"];
export type ProjectGrantPreview = components["schemas"]["ProjectGrantPreview"];
export type ProjectAccessReport = components["schemas"]["ProjectAccessReport"];
type ProjectPermission = components["schemas"]["ProjectPermission"];
type ProjectVisibility = components["schemas"]["ProjectVisibility"];
type Create =
  operations["createProject"]["requestBody"]["content"]["application/json"];
type Update =
  operations["updateProject"]["requestBody"]["content"]["application/json"];
type ReplaceFiles =
  operations["replaceProjectFiles"]["requestBody"]["content"]["application/json"];
const path = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}/projects`;
const projectPath = (companyId: string, id: string) =>
  `${path(companyId)}/${encodeURIComponent(id)}`;
// One Project and its grants are tagged apart from the Company's lists, so
// an access change reloads exactly what it touched; the Company tag still
// reloads everything on a realtime frame.
const projectTag = (id: string) => ({ type: "Project" as const, id });
const grantsTag = (id: string) => ({
  type: "Project" as const,
  id: `${id}:grants`,
});
const managedTag = (companyId: string) => ({
  type: "Project" as const,
  id: `${companyId}:managed`,
});

/**
 * Writes the entry a grant save answered into the cached access list:
 * replaced, added (Add people) or dropped (`null`, no stored source left).
 * The row's label and a picker opened next read the saved set at once,
 * not the set from before the save while the list reloads.
 */
function applyGrant(
  list: ProjectGrantCollection,
  userId: string,
  grant: ProjectGrant | null,
) {
  const index = list.items.findIndex((item) => item.userId === userId);
  if (!grant) {
    if (index !== -1) list.items.splice(index, 1);
  } else if (index === -1) {
    list.items.push(grant);
  } else {
    list.items[index] = grant;
  }
}

/** The same for a Group's entry; its members' marks wait for the reload. */
function applyGroupGrant(
  list: ProjectGrantCollection,
  groupId: string,
  grant: ProjectGroupGrant | null,
) {
  const index = list.groups.findIndex((item) => item.groupId === groupId);
  if (!grant) {
    if (index !== -1) list.groups.splice(index, 1);
  } else if (index === -1) {
    list.groups.push(grant);
  } else {
    list.groups[index] = grant;
  }
}

export const projectApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listProjects: builder.query<{ items: Project[] }, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<{ items: Project[] }>(path(companyId), {
              params: { include_archived: true },
            }),
          "Projects could not be loaded.",
        ),
      providesTags: (_r, _e, id) => [{ type: "Project", id }],
    }),
    // The Managed projects view: Projects the caller manages through the
    // Company permission `projects.manage` but cannot read (ADR 0017).
    // Archived ones are listed too, as in the readable list, so Activity
    // and the access screens still name them.
    listManagedProjects: builder.query<{ items: Project[] }, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<{ items: Project[] }>(path(companyId), {
              params: { view: "manage", include_archived: true },
            }),
          "Managed projects could not be loaded.",
        ),
      providesTags: (_r, _e, companyId) => [
        { type: "Project", id: companyId },
        managedTag(companyId),
      ],
    }),
    // People left outside restricted Projects they were working in, for
    // holders of "projects.manage" (ADR 0017). It follows grant and Task
    // changes, since either can add or clear a row.
    getProjectAccessReport: builder.query<ProjectAccessReport, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<ProjectAccessReport>(
              `${path(companyId)}/access-report`,
            ),
          "The access report could not be loaded.",
        ),
      providesTags: (_r, _e, companyId) => [
        { type: "Project", id: companyId },
        { type: "Task", id: companyId },
      ],
    }),
    getProject: builder.query<Project, { companyId: string; id: string }>({
      queryFn: ({ companyId, id }) =>
        executeBoardlyQuery(
          () => apiClient.get<Project>(projectPath(companyId, id)),
          "Project could not be loaded.",
        ),
      providesTags: (_r, _e, { companyId, id }) => [
        { type: "Project", id: companyId },
        projectTag(id),
      ],
    }),
    // Everyone with a stored source on the Project, and its Groups (Access
    // TECH D4). A Group's name and member count change with group.changed,
    // so the list also follows the Company's Group tag.
    listProjectGrants: builder.query<
      ProjectGrantCollection,
      { companyId: string; id: string }
    >({
      queryFn: ({ companyId, id }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<ProjectGrantCollection>(
              `${projectPath(companyId, id)}/grants`,
            ),
          "Couldn't load access",
        ),
      providesTags: (_r, _e, { companyId, id }) => [
        { type: "Project", id: companyId },
        grantsTag(id),
        { type: "Group", id: companyId },
      ],
    }),
    setProjectGroupGrant: builder.mutation<
      ProjectGroupGrantResult,
      {
        companyId: string;
        id: string;
        groupId: string;
        permissions: ProjectPermission[];
        expectedRevision: number;
      }
    >({
      queryFn: ({ companyId, id, groupId, permissions, expectedRevision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.put<ProjectGroupGrantResult>(
              `${projectPath(companyId, id)}/group-grants/${encodeURIComponent(groupId)}`,
              { permissions, expectedRevision },
            ),
          "Couldn't save access",
        ),
      // The saved entry shows at once; the reload below still runs.
      async onQueryStarted(
        { companyId, id, groupId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            projectApi.util.updateQueryData(
              "listProjectGrants",
              { companyId, id },
              (list) => applyGroupGrant(list, groupId, data.grant),
            ),
          );
        } catch {
          // The caller reports the failure.
        }
      },
      invalidatesTags: (_r, _e, { companyId, id }) => [
        { type: "Project", id: companyId },
        projectTag(id),
        grantsTag(id),
        { type: "Group", id: companyId },
        { type: "Task", id: companyId },
      ],
    }),
    removeProjectGroupGrant: builder.mutation<
      ProjectGroupGrantResult,
      {
        companyId: string;
        id: string;
        groupId: string;
        expectedRevision: number;
      }
    >({
      queryFn: ({ companyId, id, groupId, expectedRevision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.delete<ProjectGroupGrantResult>(
              `${projectPath(companyId, id)}/group-grants/${encodeURIComponent(groupId)}`,
              { data: { expectedRevision } },
            ),
          "Couldn't remove group",
        ),
      async onQueryStarted(
        { companyId, id, groupId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            projectApi.util.updateQueryData(
              "listProjectGrants",
              { companyId, id },
              (list) => applyGroupGrant(list, groupId, data.grant),
            ),
          );
        } catch {
          // The caller reports the failure.
        }
      },
      invalidatesTags: (_r, _e, { companyId, id }) => [
        { type: "Project", id: companyId },
        projectTag(id),
        grantsTag(id),
        { type: "Group", id: companyId },
        { type: "Task", id: companyId },
      ],
    }),
    // What setting a Group's permissions here would do; `[]` previews a
    // removal. It writes nothing, so it runs on demand, uncached.
    previewProjectGroupGrant: builder.mutation<
      GroupChangePreview,
      {
        companyId: string;
        id: string;
        groupId: string;
        permissions: ProjectPermission[];
      }
    >({
      queryFn: ({ companyId, id, groupId, permissions }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<GroupChangePreview>(
              `${projectPath(companyId, id)}/group-grants/${encodeURIComponent(groupId)}/preview`,
              { permissions },
            ),
          "Couldn't check change",
        ),
    }),
    // What setting one person's own permissions here would do: the runs it
    // stops and whether they lose the project; `[]` previews a removal. It
    // writes nothing, so it runs on demand, uncached.
    previewProjectGrant: builder.mutation<
      ProjectGrantPreview,
      {
        companyId: string;
        id: string;
        userId: string;
        permissions: ProjectPermission[];
      }
    >({
      queryFn: ({ companyId, id, userId, permissions }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<ProjectGrantPreview>(
              `${projectPath(companyId, id)}/grants/${encodeURIComponent(userId)}/preview`,
              { permissions },
            ),
          "Couldn't check change",
        ),
    }),
    setProjectGrant: builder.mutation<
      ProjectGrantResult,
      {
        companyId: string;
        id: string;
        userId: string;
        permissions: ProjectPermission[];
        expectedRevision: number;
      }
    >({
      queryFn: ({ companyId, id, userId, permissions, expectedRevision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.put<ProjectGrantResult>(
              `${projectPath(companyId, id)}/grants/${encodeURIComponent(userId)}`,
              { permissions, expectedRevision },
            ),
          "Couldn't save access",
        ),
      // The saved entry shows at once; the reload below still runs.
      async onQueryStarted(
        { companyId, id, userId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            projectApi.util.updateQueryData(
              "listProjectGrants",
              { companyId, id },
              (list) => applyGrant(list, userId, data.grant),
            ),
          );
        } catch {
          // The caller reports the failure.
        }
      },
      invalidatesTags: (_r, _e, { companyId, id }) => [
        { type: "Project", id: companyId },
        projectTag(id),
        grantsTag(id),
      ],
    }),
    removeProjectGrant: builder.mutation<
      ProjectGrantResult,
      {
        companyId: string;
        id: string;
        userId: string;
        expectedRevision: number;
      }
    >({
      queryFn: ({ companyId, id, userId, expectedRevision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.delete<ProjectGrantResult>(
              `${projectPath(companyId, id)}/grants/${encodeURIComponent(userId)}`,
              { data: { expectedRevision } },
            ),
          "Couldn't remove access",
        ),
      async onQueryStarted(
        { companyId, id, userId },
        { dispatch, queryFulfilled },
      ) {
        try {
          const { data } = await queryFulfilled;
          dispatch(
            projectApi.util.updateQueryData(
              "listProjectGrants",
              { companyId, id },
              (list) => applyGrant(list, userId, data.grant),
            ),
          );
        } catch {
          // The caller reports the failure.
        }
      },
      invalidatesTags: (_r, _e, { companyId, id }) => [
        { type: "Project", id: companyId },
        projectTag(id),
        grantsTag(id),
      ],
    }),
    joinProject: builder.mutation<
      Project,
      { companyId: string; id: string; expectedRevision: number }
    >({
      queryFn: ({ companyId, id, expectedRevision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Project>(`${projectPath(companyId, id)}/join`, {
              expectedRevision,
            }),
          "Couldn't join",
        ),
      invalidatesTags: (_r, _e, { companyId, id }) => [
        { type: "Project", id: companyId },
        projectTag(id),
        grantsTag(id),
        { type: "Task", id: companyId },
      ],
    }),
    setProjectVisibility: builder.mutation<
      Project,
      {
        companyId: string;
        id: string;
        visibility: ProjectVisibility;
        expectedRevision: number;
        expectedAccessRevision: number;
      }
    >({
      queryFn: ({
        companyId,
        id,
        visibility,
        expectedRevision,
        expectedAccessRevision,
      }) =>
        executeBoardlyQuery(
          () =>
            apiClient.patch<Project>(projectPath(companyId, id), {
              visibility,
              expectedRevision,
              expectedAccessRevision,
            }),
          "Couldn't change access",
        ),
      invalidatesTags: (_r, _e, { companyId, id }) => [
        { type: "Project", id: companyId },
        projectTag(id),
        grantsTag(id),
        { type: "Task", id: companyId },
      ],
    }),
    createProject: builder.mutation<
      Project,
      { companyId: string; request: Create }
    >({
      queryFn: ({ companyId, request }) =>
        executeBoardlyQuery(
          () => apiClient.post<Project>(path(companyId), request),
          "Project could not be created.",
        ),
      invalidatesTags: (_r, _e, { companyId }) => [
        { type: "Project", id: companyId },
      ],
    }),
    updateProject: builder.mutation<
      Project,
      { companyId: string; id: string; request: Update }
    >({
      queryFn: ({ companyId, id, request }) =>
        executeBoardlyQuery(
          () => apiClient.patch<Project>(`${path(companyId)}/${id}`, request),
          "Project could not be updated.",
        ),
      invalidatesTags: (_r, _e, { companyId }) => [
        { type: "Project", id: companyId },
      ],
    }),
    replaceProjectFiles: builder.mutation<
      Project,
      { companyId: string; id: string; request: ReplaceFiles }
    >({
      queryFn: ({ companyId, id, request }) =>
        executeBoardlyQuery(
          () =>
            apiClient.put<Project>(`${path(companyId)}/${id}/files`, request),
          "Project files could not be saved.",
        ),
      invalidatesTags: (_r, _e, { companyId }) => [
        { type: "Project", id: companyId },
      ],
    }),
    archiveProject: builder.mutation<
      Project,
      { companyId: string; id: string; expectedRevision: number }
    >({
      queryFn: ({ companyId, id, expectedRevision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Project>(`${path(companyId)}/${id}/archive`, {
              expectedRevision,
            }),
          "Project could not be archived.",
        ),
      invalidatesTags: (_r, _e, { companyId }) => [
        { type: "Project", id: companyId },
      ],
    }),
  }),
});
export const {
  useListProjectsQuery,
  useListManagedProjectsQuery,
  useGetProjectAccessReportQuery,
  useGetProjectQuery,
  useListProjectGrantsQuery,
  useSetProjectGrantMutation,
  useRemoveProjectGrantMutation,
  useSetProjectGroupGrantMutation,
  useRemoveProjectGroupGrantMutation,
  usePreviewProjectGroupGrantMutation,
  usePreviewProjectGrantMutation,
  useJoinProjectMutation,
  useSetProjectVisibilityMutation,
  useCreateProjectMutation,
  useUpdateProjectMutation,
  useReplaceProjectFilesMutation,
  useArchiveProjectMutation,
} = projectApi;
