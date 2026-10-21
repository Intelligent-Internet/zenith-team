package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type PresentationInput struct {
	ChangeID    *uuid.UUID `json:"changeId,omitempty"`
	PageID      uuid.UUID  `json:"pageId"             binding:"required"`
	RequestKey  uuid.UUID  `json:"requestKey"         binding:"required"`
	Sequence    int64      `json:"sequence"           binding:"min=0"`
	Fingerprint string     `json:"fingerprint"        binding:"required,len=64"`
}
type PresentationEntry struct {
	ChangeID  *uuid.UUID `json:"changeId,omitempty"`
	PageID    uuid.UUID  `json:"pageId"`
	Theme     string     `json:"theme"`
	ID        uuid.UUID  `json:"id"`
	Title     string     `json:"title"`
	Sequence  int64      `json:"sequence"`
	CreatedAt time.Time  `json:"createdAt"`
	RunID     *uuid.UUID `json:"runId"`
	OutputID  *uuid.UUID `json:"outputId"`
}
type Presentation struct {
	PresentationEntry
	Content                   json.RawMessage           `json:"content"`
	Documents                 map[string]wiki.BlockNode `json:"documents"`
	Fingerprint               string                    `json:"fingerprint"`
	Issues                    []string                  `json:"issues"`
	SourceCount               int                       `json:"sourceCount"`
	CanExport                 bool                      `json:"canExport"`
	RequiredSuggestionIndexes []int                     `json:"requiredSuggestionIndexes"`
}
type presentationCapture struct {
	Presentation
	root              wiki.BlockNode
	version           generated.CanvasVersionSource
	sources           map[uuid.UUID]generated.CanvasVersionSource
	markdown          string
	sharedContent     json.RawMessage
	changeSnapshot    json.RawMessage
	changeFingerprint string
}

