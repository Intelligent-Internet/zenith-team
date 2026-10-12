import { useRef, useState } from "react";
import { ArrowLeft, Check, LockKeyhole, Search, X } from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import {
  discussionsApi,
  type DiscussionSummary,
  type CreateChannelRequest,
} from "@/features/discussions/api/discussions-api";
import { useDiscussionRuntime } from "./discussion-provider";
import { DiscussionAvatar } from "./discussion-identity";
import { discussionError } from "../model/discussion-runtime";

export function CreateGroupDialog({
  open,
  onOpenChange,
  onCreated,
  initialPeerId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (conversation: DiscussionSummary) => void;
  initialPeerId?: string;
}) {
  const runtime = useDiscussionRuntime();
  const [step, setStep] = useState(1);
  const [name, setName] = useState("");
  const [topic, setTopic] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [error, setError] = useState("");
  const request = useRef<
    { fingerprint: string; body: CreateChannelRequest } | undefined
  >(undefined);
  const inFlight = useRef(false);
  const seededPeer = useRef<string | undefined>(undefined);
  const people = useListActiveCompanyMembersQuery(runtime.session.companyId, {
    skip: !open,
    refetchOnMountOrArgChange: true,
  });
  const agents = useListCompanyAgentsQuery(runtime.session.companyId, {
    skip: !open,
  });
  const [create, mutation] = discussionsApi.useCreateChatChannelMutation();
  const members =
    people.currentData?.items.filter(
      (person) =>
        !person.accountDisabled && person.actor.id !== runtime.session.userId,
    ) ?? [];
  const recipients = [
    ...members.map((person) => ({
      key: `human:${person.actor.id}`,
      id: person.actor.id,
      name: person.actor.name,
      description: person.email,
      src: person.actor.avatarUrl,
      kind: "human" as const,
    })),
    ...(agents.currentData?.items ?? [])
      .filter((agent) => !agent.archivedAt)
      .map((agent) => ({
        key: `agent:${agent.id}`,
        id: agent.id,
        name: agent.name,
        description: agent.title,
        src: undefined,
        kind: "agent" as const,
      })),
  ];
  const validName =
    Array.from(name.trim()).length > 0 &&
    Array.from(name.trim()).length <= 100 &&
    new TextEncoder().encode(name.trim()).length <= 400;
  const validTopic = new TextEncoder().encode(topic).length <= 2048;
  const humanCount =
    1 + selected.filter((key) => key.startsWith("human:")).length;
  const agentCount = selected.length - humanCount + 1;
  const query = search.trim().toLocaleLowerCase();
  const visibleRecipients = recipients.filter((person) =>
    `${person.name} ${person.description}`.toLocaleLowerCase().includes(query),
  );

  async function submit() {
    if (!validName || !validTopic || inFlight.current) return;
    if (
      selected.some((key) => !recipients.some((person) => person.key === key))
    ) {
      setError(
        "Some selected members are no longer available. Remove them and try again.",
      );
      return;
    }
    const payload = {
      name: name.trim(),
      topic,
      memberUserIds: selected
        .filter((key) => key.startsWith("human:"))
        .map((key) => key.slice(6))
        .sort(),
      agentIds: selected
        .filter((key) => key.startsWith("agent:"))
        .map((key) => key.slice(6))
        .sort(),
    };
    const fingerprint = JSON.stringify(payload);
    if (request.current?.fingerprint !== fingerprint)
      request.current = {
        fingerprint,
        body: { ...payload, requestKey: crypto.randomUUID() },
      };
    inFlight.current = true;
    setError("");
    try {
      const result = await create({
        ...runtime.session,
        body: request.current.body,
      }).unwrap();
      if (!runtime.active) return;
      setName("");
      setTopic("");
      setSelected([]);
      setStep(1);
      setSearch("");
      request.current = undefined;
      seededPeer.current = undefined;
      onOpenChange(false);
      onCreated(result.conversation);
    } catch (cause) {
      if (runtime.active) setError(discussionError(cause));
    } finally {
      inFlight.current = false;
      mutation.reset();
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="discussion-dialog discussion-create-dialog"
        data-step={step}
        showCloseButton={false}
        onOpenAutoFocus={() => {
          if (initialPeerId && seededPeer.current !== initialPeerId) {
            seededPeer.current = initialPeerId;
            setSelected((current) => [
              ...new Set([...current, `human:${initialPeerId}`]),
            ]);
          }
        }}
      >
        <header className="discussion-dialog-header">
          <div>
            <DialogTitle>Create a channel</DialogTitle>
            <DialogDescription>
              {step === 1
                ? "Give your channel a name and purpose."
                : "Choose people and agents to add."}
            </DialogDescription>
          </div>
          <button
            className="discussion-icon-button"
            aria-label="Close create channel"
            onClick={() => onOpenChange(false)}
          >
            <X size={18} />
          </button>
        </header>
        <div className="discussion-steps" aria-label={`Step ${step} of 2`}>
          <span data-current={step === 1}>1 · Details</span>
          <span data-current={step === 2}>2 · Members</span>
        </div>
        {step === 1 ? (
          <div className="discussion-form">
            <label>
              Channel name
              <input
                autoComplete="off"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="e.g. Product team"
                aria-invalid={Boolean(name) && !validName}
              />
            </label>
            <label>
              Topic <span className="discussion-muted">(optional)</span>
              <textarea
                rows={3}
                value={topic}
                onChange={(event) => setTopic(event.target.value)}
                placeholder="What will this channel work on?"
                aria-invalid={!validTopic}
              />
            </label>
            <p className="discussion-help">
              <LockKeyhole size={16} /> This channel is private.
            </p>
            {initialPeerId ? (
              <p className="discussion-help">
                This starts a new channel. Your direct message history stays
                private.
              </p>
            ) : null}
          </div>
        ) : (
          <div className="discussion-picker">
            <label className="discussion-search">
              <Search size={16} />
              <input
                autoFocus
                type="search"
                aria-label="Search channel members"
                placeholder="Search people and agents"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </label>
            <div className="discussion-selected" aria-label="Selected members">
              <span>You</span>
              {selected.map((key) => (
                <button
                  key={key}
                  onClick={() =>
                    setSelected((ids) => ids.filter((id) => id !== key))
                  }
                  aria-label={`Remove ${recipients.find((p) => p.key === key)?.name ?? "unavailable member"}`}
                  disabled={mutation.isLoading}
                >
                  <span>
                    {recipients.find((p) => p.key === key)?.name ??
                      "Unavailable member"}
                  </span>
                  <X size={12} />
                </button>
              ))}
            </div>
            {people.isError || agents.isError ? (
              <div className="discussion-error" role="alert">
                Members could not be loaded.{" "}
                <button
                  onClick={() => {
                    void people.refetch();
                    void agents.refetch();
                  }}
                >
                  Try again
                </button>
              </div>
            ) : null}
            <div
              className="discussion-picker-list"
              role="group"
              aria-label="People and agents"
            >
              {[
                { kind: "human", label: "People" },
                { kind: "agent", label: "Agents" },
              ].map(({ kind, label }) => {
                const options = visibleRecipients.filter(
                  (person) => person.kind === kind,
                );
                if (!options.length) return null;
                return (
                  <section className="discussion-picker-section" key={kind}>
                    <h3>{label}</h3>
                    {options.map((person) => {
                      const checked = selected.includes(person.key);
                      const atLimit =
                        person.kind === "human"
                          ? humanCount >= 50
                          : agentCount >= 20;
                      return (
                        <button
                          key={person.key}
                          className="discussion-member-option"
                          aria-pressed={checked}
                          disabled={mutation.isLoading || (!checked && atLimit)}
                          title={
                            !checked && atLimit
                              ? `Limit of ${person.kind === "human" ? "50 people" : "20 agents"} reached`
                              : undefined
                          }
                          onClick={() =>
                            setSelected((ids) =>
                              checked
                                ? ids.filter((id) => id !== person.key)
                                : [...ids, person.key],
                            )
                          }
                        >
                          <DiscussionAvatar
                            name={person.name}
                            src={person.src}
                            userId={
                              person.kind === "human" ? person.id : undefined
                            }
                            agentId={
                              person.kind === "agent" ? person.id : undefined
                            }
                          />
                          <span className="discussion-member-copy">
                            <strong title={person.name}>{person.name}</strong>
                            <small title={person.description}>
                              {person.description}
                            </small>
                          </span>
                          <span
                            className="discussion-checkbox"
                            data-checked={checked}
                            aria-hidden="true"
                          >
                            {checked ? <Check size={14} /> : null}
                          </span>
                        </button>
                      );
                    })}
                  </section>
                );
              })}
              {people.isLoading || agents.isLoading ? (
                <p className="discussion-picker-status" role="status">
                  Loading members…
                </p>
              ) : null}
              {!people.isLoading &&
              !agents.isLoading &&
              !people.isError &&
              !agents.isError &&
              !visibleRecipients.length ? (
                <p className="discussion-picker-status" role="status">
                  {query
                    ? "No matching members."
                    : "No members available to add."}
                </p>
              ) : null}
            </div>
            <div className="discussion-picker-summary">
              <p aria-live="polite" aria-atomic="true">
                {humanCount} of 50 people · {agentCount} of 20 agents
              </p>
            </div>
          </div>
        )}
        {error ? (
          <p className="discussion-error" role="alert">
            {error}
          </p>
        ) : null}
        <footer className="discussion-dialog-footer">
          {step === 2 ? (
            <button
              className="discussion-button"
              onClick={() => setStep(1)}
              disabled={mutation.isLoading}
            >
              <ArrowLeft size={16} />
              Back
            </button>
          ) : (
            <span />
          )}
          <button
            className="discussion-button discussion-button--primary"
            disabled={
              !validName ||
              !validTopic ||
              mutation.isLoading ||
              (step === 2 &&
                (people.isError ||
                  agents.isError ||
                  people.isLoading ||
                  agents.isLoading))
            }
            onClick={() => (step === 1 ? setStep(2) : void submit())}
          >
            {mutation.isLoading
              ? "Creating…"
              : step === 1
                ? "Choose members"
                : "Create channel"}
          </button>
        </footer>
      </DialogContent>
    </Dialog>
  );
}
