import { ArrowRight } from "@/shared/ui/icons";
import { ExampleWindow, StudioArt, revealDelay } from "./studio-primitives";

export function CoffeePreview({
  revised = false,
  compact = false,
}: {
  revised?: boolean;
  compact?: boolean;
}) {
  return (
    <div
      className={`studio-coffee ${compact ? "studio-coffee--compact" : ""} ${revised ? "is-revised" : ""}`}
    >
      <div className="studio-coffee-nav">
        <span className="studio-coffee-wordmark">daybreak.</span>
        <div aria-hidden="true">
          <span>Products</span>
          <span>Our story</span>
          <span>For creators</span>
        </div>
      </div>
      <div className="studio-coffee-body">
        <div className="studio-coffee-copy" key={String(revised)}>
          <h3>
            {revised ? (
              <>
                A brighter start.
                <br />
                Made for you.
              </>
            ) : (
              <>
                Good mornings.
                <br />
                Better beginnings.
              </>
            )}
          </h3>
          <p>
            {revised
              ? "A little warmth for your next big idea."
              : "Coffee for independent creators."}
          </p>
          <span className="studio-coffee-shop" aria-hidden="true">
            Shop coffee <ArrowRight size={12} />
          </span>
        </div>
        <StudioArt
          name="coffee-scene"
          className="studio-coffee-photo"
          alt="Daybreak coffee, an ivory mug, and a sunlit studio"
        />
      </div>
    </div>
  );
}

export function StudioShowcase() {
  return (
    <section
      className="studio-showcase studio-section"
      id="product"
      aria-labelledby="studio-output-title"
    >
      <div className="studio-container">
        <div className="studio-section-heading" data-reveal>
          <h2 className="studio-sans-title" id="studio-output-title">
            Good ideas deserve
            <br />
            to become <em>real things.</em>
          </h2>
          <p>A website. A launch plan. A story to tell.</p>
        </div>
        <div className="studio-output-grid">
          <div
            className="studio-output-website"
            data-reveal
            style={revealDelay(70)}
          >
            <ExampleWindow>
              <CoffeePreview />
            </ExampleWindow>
          </div>
          <div className="studio-output-stack">
            <div data-reveal style={revealDelay(140)}>
              <ExampleWindow className="studio-launch-window">
                <h3>Launch plan</h3>
                <ol className="studio-launch-list">
                  {[
                    ["Foundation", "Define and prepare"],
                    ["Build", "Create and test"],
                    ["Launch", "Share with the world"],
                  ].map(([title, description], index) => (
                    <li key={title}>
                      <span>{index + 1}</span>
                      <strong>{title}</strong>
                      <small>{description}</small>
                    </li>
                  ))}
                </ol>
              </ExampleWindow>
            </div>
            <div data-reveal style={revealDelay(210)}>
              <ExampleWindow className="studio-brand-window">
                <div>
                  <h3>Brand story</h3>
                  <p>
                    For small starts.
                    <br />
                    And for everything
                    <br />
                    they might become.
                  </p>
                </div>
                <StudioArt
                  name="coffee-detail"
                  alt="A coffee cup and plant in the Daybreak studio"
                />
              </ExampleWindow>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
