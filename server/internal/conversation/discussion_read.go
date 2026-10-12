package conversation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type discussionCursor struct {
	Version            int        `json:"version"`
	CompanyID          uuid.UUID  `json:"companyId"`
	UserID             uuid.UUID  `json:"userId"`
	ConversationID     uuid.UUID  `json:"conversationId"`
	MembershipRevision int64      `json:"membershipRevision"`
	Basis              string     `json:"basis"`
	Generation         *int64     `json:"generation"`
	Stream             string     `json:"stream"`
	RootID             *uuid.UUID `json:"rootId,omitempty"`
	Direction          string     `json:"direction"`
	Seek               int64      `json:"seek"`
	Lower              int64      `json:"lower"`
	Upper              int64      `json:"upper"`
	Limit              int        `json:"limit"`
}

func encodeDiscussionCursor(cursor any) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeDiscussionCursor(raw string, target any) error {
	if len(raw) > 4096 {
		return ErrDiscussionInvalidCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return ErrDiscussionInvalidCursor
	}
	if err = json.Unmarshal(data, target); err != nil {
		return ErrDiscussionInvalidCursor
	}
	return nil
}

func (c discussionCursor) authorize(p auth.UserPrincipal, row generated.ListDiscussionAccessRow) error {
	if c.Version != 1 || c.CompanyID != p.CompanyID || c.UserID != p.UserID || c.ConversationID != row.ID ||
		(c.Stream != "root" && c.Stream != "thread" && c.Stream != "pins" && c.Stream != "reactions") ||
		(c.Direction != "older" && c.Direction != "newer" && c.Direction != "refresh") ||
		c.Seek < 0 ||
		c.Seek > maxDiscussionInteger ||
		c.Lower < 0 ||
		c.Upper < c.Lower ||
		c.Upper > maxDiscussionInteger ||
		c.Limit < 1 ||
		c.Limit > 100 ||
		(c.Basis != "grant" && c.Basis != "owner") ||
		!discussionRevisionValid(c.MembershipRevision) ||
		(c.Generation != nil && !discussionRevisionValid(*c.Generation)) ||
		((c.Basis == "grant") != (c.Generation != nil)) {
		return ErrDiscussionInvalidCursor
	}
	basis := "owner"
	if row.GrantGeneration.Valid {
		basis = "grant"
	}
	if c.MembershipRevision != row.CompanyMembershipRevision || c.Basis != basis ||
		(row.GrantGeneration.Valid && (c.Generation == nil || *c.Generation != row.GrantGeneration.Int64)) {
		return ErrDiscussionCursorExpired
	}
	return nil
}

func (s *DefaultDiscussionService) ListMessages(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	options DiscussionPageOptions,
) (DiscussionPage, error) {
	var result DiscussionPage
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			access, err := currentDiscussion(ctx, q, p, id, false)
			if err != nil {
				return err
			}
			result, err = readDiscussionPage(
				ctx,
				q,
				discussionReadInput{Principal: p, Access: access, Options: options},
			)
			return err
		},
	)
	return result, err
}

type discussionReadInput struct {
	Principal auth.UserPrincipal
	Access    generated.ListDiscussionAccessRow
	Options   DiscussionPageOptions
	RootID    *uuid.UUID
}

func newDiscussionCursor(input discussionReadInput) discussionCursor {
	c := discussionCursor{
		Version:            1,
		CompanyID:          input.Principal.CompanyID,
		UserID:             input.Principal.UserID,
		ConversationID:     input.Access.ID,
		MembershipRevision: input.Access.CompanyMembershipRevision,
		Basis:              "owner",
		Generation:         discussionInt(input.Access.GrantGeneration),
		Stream:             "root",
		RootID:             input.RootID,
		Direction:          "older",
		Seek:               input.Access.HeadSeq + 1,
		Limit:              input.Options.Limit,
	}
	if input.Access.GrantGeneration.Valid {
		c.Basis = "grant"
	}
	if input.RootID != nil {
		c.Stream = "thread"
	}
	return c
}

func discussionPageLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 100 {
		return 0, ErrDiscussionInvalid
	}
	return limit, nil
}

