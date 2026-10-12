import type { CSSProperties, MouseEventHandler } from "react";
import { Bell, Check, Users } from "@/shared/ui/icons";
import type { CalendarOccurrence } from "./calendar-api";
import { timeRangeLabel } from "./calendar-dates";

export function CalendarEventChip({
  event,
  onClick,
  style,
  compact = false,
}: {
  event: CalendarOccurrence;
  onClick: MouseEventHandler<HTMLButtonElement>;
  style?: CSSProperties;
  compact?: boolean;
}) {
  return (
    <button
      className={`calendar-event ${compact ? "is-compact" : ""} ${event.completed ? "is-completed" : ""} ${event.responseStatus === "declined" ? "is-declined" : ""} ${event.responseStatus === "needs_action" ? "is-invited" : ""}`}
      style={
        {
          "--event-color": `var(--calendar-${event.color})`,
          ...style,
        } as CSSProperties
      }
      onClick={onClick}
      aria-label={`${event.title} · ${timeRangeLabel(event)}`}
      title={`${event.title} · ${timeRangeLabel(event)}`}
    >
      {event.type === "reminder" &&
        (event.completed ? <Check size={12} /> : <Bell size={12} />)}
      {event.type === "event" && event.attendees.length > 0 && (
        <Users size={12} />
      )}
      <span>
        <strong>{event.title}</strong>
        {!event.allDay && !compact && <small>{timeRangeLabel(event)}</small>}
      </span>
    </button>
  );
}
