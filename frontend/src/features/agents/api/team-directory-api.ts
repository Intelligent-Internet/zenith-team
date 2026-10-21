import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import type {
  components as ApiComponents,
  operations as ApiOperations,
} from "@/shared/api/generated/api.generated";

type MembershipMutation = ApiComponents["schemas"]["MembershipMutation"];
type MemberRoleChange = ApiComponents["schemas"]["MemberRoleChange"];

export type ActiveCompanyMember = ApiComponents["schemas"]["ActiveMember"];
export type ActiveCompanyMemberPage =
  ApiOperations["listCompanyMembers"]["responses"][200]["content"]["application/json"];

/**
 * Upserts one roster row in place, keyed by the member's user id. A row the
 * cache already holds at the same or a newer revision is left alone; a new
 * member is appended in roster order (createdAt, then user id).
 */
export function upsertMember(
  items: ActiveCompanyMember[],
  member: ActiveCompanyMember,
) {
  const index = items.findIndex((item) => item.actor.id === member.actor.id);
  if (index === -1) {
    items.push(member);
    items.sort(
      (left, right) =>
        left.createdAt.localeCompare(right.createdAt) ||
        left.actor.id.localeCompare(right.actor.id),
    );
    return;
  }
  if (items[index].revision <= member.revision) {
    items[index] = member;
  }
}

export function removeMember(
  items: ActiveCompanyMember[],
  userId: string,
  revision: number,
) {
  const index = items.findIndex((item) => item.actor.id === userId);
  if (index !== -1 && items[index].revision <= revision) {
    items.splice(index, 1);
  }
}

const teamDirectoryApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    removeCompanyMember: builder.mutation<
      void,
      { companyId: string; userId: string; body: MembershipMutation }
    >({
      invalidatesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Member" },
        { id: companyId, type: "Company" },
      ],
      queryFn: ({ companyId, userId, body }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.delete<void>(
              `/companies/${encodeURIComponent(companyId)}/members/${encodeURIComponent(userId)}`,
              { data: body },
            ),
          "Member could not be removed.",
        ),
    }),
    changeCompanyMemberRole: builder.mutation<
      ActiveCompanyMember,
      { companyId: string; userId: string; body: MemberRoleChange }
    >({
      invalidatesTags: (_result, error, { companyId }) =>
        error ? [] : [{ id: companyId, type: "Member" }],
      queryFn: ({ companyId, userId, body }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.patch<ActiveCompanyMember>(
              `/companies/${encodeURIComponent(companyId)}/members/${encodeURIComponent(userId)}`,
              body,
            ),
          "Role could not be changed.",
        ),
      async onQueryStarted({ companyId }, { dispatch, queryFulfilled }) {
        const { data } = await queryFulfilled;
        dispatch(
          teamDirectoryApi.util.updateQueryData(
            "listActiveCompanyMembers",
            companyId,
            (page) => {
              upsertMember(page.items, data);
            },
          ),
        );
      },
    }),
    transferCompanyOwnership: builder.mutation<
      ActiveCompanyMemberPage,
      { companyId: string; userId: string; body: MembershipMutation }
    >({
      invalidatesTags: (_result, error, { companyId }) =>
        error
          ? []
          : [
              { id: companyId, type: "Member" },
              { id: companyId, type: "Company" },
            ],
      queryFn: ({ companyId, userId, body }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<ActiveCompanyMemberPage>(
              `/companies/${encodeURIComponent(companyId)}/members/${encodeURIComponent(userId)}/transfer-ownership`,
              body,
            ),
          "Ownership could not be transferred.",
        ),
      async onQueryStarted({ companyId }, { dispatch, queryFulfilled }) {
        const { data } = await queryFulfilled;
        dispatch(
          teamDirectoryApi.util.updateQueryData(
            "listActiveCompanyMembers",
            companyId,
            (page) => {
              for (const member of data.items) upsertMember(page.items, member);
            },
          ),
        );
      },
    }),
    leaveCompany: builder.mutation<
      void,
      { companyId: string; body: MembershipMutation }
    >({
      queryFn: ({ companyId, body }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<void>(
              `/companies/${encodeURIComponent(companyId)}/leave`,
              body,
            ),
          "You could not leave the team.",
        ),
    }),
    listActiveCompanyMembers: builder.query<ActiveCompanyMemberPage, string>({
      providesTags: (_result, _error, companyId) => [
        { id: companyId, type: "Member" },
      ],
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<ActiveCompanyMemberPage>(
              `/companies/${encodeURIComponent(companyId)}/members`,
            ),
          "Company members could not be loaded.",
        ),
    }),
  }),
  overrideExisting: false,
});

export { teamDirectoryApi };

export const {
  useListActiveCompanyMembersQuery,
  useRemoveCompanyMemberMutation,
  useChangeCompanyMemberRoleMutation,
  useTransferCompanyOwnershipMutation,
  useLeaveCompanyMutation,
} = teamDirectoryApi;
