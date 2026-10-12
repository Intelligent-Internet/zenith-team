import { apiClient } from "@/lib/api-client";
import { boardlyApi, executeBoardlyQuery } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";

export type CalendarOccurrence = components["schemas"]["CalendarOccurrence"];
export type CalendarInput = components["schemas"]["CalendarInput"];
export type CalendarCatalog = components["schemas"]["CalendarCatalog"];
export type CalendarAttendee = components["schemas"]["CalendarAttendee"];
type CalendarEvent = components["schemas"]["CalendarEvent"];
type Occurrences = components["schemas"]["CalendarOccurrences"];
type OccurrenceInput = components["schemas"]["CalendarOccurrenceInput"];
type ResponseInput = components["schemas"]["CalendarResponseInput"];
const path = (companyId?: string | null) =>
  companyId
    ? `/companies/${encodeURIComponent(companyId)}/calendar`
    : "/me/calendar";
const api = boardlyApi.injectEndpoints({
  endpoints: (build) => ({
    calendarCatalog: build.query<CalendarCatalog, string | undefined>({
      queryFn: (companyId) =>
        executeBoardlyQuery(
          () => apiClient.get<CalendarCatalog>(`${path(companyId)}/catalog`),
          "Could not load calendars and members.",
        ),
      providesTags: [{ type: "Calendar", id: "catalog" }],
    }),
    calendarEvents: build.query<
      Occurrences,
      { from: string; to: string; companyId?: string }
    >({
      queryFn: ({ companyId, ...params }) =>
        executeBoardlyQuery(
          () =>
            apiClient.get<Occurrences>(`${path(companyId)}/events`, { params }),
          "Could not load your calendar.",
        ),
      providesTags: [{ type: "Calendar", id: "events" }],
    }),
    saveCalendarEvent: build.mutation<
      CalendarEvent,
      { id?: string; input: CalendarInput; companyId?: string | null }
    >({
      queryFn: ({ id, input, companyId }) =>
        executeBoardlyQuery(
          () =>
            id
              ? apiClient.put<CalendarEvent>(
                  `${path(companyId)}/events/${id}`,
                  input,
                )
              : apiClient.post<CalendarEvent>(
                  `${path(companyId)}/events`,
                  input,
                ),
          "Could not save your event.",
        ),
      invalidatesTags: [{ type: "Calendar", id: "events" }],
    }),
    deleteCalendarEvent: build.mutation<
      void,
      { id: string; revision: number; companyId?: string | null }
    >({
      queryFn: ({ id, revision, companyId }) =>
        executeBoardlyQuery(
          () =>
            apiClient.delete<void>(`${path(companyId)}/events/${id}`, {
              params: { expected_revision: revision },
            }),
          "Could not delete your event.",
        ),
      invalidatesTags: [{ type: "Calendar", id: "events" }],
    }),
    changeCalendarOccurrence: build.mutation<
      void,
      { id: string; input: OccurrenceInput; companyId?: string | null }
    >({
      queryFn: ({ id, input, companyId }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<void>(
              `${path(companyId)}/events/${id}/occurrences`,
              input,
            ),
          "Could not update this occurrence.",
        ),
      invalidatesTags: [{ type: "Calendar", id: "events" }],
    }),
    respondCalendarInvitation: build.mutation<
      void,
      { id: string; input: ResponseInput; companyId?: string | null }
    >({
      queryFn: ({ id, input, companyId }) =>
        executeBoardlyQuery(
          () =>
            apiClient.post<void>(
              `${path(companyId)}/events/${id}/response`,
              input,
            ),
          "Could not respond to this invitation.",
        ),
      invalidatesTags: [{ type: "Calendar", id: "events" }],
    }),
  }),
});
export const {
  useCalendarCatalogQuery,
  useCalendarEventsQuery,
  useSaveCalendarEventMutation,
  useDeleteCalendarEventMutation,
  useChangeCalendarOccurrenceMutation,
  useRespondCalendarInvitationMutation,
} = api;
