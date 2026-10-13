import type { components } from "@/shared/api/generated/api.generated";

type Block = components["schemas"]["CanvasBlock"];
const heading = (text: string) => ({
  type: "heading",
  attrs: { level: 2 },
  content: [{ type: "text", text }],
});
const paragraph = (text?: string) => ({
  type: "paragraph",
  ...(text ? { content: [{ type: "text", text }] } : {}),
});
const work = {
  type: "taskCollection",
  attrs: { filter: "active", display: "list" },
};
const results = {
  type: "outputCollection",
  attrs: { filter: "all", display: "gallery" },
};
const sections = (...names: string[]) =>
  names.flatMap((name) => [heading(name), paragraph()]);

export const documentTemplates: {
  key: string;
  title: string;
  description: string;
  content: Block;
}[] = [
  {
    key: "blank",
    title: "Blank Page",
    description: "Start with your own idea.",
    content: { type: "doc", content: [paragraph()] },
  },
  {
    key: "campaign",
    title: "Campaign",
    description: "Research, creative work and publishing.",
    content: {
      type: "doc",
      content: [
        heading("Outcome"),
        paragraph(
          "What should this campaign achieve, and how will we measure it?",
        ),
        ...sections(
          "Audience and insight",
          "Research and evidence",
          "Creative direction",
        ),
        heading("Work and publishing"),
        work,
        heading("Results and learnings"),
        results,
      ],
    },
  },
  {
    key: "research",
    title: "Research",
    description: "Findings, sources and implications.",
    content: {
      type: "doc",
      content: [
        heading("Question"),
        paragraph("What are we trying to learn?"),
        ...sections(
          "Key findings",
          "Evidence",
          "What this means",
          "Open questions",
        ),
      ],
    },
  },
  {
    key: "workflow",
    title: "Workflow",
    description: "A repeatable process and its results.",
    content: {
      type: "doc",
      content: [
        ...sections("Outcome", "Inputs and preparation", "Steps"),
        heading("Related work"),
        work,
        heading("Results"),
        results,
        ...sections("Improvements"),
      ],
    },
  },
  {
    key: "plan",
    title: "Plan",
    description: "Milestones, work and decisions.",
    content: {
      type: "doc",
      content: [
        ...sections("Approach", "Milestones"),
        heading("Work"),
        work,
        ...sections("Risks and decisions"),
      ],
    },
  },
  {
    key: "brief",
    title: "Project brief",
    description: "An outcome everyone can work toward.",
    content: {
      type: "doc",
      content: [
        ...sections("Outcome", "Audience", "Scope"),
        heading("Work"),
        work,
        heading("Results"),
        results,
        ...sections("Open questions"),
      ],
    },
  },
];
