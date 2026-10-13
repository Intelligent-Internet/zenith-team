package conversation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func discussionParameters(p auth.UserPrincipal) generated.ListDiscussionAccessParams {
	return generated.ListDiscussionAccessParams{
		CompanyID:       p.CompanyID,
		ActorUserID:     p.UserID,
		ManageRoles:     auth.RolesWith(auth.PermissionChannelsManage),
		ConversationIds: []uuid.UUID{},
		IncludeArchived: true,
		IncludeHidden:   true,
		PageLimit:       50,
	}
}

func currentDiscussion(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
	manage bool,
) (generated.ListDiscussionAccessRow, error) {
	args := discussionParameters(p)
	args.ConversationIds = []uuid.UUID{id}
	args.ManageView = manage
	rows, err := q.ListDiscussionAccess(ctx, args)
	if err != nil {
		return generated.ListDiscussionAccessRow{}, fmt.Errorf("read discussion access: %w", err)
	}
	if len(rows) == 0 {
		return generated.ListDiscussionAccessRow{}, ErrDiscussionNotFound
	}
	return rows[0], nil
}

// SHARE is compatible between unrelated conversations. Company/account/Agent
// lifecycle writers take the exclusive Company fence before any grant change.
// Never lock an enabled-user row here: account deletion owns User -> Company.
func lockDiscussionMembership(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
) (auth.CompanyRole, error) {
	if _, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID); err != nil {
		return "", discussionLookupError(err)
	}
	m, err := q.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if err != nil {
		return "", discussionLookupError(err)
	}
	if m.Status != "active" {
		return "", ErrDiscussionNotFound
	}
	if _, err = q.GetDiscussionEligibleUser(
		ctx,
		generated.GetDiscussionEligibleUserParams{CompanyID: p.CompanyID, UserID: p.UserID},
	); err != nil {
		return "", discussionLookupError(err)
	}
	return auth.CompanyRole(m.Role), nil
}

func lockDiscussionTargets(ctx context.Context, q *generated.Queries, companyID uuid.UUID, userIDs []uuid.UUID) error {
	ordered := slices.Clone(userIDs)
	slices.SortFunc(ordered, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range ordered {
		member, err := q.GetCompanyMemberForShare(
			ctx,
			generated.GetCompanyMemberForShareParams{CompanyID: companyID, UserID: id},
		)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && member.Status != "active" {
			return ErrDiscussionInvalid
		}
		if err != nil {
			return err
		}
		if _, err = q.GetDiscussionEligibleUser(
			ctx,
			generated.GetDiscussionEligibleUserParams{CompanyID: companyID, UserID: id},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDiscussionInvalid
			}
			return err
		}
	}
	return nil
}

func lockDiscussion(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, id uuid.UUID) error {
	_, err := q.LockDiscussionConversation(
		ctx,
		generated.LockDiscussionConversationParams{CompanyID: p.CompanyID, ConversationID: id},
	)
	return discussionLookupError(err)
}

func discussionLookupError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDiscussionNotFound
	}
	return err
}

func discussionConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func discussionRevisionValid(revision int64) bool {
	return revision >= 1 && revision <= maxDiscussionInteger
}

