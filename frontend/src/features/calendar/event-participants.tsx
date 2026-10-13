import { useState } from "react";
import { CalendarDays, ChevronDown, Users, XIcon } from "@/shared/ui/icons";
import { Avatar, AvatarFallback } from "@/shared/ui/core/avatar";
import { CalendarOptionPicker } from "./calendar-pickers";
import type {
  CalendarAttendee,
  CalendarCatalog,
  CalendarInput,
} from "./calendar-api";

export const responseLabels = {
  needs_action: "Awaiting reply",
  accepted: "Going",
  tentative: "Maybe",
  declined: "Not going",
} as const;

export function EventParticipants({
  attendees,
}: {
  attendees: CalendarAttendee[];
}) {
  if (!attendees.length) return null;
  return (
    <ul className="calendar-participants" aria-label="Event guests">
      {attendees.map((guest) => (
        <li key={guest.userId}>
          <Avatar className="calendar-guest-avatar">
            <AvatarFallback>
              {(guest.displayName || guest.email)
                .trim()
                .slice(0, 1)
                .toUpperCase()}
            </AvatarFallback>
          </Avatar>
          <span title={guest.email}>
            <strong>{guest.displayName || guest.email}</strong>
          </span>
          <span
            className={`calendar-response calendar-response--${guest.responseStatus}`}
          >
            {responseLabels[guest.responseStatus]}
          </span>
        </li>
      ))}
    </ul>
  );
}

export function EventSharingFields({
  input,
  patch,
  catalog,
  organizerUserId,
  editing,
  attendees,
}: {
  input: CalendarInput;
  patch: (value: Partial<CalendarInput>) => void;
  catalog: CalendarCatalog;
  organizerUserId: string;
  editing: boolean;
  attendees: CalendarAttendee[];
}) {
  const [search, setSearch] = useState("");
  const [guestsOpen, setGuestsOpen] = useState(false);
  const target =
    catalog.calendars.find((calendar) => calendar.id === input.calendarId) ??
    catalog.calendars.find((calendar) => calendar.type === "personal");
  const selected = input.attendeeUserIds ?? [];
  const members = catalog.members.filter(
    (member) =>
      member.userId !== organizerUserId &&
      `${member.displayName} ${member.email}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
  return (
    <section className="calendar-sharing" aria-label="Calendar and guests">
      <div className="grid grid-cols-[17px_minmax(0,_1fr)] items-center gap-[10px] text-[var(--workspace-text-muted)]">
        <CalendarDays size={17} aria-hidden="true" />
        <CalendarOptionPicker
          label="Calendar"
          value={input.calendarId ?? target?.id ?? ""}
          disabled={editing}
          options={catalog.calendars.map((calendar) => ({
            value: calendar.id,
            label:
              calendar.type === "company"
                ? `${calendar.name} · Company`
                : "My calendar",
            description:
              calendar.type === "company"
                ? "Visible to every member"
                : "Private until you invite someone",
          }))}
          onChange={(calendarId) => patch({ calendarId })}
        />
      </div>
      <div className="calendar-selected-guests flex flex-wrap items-center gap-[7px] text-[var(--workspace-text-muted)] pt-[5px]">
        <Users size={17} aria-hidden="true" />
        {selected.map((id) => {
          const guest =
            catalog.members.find((member) => member.userId === id) ??
            attendees.find((member) => member.userId === id);
          if (!guest) return null;
          const name = guest.displayName || guest.email;
          return (
            <span
              className="calendar-guest-chip inline-flex items-center gap-[5px] rounded-[20px] [background:var(--workspace-panel-raised-bg)] p-[3px_5px_3px_3px] min-w-0 max-w-[190px]"
              key={id}
            >
              <Avatar className="calendar-guest-avatar">
                <AvatarFallback>
                  {name.trim().slice(0, 1).toUpperCase()}
                </AvatarFallback>
              </Avatar>
              <span>{name}</span>
              <button
                type="button"
                aria-label={`Remove ${name}`}
                onClick={() =>
                  patch({
                    attendeeUserIds: selected.filter((userId) => userId !== id),
                  })
                }
              >
                <XIcon size={12} />
              </button>
            </span>
          );
        })}
        <button
          type="button"
          className="calendar-guests-toggle"
          aria-expanded={guestsOpen}
          onClick={() => setGuestsOpen((current) => !current)}
        >
          Add guests
          <ChevronDown size={14} aria-hidden="true" />
        </button>
      </div>
      {guestsOpen && (
        <div className="calendar-guests-panel">
          <label>
            <span className="sr-only">Find a company member</span>
            <input
              type="search"
              placeholder="Find a company member…"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <div className="max-h-[220px] overflow-y-auto grid gap-[4px]">
            {members.length ? (
              members.map((member) => {
                const isSelected = selected.includes(member.userId);
                const status = attendees.find(
                  (guest) => guest.userId === member.userId,
                )?.responseStatus;
                return (
                  <label
                    key={member.userId}
                    className="calendar-guest-option"
                    data-selected={isSelected}
                  >
                    <input
                      type="checkbox"
                      checked={isSelected}
                      onChange={(event) =>
                        patch({
                          attendeeUserIds: event.target.checked
                            ? [...selected, member.userId]
                            : selected.filter((id) => id !== member.userId),
                        })
                      }
                    />
                    <span className="calendar-guest-identity">
                      <strong>{member.displayName || member.email}</strong>
                      <small>{member.email}</small>
                    </span>
                    {status && isSelected && (
                      <small className="calendar-response">
                        {responseLabels[status]}
                      </small>
                    )}
                  </label>
                );
              })
            ) : (
              <p className="personal-note">
                {search
                  ? "No matching members."
                  : "No other active members in this company yet."}
              </p>
            )}
          </div>
          {editing && selected.length > 0 && (
            <p className="personal-note">
              Changing the time or repeat schedule asks guests to respond again.
            </p>
          )}
        </div>
      )}
    </section>
  );
}