func (s *DefaultPresentationService) buildPresentation(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
	sequence int64,
	changeID *uuid.UUID,
) (presentationCapture, error) {
	c := presentationCapture{
		Presentation: Presentation{
			Documents:                 map[string]wiki.BlockNode{},
			Issues:                    []string{},
			CanExport:                 true,
			RequiredSuggestionIndexes: []int{},
		},
		sources: map[uuid.UUID]generated.CanvasVersionSource{},
	}
	var err error
	c.version, err = q.GetProjectPageVersion(
		ctx,
		generated.GetProjectPageVersionParams{CompanyID: p.CompanyID, ProjectID: project, ID: pageID},
	)
	if err != nil {
		return c, lookup(err)
	}
	pendingCreation, err := q.PagePendingCreation(
		ctx,
		generated.PagePendingCreationParams{CompanyID: p.CompanyID, WikiPageID: nullable(&pageID)},
	)
	if err != nil {
		return c, err
	}
	if pendingCreation && changeID == nil {
		return c, ErrConflict
	}
	title, err := q.GetOverviewArtifactTitle(
		ctx,
		generated.GetOverviewArtifactTitleParams{
			CompanyID: p.CompanyID,
			ProjectID: project,
			ID:        c.version.ArtifactID,
		},
	)
	if err != nil {
		return c, lookup(err)
	}
	draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, c.version.WikiPageID.UUID)
	if err != nil {
		return c, err
	}
	if sequence >= 0 && draft.HeadSeq != sequence {
		return c, ErrConflict
	}
	c.sharedContent = draft.Content
	c.Title, c.Sequence, c.PageID = title, draft.HeadSeq, pageID
	metadata, err := q.GetProjectPageMetadata(
		ctx,
		generated.GetProjectPageMetadataParams{
			CompanyID: p.CompanyID,
			ProjectID: nullable(&project),
			ID:        pageID,
		},
	)
	if err != nil {
		return c, err
	}
	c.Theme = metadata.Theme
	if err = json.Unmarshal(draft.Content, &c.root); err != nil {
		return c, err
	}
	if changeID != nil {
		proposal, e := q.GetProjectPageChange(
			ctx,
			generated.GetProjectPageChangeParams{CompanyID: p.CompanyID, ProjectID: project, ID: *changeID},
		)
		if e != nil {
			return c, lookup(e)
		}
		if proposal.PageID != pageID {
			return c, ErrNotFound
		}
		if proposal.Status != "proposed" && proposal.Status != "partially_applied" {
			return c, ErrConflict
		}
		review, e := pageSuggestionState(proposal, c.root, metadata.Theme, nil)
		if e != nil {
			return c, e
		}
		var operations []wiki.BlockOperation
		for i, op := range review.operations {
			if review.decisions[i] != "" {
				continue
			}
			if review.suggestions[i].Status != "pending" {
				return c, ErrConflict
			}
			c.RequiredSuggestionIndexes = append(c.RequiredSuggestionIndexes, i)
			if op.Type != "theme" {
				operations = append(operations, op)
			}
		}
		_, c.root, err = wiki.ApplyBlockOperations(draft.State, operations)
		if err != nil {
			return c, err
		}
		if proposal.AfterTheme.Valid {
			if c.Theme != proposal.BeforeTheme.String {
				return c, ErrConflict
			}
			c.Theme = proposal.AfterTheme.String
		}
		c.ChangeID = changeID
		c.changeFingerprint = review.fingerprint
		c.changeSnapshot, _ = json.Marshal(struct {
			Before          json.RawMessage       `json:"before"`
			Operations      []wiki.BlockOperation `json:"operations"`
			Decisions       map[int]string        `json:"decisions"`
			ProposedContent wiki.BlockNode        `json:"proposedContent"`
			SharedTheme     string                `json:"sharedTheme"`
		}{proposal.BeforeContent, review.operations, review.decisions, c.root, metadata.Theme})
	}

	tasks, err := q.ListOverviewTasks(
		ctx,
		generated.ListOverviewTasksParams{CompanyID: p.CompanyID, ProjectID: project},
	)
	if err != nil {
		return c, err
	}
	artifactRows, err := q.ListCanvasArtifacts(
		ctx,
		generated.ListCanvasArtifactsParams{
			CompanyID:    p.CompanyID,
			ProjectID:    project,
			ActorUserID:  p.UserID,
			AllArtifacts: true,
		},
	)
	if err != nil {
		return c, err
	}
	artifacts, err := decodeArtifacts(ctx, q, p, project, artifactRows)
	if err != nil {
		return c, err
	}
	included := map[string]bool{}
	var collect func(wiki.BlockNode)
	collect = func(node wiki.BlockNode) {
		if node.Type == "artifactReference" {
			included[fmt.Sprint(node.Attrs["artifactId"])] = true
			included[fmt.Sprint(node.Attrs["versionId"])] = true
		}
		for _, child := range node.Content {
			collect(child)
		}
	}
	collect(c.root)
	freshness, err := pageSourceStates(ctx, q, p, project, c.root)
	if err != nil {
		return c, err
	}
	stale := map[uuid.UUID]bool{}
	for _, source := range freshness {
		if source.Status == "needs_refresh" {
			stale[source.VersionID] = true
		}
	}
	var byteCount int64
	active := map[uuid.UUID]bool{c.version.ID: true}
	var captureSource func(uuid.UUID, int, bool) error
	var walk func(*wiki.BlockNode, int) error
	captureSource = func(id uuid.UUID, depth int, live bool) error {
		documentKey := presentationDocumentKey(id.String(), live)
		if active[id] {
			c.Issues = append(
				c.Issues,
				"A recursive document reference must be changed to a link before saving.",
			)
			return nil
		}
		if _, ok := c.Documents[documentKey]; ok {
			return nil
		}
		if _, ok := c.sources[id]; ok && !live && !c.sources[id].WikiPageID.Valid {
			return nil
		}
		if len(c.sources) >= 256 || depth > 8 {
			c.Issues = append(
				c.Issues,
				"This presentation exceeds 256 sources or eight levels of embedded documents.",
			)
			return nil
		}
		if err := canReference(ctx, q, p, project, id, live); err != nil {
			if errors.Is(err, ErrNotFound) {
				c.Issues = append(
					c.Issues,
					"An included source is unavailable. Remove it or restore access before saving.",
				)
				return nil
			}
			return err
		}
		v, err := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{CompanyID: p.CompanyID, ProjectID: project, ID: id},
		)
		if err != nil {
			return err
		}
		c.sources[id] = v
		switch {
		case v.WikiRevisionID.Valid:
			var body wiki.BlockNode
			if live {
				if v.AccessOutputID.Valid {
					return ErrInvalid
				}
				page, err := q.GetCanvasWikiPage(
					ctx,
					generated.GetCanvasWikiPageParams{
						CompanyID: p.CompanyID,
						ID:        v.WikiPageID.UUID,
					},
				)
				if err != nil {
					return lookup(err)
				}
				if !page.ProjectID.Valid {
					revision, err := q.GetWikiCurrentRevision(
						ctx,
						generated.GetWikiCurrentRevisionParams{
							CompanyID: p.CompanyID,
							PageID:    page.ID,
						},
					)
					if err != nil {
						return lookup(err)
					}
					if len(revision.BlockContent) == 0 || string(revision.BlockContent) == "null" ||
						string(revision.BlockContent) == "{}" {
						body = wiki.BlocksFromMarkdown(revision.Body)
					} else if err = json.Unmarshal(
						revision.BlockContent,
						&body,
					); err != nil {
						return err
					}
					byteCount += int64(len(revision.Body) + len(revision.BlockContent))
				} else {
					draft, err := q.GetCanvasDraft(
						ctx,
						generated.GetCanvasDraftParams{
							CompanyID: p.CompanyID,
							PageID:    v.WikiPageID.UUID,
						},
					)
					if err != nil {
						return err
					}
					if err = json.Unmarshal(draft.Content, &body); err != nil {
						return err
					}
					byteCount += int64(len(draft.Content) + len(draft.Markdown))
				}
			} else {
				revision, err := q.GetCanvasWikiRevision(
					ctx,
					generated.GetCanvasWikiRevisionParams{
						CompanyID: p.CompanyID,
						PageID:    v.WikiPageID.UUID,
						ID:        v.WikiRevisionID.UUID,
					},
				)
				if err != nil {
					return err
				}
				if len(revision.BlockContent) == 0 || string(revision.BlockContent) == "null" ||
					string(revision.BlockContent) == "{}" {
					body = wiki.BlocksFromMarkdown(revision.Body)
				} else if err = json.Unmarshal(
					revision.BlockContent,
					&body,
				); err != nil {
					return err
				}
				byteCount += int64(len(revision.Body) + len(revision.BlockContent))
			}
			active[id] = true
			if err = walk(&body, depth+1); err != nil {
				return err
			}
			delete(active, id)
			c.Documents[documentKey] = body
		case v.AssetVersionID.Valid:
			size, err := q.GetPresentationAssetSize(
				ctx,
				generated.GetPresentationAssetSizeParams{
					CompanyID: p.CompanyID,
					ID:        v.AssetVersionID.UUID,
				},
			)
			if err != nil {
				return lookup(err)
			}
			byteCount += size
		case v.OutputID.Valid:
			previous, previousErr := q.GetProjectPresentationForOutput(
				ctx,
				generated.GetProjectPresentationForOutputParams{
					CompanyID: p.CompanyID,
					OutputID:  v.OutputID,
				},
			)
			if previousErr == nil {
				active[id] = true
				retained, err := q.ListProjectPresentationSources(
					ctx,
					generated.ListProjectPresentationSourcesParams{
						CompanyID:      p.CompanyID,
						ProjectID:      project,
						PresentationID: previous.ID,
					},
				)
				if err != nil {
					return err
				}
				// A saved presentation already owns its flattened evidence. Re-reading
				// the original version would substitute older content for a live capture.
				for _, source := range retained {
					if _, ok := c.sources[source.VersionID]; ok {
						continue
					}
					if len(c.sources) >= 256 {
						c.Issues = append(c.Issues, "This presentation exceeds 256 sources.")
						break
					}
					if _, err = canVersionSource(ctx, q, p, project, source.VersionID); err != nil {
						return err
					}
					version, err := q.GetCanvasVersion(
						ctx,
						generated.GetCanvasVersionParams{
							CompanyID: p.CompanyID,
							ProjectID: project,
							ID:        source.VersionID,
						},
					)
					if err != nil {
						return err
					}
					version.WikiRevisionID = source.WikiRevisionID
					c.sources[source.VersionID] = version
				}

				delete(active, id)
			} else if !errors.Is(previousErr, pgx.ErrNoRows) {
				return previousErr
			}
			outputType, err := q.GetPresentationOutputType(
				ctx,
				generated.GetPresentationOutputTypeParams{CompanyID: p.CompanyID, ID: v.OutputID.UUID},
			)
			if err != nil {
				return lookup(err)
			}
			if outputType == "app" {
				c.Issues = append(
					c.Issues,
					"Interactive apps cannot be retained as immutable evidence yet. Use an ordinary link or a file export.",
				)
			}
			items, err := q.ListOutputItems(
				ctx,
				generated.ListOutputItemsParams{
					CompanyID: p.CompanyID,
					OutputIds: []uuid.UUID{v.OutputID.UUID},
				},
			)
			if err != nil {
				return err
			}
			for _, item := range items {
				byteCount += item.SizeBytes
			}
		}
		return nil
	}
	walk = func(node *wiki.BlockNode, depth int) error {
		if node.Attrs == nil && (node.Type == "workflowReference" || node.Type == "outputCollection") {
			node.Attrs = map[string]any{}
		}
		if node.Type == "image" || node.Type == "fileBlock" || node.Type == "audioBlock" ||
			node.Type == "videoBlock" {
			if src, _ := node.Attrs["src"].(string); src != "" {
				c.Issues = append(
					c.Issues,
					"External media is not retained. Replace it with an uploaded artifact reference or an ordinary link.",
				)
			}
		}
		if node.Type == "artifactReference" {
			if err := freezeAcceptedReferences(ctx, q, p, project, node); err != nil {
				return err
			}
			id, err := uuid.Parse(fmt.Sprint(node.Attrs["versionId"]))
			if err != nil {
				return ErrInvalid
			}
			if err = captureSource(id, depth, node.Attrs["format"] == "live"); err != nil {
				return err
			}
			if source, ok := c.sources[id]; ok {
				node.Attrs["artifactId"] = source.ArtifactID.String()
				if label, _ := node.Attrs["label"].(string); label == "" {
					title, err := q.GetOverviewArtifactTitle(
						ctx,
						generated.GetOverviewArtifactTitleParams{
							CompanyID: p.CompanyID,
							ProjectID: project,
							ID:        source.ArtifactID,
						},
					)
					if err != nil {
						return err
					}
					node.Attrs["label"] = title
				}
			}
			if target, _ := node.Attrs["blockTargetId"].(string); target != "" {
				if body, ok := c.Documents[presentationDocumentKey(id.String(), node.Attrs["format"] == "live")]; ok &&
					wiki.FindBlock(body, target) == nil {
					c.Issues = append(
						c.Issues,
						"An embedded block was removed from its selected source version.",
					)
				}
			}
		}
		for _, id := range sourceVersionIDs(*node) {
			if stale[id] {
				node.Attrs["capturedNeedsRefresh"] = true
			}
			if err := captureSource(id, depth, false); err != nil {
				return err
			}
		}
		if (node.Type == "taskReference" || node.Type == "taskCollection") && node.Attrs["capturedAt"] == nil {
			rows := []OverviewTask{}
			for _, task := range tasks {
				if node.Type == "taskReference" && node.Attrs["taskId"] != task.ID.String() {
					continue
				}
				if !taskMatchesFilter(task.Status, fmt.Sprint(node.Attrs["filter"])) {
					continue
				}
				rows = append(
					rows,
					OverviewTask{
						ID:             task.ID,
						Title:          task.Title,
						Status:         task.Status,
						AssigneeUserID: task.AssigneeUserID,
						AssigneeName:   task.AssigneeName,
						AgentName:      task.AgentName,
						AgentID:        outputSource(task.ExecutedByAgentID),
						Revision:       task.Version,
					},
				)
			}
			if node.Type == "taskReference" && len(rows) == 0 {
				c.Issues = append(c.Issues, "A referenced Task is unavailable in this Project.")
			}
			if node.Attrs == nil {
				node.Attrs = map[string]any{}
			}
			node.Attrs["capturedTasks"] = rows
			node.Attrs["capturedAt"] = time.Now().UTC().Format(time.RFC3339)
		}
		if node.Type == "workflowReference" && node.Attrs["capturedAt"] == nil {
			key, _ := node.Attrs["workflowKey"].(string)
			if s.workflowLookup == nil || key == "" {
				c.Issues = append(c.Issues, "Choose an available Workflow before saving.")
			} else {
				definition, err := s.workflowLookup(key)
				if err != nil {
					c.Issues = append(
						c.Issues,
						"A referenced Workflow is no longer available. Remove it or choose another before saving.",
					)
				} else {
					node.Attrs["capturedWorkflow"] = definition
				}
			}
			rows := []OverviewTask{}
			for _, task := range tasks {
				for _, label := range task.Labels {
					if label == "workflow:"+key {
						rows = append(
							rows,
							OverviewTask{
								ID:             task.ID,
								Title:          task.Title,
								Status:         task.Status,
								AssigneeUserID: task.AssigneeUserID,
								AssigneeName:   task.AssigneeName,
								AgentName:      task.AgentName,
								AgentID:        outputSource(task.ExecutedByAgentID),
								Revision:       task.Version,
							},
						)
						break
					}
				}
			}
			node.Attrs["capturedTasks"] = rows
			node.Attrs["capturedAt"] = time.Now().UTC().Format(time.RFC3339)
		}
		if node.Type == "outputCollection" && node.Attrs["capturedAt"] != nil {
			raw, _ := json.Marshal(node.Attrs["capturedOutputs"])
			var retained []Artifact
			if err := json.Unmarshal(raw, &retained); err != nil {
				return err
			}
			for _, row := range retained {
				if err := captureSource(row.LatestVersionID, depth, false); err != nil {
					return err
				}
			}
		}
		if node.Type == "outputCollection" && node.Attrs["capturedAt"] == nil {
			rows := []Artifact{}
			for _, artifact := range artifacts {
				if artifact.LatestVersion.OutputID == nil &&
					artifact.LatestVersion.SourceOutputID == nil {
					continue
				}
				if node.Attrs["filter"] == "new" &&
					(included[artifact.ID.String()] || included[artifact.LatestVersionID.String()]) {
					continue
				}
				if len(rows) >= 24 {
					break
				}
				if err := captureSource(artifact.LatestVersionID, depth, false); err != nil {
					return err
				}
				rows = append(rows, artifact)
			}
			node.Attrs["capturedOutputs"] = rows
			node.Attrs["capturedAt"] = time.Now().UTC().Format(time.RFC3339)
		}
		for i := range node.Content {
			if err := walk(&node.Content[i], depth); err != nil {
				return err
			}
		}
		return nil
	}
	if err = walk(&c.root, 0); err != nil {
		return c, err
	}
	if byteCount > 256<<20 {
		c.Issues = append(c.Issues, "Included evidence exceeds the 256 MiB presentation limit.")
	}
	c.Content, err = json.Marshal(c.root)
	if err != nil {
		return c, err
	}
	// Capture timestamps are display evidence; they must not make a reviewed
	// fingerprint change merely because the capture button was clicked later.
	raw, _ := json.Marshal(struct {
		Title     string                    `json:"title"`
		Theme     string                    `json:"theme"`
		Content   wiki.BlockNode            `json:"content"`
		Documents map[string]wiki.BlockNode `json:"documents"`
	}{c.Title, c.Theme, c.root, c.Documents})
	var normalized any
	_ = json.Unmarshal(raw, &normalized)
	stripCaptureTime(normalized)
	canonical, _ := json.Marshal(normalized)
	hash := sha256.Sum256(append(canonical, []byte(c.changeFingerprint)...))
	c.Fingerprint = hex.EncodeToString(hash[:])
	c.SourceCount = len(c.sources)
	c.CanExport = len(c.Issues) == 0
	c.markdown = "# " + c.Title + "\n\n" + presentationMarkdown(c.root)
	keys := make([]string, 0, len(c.Documents))
	for id := range c.Documents {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		c.markdown += "\n\n<a id=\"artifact-" + id + "\"></a>\n\n## Retained source\n\n" + presentationMarkdown(
			c.Documents[id],
		)
	}
	return c, nil
}

