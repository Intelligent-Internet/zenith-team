import { useState } from "react";
import { CalendarDays, Clock3, FileText, Plug } from "@/shared/ui/icons";
import { StudioArt } from "./studio-primitives";

const weekdays = [
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
  "Sunday",
];

export function StudioTools() {
  const [provider, setProvider] = useState("Codex");
  const [day, setDay] = useState(4);
  return (
    <section
      className="studio-tools-section studio-section"
      id="possibilities"
      aria-label="Connected tools and recurring work"
    >
      <div className="studio-container studio-tools-frame" data-reveal>
        <div className="studio-tools-connect">
          <h2>
            Your tools.
            <br />
            New possibilities.
          </h2>
          <p>Use the connected sources you choose.</p>
          <StudioArt
            name="research-brief"
            className="studio-research-art"
            alt="A research brief on a wooden stand"
          />
          <div className="studio-provider-card">
            <div>
              <Plug size={24} />
              <span>
                <strong>Connected tools</strong>
                <small>Reviewed access</small>
              </span>
            </div>
            <p>
              {provider === "Codex"
                ? "Connect the tools you use to run agent work."
                : "Bring your Claude Code setup into the team."}
            </p>
            <div
              className="studio-provider-options grid grid-cols-[1fr_1fr] gap-[9px]"
              role="group"
              aria-label="Explore a provider"
            >
              {["Codex", "Claude Code"].map((name) => (
                <button
                  type="button"
                  key={name}
                  aria-pressed={provider === name}
                  onClick={() => setProvider(name)}
                >
                  {name}
                </button>
              ))}
            </div>
          </div>
        </div>
        <div className="studio-schedule">
          <h2>A rhythm for the routine.</h2>
          <p>Use reviewed sources, on your schedule.</p>
          <div className="studio-schedule-card">
            <div className="studio-schedule-title">
              <span>
                <CalendarDays size={19} />
                <strong>{weekdays[day]} research digest</strong>
              </span>
              <small>Example output</small>
            </div>
            <fieldset className="studio-week">
              <legend className="absolute w-[1px] h-[1px] p-0 m-[-1px] overflow-hidden [clip:rect(0,_0,_0,_0)] whitespace-nowrap [border:0]">
                Choose the example schedule day
              </legend>
              {weekdays.map((name, index) => (
                <label
                  className={day === index ? "is-selected" : ""}
                  key={name}
                >
                  <input
                    type="radio"
                    name="studio-schedule-day"
                    value={name}
                    checked={day === index}
                    onChange={() => setDay(index)}
                    aria-label={name}
                  />
                  <span>{name.slice(0, 3)}</span>
                  <strong>{21 + index}</strong>
                </label>
              ))}
            </fieldset>
            <div className="studio-schedule-detail">
              <div>
                <Clock3 size={25} />
                <span>
                  Every {weekdays[day]}
                  <br />
                  9:00 AM
                </span>
              </div>
              <div>
                <FileText size={25} />
                <span>
                  Source roundup
                  <small>Creator news, trends, opportunities</small>
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
