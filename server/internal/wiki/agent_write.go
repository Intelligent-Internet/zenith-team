package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/reearth/ygo/crdt"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	// MaxAgentWritePagesPerCall bounds the pages one write may carry, and
	// MaxAgentWritePagesPerRun the pages one Run may own in total.
	MaxAgentWritePagesPerCall = 12
	MaxAgentWritePagesPerRun  = 40 // across every attempt of the Company's research

	// AgentWriteCreated, AgentWriteUpdated and AgentWriteUnchanged are the
	// per-page outcomes of a write. A replayed write is all unchanged.
	AgentWriteCreated   = "created"
	AgentWriteUpdated   = "updated"
	AgentWriteUnchanged = "unchanged"

	// researchRunSource is the frozen execution profile source of the founder's
	// company-research Run, the only Run that may write the Wiki.
	researchRunSource = "company_research"
)

var (
	// ErrAgentWriteForbidden covers every Run that may not write: not live, not
	// the founder's company-research Run, or not working for the caller.
	ErrAgentWriteForbidden = errors.New("this Run may not write the Company Wiki")
	ErrAgentWriteLimit     = errors.New("Company Wiki page limit for this Run reached")
)

// agentWriteKeyPattern is a page key: the Run's own handle for a page it
// writes, and the target of a [[key]] placeholder.
var (
	agentWriteKeyPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	agentWritePlaceholder = regexp.MustCompile(`\[\[([a-z0-9][a-z0-9-]{0,63})(?:\|([^\]\n]*))?\]\]`)
	labelUnsafe           = strings.NewReplacer("[", " ", "]", " ", "|", " ", "\n", " ", "\r", " ")
)

// AgentWritePageInput is one page a Run saves. Markdown may link to another
// page of the same Run with [[key]] or [[key|label]].
type AgentWritePageInput struct {
	Key, Title, Summary, Markdown string
}

// AgentWriteInput is one write call. The research has no say in where its
// pages are filed: a new page goes into the Company's "Company" collection,
// and a page it wrote before stays wherever it is now.
type AgentWriteInput struct {
	Pages []AgentWritePageInput
}

// AgentWriteResult is one page of a write. Revision is the content revision
// number of the page's current revision.
type AgentWriteResult struct {
	Key      string    `json:"key"`
	PageID   uuid.UUID `json:"pageId"`
	Title    string    `json:"title"`
	Revision int64     `json:"revision"`
	Status   string    `json:"status"`
}

// AgentWriteResponse lists the pages the call saved (or found unchanged), the
// placeholders it could not resolve, and the Run's page total.
type AgentWriteResponse struct {
	Pages      []AgentWriteResult `json:"pages"`
	Warnings   []string           `json:"warnings"`
	TotalPages int                `json:"totalPages"`
}

// AgentWriteService is the one Wiki write an Agent has: the founder's
// company-research Run saving the pages of its research.
//
// The caller has proved the Run (its token, Company and authorizer). The
// service re-proves it in the write transaction: the Run is running, its
// frozen source is company_research, it works for the member, the Agent's
// wiki switch is on, and the member holds wiki.publish. Pages are authored by
// that member and published at once; a Run reaches only the pages it created,
// by key, and never any other page.
type AgentWriteService interface {
	Write(
		ctx context.Context,
		principal auth.UserPrincipal,
		runID uuid.UUID,
		input AgentWriteInput,
	) (AgentWriteResponse, error)
}

var _ AgentWriteService = (*DefaultAgentWriteService)(nil)

type DefaultAgentWriteService struct {
	store *db.Store
	bus   *events.Bus
}

func NewAgentWriteService(store *db.Store, bus *events.Bus) (*DefaultAgentWriteService, error) {
	if store == nil || bus == nil {
		return nil, errors.New("Wiki agent write store and event bus are required")
	}
	return &DefaultAgentWriteService{store: store, bus: bus}, nil
}

// agentWriteTarget is a page the research owns, or creates in this call.
type agentWriteTarget struct {
	pageID  uuid.UUID
	title   string
	source  string
	status  string
	created bool
	// lastRevision is the published revision the research last wrote to the
	// page, and lastDraftSeq the draft sequence that publish left.
	lastRevision uuid.NullUUID
	lastDraftSeq pgtype.Int8
}

