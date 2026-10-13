import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { channelAgentActivity, useChannelAgents } from "./channel-agents";
import { DirectMembersInvite } from "./direct-members-invite";
import { useMemberPresence } from "../model/use-member-presence";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useEffect, useId, useState, type ReactNode } from "react";
import {
  Archive,
  ArrowRight,
  Hash,
  LockKeyhole,
  MoreHorizontal,
  Plus,
  Search,
  X,
} from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { MemberProfileTrigger } from "@/shared/chat/member-profile";
import { useCompanyPermissions } from "@/shared/permissions";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import {
  discussionsApi,
  type ChannelAction,
  type ChannelManagement,
  type DiscussionDetail,
} from "../api/discussions-api";
import { useDiscussionRuntime } from "./discussion-provider";
import { discussionError } from "../model/discussion-runtime";
import { DiscussionAvatar } from "./discussion-identity";

type GroupDetailsTab = "about" | "members" | "agents";

export function GroupAboutDialog({
  detail,
  initialTab = "about",
  open,
  onOpenChange,
  onLeave,
}: {
  detail: DiscussionDetail;
  initialTab?: GroupDetailsTab;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onLeave: () => void;
}) {
  const runtime = useDiscussionRuntime();
  const { can } = useCompanyPermissions();
  const group = detail.conversation.type === "channel";
  const general = detail.conversation.isGeneral;
  const managed = discussionsApi.useGetManagedChannelQuery(
    { ...runtime.session, conversationId: detail.conversation.id },
    {
      skip: !open || !group || !can("channels.manage"),
      refetchOnMountOrArgChange: true,
    },
  );
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className={`discussion-dialog discussion-about${group ? "" : " discussion-about--direct"}`}
        showCloseButton={false}
      >
        <header className="discussion-dialog-header">
          <div>
            <DialogTitle>{detail.conversation.title}</DialogTitle>
            <DialogDescription>
              {general
                ? "Company channel"
                : group
                  ? "Private channel"
                  : "Direct message"}
            </DialogDescription>
          </div>
          <button
            className="discussion-icon-button"
            aria-label="Close conversation details"
            onClick={() => onOpenChange(false)}
          >
            <X size={18} />
          </button>
        </header>
        {group && can("channels.manage") ? (
          managed.isError ? (
            <p className="discussion-error" role="alert">
              Couldn’t load channel settings.{" "}
              <button onClick={() => void managed.refetch()}>Retry</button>
            </p>
          ) : managed.currentData ? (
            <GroupManagement
              key={detail.conversation.id}
              data={managed.currentData}
              initialTab={initialTab}
              onLeave={onLeave}
              onOpenProfile={() => onOpenChange(false)}
            />
          ) : (
            <p role="status">Loading settings…</p>
          )
        ) : group ? (
          <GroupDetailsTabs
            key={detail.conversation.id}
            initialTab={initialTab}
            memberCount={
              detail.members.filter((member) => member.active).length
            }
            agentCount={detail.agents.length}
            agents={
              <MemberRoster
                members={[]}
                agents={detail.agents}
                onOpenProfile={() => onOpenChange(false)}
              />
            }
            about={
              <>
                <dl className="discussion-group-summary">
                  <div>
                    <dt>Channel name</dt>
                    <dd>{detail.conversation.title}</dd>
                  </div>
                  <div>
                    <dt>Topic</dt>
                    <dd>{detail.conversation.topic || "No topic yet."}</dd>
                  </div>
                </dl>
                {!general &&
                detail.members.some(
                  (member) =>
                    member.userId === runtime.session.userId && member.active,
                ) ? (
                  <LeaveGroup
                    conversationId={detail.conversation.id}
                    revision={detail.conversation.configurationRevision ?? 0}
                    onLeave={onLeave}
                  />
                ) : null}
              </>
            }
            members={
              <MemberRoster
                members={detail.members.filter((member) => member.active)}
                agents={[]}
                onOpenProfile={() => onOpenChange(false)}
              />
            }
          />
        ) : (
          <GroupDetailsTabs
            key={detail.conversation.id}
            initialTab="members"
            memberCount={
              detail.members.filter((member) => member.active).length +
              detail.agents.length
            }
            members={
              <>
                {detail.conversation.capabilities.manageMembers ? (
                  <DirectMembersInvite detail={detail} />
                ) : null}
                <MemberRoster
                  members={detail.members.filter((member) => member.active)}
                  agents={detail.agents}
                  onOpenProfile={() => onOpenChange(false)}
                />
              </>
            }
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

export function GroupManagementPane({
  conversationId,
  onOpenChat,
  onLeave,
}: {
  conversationId: string;
  onOpenChat: () => void;
  onLeave: () => void;
}) {
  const runtime = useDiscussionRuntime();
  const { can } = useCompanyPermissions();
  const query = discussionsApi.useGetManagedChannelQuery(
    { ...runtime.session, conversationId },
    { skip: !can("channels.manage"), refetchOnMountOrArgChange: true },
  );
  const [change, mutation] = discussionsApi.useChangeChatChannelMutation();
  const [error, setError] = useState("");
  const data = query.currentData;
  const joined = data?.members.some(
    (member) => member.userId === runtime.session.userId && member.active,
  );
  async function join() {
    if (!data) return;
    try {
      await change({
        ...runtime.session,
        conversationId,
        expectedRevision: data.configurationRevision,
        action: "addMember",
        memberUserId: runtime.session.userId,
      }).unwrap();
      if (!runtime.active) return;
      runtime.schedule(true);
      onOpenChat();
    } catch (cause) {
      setError(discussionError(cause));
    }
  }
  return (
    <div className="discussion-pane discussion-manage-pane">
      {!can("channels.manage") || query.isError ? (
        <div className="discussion-empty" role="alert">
          <LockKeyhole size={28} />
          <h2>Channel unavailable</h2>
          <p>
            {can("channels.manage")
              ? "We couldn’t load these channel settings."
              : "You don’t have access to these channel settings."}
          </p>
          {can("channels.manage") ? (
            <button
              className="discussion-button"
              onClick={() => void query.refetch()}
            >
              Try again
            </button>
          ) : null}
        </div>
      ) : !data ? (
        <div className="discussion-empty" role="status">
          Loading channel…
        </div>
      ) : (
        <>
          <header className="discussion-header">
            <div>
              <span className="discussion-eyebrow">Channel settings</span>
              <h2>
                {data.isGeneral ? (
                  <Hash size={19} />
                ) : (
                  <LockKeyhole size={19} />
                )}
                {data.name}
              </h2>
            </div>
            {joined ? (
              <button className="discussion-button" onClick={onOpenChat}>
                Open chat
                <ArrowRight size={16} />
              </button>
            ) : (
              <button
                className="discussion-button discussion-button--primary"
                disabled={Boolean(data.archivedAt) || mutation.isLoading}
                onClick={() => void join()}
              >
                Join to read messages
                <ArrowRight size={16} />
              </button>
            )}
          </header>
          <div className="discussion-manage-content">
            {!joined ? (
              <p className="discussion-help">
                You can manage this channel. Join it to read and send messages.
              </p>
            ) : null}
            {error ? (
              <p className="discussion-error" role="alert">
                {error}
              </p>
            ) : null}
            <GroupManagement key={data.id} data={data} onLeave={onLeave} />
          </div>
        </>
      )}
    </div>
  );
}

function GroupManagement({
  data,
  initialTab = "about",
  onLeave,
  onOpenProfile,
}: {
  data: ChannelManagement;
  initialTab?: GroupDetailsTab;
  onLeave: () => void;
  onOpenProfile?: () => void;
}) {
  const runtime = useDiscussionRuntime();
  const [name, setName] = useState(data.name);
  const [topic, setTopic] = useState(data.topic);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [rosterTab, setRosterTab] = useState<GroupDetailsTab>(initialTab);
  const showingAgents = rosterTab === "agents";
  const [change, mutation] = discussionsApi.useChangeChatChannelMutation();
  const people = useListActiveCompanyMembersQuery(runtime.session.companyId, {
    skip:
      showingAgents ||
      !search.trim() ||
      data.isGeneral ||
      Boolean(data.archivedAt),
    refetchOnMountOrArgChange: true,
  });
  const agents = useListCompanyAgentsQuery(runtime.session.companyId, {
    skip: !showingAgents || !search.trim() || Boolean(data.archivedAt),
  });
  useEffect(() => {
    setName(data.name);
    setTopic(data.topic);
  }, [data.name, data.topic]);
  async function update(action: ChannelAction) {
    setError("");
    try {
      await change({
        ...runtime.session,
        conversationId: data.id,
        expectedRevision: data.configurationRevision,
        ...action,
      }).unwrap();
      if (!runtime.active) return;
      runtime.schedule(true);
      if (action.action === "addMember" || action.action === "addAgent") {
        setSearch("");
      }
      if (
        action.action === "leave" ||
        (action.action === "removeMember" &&
          action.memberUserId === runtime.session.userId)
      ) {
        runtime.revoke(data.id);
        onLeave();
      }
    } catch (cause) {
      if (runtime.active) setError(discussionError(cause));
    }
  }
  const currentMember = data.members.some(
    (member) => member.userId === runtime.session.userId && member.active,
  );
  const otherPeople = (
    data.isGeneral ? [] : (people.currentData?.items ?? [])
  ).filter(
    (person) =>
      !person.accountDisabled &&
      !data.members.some(
        (member) => member.userId === person.actor.id && member.active,
      ),
  );
  const otherAgents = (agents.currentData?.items ?? []).filter(
    (agent) =>
      !agent.archivedAt &&
      !data.agents.some((member) => member.agentId === agent.id),
  );
  const query = search.trim().toLocaleLowerCase();
  const matchingPeople = (showingAgents ? [] : otherPeople).filter((person) =>
    `${person.actor.name} ${person.email}`.toLocaleLowerCase().includes(query),
  );
  const matchingAgents = (showingAgents ? otherAgents : []).filter((agent) =>
    agent.name.toLocaleLowerCase().includes(query),
  );
  const disabled = mutation.isLoading;
  const validName =
    name.trim().length > 0 &&
    Array.from(name.trim()).length <= 100 &&
    new TextEncoder().encode(name.trim()).length <= 400;
  const memberCount = data.members.filter((member) => member.active).length;
  const rosterPanel = (
    <>
      {(showingAgents || !data.isGeneral) && (
        <label className="discussion-search">
          <Search size={15} />
          <input
            type="search"
            disabled={Boolean(data.archivedAt)}
            value={search}
            aria-label={
              showingAgents ? "Search agents to add" : "Search people to add"
            }
            placeholder={
              showingAgents ? "Search agents to add…" : "Search people to add…"
            }
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
      )}
      {query && !data.archivedAt ? (
        <>
          {(
            showingAgents ? agents.isError : !data.isGeneral && people.isError
          ) ? (
            <p role="alert" className="discussion-error">
              Couldn’t load the directory.{" "}
              <button
                onClick={() => {
                  if (showingAgents) void agents.refetch();
                  else if (!data.isGeneral) void people.refetch();
                }}
              >
                Retry
              </button>
            </p>
          ) : null}
          <div className="discussion-add-options">
            {matchingPeople.length ? <h4>People</h4> : null}
            {matchingPeople.map((person) => (
              <button
                className="discussion-member-option"
                key={person.actor.id}
                aria-label={`Add ${person.actor.name}`}
                title={person.email}
                disabled={
                  disabled || data.members.filter((m) => m.active).length >= 50
                }
                onClick={() =>
                  void update({
                    action: "addMember",
                    memberUserId: person.actor.id,
                  })
                }
              >
                <DiscussionAvatar
                  name={person.actor.name}
                  src={person.actor.avatarUrl}
                  userId={person.actor.id}
                />
                <span className="discussion-member-copy">
                  <strong>{person.actor.name}</strong>
                </span>
                <Plus size={15} aria-hidden />
              </button>
            ))}
            {matchingAgents.length ? <h4>Agents</h4> : null}
            {matchingAgents.map((agent) => (
              <button
                className="discussion-member-option"
                key={agent.id}
                aria-label={`Add ${agent.name}, agent`}
                disabled={disabled || data.agents.length >= 20}
                onClick={() =>
                  void update({ action: "addAgent", agentId: agent.id })
                }
              >
                <DiscussionAvatar name={agent.name} agentId={agent.id} />
                <span className="discussion-member-copy">
                  <strong>{agent.name}</strong>
                </span>
                <Plus size={15} aria-hidden />
              </button>
            ))}
            {(showingAgents ? agents.isLoading : people.isLoading) ? (
              <p role="status">Loading directory…</p>
            ) : null}
            {!(showingAgents
              ? agents.isLoading || agents.isError
              : people.isLoading || people.isError) &&
            matchingPeople.length + matchingAgents.length === 0 ? (
              <p className="discussion-muted">
                No matching {showingAgents ? "agents" : "people"} to add.
              </p>
            ) : null}
          </div>
        </>
      ) : null}
      <p className="discussion-member-count">
        {showingAgents ? data.agents.length : memberCount}{" "}
        {showingAgents
          ? data.agents.length === 1
            ? "agent"
            : "agents"
          : memberCount === 1
            ? "member"
            : "members"}
      </p>
      <MemberRoster
        members={
          showingAgents ? [] : data.members.filter((member) => member.active)
        }
        agents={showingAgents ? data.agents : []}
        onOpenProfile={onOpenProfile}
        disabled={disabled || Boolean(data.archivedAt)}
        onRemoveHuman={
          data.isGeneral
            ? undefined
            : (id) => void update({ action: "removeMember", memberUserId: id })
        }
        onRemoveAgent={(id) =>
          void update({ action: "removeAgent", agentId: id })
        }
      />
    </>
  );
  return (
    <>
      {data.archivedAt ? (
        <p className="discussion-notice">
          <Archive size={16} />
          This channel is archived. Its history remains available.
        </p>
      ) : null}
      <GroupDetailsTabs
        initialTab={initialTab}
        memberCount={memberCount}
        about={
          <>
            <form
              className="discussion-form"
              onSubmit={(event) => {
                event.preventDefault();
                void update({ action: "update", name: name.trim(), topic });
              }}
            >
              <label>
                Channel name
                <input
                  value={name}
                  disabled={
                    disabled || data.isGeneral || Boolean(data.archivedAt)
                  }
                  onChange={(event) => setName(event.target.value)}
                />
              </label>
              <label>
                <span className="discussion-field-label">
                  Topic <span>Optional</span>
                </span>
                <textarea
                  rows={3}
                  placeholder="What is this channel for?"
                  value={topic}
                  disabled={disabled || Boolean(data.archivedAt)}
                  onChange={(event) => setTopic(event.target.value)}
                />
              </label>
              <button
                className="discussion-button discussion-save discussion-button--primary"
                disabled={
                  disabled ||
                  Boolean(data.archivedAt) ||
                  !validName ||
                  new TextEncoder().encode(topic).length > 2048 ||
                  (name === data.name && topic === data.topic)
                }
              >
                {mutation.isLoading ? "Saving…" : "Save changes"}
              </button>
            </form>
            {!data.isGeneral && (
              <div className="discussion-management-actions">
                <button
                  className="discussion-button"
                  disabled={disabled}
                  onClick={() =>
                    void update({
                      action: data.archivedAt ? "restore" : "archive",
                    })
                  }
                >
                  {data.archivedAt ? "Restore channel" : "Archive channel"}
                </button>
                {currentMember ? (
                  <button
                    className="discussion-button discussion-danger"
                    disabled={disabled}
                    onClick={() => void update({ action: "leave" })}
                  >
                    Leave channel
                  </button>
                ) : null}
              </div>
            )}
          </>
        }
        members={rosterPanel}
        agents={rosterPanel}
        agentCount={data.agents.length}
        onTabChange={(tab) => {
          setRosterTab(tab);
          setSearch("");
        }}
      />
      {error ? (
        <p className="discussion-error" role="alert">
          {error}
        </p>
      ) : null}
    </>
  );
}

function LeaveGroup({
  conversationId,
  revision,
  onLeave,
}: {
  conversationId: string;
  revision: number;
  onLeave: () => void;
}) {
  const runtime = useDiscussionRuntime();
  const [change, mutation] = discussionsApi.useChangeChatChannelMutation();
  return (
    <div className="discussion-management-actions">
      <button
        className="discussion-button discussion-danger"
        disabled={mutation.isLoading}
        onClick={async () => {
          try {
            await change({
              ...runtime.session,
              conversationId,
              expectedRevision: revision,
              action: "leave",
            }).unwrap();
            if (runtime.active) {
              runtime.revoke(conversationId);
              runtime.schedule(true);
              onLeave();
            }
          } catch {
            /* Mutation error is shown below. */
          }
        }}
      >
        Leave channel
      </button>
      {mutation.isError ? (
        <p className="discussion-error" role="alert">
          {discussionError(mutation.error)}
        </p>
      ) : null}
    </div>
  );
}

function MemberRoster({
  members,
  agents,
  disabled,
  onRemoveHuman,
  onRemoveAgent,
  onOpenProfile,
}: Pick<ChannelManagement, "members" | "agents"> & {
  disabled?: boolean;
  onRemoveHuman?: (id: string) => void;
  onRemoveAgent?: (id: string) => void;
  onOpenProfile?: () => void;
}) {
  const runtime = useDiscussionRuntime();
  const channelAgents = useChannelAgents();
  const presence = useMemberPresence(
    members.filter((member) => member.active).map((member) => member.userId),
  );
  return (
    <div
      className="discussion-roster"
      aria-label={
        agents.length && !members.length
          ? "Conversation agents"
          : "Conversation members"
      }
    >
      {members.map((member) => (
        <div className="discussion-roster-row" key={member.userId}>
          <MemberProfileTrigger
            onOpen={onOpenProfile}
            onMessage={onOpenProfile}
            profile={{
              id: member.userId,
              name: member.displayName,
              avatarSrc: member.avatarUrl,
              kind: "human",
              title: member.active ? "Team member" : "No longer active",
              archived: !member.active,
              isCurrentUser: member.userId === runtime.session.userId,
            }}
            className="discussion-roster-person"
          >
            <DiscussionAvatar
              name={member.displayName}
              src={member.avatarUrl}
              userId={member.userId}
            />
            <span>
              <strong>
                {member.displayName}
                {member.userId === runtime.session.userId ? (
                  <span className="discussion-self-label"> (you)</span>
                ) : null}
              </strong>
              <small>
                {!member.active
                  ? "No longer active"
                  : presence.currentData?.items.find(
                        (item) => item.userId === member.userId,
                      )?.online
                    ? "Online"
                    : presence.currentData?.items.some(
                          (item) => item.userId === member.userId,
                        )
                      ? "Away"
                      : "Team member"}
              </small>
            </span>
          </MemberProfileTrigger>
          {onRemoveHuman &&
          member.active &&
          member.userId !== runtime.session.userId ? (
            <MemberActions
              name={member.displayName}
              disabled={disabled}
              onRemove={() => onRemoveHuman(member.userId)}
            />
          ) : null}
        </div>
      ))}
      {agents.map((agent) => {
        const status = channelAgents.agents.find(
          (item) => item.agentId === agent.agentId,
        );
        const activity = status
          ? channelAgentActivity(status, channelAgents.items)
          : undefined;
        const identity = (
          <>
            <FoldAvatar
              agentId={agent.agentId}
              size={32}
              state={
                activity?.current?.runStatus === "running" ? "working" : "idle"
              }
            />
            <span>
              <strong>{agent.name}</strong>
              <small>
                {status
                  ? `Managed by ${status.managerName}${activity?.queued ? ` · ${activity.queued} queued` : ""}`
                  : "Agent"}
              </small>
            </span>
          </>
        );
        const openActivity = () => {
          channelAgents.open(agent.agentId);
          onOpenProfile?.();
        };
        return (
          <div className="discussion-roster-row" key={agent.agentId}>
            {status ? (
              <button
                type="button"
                className="discussion-roster-person discussion-roster-agent"
                disabled={!status.canViewDetail}
                onClick={openActivity}
              >
                {identity}
              </button>
            ) : (
              <MemberProfileTrigger
                onOpen={onOpenProfile}
                onMessage={onOpenProfile}
                profile={{
                  id: agent.agentId,
                  name: agent.name,
                  kind: "agent",
                  title: "Agent",
                }}
                className="discussion-roster-person"
              >
                {identity}
              </MemberProfileTrigger>
            )}
            {activity ? (
              <button
                className="discussion-agent-status"
                disabled={!status?.canViewDetail}
                onClick={openActivity}
                aria-label={`${agent.name}: ${activity.label}${status?.canViewDetail ? ". View activity" : ""}`}
              >
                {activity.label}
              </button>
            ) : null}
            {onRemoveAgent ? (
              <MemberActions
                name={agent.name}
                disabled={disabled}
                onRemove={() => onRemoveAgent(agent.agentId)}
              />
            ) : null}
          </div>
        );
      })}
    </div>
  );
}

function MemberActions({
  name,
  disabled,
  onRemove,
}: {
  name: string;
  disabled?: boolean;
  onRemove: () => void;
}) {
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          className="discussion-icon-button"
          disabled={disabled}
          aria-label={`Actions for ${name}`}
        >
          <MoreHorizontal size={18} />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="discussion-member-menu"
          align="end"
          sideOffset={4}
          collisionPadding={12}
        >
          <DropdownMenu.Item disabled={disabled} onSelect={onRemove}>
            Remove from channel
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function GroupDetailsTabs({
  initialTab = "about",
  memberCount = 0,
  agentCount = 0,
  about,
  members,
  agents,
  onTabChange,
}: {
  initialTab?: GroupDetailsTab;
  memberCount?: number;
  agentCount?: number;
  about?: ReactNode;
  members?: ReactNode;
  agents?: ReactNode;
  onTabChange?: (tab: GroupDetailsTab) => void;
}) {
  const [active, setActive] = useState<GroupDetailsTab>(
    about === undefined
      ? "members"
      : members === undefined
        ? "about"
        : initialTab,
  );
  function selectTab(tab: GroupDetailsTab) {
    setActive(tab);
    onTabChange?.(tab);
  }
  const id = useId();
  const tabs: { id: GroupDetailsTab; label: ReactNode }[] = [];
  if (about !== undefined) tabs.push({ id: "about", label: "About" });
  if (members !== undefined) {
    tabs.push({
      id: "members",
      label: (
        <>
          Members <span>{memberCount}</span>
        </>
      ),
    });
  }
  if (agents !== undefined)
    tabs.push({
      id: "agents",
      label: (
        <>
          Agents <span>{agentCount}</span>
        </>
      ),
    });
  return (
    <div className="discussion-group-details">
      <div
        className="discussion-group-tabs"
        role="tablist"
        aria-label="Conversation details"
      >
        {tabs.map((tab, index) => (
          <button
            key={tab.id}
            type="button"
            role="tab"
            id={`${id}-${tab.id}-tab`}
            aria-controls={`${id}-${tab.id}-panel`}
            aria-selected={active === tab.id}
            tabIndex={active === tab.id ? 0 : -1}
            onClick={() => selectTab(tab.id)}
            onKeyDown={(event) => {
              let next: number;
              if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
              else if (event.key === "ArrowLeft")
                next = (index + tabs.length - 1) % tabs.length;
              else if (event.key === "Home") next = 0;
              else if (event.key === "End") next = tabs.length - 1;
              else return;
              event.preventDefault();
              selectTab(tabs[next].id);
              document.getElementById(`${id}-${tabs[next].id}-tab`)?.focus();
            }}
          >
            {tab.label}
          </button>
        ))}
      </div>
      {tabs.map((tab) => (
        <div
          key={tab.id}
          role="tabpanel"
          id={`${id}-${tab.id}-panel`}
          aria-labelledby={`${id}-${tab.id}-tab`}
          hidden={active !== tab.id}
          tabIndex={0}
          className="discussion-group-tab-panel"
        >
          {active === tab.id
            ? tab.id === "about"
              ? about
              : tab.id === "agents"
                ? agents
                : members
            : null}
        </div>
      ))}
    </div>
  );
}
