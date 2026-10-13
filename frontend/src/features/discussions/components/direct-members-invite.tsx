import { useState } from "react";
import { Plus, Search } from "@/shared/ui/icons";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import type { DirectMessageRecipient } from "@/features/chat-workspace/components/new-direct-message-dialog";
import { DiscussionAvatar } from "./discussion-identity";
import { discussionsApi, type DiscussionDetail } from "../api/discussions-api";
import { discussionError } from "../model/discussion-runtime";
import { useDiscussionRuntime } from "./discussion-provider";

export function DirectMembersInvite({ detail }: { detail: DiscussionDetail }) {
  const runtime = useDiscussionRuntime();
  const [search, setSearch] = useState("");
  const query = search.trim().toLocaleLowerCase();
  const [error, setError] = useState("");
  const [invite, mutation] =
    discussionsApi.useInviteHumanDiscussionMembersMutation();
  const people = useListActiveCompanyMembersQuery(runtime.session.companyId, {
    skip: !query,
    refetchOnMountOrArgChange: true,
  });
  const agents = useListCompanyAgentsQuery(runtime.session.companyId, {
    skip: !query,
    refetchOnMountOrArgChange: true,
  });
  const recipients: DirectMessageRecipient[] = [
    ...(people.currentData?.items ?? [])
      .filter(
        (person) =>
          !person.accountDisabled &&
          !detail.members.some(
            (member) => member.userId === person.actor.id && member.active,
          ),
      )
      .map((person) => ({
        id: person.actor.id,
        name: person.actor.name,
        description: person.email ?? "Team member",
        avatarSrc: person.actor.avatarUrl ?? undefined,
        kind: "human" as const,
      })),
    ...(agents.currentData?.items ?? [])
      .filter(
        (agent) =>
          !agent.archivedAt &&
          !detail.agents.some((member) => member.agentId === agent.id),
      )
      .map((agent) => ({
        id: agent.id,
        name: agent.name,
        description: agent.title,
        kind: "agent" as const,
      })),
  ];
  async function submit(selected: DirectMessageRecipient[]) {
    setError("");
    const memberUserIds = selected
      .filter((person) => person.kind === "human")
      .map((person) => person.id);
    const agentIds = selected
      .filter((person) => person.kind === "agent")
      .map((person) => person.id);
    try {
      await invite({
        ...runtime.session,
        conversationId: detail.conversation.id,
        body: {
          ...(memberUserIds.length ? { memberUserIds } : {}),
          ...(agentIds.length ? { agentIds } : {}),
        },
      }).unwrap();
      if (runtime.active) {
        runtime.schedule(true);
        setSearch("");
      }
      return true;
    } catch (cause) {
      if (runtime.active) setError(discussionError(cause));
      return false;
    }
  }
  const matches = recipients.filter((person) =>
    `${person.name} ${person.description}`.toLocaleLowerCase().includes(query),
  );
  return (
    <>
      <label className="discussion-search">
        <Search size={15} />
        <input
          type="search"
          value={search}
          aria-label="Search people and agents to add"
          placeholder="Search people or agents to add…"
          onChange={(event) => setSearch(event.target.value)}
        />
      </label>
      {query ? (
        <div className="discussion-add-options">
          {people.isError || agents.isError ? (
            <p className="discussion-error" role="alert">
              Couldn’t load the directory.{" "}
              <button
                onClick={() => {
                  void people.refetch();
                  void agents.refetch();
                }}
              >
                Retry
              </button>
            </p>
          ) : null}
          {people.isLoading || agents.isLoading ? (
            <p role="status">Loading directory…</p>
          ) : null}
          {matches.map((person) => (
            <button
              key={`${person.kind}-${person.id}`}
              className="discussion-member-option"
              aria-label={`Add ${person.name}`}
              disabled={mutation.isLoading}
              onClick={() => void submit([person])}
            >
              <DiscussionAvatar
                name={person.name}
                src={person.avatarSrc}
                userId={person.kind === "human" ? person.id : undefined}
                agentId={person.kind === "agent" ? person.id : undefined}
              />
              <span className="discussion-member-copy flex flex-col gap-[2px] min-w-0">
                <strong>{person.name}</strong>
                <small>{person.kind === "agent" ? "Agent" : "Person"}</small>
              </span>
              <Plus size={15} aria-hidden />
            </button>
          ))}
          {!people.isLoading &&
          !agents.isLoading &&
          !people.isError &&
          !agents.isError &&
          !matches.length ? (
            <p className="discussion-muted">
              No matching people or agents to add.
            </p>
          ) : null}
        </div>
      ) : null}
      {error ? (
        <p className="discussion-error" role="alert">
          {error}
        </p>
      ) : null}
    </>
  );
}