// editedByPersonInTx reports whether someone changed the page after the
// research last wrote it: a different published revision, or a draft that
// moved on without being published (the editor saves drafts continuously).
// Either way the Run must not replace that work. A page with no recorded
// revision is treated the same way; a row written before the draft sequence
// was recorded is judged by its published revision alone.
func editedByPersonInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	current PageResponse,
	target agentWriteTarget,
) (bool, error) {
	if current.CurrentRevisionID == nil || !target.lastRevision.Valid ||
		*current.CurrentRevisionID != target.lastRevision.UUID {
		return true, nil
	}
	if !target.lastDraftSeq.Valid {
		return false, nil
	}
	draft, err := BlockDraftInTx(ctx, q, companyID, current.ID)
	if err != nil {
		return false, fmt.Errorf("read Wiki draft: %w", err)
	}
	return draft.HeadSeq != target.lastDraftSeq.Int64, nil
}

// withoutTitleHeading drops a leading "# <title>" line that only repeats the
// page title: the Wiki already shows the title above the body, so the Run's
// habit of opening with it printed the title twice.
func withoutTitleHeading(title, markdown string) string {
	first, rest, _ := strings.Cut(strings.TrimLeft(markdown, "\n"), "\n")
	heading, isHeading := strings.CutPrefix(strings.TrimSpace(first), "# ")
	if !isHeading || !strings.EqualFold(strings.TrimSpace(heading), strings.TrimSpace(title)) {
		return markdown
	}
	return strings.TrimLeft(rest, "\n")
}

func isUUIDText(value string) bool {
	if len(value) != 36 {
		return false
	}
	_, err := uuid.Parse(value)
	return err == nil
}

func normalizeAgentWrite(input AgentWriteInput) (AgentWriteInput, error) {
	if len(input.Pages) < 1 || len(input.Pages) > MaxAgentWritePagesPerCall {
		return AgentWriteInput{}, ErrPageInvalidInput
	}
	result := AgentWriteInput{Pages: make([]AgentWritePageInput, len(input.Pages))}
	seen := make(map[string]struct{}, len(input.Pages))
	for i, page := range input.Pages {
		if !agentWriteKeyPattern.MatchString(page.Key) || isUUIDText(page.Key) {
			return AgentWriteInput{}, ErrPageInvalidInput
		}
		if _, duplicate := seen[page.Key]; duplicate {
			return AgentWriteInput{}, ErrPageInvalidInput
		}
		seen[page.Key] = struct{}{}
		title, markdown, summary, err := normalizePageInput(page.Title, withoutTitleHeading(page.Title, page.Markdown), page.Summary)
		if err != nil {
			return AgentWriteInput{}, err
		}
		result.Pages[i] = AgentWritePageInput{Key: page.Key, Title: title, Summary: summary, Markdown: markdown}
	}
	return result, nil
}

// wikiKeyReferences lists the page keys a markdown text links to, once each.
// A page id or an Artifact citation is not a key.
func wikiKeyReferences(markdown string) []string {
	keys := make([]string, 0)
	for _, match := range agentWritePlaceholder.FindAllStringSubmatch(markdown, -1) {
		if !isUUIDText(match[1]) && !slices.Contains(keys, match[1]) {
			keys = append(keys, match[1])
		}
	}
	return keys
}

// resolveWikiKeys turns [[key]] placeholders into page links. A key the Run has
// no page for becomes its label (or the key) as plain text and is returned as
// missing. Page-id links and Artifact citations are left untouched.
func resolveWikiKeys(markdown string, known map[string]agentWriteTarget) (string, []string) {
	missing := make([]string, 0)
	resolved := agentWritePlaceholder.ReplaceAllStringFunc(markdown, func(token string) string {
		match := agentWritePlaceholder.FindStringSubmatch(token)
		key, label := match[1], strings.TrimSpace(labelUnsafe.Replace(match[2]))
		if isUUIDText(key) {
			return token
		}
		target, ok := known[key]
		if !ok {
			if !slices.Contains(missing, key) {
				missing = append(missing, key)
			}
			if label == "" {
				return key
			}
			return label
		}
		if label == "" {
			label = strings.TrimSpace(labelUnsafe.Replace(target.title))
		}
		return "[[" + target.pageID.String() + "|" + label + "]]"
	})
	return resolved, missing
}