func stripCaptureTime(value any) {
	switch v := value.(type) {
	case map[string]any:
		delete(v, "capturedAt")
		for _, child := range v {
			stripCaptureTime(child)
		}
	case []any:
		for _, child := range v {
			stripCaptureTime(child)
		}
	}
}

func taskMatchesFilter(status, filter string) bool {
	switch filter {
	case "active":
		return status != "done" && status != "cancelled"
	case "review":
		return status == "in_review"
	case "done":
		return status == "done"
	default:
		return true
	}
}

func sourceVersionIDs(node wiki.BlockNode) []uuid.UUID {
	raw, _ := json.Marshal(node.Attrs["sourceVersionIds"])
	var strings []string
	_ = json.Unmarshal(raw, &strings)
	var ids []uuid.UUID
	for _, value := range strings {
		if id, err := uuid.Parse(value); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *DefaultPresentationService) PreviewPresentation(
	ctx context.Context,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
	sequence int64,
) (Presentation, error) {
	var c presentationCapture
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		_, err := authorize(ctx, q, p, project, true)
		if err != nil {
			return err
		}

		c, err = s.buildPresentation(ctx, q, p, project, pageID, sequence, nil)
		return err
	})
	return c.Presentation, mapWrite(err)
}

func (s *DefaultPresentationService) CapturePresentation(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	in PresentationInput,
) (Presentation, error) {
	return s.capturePresentation(ctx, p, project, in, nil)
}

