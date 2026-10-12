import type { RunOutput } from "../model/run-deliverable";
import { OutputMediaPreview, isFinalOutputMedia } from "./output-media-preview";
import "../styles/studio-canvas.css";

export function OutputDetailsPreview({
  companyId,
  runId,
  output,
}: {
  companyId: string;
  runId: string;
  output: RunOutput;
}) {
  const media = output.items.filter(isFinalOutputMedia);
  if (!media.length) return null;

  return (
    <section className="grid min-w-0 gap-4" aria-label="Result preview">
      <div className="output-preview-gallery">
        {media.map((item) => (
          <figure key={item.id}>
            <OutputMediaPreview
              companyId={companyId}
              runId={runId}
              artifactId={output.id}
              item={item}
            />
            <figcaption>
              <span>{item.label}</span>
            </figcaption>
          </figure>
        ))}
      </div>
    </section>
  );
}
