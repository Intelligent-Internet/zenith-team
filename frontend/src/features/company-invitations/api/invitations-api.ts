import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";

export type CompanyInvitation = components["schemas"]["CompanyInvitation"];
type Collection = components["schemas"]["InvitationCollection"];
type Results = components["schemas"]["InvitationResults"];
type Preview = components["schemas"]["InvitationPreview"];
type ShareLink = components["schemas"]["CompanyShareLink"];
type Acceptance = components["schemas"]["InvitationAcceptance"];
const companyPath = (id: string) =>
  `/companies/${encodeURIComponent(id)}/invitations`;
const tokenPath = (token: string) =>
  `/invitations/${encodeURIComponent(token)}`;

export const invitationsApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getCompanyShareLink: builder.query<ShareLink, string>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<ShareLink>(
              `/companies/${encodeURIComponent(companyId)}/share-link`,
            ),
          "Invite link could not be loaded.",
        ),
      providesTags: (_data, _error, id) => [
        { type: "Invitation", id: `link:${id}` },
      ],
      keepUnusedDataFor: 0,
    }),
    changeCompanyShareLink: builder.mutation<
      ShareLink,
      {
        companyId: string;
        action: "create" | "reset" | "disable";
        expectedRevision?: number;
      }
    >({
      queryFn: ({ companyId, action, expectedRevision }) =>
        executeBoardlyQuery(() => {
          const path = `/companies/${encodeURIComponent(companyId)}/share-link`;
          return action === "disable"
            ? apiClient.delete<ShareLink>(path, { data: { expectedRevision } })
            : apiClient.post<ShareLink>(
                action === "reset" ? `${path}/reset` : path,
                action === "create"
                  ? { role: "member" }
                  : { expectedRevision, role: "member" },
              );
        }, "Invite link could not be updated. Please try again."),
      invalidatesTags: (_data, _error, { companyId }) => [
        { type: "Invitation", id: `link:${companyId}` },
      ],
    }),
    listCompanyInvitations: builder.query<Collection, string>({
      queryFn: (id) =>
        executeBoardlyQuery(
          () => apiClient.get<Collection>(companyPath(id)),
          "Invitations could not be loaded.",
        ),
      providesTags: (_data, _error, id) => [{ type: "Invitation", id }],
    }),
    createCompanyInvitations: builder.mutation<
      Results,
      { companyId: string; emails: string[] }
    >({
      queryFn: ({ companyId, emails }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<Results>(companyPath(companyId), {
              emails,
              role: "member",
            }),
          "Invitations could not be sent. Try again.",
        ),
      invalidatesTags: (_data, _error, { companyId }) => [
        { type: "Invitation", id: companyId },
      ],
    }),
    changeCompanyInvitation: builder.mutation<
      CompanyInvitation,
      {
        companyId: string;
        invitation: CompanyInvitation;
        action: "resend" | "cancel";
      }
    >({
      queryFn: ({ companyId, invitation, action }) =>
        executeBoardlyQuery(() => {
          const path = `${companyPath(companyId)}/${encodeURIComponent(invitation.id)}`;
          const body = { expectedRevision: invitation.revision };
          return action === "resend"
            ? apiClient.post<CompanyInvitation>(`${path}/resend`, body)
            : apiClient.delete<CompanyInvitation>(path, { data: body });
        }, "The invitation could not be updated. Refresh and try again."),
      invalidatesTags: (_data, _error, { companyId }) => [
        { type: "Invitation", id: companyId },
      ],
    }),
    previewCompanyInvitation: builder.query<
      Preview,
      { token: string; userId?: string }
    >({
      queryFn: ({ token }) =>
        executeBoardlyQuery(
          () => apiClient.get<Preview>(tokenPath(token)),
          "This invitation could not be loaded.",
        ),
      keepUnusedDataFor: 0,
    }),
    acceptCompanyInvitation: builder.mutation<Acceptance, string>({
      queryFn: (token) =>
        executeBoardlyQuery(
          () => apiClient.post<Acceptance>(`${tokenPath(token)}/accept`, {}),
          "Could not join the company. Try again.",
        ),
      invalidatesTags: ["Company", "Invitation"],
    }),
    requestInvitationRenewal: builder.mutation<void, string>({
      queryFn: (token) =>
        executeBoardlyQuery(
          () => apiClient.post<void>(`${tokenPath(token)}/request-renewal`, {}),
          "Your request could not be sent. Try again.",
        ),
    }),
  }),
});
export const {
  useGetCompanyShareLinkQuery,
  useChangeCompanyShareLinkMutation,
  useListCompanyInvitationsQuery,
  useCreateCompanyInvitationsMutation,
  useChangeCompanyInvitationMutation,
  usePreviewCompanyInvitationQuery,
  useAcceptCompanyInvitationMutation,
  useRequestInvitationRenewalMutation,
} = invitationsApi;