func (s *DefaultPresentationService) capturePresentation(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	in PresentationInput,
	run *uuid.UUID,
) (Presentation, error) {
	if in.PageID == uuid.Nil || in.RequestKey == uuid.Nil || in.Sequence < 0 || len(in.Fingerprint) != 64 {
		return Presentation{}, ErrInvalid
	}
	var saved generated.ProjectPresentation
	var canvasSequence int64
	var pending *events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		if run != nil {
			scope, _, err := admitPageRun(ctx, q, p, *run, true)
			if err != nil {
				return err
			}
			if scope != project {
				return ErrNotFound
			}
		}
		_, err := authorize(ctx, q, p, project, true)
		if err != nil {
			return err
		}

		if _, err = q.EnsureProjectCanvas(
			ctx,
			generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
		); err != nil {
			return err
		}
		existing, err := q.GetProjectPresentationReceipt(
			ctx,
			generated.GetProjectPresentationReceiptParams{
				CompanyID:       p.CompanyID,
				ProjectID:       project,
				CreatedByUserID: p.UserID,
				RequestKey:      in.RequestKey,
			},
		)
		if err == nil {
			if existing.Fingerprint != in.Fingerprint || existing.SourceSequence != in.Sequence ||
				existing.SourceRunID != nullable(
					run,
				) || existing.PageID != in.PageID || existing.ChangeID != nullable(in.ChangeID) {
				return ErrConflict
			}
			saved = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		overview, err := q.GetProjectPageVersion(
			ctx,
			generated.GetProjectPageVersionParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				ID:        in.PageID,
			},
		)
		if err != nil {
			return lookup(err)
		}
		// Checkpoints take the artifact before its Page. Match that order.
		if _, err = q.LockCanvasArtifact(
			ctx,
			generated.LockCanvasArtifactParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				ID:        overview.ArtifactID,
			},
		); err != nil {
			return lookup(err)
		}
		c, err := s.buildPresentation(ctx, q, p, project, in.PageID, in.Sequence, in.ChangeID)
		if err != nil {
			return err
		}
		if len(c.Issues) > 0 {
			return ErrInvalid
		}
		if c.Fingerprint != in.Fingerprint {
			return ErrConflict
		}

		var versionRoot wiki.BlockNode
		if err = json.Unmarshal(c.Content, &versionRoot); err != nil {
			return err
		}
		snapshots, err := freezePageReferences(
			ctx,
			q,
			pageReferenceSnapshot{
				principal: p,
				project:   project,
				root:      &versionRoot,
				documents: c.Documents,
			},
		)
		if err != nil {
			return err
		}
		for id, version := range snapshots {
			source := c.sources[id]
			source.WikiRevisionID = version.WikiRevisionID
			c.sources[id] = source
		}
		versionContent, err := json.Marshal(versionRoot)
		if err != nil {
			return err
		}

		v := generated.ProjectArtifactVersion{
			ID:             c.version.ID,
			WikiPageID:     c.version.WikiPageID,
			WikiRevisionID: c.version.WikiRevisionID,
		}
		if in.ChangeID == nil {
			revision, err := wiki.CheckpointCanvasPageInTx(
				ctx,
				q,
				wiki.CanvasCheckpointInput{
					Principal: p,
					PageID:    c.version.WikiPageID.UUID,
					Body:      wiki.BlocksMarkdown(versionRoot),
					Sequence:  c.Sequence,
					Content:   versionContent,
				},
			)
			if err != nil {
				return err
			}
			number, err := q.AllocateCanvasVersion(ctx, c.version.ArtifactID)
			if err != nil {
				return err
			}
			v, err = q.CreateCanvasVersion(
				ctx,
				generated.CreateCanvasVersionParams{
					CompanyID:       p.CompanyID,
					ProjectID:       project,
					ArtifactID:      c.version.ArtifactID,
					Number:          number,
					SourceType:      "wiki",
					WikiPageID:      c.version.WikiPageID,
					WikiRevisionID:  nullable(&revision),
					CreatedByUserID: p.UserID,
					BaseVersionID:   nullable(&c.version.ID),
				},
			)
			if err != nil {
				return err
			}
		}
		documents, _ := json.Marshal(c.Documents)
		saved, err = q.InsertProjectPresentation(
			ctx,
			generated.InsertProjectPresentationParams{
				CompanyID:     p.CompanyID,
				ProjectID:     project,
				PageVersionID: v.ID, PageID: in.PageID, Theme: c.Theme,
				Title:           c.Title,
				Content:         c.Content,
				Documents:       documents,
				ExportMarkdown:  c.markdown,
				SourceSequence:  c.Sequence,
				Fingerprint:     c.Fingerprint,
				RequestKey:      in.RequestKey,
				CreatedByUserID: p.UserID,
				SourceRunID:     nullable(run),
			},
		)
		if err != nil {
			return err
		}
		if in.ChangeID != nil {
			if err = q.SetPresentationProposal(
				ctx,
				generated.SetPresentationProposalParams{
					CompanyID:      p.CompanyID,
					ID:             saved.ID,
					ChangeID:       nullable(in.ChangeID),
					ChangeSnapshot: c.changeSnapshot,
				},
			); err != nil {
				return err
			}
		}
		c.sources[v.ID] = generated.CanvasVersionSource{
			ID:             v.ID,
			WikiPageID:     v.WikiPageID,
			WikiRevisionID: v.WikiRevisionID,
		}
		if err = q.SetPresentationSharedContent(
			ctx,
			generated.SetPresentationSharedContentParams{
				CompanyID:     p.CompanyID,
				ID:            saved.ID,
				SharedContent: c.sharedContent,
			},
		); err != nil {
			return err
		}
		for _, source := range c.sources {
			if err = q.PinProjectPresentationSource(
				ctx,
				generated.PinProjectPresentationSourceParams{
					CompanyID:      p.CompanyID,
					ProjectID:      project,
					PresentationID: saved.ID,
					VersionID:      source.ID,
					WikiPageID:     source.WikiPageID,
					WikiRevisionID: source.WikiRevisionID,
					AssetVersionID: source.AssetVersionID,
					OutputID:       source.AccessOutputID,
				},
			); err != nil {
				return err
			}
		}
		canvasSequence, err = q.AdvanceCanvasHead(
			ctx,
			generated.AdvanceCanvasHeadParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		pending, err = changeEvent(ctx, q, p, project, canvasSequence, uuid.Nil)
		return err
	})
	if err != nil {
		return Presentation{}, mapWrite(err)
	}
	if pending != nil {
		s.bus.Publish(ctx, Topic, *pending)
	}
	return s.Presentation(ctx, p, project, saved.ID)
}

