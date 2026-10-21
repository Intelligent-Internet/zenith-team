import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";

type Scope = { companyId: string; huddleId: string };
const path = (s: Scope) =>
  `/companies/${s.companyId}/huddles/${s.huddleId}/notes`;
const api = zenithTeamApi.injectEndpoints({
  endpoints: (b) => ({
    getHuddleNotes: b.query<
      components["schemas"]["HuddleNotesStatus"],
      Scope & { cursor?: string }
    >({
      keepUnusedDataFor: 0,
      queryFn: (s, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<components["schemas"]["HuddleNotesStatus"]>(path(s), {
              params: { cursor: s.cursor },
              signal,
            }),
          "Meeting notes are unavailable.",
        ),
    }),
    setHuddleNotes: b.mutation<
      components["schemas"]["HuddleNotesStatus"],
      Scope & { body: components["schemas"]["SetHuddleNotes"] }
    >({
      queryFn: (s) =>
        executeZenithTeamQuery(
          () =>
            apiClient.put<components["schemas"]["HuddleNotesStatus"]>(
              path(s),
              s.body,
            ),
          "Could not change meeting notes. Check Company settings and reload.",
        ),
    }),
    editHuddleNotesRecap: b.mutation<
      components["schemas"]["HuddleNotesStatus"],
      Scope & { body: components["schemas"]["EditHuddleNotesRecap"] }
    >({
      queryFn: (s) =>
        executeZenithTeamQuery(
          () =>
            apiClient.patch<components["schemas"]["HuddleNotesStatus"]>(
              `${path(s)}/recap`,
              s.body,
            ),
          "The recap changed or access was lost. Reload before editing.",
        ),
    }),
    getHuddleNotesPolicy: b.query<
      components["schemas"]["HuddleNotesPolicy"],
      string
    >({
      keepUnusedDataFor: 0,
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<components["schemas"]["HuddleNotesPolicy"]>(
              `/companies/${companyId}/huddles/notes-policy`,
            ),
          "Notes settings are unavailable.",
        ),
    }),
    setHuddleNotesPolicy: b.mutation<
      components["schemas"]["HuddleNotesPolicy"],
      { companyId: string; body: components["schemas"]["SetHuddleNotesPolicy"] }
    >({
      queryFn: (s) =>
        executeZenithTeamQuery(
          () =>
            apiClient.put<components["schemas"]["HuddleNotesPolicy"]>(
              `/companies/${s.companyId}/huddles/notes-policy`,
              s.body,
            ),
          "Could not save notes settings.",
        ),
    }),
    submitHuddleNotes: b.mutation<
      components["schemas"]["HuddleNotesSubmission"],
      Scope & {
        actionIndex: number;
        body: components["schemas"]["SubmitHuddleNotes"];
      }
    >({
      queryFn: (s) =>
        executeZenithTeamQuery(
          () =>
            apiClient.post<components["schemas"]["HuddleNotesSubmission"]>(
              `${path(s)}/actions/${s.actionIndex}/submit`,
              s.body,
            ),
          "Could not create the Task. Reload and check your Project access.",
        ),
    }),
  }),
});
export const {
  useGetHuddleNotesQuery,
  useSetHuddleNotesMutation,
  useEditHuddleNotesRecapMutation,
  useGetHuddleNotesPolicyQuery,
  useSetHuddleNotesPolicyMutation,
  useSubmitHuddleNotesMutation,
} = api;
