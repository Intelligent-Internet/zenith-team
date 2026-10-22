package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type PageSourcePreviewInput struct {
	BlockID          string  `json:"blockId"                    binding:"required,uuid"`
	Source           *string `json:"source,omitempty"           binding:"omitempty,max=262144"`
	LanguageVersion  int     `json:"languageVersion,omitempty"  binding:"omitempty,oneof=1"`
	ExpectedSequence *int64  `json:"expectedSequence,omitempty" binding:"omitempty,min=0"`
	ExpectedHash     string  `json:"expectedHash,omitempty"     binding:"omitempty,len=64"`
}

type PageSourcePreview struct {
	BlockID         string                  `json:"blockId"`
	Source          string                  `json:"source"`
	LanguageVersion int                     `json:"languageVersion"`
	SchemaVersion   int                     `json:"schemaVersion"`
	Sequence        int64                   `json:"sequence"`
	ExpectedHash    string                  `json:"expectedHash"`
	Content         *wiki.BlockNode         `json:"content,omitempty"`
	Diagnostics     []wiki.SourceDiagnostic `json:"diagnostics"`
}

// PreviewPageSource compiles an isolated update and reserves identities for new
// descendants. The accepted CRDT remains untouched; the member applies the
// reviewed subtree through normal sync.
func (s *DefaultPageService) PreviewPageSource(
	ctx context.Context,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
	input PageSourcePreviewInput,
) (result PageSourcePreview, returnedErr error) {
	result = PageSourcePreview{Diagnostics: []wiki.SourceDiagnostic{}}
	if input.Source != nil {
		observation := pageSourceObservation{operation: "check", bytes: len(*input.Source), started: time.Now()}
		defer func() {
			observation.rejected = len(result.Diagnostics) > 0
			s.observeSource(observation, returnedErr)
		}()
	}
	if _, err := uuid.Parse(input.BlockID); err != nil {
		return result, ErrInvalid
	}
	if input.Source != nil && (len(*input.Source) > 262144 || input.LanguageVersion != 1 ||
		input.ExpectedSequence == nil || *input.ExpectedSequence < 0 || len(input.ExpectedHash) != 64) {
		return result, ErrInvalid
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, true); err != nil {
			return err
		}
		version, err := q.GetProjectPageVersion(ctx, generated.GetProjectPageVersionParams{
			CompanyID: p.CompanyID, ProjectID: project, ID: pageID,
		})
		if err != nil {
			return lookup(err)
		}
		target := documentTarget{ProjectID: project, VersionID: version.ID}
		page, _, err := target.resolve(ctx, q, p, true)
		if err != nil {
			return err
		}
		lockedPage, err := q.LockCanvasWikiPage(
			ctx,
			generated.LockCanvasWikiPageParams{CompanyID: p.CompanyID, ID: page.ID},
		)
		if err != nil {
			return lookup(err)
		}
		if lockedPage.Status != "active" {
			return ErrNotFound
		}
		draft, err := q.LockWikiDraft(
			ctx,
			generated.LockWikiDraftParams{CompanyID: p.CompanyID, PageID: page.ID},
		)
		if err != nil {
			return lookup(err)
		}
		var root wiki.BlockNode
		if err := json.Unmarshal(draft.Content, &root); err != nil {
			return err
		}
		block := wiki.FindBlock(root, input.BlockID)
		if block == nil {
			return ErrNotFound
		}
		// Source editing needs the exact saved subtree. Refuse unavailable
		// evidence instead of returning a redacted subtree with a different hash.
		originalHash := wiki.BlockHash(*block)
		if err := redactUnavailableReferences(ctx, q, p, project, &root); err != nil {
			return err
		}
		block = wiki.FindBlock(root, input.BlockID)
		if block == nil || wiki.BlockHash(*block) != originalHash {
			return ErrNotFound
		}
		result.BlockID, result.Sequence = input.BlockID, draft.HeadSeq
		result.LanguageVersion, result.SchemaVersion = 1, int(draft.SchemaVersion)
		result.Source, result.ExpectedHash = wiki.BlocksSource(*block), wiki.BlockHash(*block)
		result.Content = block
		if input.Source == nil {
			return nil
		}
		if *input.ExpectedSequence != draft.HeadSeq || input.ExpectedHash != result.ExpectedHash {
			return ErrConflict
		}
		result.Source, result.Content = *input.Source, nil
		replacement, err := wiki.CompileSourceReplacement(*input.Source, input.LanguageVersion, *block)
		if err != nil {
			return sourcePreviewDiagnostic(&result, err)
		}
		count := 0
		wiki.AssignMissingBlockIDs(replacement, func() string { count++; return "" })
		if count > 0 {
			ids, err := q.ReserveWikiBlockIDs(ctx, generated.ReserveWikiBlockIDsParams{
				CompanyID: p.CompanyID, PageID: page.ID, Count: int32(count),
			})
			if err != nil {
				return err
			}
			index := 0
			wiki.AssignMissingBlockIDs(
				replacement,
				func() string { id := ids[index]; index++; return id.String() },
			)
		}
		operation := wiki.BlockOperation{
			Type: "update", BlockID: input.BlockID, ExpectedHash: input.ExpectedHash, Content: replacement,
		}
		_, changed, err := wiki.ApplyBlockOperations(draft.State, []wiki.BlockOperation{operation})
		if err != nil {
			return sourcePreviewDiagnostic(
				&result,
				wiki.LocateSourceReplacementError(*input.Source, changed, input.BlockID, err),
			)
		}
		if err := s.authoring.Check(int(draft.SchemaVersion), changed); err != nil {
			return err
		}
		if err := validateDocumentReferences(ctx, q, p, page.ProjectID, changed); err != nil {
			return err
		}
		result.Content = operation.Content
		result.SchemaVersion = max(result.SchemaVersion, wiki.UIDocumentSchema(changed))
		return nil
	})
	return result, mapWrite(err)
}