func (s *DefaultPresentationService) PreviewPresentationForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run, pageID uuid.UUID,
) (Presentation, error) {
	return s.previewPresentationForRun(ctx, p, run, pageID, nil)
}

func (s *DefaultPresentationService) PreviewProposalForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run, pageID, changeID uuid.UUID,
) (Presentation, error) {
	return s.previewPresentationForRun(ctx, p, run, pageID, &changeID)
}

func (s *DefaultPresentationService) previewPresentationForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run, pageID uuid.UUID,
	changeID *uuid.UUID,
) (Presentation, error) {
	var c presentationCapture
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		project, _, err := admitPageRun(ctx, q, p, run, true)
		if err != nil {
			return err
		}
		c, err = s.buildPresentation(ctx, q, p, project, pageID, -1, changeID)
		if err != nil {
			return err
		}
		// Wiki body reads belong to wiki_read, including its capability switch
		// and evidence trail. A Task preview still reports the exact capture's
		// fingerprint, but cannot become a second Wiki content-reading tool.
		for id, source := range c.sources {
			if !source.WikiPageID.Valid || source.AccessOutputID.Valid {
				continue
			}
			page, err := q.GetCanvasWikiPage(
				ctx,
				generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: source.WikiPageID.UUID},
			)
			if err != nil {
				return lookup(err)
			}
			if !page.ProjectID.Valid {
				delete(c.Documents, presentationDocumentKey(id.String(), false))
				delete(c.Documents, presentationDocumentKey(id.String(), true))
			}
		}
		return nil
	})
	return c.Presentation, mapWrite(err)
}

