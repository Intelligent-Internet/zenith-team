import { useRef, useState } from "react";
import { Link } from "react-router";
import { BookOpen, Plus } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import type { AgentDto } from "@/shared/api/types";
import {
  type AgentSkill,
  useAgentSkillsQuery,
  useUpdateAgentSkillMutation,
} from "@/shared/api/agent-skill-api";
import { SkillImportDialog } from "./custom-skill-import-dialog";
import { SkillError, skillProblem, skillSources } from "./custom-skill-help";
import "../styles/custom-skills.css";

export function CustomSkills({
  agent,
  search,
}: {
  agent: AgentDto;
  search: string;
}) {
  const scope = { companyId: agent.companyId, agentId: agent.id };
  const query = useAgentSkillsQuery(scope, { refetchOnMountOrArgChange: true });
  const [importing, setImporting] = useState(false);
  const addSkillButton = useRef<HTMLButtonElement>(null);
  const skillsSection = useRef<HTMLElement>(null);
  const [update, updateState] = useUpdateAgentSkillMutation();
  const changingEnabled = useRef(false);
  const skills = query.data?.items ?? [];
  const shown = skills.filter((skill) =>
    `${skill.name} ${skill.description}`
      .toLowerCase()
      .includes(search.trim().toLowerCase()),
  );
  const editable = agent.access.configure;
  const pending = updateState.isLoading || query.isFetching;

  function restoreImportFocus() {
    const target = [addSkillButton.current, skillsSection.current].find(
      (element) =>
        element?.isConnected &&
        !element.matches(":disabled") &&
        element.getClientRects().length > 0 &&
        window.getComputedStyle(element).visibility === "visible",
    );
    target?.focus({ preventScroll: true });
  }

  async function toggle(skill: AgentSkill) {
    if (!editable || pending || changingEnabled.current) return;
    changingEnabled.current = true;
    try {
      await update({
        ...scope,
        skillId: skill.id,
        input: { enabled: !skill.enabled, expectedRevision: skill.revision },
      }).unwrap();
    } catch (error) {
      const problem = skillProblem(error);
      toast.error(problem.title, { description: problem.help });
    } finally {
      changingEnabled.current = false;
    }
  }

  return (
    <section
      ref={skillsSection}
      id="capabilities-skills"
      className="agent-capabilities-section custom-skills"
      aria-labelledby="custom-skills-title"
      tabIndex={-1}
    >
      <header>
        <h2 id="custom-skills-title">
          Custom skills
          <span className="agent-capabilities-badge">
            {query.data ? skills.length : ""}
          </span>
        </h2>
        {editable && (
          <button
            ref={addSkillButton}
            className="custom-skill-button custom-skill-button--secondary custom-skills-add"
            type="button"
            onClick={() => setImporting(true)}
          >
            <Plus size={16} aria-hidden /> Add skill
          </button>
        )}
      </header>
      <p className="agent-capabilities-description">
        Extra know-how for this Agent.
      </p>
      {query.isLoading ? (
        <p className="agent-capabilities-empty" role="status">
          Loading custom skills…
        </p>
      ) : query.isError ? (
        <SkillError
          problem={{
            title: "Couldn’t load custom skills",
            help: "Check your connection and try again.",
          }}
          onRetry={() => void query.refetch()}
        />
      ) : shown.length === 0 ? (
        <div className="custom-skills-empty">
          <BookOpen size={24} aria-hidden />
          <div>
            <strong>
              {skills.length
                ? "No matching skills"
                : "Teach this Agent something new"}
            </strong>
            <p>
              {skills.length
                ? "Try another search."
                : "Add a skill from GitHub, ZIP, or Markdown."}
            </p>
          </div>
        </div>
      ) : (
        <>
          <div
            className="agent-capability-columns custom-skills-columns"
            aria-hidden="true"
          >
            <span>Skill</span>
            <span>Source</span>
            <span>Allow for agent</span>
          </div>
          <ul className="agent-capabilities-grid custom-skills-list">
            {shown.map((skill) => (
              <li
                key={skill.id}
                className="agent-capability-row custom-skills-row"
              >
                <Link
                  to={`/agents/${agent.id}/skills/${skill.id}`}
                  className="custom-skills-open"
                  aria-labelledby={`custom-skill-name-${skill.id}`}
                >
                  <BookOpen size={20} aria-hidden />
                  <div className="agent-capability-copy">
                    <h3 id={`custom-skill-name-${skill.id}`}>{skill.name}</h3>
                    <p title={skill.description}>{skill.description}</p>
                  </div>
                </Link>
                <span className="agent-capability-availability">
                  {skillSources[skill.sourceType].label}
                </span>
                <div className="agent-capability-control">
                  <button
                    type="button"
                    role="switch"
                    className="agent-capability-toggle"
                    aria-label={`Allow ${skill.name} for this agent`}
                    aria-checked={skill.enabled}
                    disabled={!editable || pending}
                    aria-busy={updateState.isLoading}
                    title={
                      editable
                        ? "Applies to new runs"
                        : "You do not have permission to configure this agent"
                    }
                    onClick={() => void toggle(skill)}
                  >
                    <span />
                  </button>
                </div>
              </li>
            ))}
          </ul>
        </>
      )}
      {importing && (
        <SkillImportDialog
          agent={agent}
          onClose={() => setImporting(false)}
          onRestoreFocus={restoreImportFocus}
        />
      )}
    </section>
  );
}