func sourcePreviewDiagnostic(result *PageSourcePreview, err error) error {
	var diagnostic *wiki.SourceDiagnostic
	if errors.As(err, &diagnostic) {
		clone := *diagnostic
		clone.BlockID = result.BlockID
		result.Diagnostics = append(result.Diagnostics, clone)
		return nil
	}
	if errors.Is(err, wiki.ErrPageInvalidInput) {
		code := "source.validation"
		var expression *wiki.UIEvaluationError
		if errors.As(err, &expression) {
			code = expression.Code
		}
		result.Diagnostics = append(result.Diagnostics, wiki.SourceDiagnostic{
			Code:     code,
			Severity: "error",
			BlockID:  result.BlockID,
			End:      len(result.Source),
			Repair:   "Keep the selected root identity, omit IDs on new children, and use valid catalog properties and declared scope bindings.",
		})
		return nil
	}
	return err
}

// Source observations contain only fixed outcomes, elapsed time and byte counts.
// Successful source retries count as accepted requests, not new document writes.
type pageSourceObservation struct {
	operation string
	bytes     int
	started   time.Time
	rejected  bool
}

func (s *DefaultPageService) observeSource(observation pageSourceObservation, err error) {
	if s.sourceObserver == nil {
		return
	}
	outcome := "accepted"
	switch {
	case errors.Is(err, wiki.ErrUIAuthoringDisabled):
		outcome = "disabled"
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound):
		outcome = "denied"
	case errors.Is(err, ErrConflict), errors.Is(err, wiki.ErrPageConflict):
		outcome = "conflict"
	case observation.rejected,
		errors.Is(err, ErrInvalid),
		errors.Is(err, wiki.ErrPageInvalidInput),
		errors.Is(err, wiki.ErrPageBodyTooLarge):
		outcome = "rejected"
	case err != nil:
		outcome = "error"
	}
	s.sourceObserver(observation.operation, outcome, time.Since(observation.started), observation.bytes)
}
