import { useId, useRef, useState } from "react";
import { CalendarDays, Map, UserRound, X } from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";
import mapPreview from "./utility-map-preview.svg";

const utilities = [
  { id: "calendar", label: "Google Calendar", Icon: CalendarDays },
  { id: "maps", label: "Google Maps", Icon: Map },
  { id: "contacts", label: "Contacts", Icon: UserRound },
] as const;
type Utility = (typeof utilities)[number];

export function CalendarUtilities() {
  const id = useId();
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  const [selected, setSelected] = useState<Utility>(utilities[0]);
  const [open, setOpen] = useState(false);

  function close() {
    setOpen(false);
    tabs.current[utilities.indexOf(selected)]?.focus({ preventScroll: true });
  }

  return (
    <aside
      className="calendar-utilities"
      aria-label="Calendar utilities"
      data-open={open}
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          event.preventDefault();
          event.stopPropagation();
          close();
        }
      }}
    >
      <div
        className="calendar-utility-tabs"
        role="tablist"
        aria-label="Calendar utilities"
        aria-orientation="vertical"
      >
        {utilities.map((utility, index) => (
          <Tooltip key={utility.id} label={utility.label} disabled={open}>
            <button
              ref={(element) => {
                tabs.current[index] = element;
              }}
              id={`${id}-${utility.id}-tab`}
              type="button"
              role="tab"
              className="calendar-utility-trigger"
              aria-label={utility.label}
              aria-selected={open && selected.id === utility.id}
              aria-expanded={open && selected.id === utility.id}
              aria-controls={
                open && selected.id === utility.id ? `${id}-panel` : undefined
              }
              tabIndex={selected.id === utility.id ? 0 : -1}
              onClick={() => {
                setSelected(utility);
                setOpen(!open || selected.id !== utility.id);
              }}
              onKeyDown={(event) => {
                let next: number;
                switch (event.key) {
                  case "ArrowDown":
                    next = (index + 1) % utilities.length;
                    break;
                  case "ArrowUp":
                    next = (index + utilities.length - 1) % utilities.length;
                    break;
                  case "Home":
                    next = 0;
                    break;
                  case "End":
                    next = utilities.length - 1;
                    break;
                  default:
                    return;
                }
                event.preventDefault();
                setSelected(utilities[next]);
                setOpen(true);
                tabs.current[next]?.focus({ preventScroll: true });
              }}
            >
              <utility.Icon size={21} aria-hidden="true" />
            </button>
          </Tooltip>
        ))}
      </div>
      <div className="calendar-utility-panel-shell">
        {open && (
          <section
            id={`${id}-panel`}
            className="calendar-utility-panel"
            role="tabpanel"
            tabIndex={0}
            aria-labelledby={`${id}-${selected.id}-tab`}
          >
            <header className="calendar-utility-heading">
              <h2>{selected.label}</h2>
              <button
                type="button"
                className="personal-icon-button"
                aria-label="Close utility"
                onClick={close}
              >
                <X size={18} aria-hidden="true" />
              </button>
            </header>
            <div className="calendar-utility-content">
              <div className="calendar-utility-feature" key={selected.id}>
                <UtilityPreview utility={selected} />
                <p className="calendar-utility-status">Coming soon</p>
              </div>
              <div className="calendar-utility-shortcuts">
                {utilities
                  .filter((utility) => utility.id !== selected.id)
                  .map((utility) => (
                    <button
                      key={utility.id}
                      type="button"
                      onClick={() => {
                        setSelected(utility);
                        tabs.current[utilities.indexOf(utility)]?.focus({
                          preventScroll: true,
                        });
                      }}
                    >
                      <span>{utility.label}</span>
                      <span className="calendar-utility-shortcut-status">
                        Coming soon
                      </span>
                    </button>
                  ))}
              </div>
            </div>
          </section>
        )}
      </div>
    </aside>
  );
}

function UtilityPreview({ utility }: { utility: Utility }) {
  return (
    <div
      className={`calendar-utility-preview calendar-utility-preview--${utility.id}`}
      aria-hidden="true"
    >
      {utility.id === "maps" ? (
        <>
          <img src={mapPreview} alt="" />
          <span className="calendar-utility-map-symbol">
            <Map size={27} />
          </span>
        </>
      ) : utility.id === "calendar" ? (
        <div className="calendar-utility-miniature">
          <div className="calendar-utility-miniature-heading">
            <CalendarDays size={18} />
            <span />
          </div>
          <div className="calendar-utility-miniature-grid">
            {Array.from({ length: 28 }, (_, index) => (
              <span key={index}>{index + 1}</span>
            ))}
          </div>
        </div>
      ) : (
        <div className="calendar-utility-people">
          {[0, 1, 2].map((index) => (
            <div key={index}>
              <span>
                <UserRound size={20} />
              </span>
              <div>
                <i />
                <i />
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