type PageArtifactPreparation struct {
	PresentationID uuid.UUID `json:"presentationId"`
	AttachmentID   uuid.UUID `json:"attachmentId"`
	Title          string    `json:"title"`
}

// PreparePresentationForRun produces the primary immutable file for the normal
// Artifact submission flow. Upload failure leaves a replayable capture, not a
// partially submitted Artifact.
func (s *DefaultPresentationService) PreparePresentationForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run uuid.UUID,
	in PresentationInput,
) (PageArtifactPreparation, error) {
	var project uuid.UUID
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		project, _, err = admitPageRun(ctx, q, p, run, true)
		return err
	})
	if err != nil {
		return PageArtifactPreparation{}, mapWrite(err)
	}
	if s.attachments == nil {
		return PageArtifactPreparation{}, ErrForbidden
	}
	capture, err := s.capturePresentation(ctx, p, project, in, &run)
	if err != nil {
		return PageArtifactPreparation{}, err
	}
	body, err := s.PresentationExport(ctx, p, project, capture.ID)
	if err != nil {
		return PageArtifactPreparation{}, err
	}
	uploadInput := attachment.InitiateInput{
		CompanyID: p.CompanyID, UploadedByUserID: p.UserID,
		Filename: "project-presentation.md", ContentType: "text/markdown", SizeBytes: int64(len(body)),
	}
	// Bind retained-source access before an upload can become ready, including
	// when the Run ends or access changes while object storage is being written.
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, _, err := admitPageRun(ctx, q, p, run, true); err != nil {
			return err
		}
		row, err := q.GetProjectPresentation(
			ctx,
			generated.GetProjectPresentationParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				ID:        capture.ID,
			},
		)
		if err != nil {
			return lookup(err)
		}
		var id uuid.UUID
		uploadInput, id, err = attachment.ReserveInTx(ctx, q, uploadInput, outputSource(row.ExportAttachmentID))
		if err != nil {
			return err
		}
		rows, err := q.SetProjectPresentationExport(
			ctx,
			generated.SetProjectPresentationExportParams{
				CompanyID:          p.CompanyID,
				ProjectID:          project,
				ID:                 capture.ID,
				ExportAttachmentID: nullable(&id),
			},
		)
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return PageArtifactPreparation{}, mapWrite(err)
	}
	upload, err := s.attachments.Initiate(ctx, uploadInput, strings.NewReader(body))
	if err != nil {
		return PageArtifactPreparation{}, err
	}
	if _, err = s.attachments.Complete(
		ctx,
		attachment.CompleteInput{CompanyID: p.CompanyID, UploadedByUserID: p.UserID, ID: upload.Attachment.ID},
	); err != nil {
		return PageArtifactPreparation{}, err
	}
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, _, err := admitPageRun(ctx, q, p, run, true)
		return err
	})
	return PageArtifactPreparation{
			PresentationID: capture.ID,
			AttachmentID:   upload.Attachment.ID,
			Title:          capture.Title,
		}, mapWrite(
			err,
		)
}

