import { ArrowUpRight, PackageCheck } from "@/shared/ui/icons";
import { Link } from "react-router";

import { useGetRunResultQuery } from "@/features/run-deliverables/api/run-deliverables-api";
import type { RunOutputItem } from "../model/run-deliverable";
import { OutputMediaPreview, isFinalOutputMedia } from "./output-media-preview";

export function ChatOutputPreview({
  companyId,
  runId,
}: {
  companyId: string;
  runId: string;
}) {
  const query = useGetRunResultQuery(
    { companyId, runId },
    { skip: !companyId },
  );
  const output = query.currentData?.output;
  const media = output?.items.filter(isFinalOutputMedia);
  const orderedMedia = media?.sort(
    (a, b) => Number(b.role === "primary") - Number(a.role === "primary"),
  );

  return (
    <section
      className={`chat-output-preview${media && media.length > 1 ? " chat-output-preview--multiple" : ""}`}
      aria-label="Run result"
    >
      {output && orderedMedia?.length ? (
        <OutputGallery
          companyId={companyId}
          runId={runId}
          artifactId={output.id}
          items={orderedMedia}
        />
      ) : null}
      <Link className="chat-output-preview__details" to={`/runs/${runId}`}>
        <PackageCheck size={18} aria-hidden />
        <span>
          <strong>{output?.title ?? "View Run result"}</strong>
          {output ? (
            <small>
              View details
              {media && media.length > 1
                ? ` · ${media.length} media files`
                : ""}
            </small>
          ) : null}
        </span>
        <ArrowUpRight size={16} aria-hidden />
      </Link>
    </section>
  );
}

function OutputGallery({
  companyId,
  runId,
  artifactId,
  items,
}: {
  companyId: string;
  runId: string;
  artifactId: string;
  items: RunOutputItem[];
}) {
  if (items.length === 1) {
    return (
      <OutputMediaPreview
        companyId={companyId}
        runId={runId}
        artifactId={artifactId}
        item={items[0]}
      />
    );
  }

  return (
    <div
      className="chat-output-gallery__track"
      role="region"
      aria-label="Output media"
      tabIndex={0}
    >
      {items.map((item) => (
        <figure
          className="chat-output-gallery__item [flex:0_0_220px] min-w-0 m-0"
          key={item.id}
        >
          <OutputMediaPreview
            companyId={companyId}
            runId={runId}
            artifactId={artifactId}
            item={item}
          />
          <figcaption>{item.label}</figcaption>
        </figure>
      ))}
    </div>
  );
}
