package canvas

import (
	"context"
	"encoding/json"

	generated "boardly.dev/boardly/server/pkg/db/generated"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
)

func readOverviewForRun(s *canvasServices, ctx context.Context, p auth.UserPrincipal, run uuid.UUID) (Page, error) {
	project, err := s.ReadProjectForRun(ctx, p, run)
	if err != nil {
		return Page{}, err
	}
	if project.OverviewPageID == nil {
		return Page{}, ErrNotFound
	}
	return s.ReadPageForRun(ctx, p, run, *project.OverviewPageID)
}

func editOverviewForRun(
	s *canvasServices,
	ctx context.Context,
	p auth.UserPrincipal,
	run uuid.UUID,
	in PageEditInput,
) (PageEditResult, error) {
	project, err := s.ReadProjectForRun(ctx, p, run)
	if err != nil {
		return PageEditResult{}, err
	}
	if project.OverviewPageID == nil {
		return PageEditResult{}, ErrNotFound
	}
	return s.EditPageForRun(ctx, p, run, *project.OverviewPageID, in)
}

func previewOverview(
	s *canvasServices,
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
) (Presentation, error) {
	overview, err := s.Overview(ctx, p, project)
	if err != nil {
		return Presentation{}, err
	}
	return s.PreviewPresentation(ctx, p, project, *overview.PageID, -1)
}

func createOverviewPage(
	s *canvasServices,
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	in OverviewInput,
) (Page, error) {
	overview, err := s.UpdateOverview(ctx, p, project, in)
	if err != nil {
		return Page{}, err
	}
	if overview.PageID == nil {
		return Page{}, ErrNotFound
	}
	return s.Page(ctx, p, project, *overview.PageID)
}

// Multi-surface scenarios exercise the independently constructed owners.
type canvasServices struct {
	*DefaultCanvasService
	*DefaultPageService
	*DefaultPresentationService
}

func newCanvasServices(
	store *db.Store,
	bus *events.Bus,
	attachments attachment.AttachmentService,
	catalog ...func(string) (json.RawMessage, error),
) *canvasServices {
	return &canvasServices{
		NewCanvasService(store, bus, attachments),
		NewPageService(store, bus),
		NewPresentationService(store, bus, attachments, catalog...),
	}
}

func (s *canvasServices) ReviewPageChange(
	ctx context.Context,
	p auth.UserPrincipal,
	project, page, id uuid.UUID,
	action string,
) (PageEditResult, error) {
	preview, err := s.PageChangeDetail(ctx, p, project, page, id)
	if err != nil {
		return PageEditResult{}, err
	}
	return s.DefaultPageService.ReviewPageChange(
		ctx,
		p,
		project,
		page,
		id,
		PageChangeReviewInput{RequestKey: uuid.New(), Fingerprint: preview.Fingerprint, Action: action},
	)
}

// Existing capture scenarios install their setup contributions through explicit
// human review; Agent edits themselves are always proposals.
func editAndAcceptPageForRun(
	s *canvasServices,
	ctx context.Context,
	p auth.UserPrincipal,
	run, page uuid.UUID,
	in PageEditInput,
) (PageEditResult, error) {
	change, err := s.EditPageForRun(ctx, p, run, page, in)
	if err != nil {
		return change, err
	}
	project, err := s.ReadProjectForRun(ctx, p, run)
	if err != nil {
		return change, err
	}
	detail, err := s.PageChangeDetail(ctx, p, project.ProjectID, page, change.ID)
	if err != nil {
		return change, err
	}
	if detail.Status == "applied" {
		change.Status = "applied"
		return change, nil
	}
	return s.ReviewPageChange(ctx, p, project.ProjectID, page, change.ID, "apply")
}

func editAndAcceptOverviewForRun(
	s *canvasServices,
	ctx context.Context,
	p auth.UserPrincipal,
	run uuid.UUID,
	in PageEditInput,
) (PageEditResult, error) {
	project, err := s.ReadProjectForRun(ctx, p, run)
	if err != nil {
		return PageEditResult{}, err
	}
	return editAndAcceptPageForRun(s, ctx, p, run, *project.OverviewPageID, in)
}

func createAndAcceptPageForRun(
	s *canvasServices,
	ctx context.Context,
	p auth.UserPrincipal,
	run uuid.UUID,
	in PageCreateInput,
) (PageCreateResult, error) {
	page, err := s.CreatePageForRun(ctx, p, run, in)
	if err != nil || page.ChangeID == nil {
		return page, err
	}
	project, err := s.ReadProjectForRun(ctx, p, run)
	if err != nil {
		return page, err
	}
	detail, err := s.PageChangeDetail(ctx, p, project.ProjectID, page.PageID, *page.ChangeID)
	if err != nil {
		return page, err
	}
	if detail.Status != "applied" {
		_, err = s.ReviewPageChange(ctx, p, project.ProjectID, page.PageID, *page.ChangeID, "apply")
		if err != nil {
			return page, err
		}
		preview, e := s.PreviewPresentation(ctx, p, project.ProjectID, page.PageID, -1)
		if e != nil {
			return page, e
		}
		if _, e = s.CapturePresentation(
			ctx,
			p,
			project.ProjectID,
			PresentationInput{
				PageID:      page.PageID,
				RequestKey:  uuid.New(),
				Sequence:    preview.Sequence,
				Fingerprint: preview.Fingerprint,
			},
		); e != nil {
			return page, e
		}
	}
	current, err := s.ReadPageForRun(ctx, p, run, page.PageID)
	version, versionErr := s.DefaultPageService.store.Queries().
		GetProjectPageVersion(ctx, generated.GetProjectPageVersionParams{CompanyID: p.CompanyID, ProjectID: project.ProjectID, ID: page.PageID})
	if versionErr != nil {
		return page, versionErr
	}
	page.VersionID = version.ID
	page.Sequence = current.Sequence
	page.ChangeID = nil
	return page, err
}
