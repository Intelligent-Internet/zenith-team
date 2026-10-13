import { useListWorkspaceActivityPagesInfiniteQuery } from "@/shared/api/activity-api";
import { useMemo, useState } from "react";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import {
  activityPeriodBounds,
  type ActivityPeriod,
} from "@/features/home/model/activity-presentation";
import type { ActivityListOptions } from "@/shared/api/activity.service";

type ActorFilter = {
  value: string;
  label: string;
  actorType?: ActivityListOptions["actorType"];
  actorId?: string;
};

const actorGroups: ActorFilter[] = [
  { value: "all", label: "Anyone" },
  { value: "user", label: "All people", actorType: "user" },
  { value: "agent", label: "All agents", actorType: "agent" },
  { value: "system", label: "System", actorType: "system" },
];

export function useCompanyActivity(companyId: string) {
  const [entityType, setEntityType] = useState("all");
  const [actor, setActor] = useState(actorGroups[0]);
  const [period, setPeriod] = useState<ActivityPeriod>("all");
  const query = useListWorkspaceActivityPagesInfiniteQuery(
    {
      companyId,
      options: {
        ...(entityType === "all" ? {} : { entityType }),
        actorType: actor.actorType,
        actorId: actor.actorId,
        ...activityPeriodBounds(period),
      },
    },
    { refetchOnMountOrArgChange: 60 },
  );
  const members = useListActiveCompanyMembersQuery(companyId);
  const agents = useListCompanyAgentsQuery(companyId);
  const events = useMemo(() => {
    const entries =
      query.currentData?.pages.flatMap((page) => page.activities) ?? [];
    return [
      ...new Map(entries.map((event) => [event.event_id, event])).values(),
    ];
  }, [query.currentData]);

  const people = new Map<string, ActorFilter>();
  for (const member of members.currentData?.items ?? []) {
    const value = `user:${member.actor.id}`;
    people.set(value, {
      value,
      label: member.actor.name,
      actorType: "user",
      actorId: member.actor.id,
    });
  }
  const agentOptions = new Map<string, ActorFilter>();
  for (const agent of agents.currentData?.items ?? []) {
    const value = `agent:${agent.id}`;
    agentOptions.set(value, {
      value,
      label: agent.archivedAt ? `${agent.name} (archived)` : agent.name,
      actorType: "agent",
      actorId: agent.id,
    });
  }
  // Historical actors may no longer be in the current roster.
  for (const event of events) {
    if (event.actor.kind === "system" || !event.actor.id) continue;
    const value = `${event.actor.kind}:${event.actor.id}`;
    const options = event.actor.kind === "user" ? people : agentOptions;
    if (!options.has(value)) {
      options.set(value, {
        value,
        label: event.actor.displayName || "Unknown actor",
        actorType: event.actor.kind,
        actorId: event.actor.id,
      });
    }
  }
  if (actor.actorId) {
    (actor.actorType === "user" ? people : agentOptions).set(
      actor.value,
      actor,
    );
  }
  const sortActors = (options: Map<string, ActorFilter>) =>
    [...options.values()].sort((left, right) =>
      left.label.localeCompare(right.label),
    );
  const actorOptions = {
    groups: actorGroups,
    people: sortActors(people),
    agents: sortActors(agentOptions),
  };
  // Names for the member and Agent ids in change snapshots, keyed like the
  // filter values ("user:<id>", "agent:<id>"). Only the current roster names
  // them, so an id reads the same whatever history is loaded. Null until both
  // lists have loaded: before that, a missing id cannot be told apart from a
  // member or Agent that is gone.
  const actorNames: ReadonlyMap<string, string> | null =
    members.currentData && agents.currentData
      ? new Map([
          ...members.currentData.items.map(
            (member) => [`user:${member.actor.id}`, member.actor.name] as const,
          ),
          ...agents.currentData.items.map(
            (agent) =>
              [
                `agent:${agent.id}`,
                agent.archivedAt ? `${agent.name} (archived)` : agent.name,
              ] as const,
          ),
        ])
      : null;

  return {
    query,
    events,
    entityType,
    setEntityType,
    period,
    setPeriod,
    actor,
    actorOptions,
    actorNames,
    isRosterLoading: members.isLoading || agents.isLoading,
    isRosterError: members.isError || agents.isError,
    hasFilters:
      entityType !== "all" || actor.value !== "all" || period !== "all",
    setActor(value: string) {
      const option = [
        ...actorOptions.groups,
        ...actorOptions.people,
        ...actorOptions.agents,
      ].find((item) => item.value === value);
      if (option) setActor(option);
    },
    clearFilters() {
      setEntityType("all");
      setActor(actorGroups[0]);
      setPeriod("all");
    },
  };
}
