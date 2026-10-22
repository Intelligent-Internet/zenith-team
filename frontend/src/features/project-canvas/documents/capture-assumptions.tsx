import { sourceCoverageDescription } from "./intelligent-ui/source-coverage";
import type { Presentation } from "../api/page-api";

export function CaptureAssumptions({
  evaluations,
}: {
  evaluations: Presentation["evaluations"];
}) {
  if (!evaluations || Object.keys(evaluations).length === 0) return null;
  const bounded = Object.values(evaluations)
    .flatMap((evaluation) => evaluation.sources)
    .filter((source) => source.snapshot.coverage?.complete === false).length;
  return (
    <>
      {bounded > 0 && (
        <p className="mb-3 text-sm text-workspace-muted" role="note">
          This capture includes {bounded} bounded{" "}
          {bounded === 1 ? "source sample" : "source samples"}. Calculations
          using these sources cover the retained rows only.
        </p>
      )}
      <details className="mb-4 rounded-workspace-control border border-workspace-divider p-3">
        <summary className="cursor-pointer text-sm">
          Captured assumptions and sources
        </summary>
        {Object.entries(evaluations).map(([key, evaluation]) => (
          <section key={key} className="mt-3 text-sm">
            <h3 className="font-medium">
              {key === "page" ? "This Page" : "Included source"}
            </h3>
            {evaluation.scopes.map((scope) => (
              <div key={scope.path} className="mt-2">
                <h4>{scope.name}</h4>
                <dl>
                  {Object.entries(scope.selected).map(([name, value]) => (
                    <div
                      key={name}
                      className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)] gap-3 break-words"
                    >
                      <dt>{name}</dt>
                      <dd>{value === null ? "Unavailable" : String(value)}</dd>
                    </div>
                  ))}
                </dl>
              </div>
            ))}
            {evaluation.sources.map((source) => (
              <p
                key={source.scopePath + "/" + source.name}
                className="mt-2 text-workspace-muted"
              >
                {source.name}: observed{" "}
                <time dateTime={source.snapshot.observedAt}>
                  {new Date(source.snapshot.observedAt).toLocaleString()}
                </time>
                <span className="block">
                  {sourceCoverageDescription(source.snapshot.coverage)}
                </span>
              </p>
            ))}
          </section>
        ))}
      </details>
    </>
  );
}
