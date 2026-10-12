import { studioAssets } from "./studio-primitives";

const workspaceZones = [
  {
    number: "01",
    title: "Think",
    description: "Turn a question into a clear direction.",
  },
  {
    number: "02",
    title: "Make",
    description: "Give each task an owner and let the work move.",
  },
  {
    number: "03",
    title: "Decide",
    description: "Review what came back and shape the next version.",
  },
] as const;

export function StudioSpaces() {
  return (
    <section
      className="studio-spaces studio-section"
      aria-labelledby="studio-spaces-title"
    >
      <div className="studio-container">
        <header className="studio-spaces-heading" data-reveal>
          <span>ONE SHARED WORKSPACE</span>
          <h2 id="studio-spaces-title">
            A place to think, make,
            <br />
            <em>and decide.</em>
          </h2>
          <p>
            Zenith keeps the brief, the work, and the conversation together—so
            every agent can move with the same direction.
          </p>
        </header>

        <figure className="studio-spaces-overview" data-reveal>
          <img
            src={`${studioAssets}/workspace-overview.webp`}
            alt="An open studio with separate spaces for research, making, and review"
            width="2160"
            height="1260"
            loading="lazy"
            decoding="async"
          />
          <figcaption>
            {workspaceZones.map((zone) => (
              <span key={zone.number}>
                <small>{zone.number}</small>
                <strong>{zone.title}</strong>
                <span>{zone.description}</span>
              </span>
            ))}
          </figcaption>
        </figure>

        <div className="studio-spaces-details">
          <figure data-reveal>
            <img
              src={`${studioAssets}/workspace-focus.webp`}
              alt="A bright research desk with a plan board, books, and a computer"
              width="1800"
              height="1200"
              loading="lazy"
              decoding="async"
            />
            <figcaption>
              <span>From question to clarity</span>
              <strong>Keep the source material close.</strong>
            </figcaption>
          </figure>
          <figure data-reveal>
            <img
              src={`${studioAssets}/workspace-review.webp`}
              alt="A quiet review corner with a chair, books, and an open conversation on a phone"
              width="1800"
              height="1200"
              loading="lazy"
              decoding="async"
            />
            <figcaption>
              <span>Room for your judgment</span>
              <strong>Step back. Review. Choose what comes next.</strong>
            </figcaption>
          </figure>
        </div>
      </div>
    </section>
  );
}