func discussionInt(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func discussionCapabilities(row generated.ListDiscussionAccessRow) DiscussionCapabilities {
	open := !row.ArchivedAt.Valid
	if row.Kind == "human_direct" {
		open = row.PeerActive
	}
	write := row.ContentAccess && open && auth.CompanyRole(row.CompanyRole).Can(auth.PermissionConversationsDiscuss)
	return DiscussionCapabilities{
		Threads:     row.ContentAccess,
		ReadState:   row.ContentAccess,
		Search:      row.ContentAccess,
		Edit:        write,
		Delete:      write,
		Reactions:   write,
		Pins:        write,
		ReadHistory: row.ContentAccess,
		Attachments: row.ContentAccess && open,
		References:  row.ContentAccess && open,
		Send: row.ContentAccess && open &&
			auth.CompanyRole(row.CompanyRole).Can(auth.PermissionConversationsDiscuss),
		ManageMembers: (row.ManagementAccess && open) ||
			(row.Kind == "human_direct" && !row.UserLowID.Valid && row.ContentAccess &&
				auth.CompanyRole(row.CompanyRole).Can(auth.PermissionConversationsDiscuss)),
		Archive: row.ManagementAccess && open && !row.IsGeneral,
		Restore: row.ManagementAccess && !open && !row.IsGeneral,
	}
}

func discussionUserState(row generated.ListDiscussionAccessRow) *DiscussionUserState {
	if !row.GrantGeneration.Valid {
		return nil
	}
	return &DiscussionUserState{
		Revision:          row.StateRevision.Int64,
		ReadThroughSeq:    row.MainReadSeq,
		ManualUnreadSeq:   discussionInt(row.ManualUnreadSeq),
		UnreadCount:       &row.UnreadCount,
		PinnedAt:          util.TimestampToTime(row.StatePinnedAt),
		HiddenAt:          util.TimestampToTime(row.HiddenAt),
		NotificationLevel: row.NotificationLevel,
	}
}

func discussionState(row generated.ListDiscussionAccessRow) DiscussionState {
	return DiscussionState{
		ConversationID:        row.ID,
		ConversationRevision:  row.Version,
		ConfigurationRevision: discussionInt(row.ConfigurationRevision),
		HeadSeq:               row.HeadSeq,
		GrantGeneration: discussionInt(
			row.GrantGeneration,
		),
		GrantRevision:             discussionInt(row.GrantRevision),
		CompanyMembershipRevision: row.CompanyMembershipRevision,
		Capabilities:              discussionCapabilities(row),
		UserState:                 discussionUserState(row),
	}
}

func discussionSummary(row generated.ListDiscussionAccessRow, userID uuid.UUID) DiscussionSummary {
	result := DiscussionSummary{
		IsGeneral: row.IsGeneral,
		ID:        row.ID,
		CompanyID: row.CompanyID,
		Type:      DiscussionType(row.Kind),
		Title:     row.Title.String,
		Topic:     util.TextToPtr(row.Topic),
		ArchivedAt: util.TimestampToTime(
			row.ArchivedAt,
		),
		CreatedAt:            row.CreatedAt.Time.UTC(),
		UpdatedAt:            row.UpdatedAt.Time.UTC(),
		ConversationRevision: row.Version,
		ConfigurationRevision: discussionInt(
			row.ConfigurationRevision,
		),
		HeadSeq:      row.HeadSeq,
		MemberCount:  row.MemberCount,
		AgentCount:   row.AgentCount,
		Capabilities: discussionCapabilities(row),
		UserState:    discussionUserState(row),
	}
	if row.Kind == "human_direct" {
		if !row.UserLowID.Valid {
			result.Title = row.PeerDisplayName
			return result
		}
		result.Title = boundedDiscussionLabel(row.PeerDisplayName)
		peer := row.UserLowID.UUID
		if peer == userID {
			peer = row.UserHighID.UUID
		}
		result.Peer = &DiscussionPeer{
			UserID:      peer,
			DisplayName: result.Title,
			AvatarURL:   discussionOptionalString(row.PeerAvatarUrl),
			Active:      row.PeerActive,
		}
	}
	return result
}

func discussionRoster(
	ctx context.Context,
	q *generated.Queries,
	companyID, id uuid.UUID,
) ([]DiscussionMember, []DiscussionAgent, error) {
	members, err := q.ListDiscussionMembers(
		ctx,
		generated.ListDiscussionMembersParams{CompanyID: companyID, ConversationID: id},
	)
	if err != nil {
		return nil, nil, err
	}
	agents, err := q.ListDiscussionAgents(
		ctx,
		generated.ListDiscussionAgentsParams{CompanyID: companyID, ConversationID: id},
	)
	if err != nil {
		return nil, nil, err
	}
	humans := make([]DiscussionMember, len(members))
	for i, m := range members {
		humans[i] = DiscussionMember{
			UserID:      m.UserID,
			DisplayName: boundedDiscussionLabel(m.DisplayName),
			AvatarURL:   discussionOptionalString(m.AvatarUrl),
			Active:      m.Active,
			Generation:  m.Generation,
			Revision:    m.Version,
		}
	}
	roster := make([]DiscussionAgent, len(agents))
	for i, a := range agents {
		roster[i] = DiscussionAgent{
			AgentID:    a.AgentID,
			Name:       boundedDiscussionLabel(a.Name),
			Generation: a.Generation,
			Revision:   a.Version,
		}
	}
	return humans, roster, nil
}

func channelManagement(
	ctx context.Context,
	q *generated.Queries,
	row generated.ListDiscussionAccessRow,
) (ChannelManagement, error) {
	members, agents, err := discussionRoster(ctx, q, row.CompanyID, row.ID)
	if err != nil {
		return ChannelManagement{}, err
	}
	return channelManagementProjection(row, members, agents), nil
}

func normalizeChannelName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 100 ||
		len(name) > 400 ||
		strings.ContainsRune(name, 0) {
		return "", ErrDiscussionInvalid
	}
	return name, nil
}

