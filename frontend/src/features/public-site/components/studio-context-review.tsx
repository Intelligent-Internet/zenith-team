import { useEffect, useRef, useState, type FormEvent } from "react";
import {
  ArrowRight,
  Check,
  FileText,
  Folder,
  RotateCcw,
  Users,
} from "@/shared/ui/icons";
import { StudioArt, ExampleWindow, revealDelay } from "./studio-primitives";
import { CoffeePreview } from "./studio-showcase";

const contextSources = [
  { name: "Figma", description: "Designs & brand guidelines", asset: "figma" },
  { name: "Gmail", description: "Customer conversations", asset: "gmail" },
  { name: "Slack", description: "Team discussions & updates", asset: "slack" },
];
const companyContext = [
  {
    name: "Brand voice",
    description: "Your tone. Your personality.",
    Icon: FileText,
  },
  {
    name: "Audience",
    description: "The people you’re building for.",
    Icon: Users,
  },
  {
    name: "Decisions",
    description: "What matters. Where to go next.",
    Icon: Folder,
  },
];

export function StudioContext() {
  return (
    <section
      className="studio-knowledge studio-section"
      id="company-context"
      aria-labelledby="studio-context-title"
    >
      <div className="studio-container">
        <header className="studio-knowledge-heading">
          <span className="studio-knowledge-eyebrow">
            YOUR CONTEXT, CONNECTED
          </span>
          <h2 id="studio-context-title">
            Many strengths.
            <br />
            <em>One shared direction.</em>
          </h2>
          <p>
            Bring your brief, your decisions, and the tools you use.
            <br className="studio-knowledge-break" /> Give your team the context
            behind the work.
          </p>
        </header>
        <div className="studio-knowledge-map">
          <div className="studio-knowledge-sources">
            <h3>From your tools</h3>
            <ul>
              {contextSources.map(({ name, description, asset }) => (
                <li className="studio-knowledge-card" key={name}>
                  <span className="studio-knowledge-logo">
                    <img
                      src={`/images/home/approved/approved-home-connector-${asset}.png`}
                      alt=""
                      width="64"
                      height="64"
                      loading="lazy"
                      decoding="async"
                    />
                  </span>
                  <span>
                    <strong>{name}</strong>
                    <small>{description}</small>
                  </span>
                </li>
              ))}
            </ul>
          </div>
          <div className="studio-knowledge-center">
            <span className="studio-knowledge-center-label">
              <Folder size={16} aria-hidden="true" /> Company context
            </span>
            <StudioArt
              name="company-context"
              alt="CEO and Product Folds with the Daybreak company brief, brand voice, audience and decisions"
            />
            <p>
              A clearer starting point.
              <br />
              <strong>For every agent on your team.</strong>
            </p>
          </div>
          <div className="studio-knowledge-sources studio-knowledge-sources--brief">
            <h3>With your direction</h3>
            <ul>
              {companyContext.map(({ name, description, Icon }) => (
                <li className="studio-knowledge-card" key={name}>
                  <span className="studio-knowledge-symbol">
                    <Icon size={27} aria-hidden="true" />
                  </span>
                  <span>
                    <strong>{name}</strong>
                    <small>{description}</small>
                  </span>
                </li>
              ))}
            </ul>
          </div>
        </div>
        <p className="studio-knowledge-footnote">
          Connect through plugins and connectors. You choose the sources your
          agents can use.
          <br />
          Available connections depend on your provider and setup.
        </p>
      </div>
    </section>
  );
}

export function StudioReview() {
  const [feedback, setFeedback] = useState("Make the opening feel warmer.");
  const [status, setStatus] = useState<"idle" | "updating" | "ready">("idle");
  const [showOriginal, setShowOriginal] = useState(false);
  const [error, setError] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!feedback.trim()) {
      setError(true);
      return;
    }
    setError(false);
    setStatus("updating");
    setShowOriginal(false);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setStatus("ready"), 650);
  };

  return (
    <section
      className="studio-review studio-section"
      id="your-call"
      aria-labelledby="studio-review-title"
    >
      <div className="studio-container studio-review-grid">
        <div className="studio-review-copy" data-reveal>
          <h2 id="studio-review-title">
            A little more help.
            <br />
            <em>Still your call.</em>
          </h2>
          <p>Review the result. Give feedback. Shape the next version.</p>
          <form className="studio-feedback" onSubmit={submit}>
            <div className="studio-feedback-title">
              <span>
                <FileText size={20} />
                <strong>First website</strong>
              </span>
              <span
                className={`studio-review-pill ${status === "ready" ? "is-ready" : ""}`}
              >
                {status === "ready" ? "Feedback added" : "In review"}
              </span>
            </div>
            <label htmlFor="studio-feedback-input">Your feedback</label>
            <input
              id="studio-feedback-input"
              value={feedback}
              onChange={(event) => {
                setFeedback(event.target.value);
                setError(false);
              }}
              maxLength={180}
              aria-invalid={error}
              aria-describedby={error ? "studio-feedback-error" : undefined}
            />
            {error && (
              <p className="studio-form-error" id="studio-feedback-error">
                Add a little direction first.
              </p>
            )}
            <button
              className="studio-button"
              type="submit"
              disabled={status === "updating"}
            >
              {status === "updating"
                ? "Shaping the next version…"
                : status === "ready"
                  ? "Review again"
                  : "Review result"}
              <ArrowRight size={16} />
            </button>
            <span className="studio-feedback-status" role="status">
              {status === "ready"
                ? "Your feedback is in. Explore the example revision."
                : ""}
            </span>
          </form>
        </div>
        <div
          className={`studio-review-stage ${status === "updating" ? "is-updating" : ""}`}
          data-reveal
          style={revealDelay(100)}
        >
          <ExampleWindow
            label={
              status === "ready" && !showOriginal
                ? "Example revision"
                : "Example output"
            }
            className="studio-review-window"
          >
            <CoffeePreview revised={status === "ready" && !showOriginal} />
          </ExampleWindow>
          {status === "ready" && (
            <div className="studio-revision-controls">
              <span>
                <Check size={13} /> Your direction. A fresh version.
              </span>
              <button
                type="button"
                onClick={() => setShowOriginal(!showOriginal)}
              >
                <RotateCcw size={13} />
                {showOriginal ? "View revision" : "View original"}
              </button>
            </div>
          )}
          <span className="studio-review-note" aria-hidden="true">
            Better ideas.
            <br />
            Brighter days.
          </span>
        </div>
      </div>
    </section>
  );
}
