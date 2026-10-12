package approval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// QuestionService owns the Question ask/read/resolve lifecycle. Ask's boolean
// reports whether this call created the Question (false on an exact
// idempotent replay); the transport turns it into the 201/200 split — it is
// never a wire field.
type QuestionService interface {
	Ask(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		uuid.UUID,
		QuestionAskInput,
	) (Question, bool, error)
	List(context.Context, auth.UserPrincipal, QuestionListOptions) (QuestionPage, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (Question, error)
	Answer(context.Context, auth.UserPrincipal, uuid.UUID, QuestionAnswerInput) (Question, error)
	Withdraw(context.Context, auth.UserPrincipal, uuid.UUID, int64) (Question, error)
}

var _ QuestionService = (*DefaultQuestionService)(nil)

// DefaultQuestionService stores durable Questions and their answers.
type DefaultQuestionService struct {
	store          *db.Store
	bus            *events.Bus
	inboxPresenter inbox.Presenter
}

func NewQuestionService(
	store *db.Store,
	bus *events.Bus,
	inboxPresenter inbox.Presenter,
) (*DefaultQuestionService, error) {
	if store == nil || bus == nil {
		return nil, ErrQuestionConfiguration
	}
	return &DefaultQuestionService{
		store:          store,
		bus:            bus,
		inboxPresenter: inboxPresenter,
	}, nil
}

func (s *DefaultQuestionService) Ask(
	ctx context.Context,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	requestKey uuid.UUID,
	input QuestionAskInput,
) (Question, bool, error) {
	input, err := normalizeQuestionAskInput(input)
	if err != nil {
		return Question{}, false, err
	}
	if err := authorizeQuestionRaise(principal, origin, requestKey); err != nil {
		return Question{}, false, err
	}
	prompts, err := json.Marshal(input.Prompts)
	if err != nil {
		return Question{}, false, ErrQuestionInvalidInput
	}
	digestInput, err := json.Marshal(struct {
		Title   string           `json:"title"`
		Prompts []QuestionPrompt `json:"prompts"`
	}{Title: input.Title, Prompts: input.Prompts})
	if err != nil {
		return Question{}, false, ErrQuestionInvalidInput
	}
	digest := sha256.Sum256(digestInput)
	var result Question
	var created, noResolver bool
	var pending []events.Event
	var inboxResults []inbox.CreateResult
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = pending[:0]
		inboxResults = inboxResults[:0]
		created, noResolver = false, false
		if _, err := lockLiveHumanInputOrigin(ctx, q, principal, origin); err != nil {
			return err
		}
		existing, err := q.GetQuestionByOriginRequestKey(
			ctx,
			generated.GetQuestionByOriginRequestKeyParams{
				OriginRunID: origin.RunID, RequestKey: requestKey,
			},
		)
		if err == nil {
			if !bytes.Equal(existing.RequestSha256, digest[:]) {
				return ErrQuestionConflict
			}
			result, err = questionProjectionForActor(ctx, q, existing, humanInputProjectionActor(principal, origin))
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load Question request replay: %w", err)
		}
		// A Task-origin Question is answered only by readers of the Task
		// chat's Project holding project.approvals.resolve (ADR 0017). With
		// nobody eligible the asking Run ends and the question is refused.
		var deciders []uuid.UUID
		var scoped bool
		if origin.TaskID != nil {
			deciders, scoped, err = auth.TaskChatDecidersInTx(
				ctx, q, origin.CompanyID, origin.ConversationID, auth.ProjectPermissionApprovalsResolve,
			)
			if err != nil {
				return err
			}
			if scoped && len(deciders) == 0 {
				noResolver = true
				notice, err := endRunWithoutResolverInTx(ctx, q, &pending, origin.CompanyID, origin.RunID)
				inboxResults = append(inboxResults, notice)
				return err
			}
		}

		row, err := q.CreateQuestion(ctx, generated.CreateQuestionParams{
			CompanyID:      principal.CompanyID,
			Title:          input.Title,
			AskedByAgentID: origin.AgentID,
			OriginRunID:    origin.RunID,
			RequestKey:     requestKey,
			TaskID:         util.NullUUIDFromPtr(origin.TaskID),
			ConversationID: origin.ConversationID,
			OriginUserID:   util.NullUUIDFromPtr(origin.OriginUserID),
			Prompts:        prompts,
			RequestSha256:  digest[:],
			ExpiresAt: pgtype.Timestamptz{
				Time: time.Now().UTC().Add(24 * time.Hour), Valid: true,
			},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			existing, replayErr := q.GetQuestionByOriginRequestKey(
				ctx,
				generated.GetQuestionByOriginRequestKeyParams{
					OriginRunID: origin.RunID, RequestKey: requestKey,
				},
			)
			if replayErr != nil {
				return fmt.Errorf("load concurrent Question request replay: %w", replayErr)
			}
			if !bytes.Equal(existing.RequestSha256, digest[:]) {
				return ErrQuestionConflict
			}
			result, err = questionProjectionForActor(ctx, q, existing, humanInputProjectionActor(principal, origin))
			return err
		}
		if err != nil {
			return fmt.Errorf("create Question: %w", err)
		}
		result, err = questionProjectionForActor(ctx, q, row, humanInputProjectionActor(principal, origin))
		if err != nil {
			return err
		}
		inboxResults, err = createQuestionInboxItems(ctx, q, &pending, row, deciders, scoped)
		if err != nil {
			return err
		}
		if err := stageQuestionEvent(
			ctx, q, &pending,
			EventQuestionAsked,
			origin.Actor(),
			result,
		); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return Question{}, false, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, inboxResult := range inboxResults {
		inbox.PresentCreated(s.inboxPresenter, inboxResult)
	}
	if noResolver {
		return Question{}, false, auth.ErrNoEligibleResolver
	}
	return result, created, nil
}

func (s *DefaultQuestionService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options QuestionListOptions,
) (QuestionPage, error) {
	options, err := normalizeQuestionListOptions(options)
	if err != nil {
		return QuestionPage{}, err
	}
	sets := auth.ProjectPermissionHeldSets()
	params := generated.ListQuestionProjectionsParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID,
		PageSize: options.Limit + 1, Statuses: []string{},
		DecideView:                options.Decide,
		DecidePermission:          string(auth.ProjectPermissionApprovalsResolve),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
	}
	if len(options.Statuses) > 0 {
		params.HasStatuses = true
		params.Statuses = make([]string, len(options.Statuses))
		for index, status := range options.Statuses {
			params.Statuses[index] = string(status)
		}
	}
	if options.AgentID != nil {
		params.HasAgentID = true
		params.AgentID = *options.AgentID
	}
	if options.TaskID != nil {
		params.HasTaskID = true
		params.TaskID = *options.TaskID
	}
	if options.Before != nil {
		params.HasCursor = true
		params.BeforeCreatedAt = pgtype.Timestamptz{Time: options.Before.CreatedAt.UTC(), Valid: true}
		params.BeforeID = options.Before.ID
	}
	rows, err := s.store.Queries().ListQuestionProjections(ctx, params)
	if err != nil {
		return QuestionPage{}, fmt.Errorf("list Questions: %w", err)
	}
	hasNext := len(rows) > int(options.Limit)
	if hasNext {
		rows = rows[:options.Limit]
	}
	page := QuestionPage{Items: make([]Question, len(rows))}
	for index, row := range rows {
		page.Items[index], err = questionFromProjection(
			row.Question,
			row.AskingAgentName,
			row.ResolvedByDisplayName,
			row.ResolvedByAvatarUrl,
		)
		if err != nil {
			return QuestionPage{}, fmt.Errorf("decode Question projection: %w", err)
		}
	}
	if hasNext {
		last := page.Items[len(page.Items)-1]
		page.Next = &QuestionCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// Get reads one Question under the same visibility rule as List: a Task
// Question is Company-visible, a direct one only to its origin user. Anything
// else, including an id in another company, is not found.
func (s *DefaultQuestionService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	questionID uuid.UUID,
) (Question, error) {
	row, err := s.store.Queries().GetQuestionProjection(ctx, generated.GetQuestionProjectionParams{
		CompanyID: principal.CompanyID, QuestionID: questionID, ActorUserID: principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Question{}, ErrQuestionNotFound
	}
	if err != nil {
		return Question{}, fmt.Errorf("get Question: %w", err)
	}
	return questionFromProjection(
		row.Question,
		row.AskingAgentName,
		row.ResolvedByDisplayName,
		row.ResolvedByAvatarUrl,
	)
}

func (s *DefaultQuestionService) Answer(
	ctx context.Context,
	principal auth.UserPrincipal,
	questionID uuid.UUID,
	input QuestionAnswerInput,
) (Question, error) {
	return s.resolve(ctx, principal, questionID, QuestionStatusAnswered, input, 0)
}

func (s *DefaultQuestionService) Withdraw(
	ctx context.Context,
	principal auth.UserPrincipal,
	questionID uuid.UUID,
	expectedRevision int64,
) (Question, error) {
	return s.resolve(
		ctx,
		principal,
		questionID,
		QuestionStatusWithdrawn,
		QuestionAnswerInput{},
		expectedRevision,
	)
}

// questionHasLapsed decides whether a locked Question is past its deadline, by
// either of the two ways that can be true: the sweeper already stamped the row
// expired, or the row is still pending because the sweeper has not reached it
// yet and its deadline has passed. The row cannot say so on its own — status is
// what the last writer left, and expires_at is only a timestamp — so the answer
// needs the clock as well as the row.
func questionHasLapsed(current generated.Question, now time.Time) bool {
	return current.Status == string(QuestionStatusExpired) ||
		current.Status == string(QuestionStatusPending) && !current.ExpiresAt.Time.After(now)
}

func (s *DefaultQuestionService) resolve(
	ctx context.Context,
	principal auth.UserPrincipal,
	questionID uuid.UUID,
	status QuestionStatus,
	answerInput QuestionAnswerInput,
	withdrawRevision int64,
) (Question, error) {
	var result Question
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = pending[:0]
		role, active, err := lockActiveHumanInputMember(
			ctx,
			q,
			principal.CompanyID,
			principal.UserID,
		)
		if err != nil {
			return err
		}
		if !active {
			return ErrQuestionNotFound
		}
		current, err := q.LockQuestionForResolution(
			ctx,
			generated.LockQuestionForResolutionParams{
				CompanyID: principal.CompanyID, QuestionID: questionID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrQuestionNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Question: %w", err)
		}
		if !current.TaskID.Valid && current.OriginUserID.UUID != principal.UserID {
			return ErrQuestionNotFound
		}
		// Inside a Project, answering and withdrawing a Task-origin Question
		// both need project.approvals.resolve there (ADR 0017); the Company
		// catalog is not consulted. Outside one, anyone who sees a Task
		// Question may answer it and withdrawing needs the Company
		// approvals.resolve, as before.
		scoped := false
		if current.TaskID.Valid {
			_, scoped, err = auth.RequireTaskChatPermission(
				ctx, q, principal, current.ConversationID, auth.ProjectPermissionApprovalsResolve,
			)
			if errors.Is(err, auth.ErrTaskNotVisible) {
				return ErrQuestionNotFound
			}
			if err != nil {
				return err
			}
		}
		if questionHasLapsed(current, time.Now().UTC()) {
			return ErrQuestionExpired
		}
		if current.Status != string(QuestionStatusPending) {
			return ErrQuestionConflict
		}
		var answers []byte
		expectedRevision := withdrawRevision
		if status == QuestionStatusAnswered {
			var prompts []QuestionPrompt
			if err := json.Unmarshal(current.Prompts, &prompts); err != nil {
				return fmt.Errorf("decode Question prompts: %w", err)
			}
			answerInput, err = normalizeQuestionAnswerInput(prompts, answerInput)
			if err != nil {
				return err
			}
			expectedRevision = answerInput.ExpectedRevision
			answers, err = json.Marshal(answerInput.Answers)
			if err != nil {
				return ErrQuestionInvalidInput
			}
		} else if current.TaskID.Valid && !scoped && !role.Can(auth.PermissionApprovalsResolve) {
			return ErrQuestionForbidden
		}
		if current.Version != expectedRevision {
			return ErrQuestionConflict
		}
		resolved, err := q.ResolveQuestion(ctx, generated.ResolveQuestionParams{
			Status:           string(status),
			Answers:          answers,
			ResolvedByUserID: principal.UserID,
			ContinuationPrompt: fmt.Sprintf(
				"Question %s. %s", current.ID, questionContinuationPrompt(status, answerInput.Answers),
			),
			CompanyID:       principal.CompanyID,
			QuestionID:      questionID,
			ExpectedVersion: expectedRevision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrQuestionConflict
		}
		if err != nil {
			return fmt.Errorf("resolve Question: %w", err)
		}
		if err := inbox.CloseEntityInTx(
			ctx,
			q,
			&pending,
			principal.CompanyID,
			inbox.Entity{Type: protocol.EntityQuestion, ID: questionID.String()},
			inbox.Actor{Kind: "user", ID: &principal.UserID},
		); err != nil {
			return fmt.Errorf("close Question Inbox items: %w", err)
		}
		result, err = questionProjectionForActor(ctx, q, resolved, principal.UserID)
		if err != nil {
			return err
		}
		return stageQuestionEvent(
			ctx, q, &pending,
			EventQuestionResolved,
			events.UserActor(principal.UserID.String()),
			result,
		)
	})
	if err != nil {
		return Question{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func authorizeQuestionRaise(
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	requestKey uuid.UUID,
) error {
	if principal.CompanyID != origin.CompanyID || !util.ValidUUID(requestKey) {
		return ErrQuestionInvalidInput
	}
	if origin.TaskID == nil {
		if origin.OriginUserID == nil || (origin.ConversationKind != "channel_agent" && *origin.OriginUserID != principal.UserID) {
			return ErrQuestionForbidden
		}
	}
	return nil
}

func normalizeQuestionListOptions(options QuestionListOptions) (QuestionListOptions, error) {
	if options.Limit == 0 {
		options.Limit = 50
	}
	if len(options.Statuses) == 0 {
		options.Statuses = []QuestionStatus{QuestionStatusPending}
	}
	if options.Limit < 1 || options.Limit > 100 || options.AgentID != nil && !util.ValidUUID(*options.AgentID) ||
		options.TaskID != nil && !util.ValidUUID(*options.TaskID) {
		return QuestionListOptions{}, ErrQuestionInvalidInput
	}
	seen := make(map[QuestionStatus]struct{}, len(options.Statuses))
	for _, status := range options.Statuses {
		if !validQuestionStatus(status) {
			return QuestionListOptions{}, ErrQuestionInvalidInput
		}
		if _, duplicate := seen[status]; duplicate {
			return QuestionListOptions{}, ErrQuestionInvalidInput
		}
		seen[status] = struct{}{}
	}
	if options.Before != nil && (options.Before.CreatedAt.IsZero() || !util.ValidUUID(options.Before.ID)) {
		return QuestionListOptions{}, ErrQuestionInvalidInput
	}
	return options, nil
}

// createQuestionInboxItems addresses the Question to who may answer it: the
// deciders of a Task-origin Question in a Task chat with a Project (its
// readers holding project.approvals.resolve, ADR 0017), every active member
// for another Task Question, and the origin person for a direct one.
func createQuestionInboxItems(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	row generated.Question,
	deciders []uuid.UUID,
	scoped bool,
) ([]inbox.CreateResult, error) {
	var recipients []uuid.UUID
	var err error
	switch {
	case scoped:
		recipients = deciders
	case row.TaskID.Valid:
		recipients, err = q.ListActiveMemberUserIDs(ctx, row.CompanyID)
	default:
		recipients = []uuid.UUID{row.OriginUserID.UUID}
	}
	if err != nil {
		return nil, fmt.Errorf("list Question resolvers: %w", err)
	}
	deepLink := requestChatDeepLink(row.AskedByAgentID, row.ConversationID, row.TaskID) +
		"&questionId=" + row.ID.String()
	results := make([]inbox.CreateResult, 0, len(recipients))
	for _, recipientID := range recipients {
		result, err := inbox.CreateInTx(ctx, q, pending, inbox.CreateInput{
			CompanyID:       row.CompanyID,
			RecipientUserID: recipientID,
			ConversationID:  &row.ConversationID,
			Type:            inbox.TypeQuestionRequest,
			Entity:          inbox.Entity{Type: protocol.EntityQuestion, ID: row.ID.String()},
			Title:           row.Title,
			Body:            "An agent needs your answer before it can continue.",
			DeepLink:        &deepLink,
			DedupeKey:       "question:" + row.ID.String() + ":asked",
			Actor:           inbox.Actor{Kind: "agent", ID: &row.AskedByAgentID},
		})
		if err != nil {
			return nil, fmt.Errorf("create Question Inbox item: %w", err)
		}
		if result.Created {
			results = append(results, result)
		}
	}
	return results, nil
}

func questionProjectionForActor(
	ctx context.Context,
	q *generated.Queries,
	row generated.Question,
	actorUserID uuid.UUID,
) (Question, error) {
	projection, err := q.GetQuestionProjection(ctx, generated.GetQuestionProjectionParams{
		CompanyID: row.CompanyID, QuestionID: row.ID, ActorUserID: actorUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Question{}, ErrQuestionNotFound
	}
	if err != nil {
		return Question{}, fmt.Errorf("load Question projection: %w", err)
	}
	return questionFromProjection(
		projection.Question,
		projection.AskingAgentName,
		projection.ResolvedByDisplayName,
		projection.ResolvedByAvatarUrl,
	)
}

// questionProjectionForSystem is approvalProjectionForSystem for a Question.
func questionProjectionForSystem(
	ctx context.Context,
	q *generated.Queries,
	row generated.Question,
) (Question, error) {
	projection, err := q.GetQuestionProjectionForSystem(ctx, generated.GetQuestionProjectionForSystemParams{
		CompanyID: row.CompanyID, QuestionID: row.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Question{}, ErrQuestionNotFound
	}
	if err != nil {
		return Question{}, fmt.Errorf("load Question projection: %w", err)
	}
	return questionFromProjection(
		projection.Question,
		projection.AskingAgentName,
		projection.ResolvedByDisplayName,
		projection.ResolvedByAvatarUrl,
	)
}

func questionFromProjection(
	row generated.Question,
	askingAgentName string,
	resolvedByDisplayName, resolvedByAvatarURL pgtype.Text,
) (Question, error) {
	var prompts []QuestionPrompt
	if err := json.Unmarshal(row.Prompts, &prompts); err != nil {
		return Question{}, fmt.Errorf("decode Question prompts: %w", err)
	}
	var answers []QuestionAnswer
	if row.Answers != nil {
		if err := json.Unmarshal(row.Answers, &answers); err != nil {
			return Question{}, fmt.Errorf("decode Question answers: %w", err)
		}
	}
	value := Question{
		ID: row.ID, CompanyID: row.CompanyID, Status: QuestionStatus(row.Status), Title: row.Title,
		AskedByAgent: ActorRef{ID: row.AskedByAgentID, Kind: "agent", DisplayName: askingAgentName},
		OriginRunID:  row.OriginRunID, TaskID: util.NullUUIDPtr(row.TaskID),
		ConversationID: row.ConversationID, Prompts: prompts,
		OriginUserID:      row.OriginUserID.UUID,
		ExpiresAt:         row.ExpiresAt.Time.UTC(),
		Answers:           answers,
		ContinuationRunID: util.NullUUIDPtr(row.ContinuationRunID),
		CreatedAt:         row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
	if row.ResolvedByUserID.Valid {
		value.AnsweredBy = &ActorRef{
			ID: row.ResolvedByUserID.UUID, Kind: "user",
			DisplayName: resolvedByDisplayName.String,
			AvatarURL:   util.TextToPtr(resolvedByAvatarURL),
		}
	}
	return value, nil
}

// stageQuestionEvent adds one Question frame to the producing transaction's
// batch, and aborts that transaction when the frame cannot be event, exactly
// as stageApprovalEvent does. Routing is decided the same way: the Project's
// readers for a Task-origin Question, else the origin user.
func stageQuestionEvent(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	name string,
	actor events.Actor,
	value Question,
) error {
	return stageApprovalFrames(ctx, q, pending, questionEvent(name, actor, value))
}

// questionEvent builds one question.* frame around a Question snapshot.
func questionEvent(name string, actor events.Actor, value Question) events.Event {
	return events.Event{
		EntityType:     TopicQuestion,
		Name:           name,
		CompanyID:      value.CompanyID,
		CreatedAt:      time.Now().UTC(),
		RunID:          value.OriginRunID,
		TaskID:         util.UUIDToValue(value.TaskID),
		ConversationID: value.ConversationID,
		Actor:          actor,
		Payload:        value,
	}
}

func questionContinuationPrompt(status QuestionStatus, answers []QuestionAnswer) string {
	if status != QuestionStatusAnswered {
		return "The question was withdrawn without an answer."
	}
	encoded, err := json.Marshal(answers)
	if err != nil {
		return "The question was answered."
	}
	var builder strings.Builder
	builder.WriteString("The question was answered. Answers: ")
	builder.Write(encoded)
	return builder.String()
}