func validDiscussionIDs(ids []uuid.UUID, cap int) bool {
	if len(ids) > cap {
		return false
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func discussionOptionalString(value string) *string {
	if value == "" || len(value) > 2048 {
		return nil
	}
	return &value
}

func channelManagementProjection(
	row generated.ListDiscussionAccessRow,
	members []DiscussionMember,
	agents []DiscussionAgent,
) ChannelManagement {
	return ChannelManagement{
		IsGeneral:             row.IsGeneral,
		ID:                    row.ID,
		CompanyID:             row.CompanyID,
		Name:                  row.Title.String,
		Topic:                 row.Topic.String,
		ArchivedAt:            util.TimestampToTime(row.ArchivedAt),
		ConfigurationRevision: row.ConfigurationRevision.Int64,
		Members:               members,
		Agents:                agents,
	}
}

type discussionPeople struct {
	Members []DiscussionMember
	Agents  []DiscussionAgent
}

func discussionRosterBulk(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	ids []uuid.UUID,
) (map[uuid.UUID]discussionPeople, error) {
	result := make(map[uuid.UUID]discussionPeople, len(ids))
	for _, id := range ids {
		result[id] = discussionPeople{Members: []DiscussionMember{}, Agents: []DiscussionAgent{}}
	}
	members, err := q.ListDiscussionMembersBulk(
		ctx,
		generated.ListDiscussionMembersBulkParams{CompanyID: companyID, ConversationIds: ids},
	)
	if err != nil {
		return nil, err
	}
	agents, err := q.ListDiscussionAgentsBulk(
		ctx,
		generated.ListDiscussionAgentsBulkParams{CompanyID: companyID, ConversationIds: ids},
	)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		people := result[m.ConversationID]
		people.Members = append(
			people.Members,
			DiscussionMember{
				UserID:      m.UserID,
				DisplayName: boundedDiscussionLabel(m.DisplayName),
				AvatarURL:   discussionOptionalString(m.AvatarUrl),
				Active:      m.Active,
				Generation:  m.Generation,
				Revision:    m.Version,
			},
		)
		result[m.ConversationID] = people
	}
	for _, a := range agents {
		people := result[a.ConversationID]
		people.Agents = append(
			people.Agents,
			DiscussionAgent{
				AgentID:    a.AgentID,
				Name:       boundedDiscussionLabel(a.Name),
				Generation: a.Generation,
				Revision:   a.Version,
			},
		)
		result[a.ConversationID] = people
	}
	return result, nil
}
