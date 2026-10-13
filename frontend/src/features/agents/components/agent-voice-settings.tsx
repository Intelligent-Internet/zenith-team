import { useUpdateWorkspaceAgentMutation } from "@/shared/api/agent-api";
import { useId, useState } from "react";
import type { AgentDto } from "@/shared/api/types";

import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { Button } from "@/shared/ui/button";
import { Mic } from "@/shared/ui/icons";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/ui/select";
import { Switch } from "@/shared/ui/switch";
import "../styles/agent-voice.css";

// Matches the server: voice is on unless turned off for this Agent.
const defaults: NonNullable<AgentDto["voice"]> = {
  enabled: true,
  voice: "marin",
  greet: true,
  participation: "addressed",
};

const voiceSelectClassName =
  "agent-voice-select !h-9 !rounded-lg !border-[var(--workspace-divider)] !bg-[var(--workspace-input-bg)] !py-0 !shadow-none [&_svg]:!size-3.5 [&_svg]:!text-[var(--workspace-native-text-muted)] disabled:!text-[var(--workspace-native-text-muted)] focus-visible:!border-[var(--workspace-native-accent)]";

export function AgentVoiceSettings({ agent }: { agent: AgentDto }) {
  const id = useId();
  const [draft, setDraft] = useState(agent.voice ?? defaults);
  const [revision, setRevision] = useState(agent.revision);
  const [baseline, setBaseline] = useState(agent.voice ?? defaults);
  const [save, mutation] = useUpdateWorkspaceAgentMutation();
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const dirty = JSON.stringify(draft) !== JSON.stringify(baseline);
  const stale = revision !== agent.revision;
  const locked = !agent.access.configure || mutation.isLoading || stale;
  const preferencesDisabled = locked || !draft.enabled;

  function editVoice(changes: Partial<typeof draft>) {
    setDraft({ ...draft, ...changes });
    setError("");
    setSaved(false);
  }

  function resetDraft() {
    const voice = stale ? (agent.voice ?? defaults) : baseline;
    setDraft(voice);
    setBaseline(voice);
    if (stale) setRevision(agent.revision);
    setError("");
    setSaved(false);
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!dirty || locked) return;
    setError("");
    setSaved(false);
    try {
      const updated = await save({
        companyId: agent.companyId,
        agentId: agent.id,
        request: {
          expectedRevision: revision,
          voice: { ...draft, greet: true },
        },
      }).unwrap();
      setDraft(updated.voice ?? defaults);
      setBaseline(updated.voice ?? defaults);
      setRevision(updated.revision);
      setSaved(true);
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    }
  }

  return (
    <form
      aria-labelledby={`${id}-heading`}
      aria-busy={mutation.isLoading}
      className="agent-voice-settings"
      onSubmit={(event) => void submit(event)}
    >
      <header className="agent-voice-heading">
        <Mic aria-hidden="true" size={20} />
        <div>
          <h2 id={`${id}-heading`}>Voice</h2>
          <p>Choose how this Agent sounds and participates in Huddles.</p>
        </div>
      </header>
      <div className="agent-voice-card">
        <div className="agent-voice-toggle">
          <div>
            <label htmlFor={`${id}-enabled`}>Enable Voice</label>
            <p id={`${id}-enabled-description`}>Join Huddles when invited.</p>
          </div>
          <Switch
            id={`${id}-enabled`}
            aria-label="Enable Voice"
            aria-describedby={`${id}-enabled-description`}
            disabled={locked}
            checked={draft.enabled}
            onCheckedChange={(enabled) => editVoice({ enabled })}
          />
        </div>
        <fieldset className="agent-voice-fields" disabled={preferencesDisabled}>
          <legend className="sr-only">Voice preferences</legend>
          <div className="agent-voice-field">
            <label htmlFor={`${id}-voice`}>Voice</label>
            <Select
              value={draft.voice}
              disabled={preferencesDisabled}
              onValueChange={(voice) =>
                editVoice({ voice: voice as typeof draft.voice })
              }
            >
              <SelectTrigger
                id={`${id}-voice`}
                className={voiceSelectClassName}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(
                  [
                    "aster",
                    "beacon",
                    "cinder",
                    "marin",
                    "stone",
                    "vesper",
                  ] as const
                ).map((voice) => (
                  <SelectItem key={voice} value={voice}>
                    {voice[0].toUpperCase() + voice.slice(1)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="agent-voice-field">
            <label htmlFor={`${id}-participation`}>When to speak</label>
            <Select
              value={draft.participation}
              disabled={preferencesDisabled}
              onValueChange={(participation) =>
                editVoice({
                  participation: participation as typeof draft.participation,
                })
              }
            >
              <SelectTrigger
                id={`${id}-participation`}
                className={voiceSelectClassName}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="addressed">When addressed</SelectItem>
                <SelectItem value="natural">
                  At useful natural pauses
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </fieldset>
        {stale && (
          <p className="agent-voice-feedback" role="status">
            Settings changed elsewhere. Reload to continue.
          </p>
        )}
        {error && (
          <p className="agent-voice-feedback" role="alert">
            {error}
          </p>
        )}
        <footer className="agent-voice-footer">
          <p className="agent-voice-save-note" role="status">
            {dirty
              ? baseline.enabled && !draft.enabled
                ? "Saving will end active voice sessions."
                : "Applies to the next invitation."
              : saved
                ? "Changes saved."
                : ""}
          </p>
          <div className="agent-voice-save-actions">
            {(dirty || stale) && (
              <Button
                type="button"
                variant="ghost"
                disabled={mutation.isLoading}
                onClick={resetDraft}
              >
                {stale ? "Reload settings" : "Discard changes"}
              </Button>
            )}
            <Button type="submit" variant="default" disabled={!dirty || locked}>
              {mutation.isLoading ? "Saving…" : "Save changes"}
            </Button>
          </div>
        </footer>
      </div>
    </form>
  );
}
