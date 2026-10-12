import {
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
} from "react";
import {
  ArrowRight,
  Check,
  ChevronDown,
  Circle,
  FileText,
  Send,
} from "@/shared/ui/icons";
import { StudioAvatar } from "./studio-primitives";
import { useStudioScrollSteps } from "../lib/use-studio-scroll-steps";

const steps = [
  {
    title: "Share the idea",
    description: "Tell your AI CEO what you’re building.",
  },
  { title: "Review the plan", description: "Look over the proposed plan." },
  { title: "Start the work", description: "Turn the plan into tasks." },
  {
    title: "Review the result",
    description: "See what’s ready and give feedback.",
  },
];

const proposedTasks = [
  {
    title: "Brand story",
    description: "Clarify our story and key messages.",
    role: "Marketing",
  },
  {
    title: "First website",
    description: "Design and build a first version.",
    role: "Engineering",
  },
  {
    title: "Interview guide",
    description: "Create questions for creator interviews.",
    role: "Product",
  },
];

export function StudioWorkflow() {
  const tabs = useRef<Array<HTMLButtonElement | null>>([]);
  const [horizontal, setHorizontal] = useState(
    () => window.matchMedia("(max-width: 767px)").matches,
  );
  const { track, pin, step, pinned, selectStep } = useStudioScrollSteps(
    steps.length,
  );
  useEffect(() => {
    const media = window.matchMedia("(max-width: 767px)");
    const update = () => setHorizontal(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  const onTabKey = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let next: number;
    if (event.key === "ArrowDown" || event.key === "ArrowRight")
      next = (index + 1) % steps.length;
    else if (event.key === "ArrowUp" || event.key === "ArrowLeft")
      next = (index + steps.length - 1) % steps.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = steps.length - 1;
    else return;
    event.preventDefault();
    event.stopPropagation();
    tabs.current[next]?.focus({ preventScroll: true });
    selectStep(next);
  };

  return (
    <section
      ref={track}
      className="studio-workflow studio-section"
      id="how-it-works"
      aria-labelledby="studio-plan-title"
      style={{ "--workflow-step-count": steps.length } as CSSProperties}
    >
      <div ref={pin} className="studio-workflow-pin">
        <div className="studio-container studio-workflow-grid">
          <div className="studio-workflow-copy" data-reveal>
            <h2 id="studio-plan-title">
              Start with a thought.
              <br />
              Shape a <em>company plan.</em>
            </h2>
            <div
              className="studio-steps"
              role="tablist"
              aria-label="How it works"
              aria-orientation={horizontal ? "horizontal" : "vertical"}
              style={{ "--active-step": step } as CSSProperties}
            >
              {steps.map((item, index) => (
                <button
                  ref={(element) => {
                    tabs.current[index] = element;
                  }}
                  type="button"
                  role="tab"
                  id={`studio-step-${index}`}
                  aria-controls="studio-plan-panel"
                  aria-selected={step === index}
                  tabIndex={step === index ? 0 : -1}
                  className={`studio-step ${step === index ? "is-active" : ""}`}
                  key={item.title}
                  onClick={() => selectStep(index)}
                  onKeyDown={(event) => onTabKey(event, index)}
                >
                  <span className="studio-step-number">0{index + 1}</span>
                  <span className="studio-step-point" />
                  <span>
                    <strong>{item.title}</strong>
                    <small>{item.description}</small>
                  </span>
                </button>
              ))}
            </div>
          </div>
          <div className="studio-plan-demo" data-reveal>
            <p className="studio-feature-intro">
              A clear next step, with owners and room for your judgment.
            </p>
            <div
              className="studio-plan-panel"
              id="studio-plan-panel"
              role="tabpanel"
              aria-labelledby={`studio-step-${step}`}
              tabIndex={0}
            >
              <div className="studio-panel-top">
                <span>
                  <strong>Daybreak</strong>
                  <span>
                    {" "}
                    /{" "}
                    {step === 0
                      ? "Your idea"
                      : step === 1
                        ? "Proposed plan"
                        : step === 2
                          ? "Team at work"
                          : "Ready for review"}
                  </span>
                </span>
                <small>
                  Example company <ChevronDown size={11} />
                </small>
              </div>
              <div className="studio-plan-scene" key={step}>
                {step === 0 ? (
                  <div className="studio-idea-demo">
                    <span className="studio-example-note">
                      It starts with a conversation
                    </span>
                    <p className="studio-idea-message">
                      I want to build a coffee brand for independent creators.
                    </p>
                    <div className="studio-ceo-reply">
                      <StudioAvatar role="CEO" size={42} />
                      <div>
                        <strong>Your CEO</strong>
                        <p>
                          Let’s give it a clear direction. We’ll start with your
                          story, a first website, and a few good customer
                          conversations.
                        </p>
                      </div>
                    </div>
                    <button
                      className="studio-button"
                      type="button"
                      onClick={() => selectStep(1)}
                    >
                      Shape the plan <Send size={15} />
                    </button>
                  </div>
                ) : step === 1 || step === 2 ? (
                  <>
                    <p className="studio-plan-count">
                      {step === 1
                        ? "3 proposed tasks"
                        : "A shared plan. Clear owners."}
                    </p>
                    <div className="studio-plan-tasks">
                      {proposedTasks.map((task, index) => (
                        <div
                          className="studio-plan-task"
                          key={task.title}
                          style={
                            {
                              "--row-delay": `${index * 65}ms`,
                            } as CSSProperties
                          }
                        >
                          <span className="studio-plan-check">
                            {step === 1 ? (
                              <Circle size={16} />
                            ) : (
                              <Check size={16} />
                            )}
                          </span>
                          <StudioAvatar role={task.role} size={39} />
                          <div>
                            <strong>{task.title}</strong>
                            <small>
                              {step === 1
                                ? task.description
                                : index === 1
                                  ? "Building a first version for your review."
                                  : "Your specialist is on it."}
                            </small>
                          </div>
                          <span
                            className={`studio-owner studio-owner--${task.role.toLowerCase()}`}
                          >
                            <FileText size={15} />
                            {step === 1
                              ? task.role
                              : index === 1
                                ? "In progress"
                                : "To do"}
                          </span>
                        </div>
                      ))}
                    </div>
                    <div className="studio-plan-bottom">
                      <span>
                        {step === 2 ? "You decide what happens next." : ""}
                      </span>
                      <button
                        type="button"
                        className="studio-button"
                        onClick={() => selectStep(step + 1)}
                      >
                        {step === 1 ? "Review plan" : "See the result"}
                        <ArrowRight size={16} />
                      </button>
                    </div>
                  </>
                ) : (
                  <div className="studio-result-demo">
                    <div className="studio-result-icon">
                      <FileText size={28} />
                    </div>
                    <span className="studio-review-pill">In review</span>
                    <h3>Your first website is ready.</h3>
                    <p>
                      Open the result. Share a little feedback.
                      <br />
                      Make the next version your own.
                    </p>
                    <a className="studio-button" href="#your-call">
                      Take a closer look <ArrowRight size={16} />
                    </a>
                  </div>
                )}
              </div>
              <span className="studio-demo-caption">
                <span>
                  {pinned
                    ? step === steps.length - 1
                      ? "Keep scrolling. See what comes next."
                      : "Scroll to explore. The direction stays yours."
                    : "Explore the four steps at your own pace."}
                </span>
                <span className="studio-step-counter" aria-hidden="true">
                  0{step + 1} / 04
                </span>
              </span>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
