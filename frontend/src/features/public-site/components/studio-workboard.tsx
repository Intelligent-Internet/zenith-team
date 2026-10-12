import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import {
  ArrowRight,
  BookOpen,
  Briefcase,
  Check,
  ChevronDown,
  Folder,
  Home,
  MessageSquare,
  Play,
  RotateCcw,
  Settings,
} from "@/shared/ui/icons";
import { StudioArt, StudioAvatar } from "./studio-primitives";
import { CoffeePreview } from "./studio-showcase";
import { useStudioMotion, useStudioVisibility } from "../lib/studio-motion";

const tasks = [
  {
    id: "launch",
    title: "Launch content",
    description: "Write and schedule launch social posts.",
    role: "Marketing",
    date: "Apr 25",
  },
  {
    id: "website",
    title: "Build the Daybreak website",
    description: "Build a first version for review.",
    role: "Engineering",
    date: "Apr 28",
  },
  {
    id: "interview",
    title: "Interview guide",
    description: "Questions for creator interviews.",
    role: "Product",
    date: "Apr 26",
  },
];

export function StudioWorkboard() {
  const [delivered, setDelivered] = useState(false);
  const [engaged, setEngaged] = useState(false);
  const { ref, visible } = useStudioVisibility<HTMLElement>(0.4);
  const { enabled } = useStudioMotion();
  const board = useRef<HTMLDivElement>(null);
  const boardViewport = useRef<HTMLDivElement>(null);
  const previousPositions = useRef(new Map<string, DOMRect>());
  const played = useRef(false);

  const moveTask = useCallback(() => {
    previousPositions.current.clear();
    board.current
      ?.querySelectorAll<HTMLElement>("[data-studio-task]")
      .forEach((element) => {
        previousPositions.current.set(
          element.dataset.studioTask!,
          element.getBoundingClientRect(),
        );
      });
    played.current = true;
    setDelivered((current) => !current);
  }, []);

  useLayoutEffect(() => {
    if (!enabled || !visible) {
      previousPositions.current.clear();
      return;
    }
    const animations: Animation[] = [];
    board.current
      ?.querySelectorAll<HTMLElement>("[data-studio-task]")
      .forEach((element) => {
        const before = previousPositions.current.get(
          element.dataset.studioTask!,
        );
        if (!before) return;
        const after = element.getBoundingClientRect();
        const x = before.left - after.left;
        const y = before.top - after.top;
        if (Math.abs(x) + Math.abs(y) < 1) return;
        animations.push(
          element.animate(
            [
              { transform: `translate(${x}px, ${y}px)`, zIndex: 3 },
              { transform: "translate(0, 0)", zIndex: 3 },
            ],
            { duration: 680, easing: "cubic-bezier(.22,1,.36,1)" },
          ),
        );
      });
    previousPositions.current.clear();
    return () => animations.forEach((animation) => animation.cancel());
  }, [delivered, enabled, visible]);

  useLayoutEffect(() => {
    if (!played.current || !window.matchMedia("(max-width: 767px)").matches)
      return;
    const viewport = boardViewport.current;
    const column = board.current
      ?.querySelector('[data-studio-task="website"]')
      ?.closest("section");
    if (!viewport || !column) return;
    viewport.scrollTo({
      left:
        viewport.scrollLeft +
        column.getBoundingClientRect().left -
        viewport.getBoundingClientRect().left,
      behavior: enabled ? "smooth" : "instant",
    });
  }, [delivered, enabled]);

  useEffect(() => {
    if (
      !enabled ||
      !visible ||
      engaged ||
      played.current ||
      window.matchMedia("(max-width: 767px)").matches
    )
      return;
    const timer = window.setTimeout(() => {
      if (!window.matchMedia("(max-width: 767px)").matches) moveTask();
    }, 4200);
    return () => window.clearTimeout(timer);
  }, [enabled, visible, engaged, moveTask]);

  return (
    <section
      className="studio-work studio-section"
      id="work"
      aria-labelledby="studio-work-title"
      ref={ref}
    >
      <StudioArt
        name="work-plants-left"
        className="studio-work-plant studio-work-plant--left"
      />
      <StudioArt
        name="work-plants-right"
        className="studio-work-plant studio-work-plant--right"
      />
      <div className="studio-container">
        <div className="studio-section-heading" data-reveal>
          <h2 id="studio-work-title">
            Know who is on it.
            <br />
            See what is <em>moving.</em>
          </h2>
          <p>Different skills. A shared plan. Real progress.</p>
        </div>
        <div className="studio-work-surface" data-reveal>
          <div
            className="studio-board"
            ref={board}
            onMouseEnter={() => setEngaged(true)}
            onMouseLeave={() => setEngaged(false)}
            onFocusCapture={() => setEngaged(true)}
            onBlurCapture={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget))
                setEngaged(false);
            }}
          >
            <div className="studio-board-top">
              <span>
                <Briefcase size={21} />
                <strong>Daybreak</strong>
                <span> / Work</span>
              </span>
              <span>
                <button
                  type="button"
                  className="studio-board-play"
                  onClick={moveTask}
                  aria-label={
                    delivered ? "Reset workboard demo" : "Preview task progress"
                  }
                >
                  {delivered ? <RotateCcw size={14} /> : <Play size={14} />}
                  <span>{delivered ? "Replay" : "See progress"}</span>
                </button>
                <small>
                  Example company <ChevronDown size={12} />
                </small>
              </span>
            </div>
            <div className="studio-board-body" ref={boardViewport}>
              <div className="studio-board-sidebar" aria-hidden="true">
                {[
                  [Home, "Home"],
                  [Briefcase, "Work"],
                  [MessageSquare, "Chat"],
                  [BookOpen, "Knowledge"],
                  [Folder, "Files"],
                  [Settings, "Settings"],
                ].map(([Icon, name]) => {
                  const Glyph = Icon as typeof Home;
                  return (
                    <span
                      className={name === "Work" ? "is-selected" : ""}
                      key={String(name)}
                    >
                      <Glyph size={17} />
                      {String(name)}
                    </span>
                  );
                })}
              </div>
              <div className="studio-board-columns">
                {["To do", "In progress", "In review"].map((title, index) => {
                  const columnTasks = tasks.filter((task) =>
                    task.id === "launch"
                      ? index === 0
                      : task.id === "website"
                        ? index === (delivered ? 2 : 1)
                        : index === 2,
                  );
                  return (
                    <section
                      className="studio-board-column"
                      aria-label={title}
                      key={title}
                    >
                      <h3>
                        {title}
                        <span>{columnTasks.length}</span>
                      </h3>
                      <div className="studio-board-cards">
                        {columnTasks.map((task) => (
                          <article
                            className={`studio-task-card ${task.id === "website" ? "studio-task-card--website" : ""}`}
                            key={task.id}
                            data-studio-task={task.id}
                          >
                            <h4>{task.title}</h4>
                            <p>{task.description}</p>
                            <div className="studio-task-owner">
                              <StudioAvatar role={task.role} size={30} />
                              <span>{task.role}</span>
                              <small>{task.date}</small>
                            </div>
                            {task.id === "website" && (
                              <div className="studio-task-preview">
                                <CoffeePreview compact />
                              </div>
                            )}
                          </article>
                        ))}
                        {index === 1 && delivered && (
                          <p className="studio-board-empty">
                            A first version, ready
                            <br />
                            for your fresh eyes.
                          </p>
                        )}
                      </div>
                      {index === 2 && (
                        <div
                          className={`studio-board-success ${delivered ? "is-delivered" : ""}`}
                        >
                          <span>
                            <Check size={12} />
                          </span>
                          {delivered
                            ? "First website ready for review."
                            : "Brand story accepted."}
                        </div>
                      )}
                    </section>
                  );
                })}
              </div>
            </div>
            <p className="studio-board-hint">
              Swipe to explore your team’s work <ArrowRight size={12} />
            </p>
            <span className="studio-sr-only" role="status">
              {delivered
                ? "The website task has moved from In progress to In review."
                : "The website task is in progress."}
            </span>
          </div>
        </div>
      </div>
    </section>
  );
}
