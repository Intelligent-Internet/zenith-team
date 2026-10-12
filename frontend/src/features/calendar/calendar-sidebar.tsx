import { ChevronLeft, ChevronRight } from "@/shared/ui/icons";
import type { CalendarCatalog } from "./calendar-api";
import { dateKey, monthDays } from "./calendar-dates";
const weekdays = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
export function CalendarSidebar({
  date,
  setDate,
  today,
  catalog,
  companyId,
  companyName,
  showPersonal,
  setShowPersonal,
  showCompany,
  setShowCompany,
  showDeclined,
  setShowDeclined,
  showEvents,
  setShowEvents,
  showReminders,
  setShowReminders,
}: {
  date: Date;
  setDate: (date: Date) => void;
  today: string;
  catalog?: CalendarCatalog;
  companyId?: string;
  companyName?: string;
  showPersonal: boolean;
  setShowPersonal: (value: boolean) => void;
  showCompany: boolean;
  setShowCompany: (value: boolean) => void;
  showDeclined: boolean;
  setShowDeclined: (value: boolean) => void;
  showEvents: boolean;
  setShowEvents: (value: boolean) => void;
  showReminders: boolean;
  setShowReminders: (value: boolean) => void;
}) {
  return (
    <aside className="calendar-sidebar" aria-label="Calendars and filters">
      <header className="calendar-heading">
        <h1>Calendar</h1>
      </header>
      <div className="calendar-mini-heading">
        <strong>
          {date.toLocaleDateString([], { month: "long", year: "numeric" })}
        </strong>
        <div>
          <button
            className="personal-icon-button"
            aria-label="Previous month"
            onClick={() =>
              setDate(new Date(date.getFullYear(), date.getMonth() - 1, 1))
            }
          >
            <ChevronLeft size={15} />
          </button>
          <button
            className="personal-icon-button"
            aria-label="Next month"
            onClick={() =>
              setDate(new Date(date.getFullYear(), date.getMonth() + 1, 1))
            }
          >
            <ChevronRight size={15} />
          </button>
        </div>
      </div>
      <div className="calendar-mini">
        <div className="calendar-mini-weekdays">
          {weekdays.map((d) => (
            <span key={d}>{d[0]}</span>
          ))}
        </div>
        <div className="calendar-mini-days">
          {monthDays(date).map((d) => (
            <button
              key={dateKey(d)}
              className={`${d.getMonth() !== date.getMonth() ? "is-muted" : ""} ${dateKey(d) === today ? "is-today" : ""}`}
              aria-label={d.toLocaleDateString([], { dateStyle: "full" })}
              aria-pressed={dateKey(d) === dateKey(date)}
              onClick={() => setDate(d)}
            >
              {d.getDate()}
            </button>
          ))}
        </div>
      </div>
      <div className="calendar-sidebar-section">
        <h2>Calendars</h2>
        <label>
          <input
            type="checkbox"
            checked={showPersonal}
            onChange={(event) => setShowPersonal(event.target.checked)}
          />
          <span>My calendar & invitations</span>
        </label>
        {companyId && (
          <label>
            <input
              type="checkbox"
              checked={showCompany}
              onChange={(event) => setShowCompany(event.target.checked)}
            />
            <span>
              {catalog?.calendars.find(
                (calendar) => calendar.type === "company",
              )?.name ?? companyName}{" "}
              · Company
            </span>
          </label>
        )}
      </div>
      <div className="calendar-sidebar-section">
        <h2>Show</h2>
        <label>
          <input
            type="checkbox"
            checked={showEvents}
            onChange={(e) => setShowEvents(e.target.checked)}
          />
          Events
        </label>
        <label>
          <input
            type="checkbox"
            checked={showReminders}
            onChange={(e) => setShowReminders(e.target.checked)}
          />
          Reminders
        </label>
        <label>
          <input
            type="checkbox"
            checked={showDeclined}
            onChange={(event) => setShowDeclined(event.target.checked)}
          />{" "}
          Declined invitations
        </label>
      </div>
    </aside>
  );
}
