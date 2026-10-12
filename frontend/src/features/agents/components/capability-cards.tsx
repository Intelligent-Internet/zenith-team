import {
  ArrowUpRight,
  BookOpen,
  FolderOpen,
  HelpCircle,
  ListTodo,
  Target,
  Terminal,
  type IconComponent,
} from "@/shared/ui/icons";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import type { PlatformSkill } from "@/shared/api/types";
import { AppAvatar, type AppAvatarProps } from "@/shared/ui/app-avatar";

export interface CapabilityCard {
  id: string;
  name: string;
  description: string;
  detail: string;
  enabled: boolean;
  setupLink?: string;
  setupLabel?: string;
  icon?: IconComponent;
  /** The plugin's or connector's logo, shown instead of the section glyph. */
  avatar?: AppAvatarProps;
  tone?: "accent" | "info" | "success" | "warning";
  /** A compiled platform skill: every run loads it, so it carries no switch. */
  alwaysOn?: boolean;
}

// The five run-scoped tool groups whoever configures the agent can switch off. They
// gate boardly-mcp tools. The compiled platform skill sits in the same list
// (see platformSkillCards) but is always on.
export const builtInToolGroups: CapabilityCard[] = [
  {
    id: "builtin:planning",
    name: "Company planning",
    description: "Read the company context and connect work to its outcomes.",
    detail: "Company context",
    enabled: true,
    icon: Target,
    tone: "accent",
  },
  {
    id: "builtin:coordination",
    name: "Work coordination",
    description:
      "Coordinate tasks, draft email, manage calendars and publish social posts in chat.",
    detail: "Run-scoped tools",
    enabled: true,
    icon: ListTodo,
    tone: "info",
  },
  {
    id: "builtin:saved-work",
    name: "Saved work",
    description: "Keep deliverables attached to the task that produced them.",
    detail: "Files & evidence",
    enabled: true,
    icon: FolderOpen,
    tone: "success",
  },
  {
    id: "builtin:founder-input",
    name: "Founder input",
    description: "Ask a focused question when a decision needs you.",
    detail: "Questions & review",
    enabled: true,
    icon: HelpCircle,
    tone: "warning",
  },
  {
    id: "builtin:wiki",
    name: "Company Wiki",
    description:
      "Search and read published Company Wiki pages. Read-only: the Agent cannot edit pages, and every page it reads is listed on the run.",
    detail: "Read-only knowledge",
    enabled: true,
    icon: BookOpen,
    tone: "info",
  },
];

// What each compiled platform skill is for, in the person's words: the
// server's description tells the Agent when to load it.
const PLATFORM_SKILL_SUMMARIES: Record<string, string> = {
  "zenith-platform": "Core workspace tools for every run.",
  "zenith-workflows": "Playbooks for Tasks launched from a Workflow.",
};

// The platform skills compiled into this release. They come from the server
// so the cards are exactly what the installed release ships; they cannot be
// switched off. The person reads a name; the key is what an Agent loads.
export function platformSkillCards(skills: PlatformSkill[]): CapabilityCard[] {
  return skills.map((skill) => ({
    id: `skill:${skill.key}`,
    name: skill.name,
    description: PLATFORM_SKILL_SUMMARIES[skill.key] ?? skill.description,
    detail: `${skill.key} · ${
      skill.references.length === 1
        ? "1 reference file"
        : `${skill.references.length} reference files`
    }`,
    enabled: true,
    icon: Terminal,
    tone: "accent",
    alwaysOn: true,
  }));
}