func readDiscussionPage(ctx context.Context, q *generated.Queries, input discussionReadInput) (DiscussionPage, error) {
	limit, err := discussionPageLimit(input.Options.Limit)
	if err != nil {
		return DiscussionPage{}, err
	}
	input.Options.Limit = limit
	cursor := newDiscussionCursor(input)
	initial := input.Options.Cursor == ""
	if !initial {
		if err = decodeDiscussionCursor(input.Options.Cursor, &cursor); err != nil {
			return DiscussionPage{}, err
		}
		if err = cursor.authorize(input.Principal, input.Access); err != nil {
			return DiscussionPage{}, err
		}
		expected := newDiscussionCursor(input)
		if cursor.Stream != expected.Stream || !sameDiscussionRoot(cursor.RootID, input.RootID) {
			return DiscussionPage{}, ErrDiscussionInvalidCursor
		}
	}
	args := generated.ListDiscussionMessagesParams{
		CompanyID:      input.Principal.CompanyID,
		ConversationID: input.Access.ID,
		ThroughSeq:     input.Access.HeadSeq,
		PageLimit:      int32(limit + 1),
		ThreadRootID:   util.NullUUIDFromPtr(input.RootID),
	}
	ascending := false
	switch cursor.Direction {
	case "older":
		if !initial {
			args.ThroughSeq = cursor.Seek - 1
		}
	case "newer":
		args.AfterSeq = cursor.Seek
		ascending = true
	case "refresh":
		limit = cursor.Limit
		args.PageLimit = int32(limit + 1)
		ascending = true
		if cursor.Lower > 0 {
			args.AfterSeq = cursor.Lower - 1
		}
		args.ThroughSeq = cursor.Upper
	}
	rows, err := listDiscussionWindow(ctx, q, args, ascending)
	if err != nil {
		return DiscussionPage{}, err
	}
	messages := make([]DiscussionMessage, 0, len(rows))
	for _, row := range rows {
		message, err := discussionMessage(row)
		if err != nil {
			return DiscussionPage{}, err
		}
		messages = append(messages, message)
	}
	if err = hydrateDiscussionMessages(ctx, q, input.Principal, messages); err != nil {
		return DiscussionPage{}, err
	}
	budget := maxDiscussionPageBytes - 8192
	if input.RootID != nil {
		budget -= maxDiscussionItemBytes
	}
	used := 0
	end := 0
	for i, message := range messages {
		if i >= limit {
			break
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return DiscussionPage{}, err
		}
		if used+len(encoded)+1 > budget {
			break
		}
		used += len(encoded) + 1
		end++
	}
	messages = messages[:end]
	if !ascending {
		slices.Reverse(messages)
	}
	return finishDiscussionPage(
		ctx,
		q,
		discussionPageFinish{Input: input, Cursor: cursor, Items: messages, Limit: limit, Initial: initial},
	)
}