type PresentationOutputInput struct {
	RunID                 uuid.UUID
	PresentationID        uuid.UUID
	OutputID              uuid.UUID
	PrimaryAssetVersionID uuid.UUID
}

// BindPresentationOutputInTx uses the caller's submission transaction and the
// same Run proof/generation scope as authoring. Only the server-produced export
// can be the primary file of a native Project presentation.
func BindPresentationOutputInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	in PresentationOutputInput,
) error {
	project, _, err := admitPageRun(ctx, q, p, in.RunID, true)
	if err != nil {
		return err
	}
	row, err := q.GetProjectPresentation(
		ctx,
		generated.GetProjectPresentationParams{
			CompanyID: p.CompanyID,
			ProjectID: project,
			ID:        in.PresentationID,
		},
	)
	if err != nil {
		return lookup(err)
	}
	if !row.SourceRunID.Valid || row.SourceRunID.UUID != in.RunID || !row.ExportAttachmentID.Valid {
		return ErrForbidden
	}
	asset, err := q.GetReadyAssetVersionForOutputForShare(
		ctx,
		generated.GetReadyAssetVersionForOutputForShareParams{
			CompanyID:      p.CompanyID,
			AssetVersionID: in.PrimaryAssetVersionID,
		},
	)
	if err != nil {
		return lookup(err)
	}
	if asset.AttachmentID != row.ExportAttachmentID.UUID {
		return ErrInvalid
	}
	sources, err := q.ListProjectPresentationSources(
		ctx,
		generated.ListProjectPresentationSourcesParams{
			CompanyID:      p.CompanyID,
			ProjectID:      project,
			PresentationID: row.ID,
		},
	)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if _, err = canVersionSource(ctx, q, p, project, source.VersionID); err != nil {
			return err
		}
	}
	rows, err := q.BindProjectPresentationOutput(
		ctx,
		generated.BindProjectPresentationOutputParams{
			CompanyID:   p.CompanyID,
			ProjectID:   project,
			ID:          row.ID,
			OutputID:    nullable(&in.OutputID),
			SourceRunID: nullable(&in.RunID),
		},
	)
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrConflict
	}
	return q.LinkPresentationDocument(
		ctx,
		generated.LinkPresentationDocumentParams{CompanyID: p.CompanyID, ID: row.ID},
	)
}

// PresentationOutputAccessInTx keeps the aggregate export behind every retained
// source's current access. Ordinary Artifacts have no presentation binding.
func PresentationOutputAccessInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	output uuid.UUID,
) error {
	row, err := q.GetProjectPresentationForOutput(
		ctx,
		generated.GetProjectPresentationForOutputParams{CompanyID: p.CompanyID, OutputID: nullable(&output)},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	sources, err := q.ListProjectPresentationSources(
		ctx,
		generated.ListProjectPresentationSourcesParams{
			CompanyID:      p.CompanyID,
			ProjectID:      row.ProjectID,
			PresentationID: row.ID,
		},
	)
	if err != nil {
		return err
	}
	for _, source := range sources {
		// The Output grant covers this immutable primary snapshot. Other sources retain their own access.
		if source.VersionID == row.PageVersionID {
			continue
		}
		if _, err = canVersionSource(ctx, q, p, row.ProjectID, source.VersionID); err != nil {
			return err
		}
	}
	return nil
}

func presentationEntry(row generated.ProjectPresentation) PresentationEntry {
	return PresentationEntry{
		ChangeID:  outputSource(row.ChangeID),
		ID:        row.ID,
		PageID:    row.PageID,
		Theme:     row.Theme,
		Title:     row.Title,
		Sequence:  row.SourceSequence,
		CreatedAt: row.CreatedAt.Time,
		RunID:     outputSource(row.SourceRunID),
		OutputID:  outputSource(row.OutputID),
	}
}

func (s *DefaultPresentationService) Presentations(
	ctx context.Context,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
) ([]PresentationEntry, error) {
	result := []PresentationEntry{}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		rows, err := q.ListProjectPresentations(
			ctx,
			generated.ListProjectPresentationsParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				PageID:    uuid.NullUUID{UUID: pageID, Valid: pageID != uuid.Nil},
			},
		)
		for _, row := range rows {
			result = append(
				result,
				PresentationEntry{
					ID: row.ID, PageID: row.PageID, Theme: row.Theme,
					Title:     row.Title,
					Sequence:  row.SourceSequence,
					CreatedAt: row.CreatedAt.Time,
					RunID:     outputSource(row.SourceRunID),
					OutputID:  outputSource(row.OutputID),
				},
			)
		}
		return err
	})
	return result, mapWrite(err)
}