export function CapabilityCards({
  title,
  badge,
  icon: Icon,
  description,
  items,
  note,
  runtimeLink,
  editable,
  busy,
  overrides,
  onToggle,
  children,
  sectionId,
  search = "",
  builtIn = false,
  empty,
  managedInPlugins = false,
}: {
  children?: ReactNode;
  title: string;
  badge: string;
  icon: IconComponent;
  description: string;
  items: CapabilityCard[];
  note?: string;
  runtimeLink?: string;
  editable: boolean;
  busy: boolean;
  overrides: Record<string, boolean>;
  onToggle: (id: string, enabled: boolean) => void;
  sectionId: string;
  search?: string;
  builtIn?: boolean;
  /** What the section says when it has no rows at all. */
  empty?: string;
  managedInPlugins?: boolean;
}) {
  const [expanded, setExpanded] = useState(false);
  const filtered = items.filter((item) =>
    `${item.name} ${item.description}`
      .toLowerCase()
      .includes(search.trim().toLowerCase()),
  );
  const visible =
    expanded || search.trim() || builtIn ? filtered : filtered.slice(0, 4);
  return (
    <section
      id={sectionId}
      className="agent-capabilities-section"
      aria-label={title}
      tabIndex={-1}
    >
      <header>
        <h2>
          {title}
          <span className="agent-capabilities-badge">{badge}</span>
        </h2>
        {runtimeLink && (
          <Link to={runtimeLink}>
            View runtime <ArrowUpRight size={13} />
          </Link>
        )}
      </header>
      <p className="agent-capabilities-description">{description}</p>
      {note && (
        <p className="agent-capabilities-note" role="status">
          {note}
        </p>
      )}
      {(visible.length > 0 || children) && (
        <>
          <div className="agent-capability-columns" aria-hidden="true">
            <span>Capability</span>
            <span>
              {builtIn
                ? ""
                : title === "Plugins"
                  ? "Contents"
                  : "Ready for this agent"}
            </span>
            <span>{managedInPlugins ? "Managed by" : "Allow for agent"}</span>
          </div>
          <div className="agent-capabilities-grid">
            {visible.map((item) => {
              const Glyph = item.icon ?? Icon;
              const enabled = item.alwaysOn || overrides[item.id] !== false;
              return (
                <article key={item.id} className="agent-capability-row">
                  {item.avatar ? (
                    <AppAvatar
                      {...item.avatar}
                      className="agent-capability-avatar"
                      size={24}
                    />
                  ) : (
                    <Glyph size={20} />
                  )}
                  <div className="agent-capability-copy">
                    <h3>{item.name}</h3>
                    {item.description !== item.name && (
                      <p title={item.description}>{item.description}</p>
                    )}
                  </div>
                  <span
                    className="agent-capability-availability"
                    title={item.detail}
                  >
                    {!builtIn && (
                      <span>
                        {title === "Plugins"
                          ? item.detail
                          : !enabled
                            ? "Disabled for this agent"
                            : item.enabled
                              ? "Ready"
                              : item.detail || "Unavailable"}
                      </span>
                    )}
                    {enabled && item.setupLink ? (
                      <Link
                        className="agent-capability-setup-link"
                        to={item.setupLink}
                      >
                        {item.setupLabel ?? "Connection details"}{" "}
                        <ArrowUpRight aria-hidden size={12} />
                      </Link>
                    ) : null}
                  </span>
                  <div className="agent-capability-control">
                    {managedInPlugins ? (
                      <button
                        type="button"
                        className="agent-capability-manage"
                        onClick={() => {
                          const section = document.getElementById(
                            "capabilities-plugins",
                          );
                          section?.scrollIntoView({ block: "start" });
                          section?.focus({ preventScroll: true });
                        }}
                      >
                        Plugins <ArrowUpRight size={13} />
                      </button>
                    ) : item.alwaysOn ? (
                      <span className="agent-capability-always-on">
                        Always on
                      </span>
                    ) : (
                      <button
                        type="button"
                        className="agent-capability-toggle"
                        role="switch"
                        aria-label={`Allow ${item.name} for this agent (${title})`}
                        aria-checked={enabled}
                        disabled={!editable || busy}
                        title={
                          editable
                            ? item.enabled
                              ? "Applies to future runs"
                              : `Allowed for future runs, but not ready: ${item.detail}`
                            : "You do not have permission to configure this agent"
                        }
                        onClick={() => onToggle(item.id, enabled)}
                      >
                        <span />
                      </button>
                    )}
                  </div>
                </article>
              );
            })}
            {children}
          </div>
        </>
      )}
      {!filtered.length && !children && !note && (
        <p className="agent-capabilities-empty">
          {items.length
            ? "No matching capabilities."
            : (empty ?? `No ${title.toLowerCase()} reported.`)}
        </p>
      )}
      {!builtIn && !search.trim() && filtered.length > 4 && (
        <button
          type="button"
          className="agent-capabilities-show"
          aria-expanded={expanded}
          onClick={() => setExpanded(!expanded)}
        >
          {expanded
            ? "Show fewer"
            : `View all ${filtered.length} ${title.toLowerCase()}`}
        </button>
      )}
    </section>
  );
}