func sameDiscussionRoot(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

type discussionPageFinish struct {
	Input   discussionReadInput
	Cursor  discussionCursor
	Items   []DiscussionMessage
	Limit   int
	Initial bool
}

func finishDiscussionPage(
	ctx context.Context,
	q *generated.Queries,
	finish discussionPageFinish,
) (DiscussionPage, error) {
	input := finish.Input
	result := DiscussionPage{
		Items:                     finish.Items,
		HeadSeq:                   input.Access.HeadSeq,
		ConversationRevision:      input.Access.Version,
		GrantGeneration:           discussionInt(input.Access.GrantGeneration),
		CompanyMembershipRevision: input.Access.CompanyMembershipRevision,
	}
	latest, err := q.ListDiscussionMessages(
		ctx,
		generated.ListDiscussionMessagesParams{
			CompanyID:      input.Principal.CompanyID,
			ConversationID: input.Access.ID,
			ThroughSeq:     input.Access.HeadSeq,
			PageLimit:      1,
			ThreadRootID:   util.NullUUIDFromPtr(input.RootID),
		},
	)
	if err != nil {
		return result, err
	}
	if len(latest) > 0 {
		result.StreamHeadSeq = latest[0].Seq
	}
	lower, upper := int64(0), int64(0)
	if len(result.Items) > 0 {
		lower = result.Items[0].Seq
		upper = result.Items[len(result.Items)-1].Seq
	}
	base := finish.Cursor
	base.Limit = finish.Limit
	base.Direction = "refresh"
	base.Lower = lower
	base.Upper = upper
	base.Seek = upper
	if len(result.Items) == 0 {
		base.Seek = input.Access.HeadSeq
		if !finish.Initial {
			switch finish.Cursor.Direction {
			case "refresh":
				base.Seek = finish.Cursor.Seek
			case "newer":
				base.Seek = max(finish.Cursor.Seek, input.Access.HeadSeq)
			}
		}
	}
	result.RefreshCursor = encodeDiscussionCursor(base)
	base.Direction = "newer"
	result.NewerCursor = encodeDiscussionCursor(base)
	if lower > 0 {
		before, err := q.ListDiscussionMessages(
			ctx,
			generated.ListDiscussionMessagesParams{
				CompanyID:      input.Principal.CompanyID,
				ConversationID: input.Access.ID,
				ThroughSeq:     lower - 1,
				PageLimit:      1,
				ThreadRootID:   util.NullUUIDFromPtr(input.RootID),
			},
		)
		if err != nil {
			return result, err
		}
		if len(before) > 0 {
			base.Direction = "older"
			base.Seek = lower
			older := encodeDiscussionCursor(base)
			result.OlderCursor = &older
		}
	}
	return result, nil
}

type discussionListCursor struct {
	Version       int            `json:"version"`
	CompanyID     uuid.UUID      `json:"companyId"`
	UserID        uuid.UUID      `json:"userId"`
	Type          DiscussionType `json:"type"`
	Manage        bool           `json:"manage"`
	Archived      bool           `json:"archived"`
	IncludeHidden bool           `json:"includeHidden"`
	Query         string         `json:"query"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	ID            uuid.UUID      `json:"id"`
}

func discussionListParams(
	p auth.UserPrincipal,
	kind DiscussionType,
	manage bool,
	options DiscussionListOptions,
) (generated.ListDiscussionAccessParams, error) {
	limit := options.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || len(options.Query) > 400 {
		return generated.ListDiscussionAccessParams{}, ErrDiscussionInvalid
	}
	args := discussionParameters(p)
	args.PageLimit = int32(limit + 1)
	args.TypeFilter = string(kind)
	args.ManageView = manage
	args.IncludeHidden = options.IncludeHidden || manage
	args.NameQuery = options.Query
	if kind == DiscussionTypeChannel {
		args.IncludeArchived = options.Archived
		args.OnlyArchived = options.Archived
	}
	if options.Cursor != "" {
		var cursor discussionListCursor
		if err := decodeDiscussionCursor(options.Cursor, &cursor); err != nil {
			return args, err
		}
		if cursor.Version != 1 || cursor.CompanyID != p.CompanyID || cursor.UserID != p.UserID ||
			cursor.Type != kind ||
			cursor.Manage != manage ||
			cursor.Archived != options.Archived ||
			cursor.IncludeHidden != options.IncludeHidden ||
			cursor.Query != options.Query ||
			cursor.ID == uuid.Nil ||
			cursor.UpdatedAt.IsZero() {
			return args, ErrDiscussionInvalidCursor
		}
		args.SeekTime = pgtype.Timestamptz{Time: cursor.UpdatedAt, Valid: true}
		args.SeekID = uuid.NullUUID{UUID: cursor.ID, Valid: true}
	}
	return args, nil
}

func nextDiscussionListCursor(
	p auth.UserPrincipal,
	kind DiscussionType,
	manage bool,
	options DiscussionListOptions,
	row generated.ListDiscussionAccessRow,
) *string {
	encoded := encodeDiscussionCursor(
		discussionListCursor{
			Version:       1,
			CompanyID:     p.CompanyID,
			UserID:        p.UserID,
			Type:          kind,
			Manage:        manage,
			Archived:      options.Archived,
			IncludeHidden: options.IncludeHidden,
			Query:         options.Query,
			UpdatedAt:     row.UpdatedAt.Time.UTC(),
			ID:            row.ID,
		},
	)
	return &encoded
}

func (s *DefaultConversationService) ListHumanDirects(
	ctx context.Context,
	p auth.UserPrincipal,
	options DiscussionListOptions,
) (DiscussionList, error) {
	return s.listDiscussions(ctx, p, DiscussionTypeHumanDirect, options)
}

func (s *DefaultConversationService) ListChannels(
	ctx context.Context,
	p auth.UserPrincipal,
	options DiscussionListOptions,
) (DiscussionList, error) {
	return s.listDiscussions(ctx, p, DiscussionTypeChannel, options)
}

func (s *DefaultConversationService) listDiscussions(
	ctx context.Context,
	p auth.UserPrincipal,
	kind DiscussionType,
	options DiscussionListOptions,
) (DiscussionList, error) {
	args, err := discussionListParams(p, kind, false, options)
	if err != nil {
		return DiscussionList{}, err
	}
	result := DiscussionList{Items: []DiscussionSummary{}}
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			rows, err := q.ListDiscussionAccess(ctx, args)
			if err != nil {
				return err
			}
			used := 0
			for i, row := range rows {
				item := discussionSummary(row, p.UserID)
				encoded, err := json.Marshal(item)
				if err != nil {
					return err
				}
				if i == int(args.PageLimit)-1 || used+len(encoded) > maxDiscussionPageBytes-8192 {
					result.NextCursor = nextDiscussionListCursor(p, kind, false, options, rows[i-1])
					break
				}
				used += len(encoded) + 1
				result.Items = append(result.Items, item)
			}
			return nil
		},
	)
	return result, err
}

func (s *DefaultConversationService) FetchHumanDirect(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (HumanDirectDetail, error) {
	var result HumanDirectDetail
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			row, err := currentDiscussion(ctx, q, p, id, false)
			if err != nil {
				return err
			}
			if row.Kind != "human_direct" {
				return ErrDiscussionNotFound
			}
			members, agents, err := discussionRoster(ctx, q, p.CompanyID, id)
			if err != nil {
				return err
			}
			result = HumanDirectDetail{Conversation: discussionSummary(row, p.UserID), Members: members, Agents: agents}
			return nil
		},
	)
	return result, err
}

func (s *DefaultConversationService) FetchChannel(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (ChannelDetail, error) {
	var result ChannelDetail
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			row, err := currentDiscussion(ctx, q, p, id, false)
			if err != nil {
				return err
			}
			if row.Kind != "channel" {
				return ErrDiscussionNotFound
			}
			members, agents, err := discussionRoster(ctx, q, p.CompanyID, id)
			if err != nil {
				return err
			}
			result = ChannelDetail{
				Conversation: discussionSummary(row, p.UserID),
				Members:      members,
				Agents:       agents,
			}
			return nil
		},
	)
	return result, err
}

func (s *DefaultConversationService) FetchChannelManagement(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (ChannelManagement, error) {
	var result ChannelManagement
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			row, err := currentDiscussion(ctx, q, p, id, true)
			if err != nil {
				return err
			}
			result, err = channelManagement(ctx, q, row)
			return err
		},
	)
	return result, err
}

func (s *DefaultConversationService) ListChannelManagement(
	ctx context.Context,
	p auth.UserPrincipal,
	options DiscussionListOptions,
) (ChannelManagementList, error) {
	args, err := discussionListParams(p, DiscussionTypeChannel, true, options)
	if err != nil {
		return ChannelManagementList{}, err
	}
	result := ChannelManagementList{Items: []ChannelManagement{}}
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			member, err := q.GetActiveCompanyMembership(
				ctx,
				generated.GetActiveCompanyMembershipParams{CompanyID: p.CompanyID, UserID: p.UserID},
			)
			if err != nil {
				return discussionLookupError(err)
			}
			if !auth.CompanyRole(member.Role).Can(auth.PermissionChannelsManage) {
				return ErrDiscussionForbidden
			}
			rows, err := q.ListDiscussionAccess(ctx, args)
			if err != nil {
				return err
			}
			ids := make([]uuid.UUID, 0, len(rows))
			for i, row := range rows {
				if i == int(args.PageLimit)-1 {
					break
				}
				ids = append(ids, row.ID)
			}
			roster, err := discussionRosterBulk(ctx, q, p.CompanyID, ids)
			if err != nil {
				return err
			}
			used := 0
			for i, row := range rows {
				if i == int(args.PageLimit)-1 {
					result.NextCursor = nextDiscussionListCursor(
						p,
						DiscussionTypeChannel,
						true,
						options,
						rows[i-1],
					)
					break
				}
				people := roster[row.ID]
				item := channelManagementProjection(row, people.Members, people.Agents)
				encoded, err := json.Marshal(item)
				if err != nil {
					return err
				}
				// A company-wide General roster must fit as one management item.
				if i > 0 && used+len(encoded) > maxDiscussionPageBytes-8192 {
					result.NextCursor = nextDiscussionListCursor(
						p,
						DiscussionTypeChannel,
						true,
						options,
						rows[i-1],
					)
					break
				}
				used += len(encoded)
				result.Items = append(result.Items, item)
			}
			return nil
		},
	)
	return result, err
}

// Separate forward/backward SQL statements keep ORDER BY matched to the seq
// index even when pgx promotes a prepared statement to a generic plan.
func listDiscussionWindow(
	ctx context.Context,
	q *generated.Queries,
	args generated.ListDiscussionMessagesParams,
	ascending bool,
) ([]generated.ListDiscussionMessagesRow, error) {
	if !ascending {
		return q.ListDiscussionMessages(ctx, args)
	}
	rows, err := q.ListDiscussionMessagesForward(ctx, generated.ListDiscussionMessagesForwardParams(args))
	if err != nil {
		return nil, err
	}
	result := make([]generated.ListDiscussionMessagesRow, len(rows))
	for i, row := range rows {
		result[i] = generated.ListDiscussionMessagesRow(row)
	}
	return result, nil
}