func (s *DefaultPresentationService) Presentation(
	ctx context.Context,
	p auth.UserPrincipal,
	project, id uuid.UUID,
) (Presentation, error) {
	var result Presentation
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		row, err := q.GetProjectPresentation(
			ctx,
			generated.GetProjectPresentationParams{CompanyID: p.CompanyID, ProjectID: project, ID: id},
		)
		if err != nil {
			return lookup(err)
		}
		result = Presentation{
			PresentationEntry:         presentationEntry(row),
			Content:                   row.Content,
			Fingerprint:               row.Fingerprint,
			Documents:                 map[string]wiki.BlockNode{},
			Issues:                    []string{},
			CanExport:                 true,
			RequiredSuggestionIndexes: []int{},
		}
		if row.ChangeID.Valid {
			var snapshot struct {
				Operations []json.RawMessage `json:"operations"`
				Decisions  map[int]string    `json:"decisions"`
			}
			if err = json.Unmarshal(row.ChangeSnapshot, &snapshot); err != nil {
				return err
			}
			for index := range snapshot.Operations {
				switch snapshot.Decisions[index] {
				case "applied", "rejected", "already_reflected":
				default:
					result.RequiredSuggestionIndexes = append(
						result.RequiredSuggestionIndexes,
						index,
					)
				}
			}
		}
		if err = json.Unmarshal(row.Documents, &result.Documents); err != nil {
			return err
		}
		sources, err := q.ListProjectPresentationSources(
			ctx,
			generated.ListProjectPresentationSourcesParams{
				CompanyID:      p.CompanyID,
				ProjectID:      project,
				PresentationID: id,
			},
		)
		if err != nil {
			return err
		}
		// The capture pins its own revisions and flattens their dependencies.
		// Authorize those sources, not the original versions' historical dependencies.
		denied := map[uuid.UUID]bool{}
		for _, source := range sources {
			_, err = canVersionSource(ctx, q, p, project, source.VersionID)
			if errors.Is(err, ErrNotFound) {
				denied[source.VersionID] = true
				delete(result.Documents, source.VersionID.String())
				delete(result.Documents, presentationDocumentKey(source.VersionID.String(), true))
				result.CanExport = false
				continue
			}
			if err != nil {
				return err
			}
		}
		result.SourceCount = len(sources) - len(denied)
		var root wiki.BlockNode
		if err = json.Unmarshal(result.Content, &root); err != nil {
			return err
		}
		redactPresentation(&root, denied)
		result.Content, _ = json.Marshal(root)
		for key, body := range result.Documents {
			redactPresentation(&body, denied)
			result.Documents[key] = body
		}
		if !result.CanExport {
			result.Issues = append(
				result.Issues,
				"Some captured evidence is unavailable with your current access. The original presentation is retained; its export is unavailable.",
			)
		}
		return nil
	})
	return result, mapWrite(err)
}

func redactPresentation(node *wiki.BlockNode, denied map[uuid.UUID]bool) {
	ids := sourceVersionIDs(*node)
	if node.Type == "artifactReference" {
		if id, err := uuid.Parse(fmt.Sprint(node.Attrs["versionId"])); err == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if denied[id] {
			*node = wiki.BlockNode{
				Type:  "paragraph",
				Attrs: map[string]any{"blockId": node.Attrs["blockId"]},
				Content: []wiki.BlockNode{
					{Type: "text", Text: "Captured source unavailable with your current access."},
				},
			}
			return
		}
	}
	if node.Type == "outputCollection" {
		raw, _ := json.Marshal(node.Attrs["capturedOutputs"])
		var rows []Artifact
		_ = json.Unmarshal(raw, &rows)
		visible := []Artifact{}
		for _, row := range rows {
			if !denied[row.LatestVersionID] {
				visible = append(visible, row)
			}
		}
		node.Attrs["capturedOutputs"] = visible
	}
	for i := range node.Content {
		redactPresentation(&node.Content[i], denied)
	}
}

func (s *DefaultPresentationService) PresentationExport(
	ctx context.Context,
	p auth.UserPrincipal,
	project, id uuid.UUID,
) (string, error) {
	var body string
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		row, err := q.GetProjectPresentation(
			ctx,
			generated.GetProjectPresentationParams{CompanyID: p.CompanyID, ProjectID: project, ID: id},
		)
		if err != nil {
			return lookup(err)
		}
		sources, err := q.ListProjectPresentationSources(
			ctx,
			generated.ListProjectPresentationSourcesParams{
				CompanyID:      p.CompanyID,
				ProjectID:      project,
				PresentationID: id,
			},
		)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if _, err = canVersionSource(ctx, q, p, project, source.VersionID); err != nil {
				if errors.Is(err, ErrNotFound) {
					return ErrForbidden
				}
				return err
			}
		}
		body = row.ExportMarkdown
		return nil
	})
	return body, mapWrite(err)
}

// AdmitPresentationRunInTx establishes the canonical Task/Conversation/Run lock
// order before the submission owner locks the Run and creates its Artifact.
func AdmitPresentationRunInTx(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, run uuid.UUID) error {
	_, _, err := admitPageRun(ctx, q, p, run, true)
	return err
}

func presentationDocumentKey(version string, live bool) string {
	if live {
		return version + ":live"
	}
	return version
}

func presentationMarkdown(root wiki.BlockNode) string {
	raw, _ := json.Marshal(root)
	var clone wiki.BlockNode
	_ = json.Unmarshal(raw, &clone)
	var walk func(*wiki.BlockNode)
	walk = func(node *wiki.BlockNode) {
		if node.Type == "artifactReference" && node.Attrs["format"] == "live" {
			node.Attrs["versionId"] = presentationDocumentKey(fmt.Sprint(node.Attrs["versionId"]), true)
		}
		for i := range node.Content {
			walk(&node.Content[i])
		}
	}
	walk(&clone)
	return wiki.BlocksMarkdown(clone)
}
