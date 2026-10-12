import { BlockDocument } from "../project-canvas/block-editor-schema";
import type { components } from "@/shared/api/generated/api.generated";
import { WikiMarkdown } from "./wiki-markdown";

/** Published revisions and review previews use the same native block renderer. */
export function WikiContent({
  companyId,
  pageId,
  content,
  body,
}: {
  companyId: string;
  pageId: string;
  content?: components["schemas"]["CanvasBlock"];
  body: string;
}) {
  return content ? (
    <BlockDocument
      scope={{ companyId, wikiPageId: pageId }}
      content={content}
      mode="snapshot"
    />
  ) : (
    <WikiMarkdown companyId={companyId} body={body} pages={[]} />
  );
}
