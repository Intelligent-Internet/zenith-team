import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";

// Groups (Access TECH D3): a Company's named sets of Team members. Every
// active member reads them; owners and admins (members.manage) change them.
// Every Group read carries the Company's Group tag, which the realtime
// frames group.changed, group.deleted and project.access_changed invalidate
// (realtime-cache-sync.ts, project-access-cache-updater.ts).

export type GroupSummary = components["schemas"]["GroupSummary"];
export type Group = components["schemas"]["Group"];
export type GroupChangePreview = components["schemas"]["GroupChangePreview"];

const groupsPath = (companyId: string) =>
  `/companies/${encodeURIComponent(companyId)}/groups`;
const groupPath = (companyId: string, id: string) =>
  `${groupsPath(companyId)}/${encodeURIComponent(id)}`;
const groupsTag = (companyId: string) => ({
  type: "Group" as const,
  id: companyId,
});

// A change of members or a delete moves access on every Project the Group
// holds: the Projects, their grant lists, Tasks and the feed read again.
const accessMovedTags = (companyId: string) => [
  groupsTag(companyId),
  { type: "Project" as const, id: companyId },
  { type: "Task" as const, id: companyId },
  { type: "Activity" as const, id: companyId },
];

export const groupsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    listGroups: builder.query<{ items: GroupSummary[] }, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => apiClient.get<{ items: GroupSummary[] }>(groupsPath(companyId)),
          "Couldn't load groups",
        ),
      providesTags: (_r, _e, companyId) => [groupsTag(companyId)],
    }),
    getGroup: builder.query<Group, { companyId: string; id: string }>({
      queryFn: ({ companyId, id }) =>
        executeBoardlyQuery(
          () => apiClient.get<Group>(groupPath(companyId, id)),
          "Couldn't load group",
        ),
      providesTags: (_r, _e, { companyId }) => [groupsTag(companyId)],
    }),
    createGroup: builder.mutation<
      Group,
      { companyId: string; name: string; description: string }
    >({
      queryFn: ({ companyId, name, description }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Group>(groupsPath(companyId), { name, description }),
          "Couldn't create group",
        ),
      invalidatesTags: (_r, error, { companyId }) =>
        error
          ? []
          : [groupsTag(companyId), { type: "Activity", id: companyId }],
    }),
    updateGroup: builder.mutation<
      Group,
      {
        companyId: string;
        id: string;
        name?: string;
        description?: string;
        expectedRevision: number;
      }
    >({
      queryFn: ({ companyId, id, name, description, expectedRevision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.patch<Group>(groupPath(companyId, id), {
              ...(name === undefined ? {} : { name }),
              ...(description === undefined ? {} : { description }),
              expectedRevision,
            }),
          "Couldn't save group",
        ),
      invalidatesTags: (_r, _e, { companyId }) => [
        groupsTag(companyId),
        { type: "Project", id: companyId },
        { type: "Activity", id: companyId },
      ],
    }),
    deleteGroup: builder.mutation<
      void,
      { companyId: string; id: string; expectedRevision: number }
    >({
      queryFn: ({ companyId, id, expectedRevision }) =>
        executeBoardlyQuery(
          () =>
            apiClient.delete<void>(groupPath(companyId, id), {
              data: { expectedRevision },
            }),
          "Couldn't delete group",
        ),
      invalidatesTags: (_r, _e, { companyId }) => accessMovedTags(companyId),
    }),
    changeGroupMembers: builder.mutation<
      Group,
      {
        companyId: string;
        id: string;
        addUserIds: string[];
        removeUserIds: string[];
        expectedRevision: number;
      }
    >({
      queryFn: ({
        companyId,
        id,
        addUserIds,
        removeUserIds,
        expectedRevision,
      }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Group>(`${groupPath(companyId, id)}/members`, {
              addUserIds,
              removeUserIds,
              expectedRevision,
            }),
          "Couldn't save people",
        ),
      invalidatesTags: (_r, _e, { companyId }) => accessMovedTags(companyId),
    }),
    // What a change of members, or a delete, would do. It writes nothing,
    // so it is a mutation only to run on demand, uncached.
    previewGroupChange: builder.mutation<
      GroupChangePreview,
      {
        companyId: string;
        id: string;
        addUserIds: string[];
        removeUserIds: string[];
        delete?: boolean;
      }
    >({
      queryFn: ({ companyId, id, addUserIds, removeUserIds, delete: remove }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<GroupChangePreview>(
              `${groupPath(companyId, id)}/preview`,
              {
                addUserIds,
                removeUserIds,
                ...(remove ? { delete: true } : {}),
              },
            ),
          "Couldn't check change",
        ),
    }),
  }),
});

export const {
  useListGroupsQuery,
  useGetGroupQuery,
  useCreateGroupMutation,
  useUpdateGroupMutation,
  useDeleteGroupMutation,
  useChangeGroupMembersMutation,
  usePreviewGroupChangeMutation,
} = groupsApi;