// replaceDraftInTx makes the page's draft the given content, title, summary and
// collection, as one new draft sequence. It returns that sequence.
//
// The Markdown parse and the CRDT encode stay inside the transaction on
// purpose: the Markdown is the resolved text, whose [[key]] placeholders
// became the ids of pages this same transaction created, the block ids come
// from the page's reservation rows, and the CRDT edit replaces the content of
// the draft state read under the page lock (an encode against any earlier
// state would discard a person's concurrent save). What the batch holds is its
// own page rows and the research-write lock, never the Company or anyone
// else's page.
func replaceDraftInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, pageID uuid.UUID,
	title, summary string,
	collectionID *uuid.UUID,
	markdown string,
	agentID uuid.UUID,
) (int64, error) {
	draft, err := BlockDraftInTx(ctx, q, companyID, pageID)
	if err != nil {
		return 0, err
	}
	root := BlocksFromMarkdown(markdown)
	if err = ValidateStandaloneBlocks(root); err != nil {
		return 0, err
	}
	if err = AllocateBlockTreeInTx(ctx, q, companyID, pageID, &root); err != nil {
		return 0, err
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err = crdt.ApplyUpdateV1(doc, draft.State, nil); err != nil {
		return 0, err
	}
	fragment := doc.GetXmlFragment("content")
	doc.Transact(func(tx *crdt.Transaction) {
		fragment.Delete(tx, 0, fragment.Len())
		appendBlockNodes(tx, fragment, 0, root.Content)
	})
	state, merged, body, err := MergeBlocks(doc.EncodeStateAsUpdate(), nil)
	if err != nil {
		return 0, err
	}
	content, err := json.Marshal(merged)
	if err != nil {
		return 0, err
	}
	saved, err := q.RestoreWikiBlockDraft(ctx, generated.RestoreWikiBlockDraftParams{
		CompanyID: companyID, PageID: pageID, State: state, Content: content,
		Markdown: strings.TrimRight(body, "\n"), Title: title, Summary: summary,
		CollectionID: nullableUUID(collectionID), HeadSeq: draft.HeadSeq,
	})
	if err != nil {
		return 0, err
	}
	// The saved document history names the Agent that wrote it, never the
	// founder the Run works for.
	if err = RecordDocumentChangeInTx(ctx, q, DocumentChangeInput{
		CompanyID: companyID, PageID: pageID, ActorType: "agent", ActorID: agentID,
		ChangeType: "edit", Sequence: saved.HeadSeq, Before: draft.State, State: state,
	}); err != nil {
		return 0, err
	}
	return saved.HeadSeq, nil
}

// savePageInTx replaces a page's content, publishes it as the Agent's revision
// on behalf of the member, and returns the draft sequence the publish left.
func savePageInTx(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	pageID uuid.UUID,
	title, summary string,
	collectionID *uuid.UUID,
	markdown string,
	agentID uuid.UUID,
) (PageResponse, bool, int64, error) {
	page, err := q.GetWikiPageForUpdate(ctx, generated.GetWikiPageForUpdateParams{
		CompanyID: principal.CompanyID, PageID: pageID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PageResponse{}, false, 0, ErrPageNotFound
	}
	if err != nil {
		return PageResponse{}, false, 0, fmt.Errorf("lock Wiki page: %w", err)
	}
	sequence, err := replaceDraftInTx(ctx, q, principal.CompanyID, pageID, title, summary, collectionID, markdown, agentID)
	if err != nil {
		return PageResponse{}, false, 0, err
	}
	published, created, err := publishDraftInTx(ctx, q, principal, pageID, PublishPageInput{
		ExpectedRevision: page.Version, ExpectedSequence: sequence,
	})
	if err != nil {
		return PageResponse{}, false, 0, err
	}
	if err = q.SetWikiRevisionAuthorAgent(ctx, generated.SetWikiRevisionAuthorAgentParams{
		CompanyID: principal.CompanyID, RevisionID: *published.CurrentRevisionID, AgentID: agentID,
	}); err != nil {
		return PageResponse{}, false, 0, fmt.Errorf("record Wiki revision Agent: %w", err)
	}
	draft, err := BlockDraftInTx(ctx, q, principal.CompanyID, pageID)
	if err != nil {
		return PageResponse{}, false, 0, fmt.Errorf("read Wiki draft: %w", err)
	}
	return published, created, draft.HeadSeq, nil
}

func (s *DefaultAgentWriteService) Write(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	input AgentWriteInput,
) (AgentWriteResponse, error) {
	if runID == uuid.Nil {
		return AgentWriteResponse{}, ErrRunNotFound
	}
	input, err := normalizeAgentWrite(input)
	if err != nil {
		return AgentWriteResponse{}, err
	}
	var response AgentWriteResponse
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		response, pending = AgentWriteResponse{Pages: []AgentWriteResult{}, Warnings: []string{}}, nil
		return writeInTx(ctx, q, principal, runID, input, &response, &pending)
	})
	if err != nil {
		return AgentWriteResponse{}, err
	}
	for _, event := range pending {
		s.bus.Publish(ctx, TopicWiki, event)
	}
	return response, nil
}

