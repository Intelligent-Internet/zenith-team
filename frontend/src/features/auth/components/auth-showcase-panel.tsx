export function AuthShowcasePanel() {
  return (
    <aside className="auth-showcase" aria-label="Your team on Zenith Team">
      <div className="auth-showcase-scene" aria-hidden="true" />
      <div className="auth-showcase-copy absolute top-[clamp(106px,_13vh,_156px)] right-[28px] left-[clamp(28px,_calc(4vw_-_16px),_60px)] text-[var(--auth-art-accent)]">
        <h2>
          Big ideas. <span>Better together.</span>
        </h2>
        <p>A shared space for you and your agents.</p>
      </div>
    </aside>
  );
}
