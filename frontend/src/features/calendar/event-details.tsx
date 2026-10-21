import { recurrenceSummary } from "./calendar-recurrence";
import { useState, type CSSProperties } from "react";
import { toast } from "sonner";
import {
  CalendarDays,
  ChevronDown,
  Map,
  Pencil,
  Users,
} from "@/shared/ui/icons";
import {
  CalendarEventDialog,
  type CalendarEventAnchor,
} from "./calendar-event-dialog";
import { getZenithTeamQueryErrorMessage } from "@/shared/api/zenith-team-api";
import {
  useChangeCalendarOccurrenceMutation,
  useRespondCalendarInvitationMutation,
  type CalendarOccurrence,
} from "./calendar-api";
import { addDays, dayAt } from "./calendar-dates";
import { EventParticipants, responseLabels } from "./event-participants";
import { useCalendarTheme } from "./calendar-theme";

export function EventDetails({
  event,
  anchor,
  onClose,
  onEdit,
}: {
  event: CalendarOccurrence;
  anchor: CalendarEventAnchor;
  onClose: () => void;
  onEdit?: () => void;
}) {
  const theme = useCalendarTheme();
  const [respond, state] = useRespondCalendarInvitationMutation();
  const [change, changing] = useChangeCalendarOccurrenceMutation();
  const busy = state.isLoading || changing.isLoading;
  const [error, setError] = useState("");
  const [moreOpen, setMoreOpen] = useState(false);
  const lastDay = addDays(dayAt(event.endLocal.slice(0, 10)), -1);
  const date = (
    event.allDay ? dayAt(event.startLocal) : new Date(event.startAt)
  ).toLocaleDateString([], {
    dateStyle: "medium",
    ...(event.allDay ? {} : { timeZone: event.timeZone }),
  });
  async function reply(responseStatus: "accepted" | "tentative" | "declined") {
    setError("");
    try {
      await respond({
        id: event.id,
        companyId: event.companyId,
        input: { expectedRevision: event.revision, responseStatus },
      }).unwrap();
      toast.success("Response saved");
      onClose();
    } catch (error) {
      setError(getZenithTeamQueryErrorMessage(error));
    }
  }
  return (
    <CalendarEventDialog
      anchor={anchor}
      busy={busy}
      style={
        {
          ...theme,
          "--event-color": `var(--calendar-${event.color})`,
        } as CSSProperties
      }
      headingActions={
        onEdit && (
          <button
            className="personal-icon-button"
            aria-label="Edit event"
            disabled={busy}
            onClick={onEdit}
          >
            <Pencil size={18} />
          </button>
        )
      }
      className="calendar-detail-dialog"
      title={event.title}
      description={
        event.calendarType === "company"
          ? `${event.calendarName} · Company calendar`
          : event.attendees.length
            ? "Invited guests only"
            : "My calendar"
      }
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <div className="calendar-event-details">
        <p>
          <CalendarDays size={18} />
          <span>
            {date}
            <small>
              {event.allDay
                ? `All day${lastDay.getTime() > dayAt(event.startLocal).getTime() ? ` · through ${lastDay.toLocaleDateString([], { dateStyle: "medium" })}` : ""}`
                : `${new Date(event.startAt).toLocaleTimeString([], { hour: "numeric", minute: "2-digit", timeZone: event.timeZone })} – ${new Date(event.endAt).toLocaleString([], { ...(event.startLocal.slice(0, 10) !== event.endLocal.slice(0, 10) ? ({ month: "short", day: "numeric" } as const) : {}), hour: "numeric", minute: "2-digit", timeZone: event.timeZone })}`}
            </small>
            {!event.allDay &&
              event.timeZone !==
                Intl.DateTimeFormat().resolvedOptions().timeZone && (
                <small>{event.timeZone}</small>
              )}
            {event.recurrence !== "none" && (
              <small>{recurrenceSummary(event, event.seriesStartLocal)}</small>
            )}
          </span>
        </p>
        {event.location && (
          <p>
            <Map size={18} />
            <span>{event.location}</span>
          </p>
        )}
        {event.attendees.length > 0 && (
          <div className="calendar-detail-guests flex gap-[16px] items-start">
            <Users size={18} />
            <EventParticipants attendees={event.attendees} />
          </div>
        )}
        <button
          type="button"
          className="calendar-more-toggle"
          aria-expanded={moreOpen}
          onClick={() => setMoreOpen(!moreOpen)}
        >
          {moreOpen ? "Fewer details" : "More details"}
          <ChevronDown size={14} />
        </button>
        {moreOpen && (
          <div className="calendar-detail-meta">
            <p>
              {event.calendarName} ·{" "}
              {event.calendarType === "company"
                ? "Company calendar"
                : "Personal calendar"}
            </p>
            <p>Organized by {event.organizerName}</p>
            <p>{event.timeZone}</p>
            {event.description && (
              <p className="calendar-event-description">{event.description}</p>
            )}
          </div>
        )}
        {event.responseStatus && (
          <section
            className="calendar-rsvp grid gap-[12px] py-[16px] [border-top:1px_solid_var(--workspace-border)]"
            aria-label="Your response"
          >
            <strong>
              {event.responseStatus === "needs_action"
                ? "Will you join?"
                : `Your response: ${responseLabels[event.responseStatus]}`}
            </strong>
            {event.recurrence !== "none" && (
              <p className="personal-note">
                Your response applies to the entire series.
              </p>
            )}
            <div>
              {(["accepted", "tentative", "declined"] as const).map(
                (status) => (
                  <button
                    key={status}
                    className={`personal-button ${event.responseStatus === status ? "personal-button--primary" : ""}`}
                    disabled={busy}
                    aria-pressed={event.responseStatus === status}
                    onClick={() => void reply(status)}
                  >
                    {responseLabels[status]}
                  </button>
                ),
              )}
            </div>
          </section>
        )}
        {error && (
          <p
            className="text-[length:13px] leading-[1.6] text-workspace-danger p-[10px_0]"
            role="alert"
          >
            {error}
          </p>
        )}
        <div className="personal-dialog-actions">
          {event.type === "reminder" && (
            <button
              className="personal-button"
              disabled={busy}
              onClick={() => {
                setError("");
                void change({
                  id: event.id,
                  companyId: event.companyId,
                  input: {
                    expectedRevision: event.revision,
                    occurrenceDate: event.occurrenceDate,
                    action: event.completed ? "reopen" : "complete",
                  },
                })
                  .unwrap()
                  .then(onClose)
                  .catch((error: unknown) =>
                    setError(getZenithTeamQueryErrorMessage(error)),
                  );
              }}
            >
              {event.completed ? "Reopen" : "Mark done"}
            </button>
          )}
          <button className="personal-button" onClick={onClose} disabled={busy}>
            Close
          </button>
        </div>
      </div>
    </CalendarEventDialog>
  );
}