func writeInTx(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	input AgentWriteInput,
	response *AgentWriteResponse,
	pending *[]events.Event,
) error {
	if err := authorizePublisher(ctx, q, principal); err != nil {
		return err
	}
	standing, err := q.LockRunForWikiWrite(ctx, generated.LockRunForWikiWriteParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID, RunID: runID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("lock Run for Wiki write: %w", err)
	}
	if standing.Status != "running" || standing.Source != researchRunSource || !standing.Authorized {
		return ErrAgentWriteForbidden
	}
	if standing.Disabled {
		return ErrAgentAccessDisabled
	}
	// Only a proven research Run reaches this point. Its writes queue behind
	// one another here, ahead of every page lock: the 40-page cap and key
	// ownership read the whole lineage's pages and then insert, so two
	// concurrent calls must not both read the same total. Only a research
	// write takes this lock; editors, autosaves and other Wiki writes never
	// wait for it, and the batch locks page rows one by one as it reaches
	// them, so an autosave of any other page is untouched for its whole run.
	if err := q.LockWikiResearchWrites(ctx, principal.CompanyID); err != nil {
		return fmt.Errorf("lock research Wiki writes: %w", err)
	}
	// The batch files pages into the "Company" collection and publishes them,
	// so it holds the shared collections lock like any other filing write.
	if err := q.LockWikiCollectionsShared(ctx, principal.CompanyID); err != nil {
		return fmt.Errorf("lock Wiki collections: %w", err)
	}
	owned, err := q.ListRunWikiWrites(ctx, principal.CompanyID)
	if err != nil {
		return fmt.Errorf("list research Wiki writes: %w", err)
	}
	actor := events.RunActor(standing.AgentID.String(), runID.String(), principal.UserID.String())
	known := make(map[string]agentWriteTarget, len(owned)+len(input.Pages))
	for _, row := range owned {
		known[row.Key] = agentWriteTarget{
			pageID: row.PageID, title: row.Title, source: row.SourceMarkdown, status: row.Status,
			lastRevision: row.LastRevisionID, lastDraftSeq: row.LastDraftSeq,
		}
	}
	added := make([]string, 0, len(input.Pages))
	for _, page := range input.Pages {
		if _, exists := known[page.Key]; !exists {
			added = append(added, page.Key)
		}
	}
	response.TotalPages = len(owned) + len(added)
	if response.TotalPages > MaxAgentWritePagesPerRun {
		return ErrAgentWriteLimit
	}

	// The research files new pages in the Company's "Company" collection: the
	// pages describe the Company, and people go on editing them there. An
	// existing collection of that name, in any case, is reused.
	var collectionID *uuid.UUID
	if len(added) > 0 {
		collection, err := q.GetWikiCollectionByName(ctx, generated.GetWikiCollectionByNameParams{
			CompanyID: principal.CompanyID, Name: "Company",
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// A person may create the same name between the read and the
			// insert; no row back means they won, and their collection is reused.
			collection, err = q.CreateWikiCollectionIfAbsent(ctx, generated.CreateWikiCollectionIfAbsentParams{
				CompanyID: principal.CompanyID, Name: "Company",
			})
			if errors.Is(err, pgx.ErrNoRows) {
				collection, err = q.GetWikiCollectionByName(ctx, generated.GetWikiCollectionByNameParams{
					CompanyID: principal.CompanyID, Name: "Company",
				})
			} else if err == nil {
				*pending = append(*pending, newCollectionEvent(principal.CompanyID, actor, EventCollectionCreated, collectionResponse(collection)))
			}
			if err != nil {
				return fmt.Errorf("create Wiki collection: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("find Wiki collection: %w", err)
		}
		collectionID = &collection.ID
	}

	// Create every new page first: a placeholder can then name any page of
	// the batch, whatever the order the Run listed them in.
	for _, page := range input.Pages {
		target, exists := known[page.Key]
		if !exists {
			id, err := createDraftInTx(ctx, q, principal, page.Title, page.Summary, "", collectionID)
			if err != nil {
				return err
			}
			if err = q.InsertRunWikiWrite(ctx, generated.InsertRunWikiWriteParams{
				RunID: runID, CompanyID: principal.CompanyID, Key: page.Key, PageID: id,
			}); err != nil {
				return fmt.Errorf("record Run Wiki write: %w", err)
			}
			target = agentWriteTarget{pageID: id, status: "active", created: true}
		}
		target.title = page.Title
		known[page.Key] = target
	}

	inBatch := make(map[string]struct{}, len(input.Pages))
	published := make([]PageResponse, 0, len(input.Pages))
	save := func(key string, target agentWriteTarget, title, summary string, collection *uuid.UUID, source, markdown string) error {
		resolved, missing := resolveWikiKeys(markdown, known)
		for _, missingKey := range missing {
			response.Warnings = append(response.Warnings, fmt.Sprintf(
				"page %q links to [[%s]], which is not a page the research has written; it was left as plain text", key, missingKey))
		}
		page, firstPublication, draftSeq, err := savePageInTx(ctx, q, principal, target.pageID, title, summary, collection, resolved, standing.AgentID)
		if err != nil {
			return err
		}
		if err = q.SetRunWikiWriteSaved(ctx, generated.SetRunWikiWriteSavedParams{
			CompanyID: principal.CompanyID, PageID: target.pageID, SourceMarkdown: source,
			LastRevisionID: nullableUUID(page.CurrentRevisionID),
			LastDraftSeq:   pgtype.Int8{Int64: draftSeq, Valid: true},
		}); err != nil {
			return fmt.Errorf("record Run Wiki source: %w", err)
		}
		name, status := EventPageUpdated, AgentWriteUpdated
		if firstPublication {
			name = EventPageCreated
		}
		if target.created {
			status = AgentWriteCreated
		}
		*pending = append(*pending, newPageEvent(principal.CompanyID, actor, name, page))
		response.Pages = append(response.Pages, AgentWriteResult{
			Key: key, PageID: page.ID, Title: page.Title, Revision: page.ContentRevision, Status: status,
		})
		published = append(published, page)
		return nil
	}

	for _, page := range input.Pages {
		inBatch[page.Key] = struct{}{}
		target := known[page.Key]
		collection := collectionID
		if !target.created {
			current, err := readPage(ctx, q, principal, target.pageID)
			if err != nil {
				return err
			}
			if current.Status != "active" {
				response.Warnings = append(response.Warnings, fmt.Sprintf(
					"page %q was archived by a person and was not changed", page.Key))
				continue
			}
			edited, err := editedByPersonInTx(ctx, q, principal.CompanyID, current, target)
			if err != nil {
				return err
			}
			if edited {
				response.Warnings = append(response.Warnings, fmt.Sprintf(
					"page %q was edited by a person after the research wrote it and was not changed", page.Key))
				continue
			}
			collection = current.CollectionID
			unchanged := current.Title == page.Title && current.Summary == page.Summary &&
				target.source == page.Markdown && !referencesAny(page.Markdown, added)
			if unchanged {
				response.Pages = append(response.Pages, AgentWriteResult{
					Key: page.Key, PageID: current.ID, Title: current.Title,
					Revision: current.ContentRevision, Status: AgentWriteUnchanged,
				})
				continue
			}
		}
		if err := save(page.Key, target, page.Title, page.Summary, collection, page.Markdown, page.Markdown); err != nil {
			return err
		}
	}

	// A page written by an earlier call may have linked to a key that did not
	// exist then and was left as text; it links now that the key does.
	for _, row := range owned {
		if _, current := inBatch[row.Key]; current || row.Status != "active" || !referencesAny(row.SourceMarkdown, added) {
			continue
		}
		page, err := readPage(ctx, q, principal, row.PageID)
		if err != nil {
			return err
		}
		edited, err := editedByPersonInTx(ctx, q, principal.CompanyID, page, known[row.Key])
		if err != nil {
			return err
		}
		if edited {
			response.Warnings = append(response.Warnings, fmt.Sprintf(
				"page %q was edited by a person after the research wrote it; its new links were not added", row.Key))
			continue
		}
		if err = save(row.Key, known[row.Key], page.Title, page.Summary, page.CollectionID, row.SourceMarkdown, row.SourceMarkdown); err != nil {
			return err
		}
	}

	// Publishing records an edge only to a page already published, so a link to
	// a later page of the batch was dropped: every edge is rebuilt now that all
	// of them are.
	for _, page := range published {
		if err = replaceLinks(ctx, q, principal.CompanyID, page.ID, page.Body); err != nil {
			return err
		}
	}
	return nil
}

func referencesAny(markdown string, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	for _, key := range wikiKeyReferences(markdown) {
		if slices.Contains(keys, key) {
			return true
		}
	}
	return false
}
