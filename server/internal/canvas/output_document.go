package canvas

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/wiki"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func outputSource(id uuid.NullUUID) *uuid.UUID {
	if id.Valid {
		return &id.UUID
	}
	return nil
}

// Read immutable storage outside the mutation transaction. The write rechecks
// membership and the source Output under its lifecycle lock before importing.
func (s *DefaultCanvasService) outputDocumentBody(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	c Command,
) (string, error) {
	var source generated.GetCanvasOutputDocumentSourceRow
	var imported bool
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, err := authorize(ctx, q, p, project, true)
		if err != nil {
			return err
		}
		if err = canVersion(ctx, q, p, project, *c.VersionID); err != nil {
			return err
		}
		version, err := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{CompanyID: p.CompanyID, ProjectID: project, ID: *c.VersionID},
		)
		if err != nil {
			return err
		}
		if version.ArtifactID != *c.ArtifactID || version.SourceType != "output" {
			return ErrInvalid
		}
		source, err = q.GetCanvasOutputDocumentSource(ctx, generated.GetCanvasOutputDocumentSourceParams{
			CompanyID:    p.CompanyID,
			ProjectID:    project,
			VersionID:    *c.VersionID,
			OutputItemID: *c.OutputItemID,
		})
		if err != nil {
			return lookup(err)
		}
		_, err = q.GetCanvasImportedDocument(ctx, generated.GetCanvasImportedDocumentParams{
			CompanyID: p.CompanyID, ArtifactID: *c.ArtifactID, OutputItemID: *c.OutputItemID,
		})
		imported = err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil || imported {
		return "", err
	}
	return s.readOutputMarkdown(
		ctx,
		p,
		outputDocumentFile{source.AttachmentID, source.Filename, source.ContentType, source.SizeBytes},
	)
}

type outputDocumentFile struct {
	AttachmentID          uuid.UUID
	Filename, ContentType string
	SizeBytes             int64
}

func (s *DefaultCanvasService) readOutputMarkdown(
	ctx context.Context,
	p auth.UserPrincipal,
	source outputDocumentFile,
) (string, error) {
	ext := strings.ToLower(path.Ext(source.Filename))
	if ext != ".md" && ext != ".markdown" && source.ContentType != "text/markdown" &&
		source.ContentType != "text/x-markdown" {
		return "", ErrInvalid
	}
	if source.SizeBytes > 262144 {
		return "", ErrLimit
	}
	if s.attachments == nil {
		return "", ErrInvalid
	}
	file, err := s.attachments.DownloadResolved(ctx, attachment.ResolvedDownloadInput{
		CompanyID:            p.CompanyID,
		ID:                   source.AttachmentID,
		RequestedByUserID:    p.UserID,
		OutputAccessResolved: true,
	})
	if err != nil {
		return "", err
	}
	defer file.Body.Close()
	body, err := io.ReadAll(io.LimitReader(file.Body, 262145))
	if err != nil {
		return "", err
	}
	if len(body) > 262144 {
		return "", ErrLimit
	}
	if !utf8.Valid(body) || strings.ContainsRune(string(body), 0) {
		return "", ErrInvalid
	}
	return strings.TrimPrefix(string(body), "\ufeff"), nil
}

func importOutputDocumentInTx(
	ctx context.Context,
	q *generated.Queries,
	m mutation,
	artifact generated.ProjectArtifact,
) (uuid.UUID, uuid.UUID, error) {
	c, p := m.Input, m.Principal
	source, err := q.GetCanvasOutputDocumentSource(ctx, generated.GetCanvasOutputDocumentSourceParams{
		CompanyID: p.CompanyID, ProjectID: m.ProjectID, VersionID: *c.VersionID, OutputItemID: *c.OutputItemID,
	})
	if err != nil {
		return uuid.Nil, uuid.Nil, lookup(err)
	}
	if _, err = authorizeOutputDocument(ctx, q, p, source.OutputID, true); err != nil {
		return uuid.Nil, uuid.Nil, lookup(err)
	}
	existing, e := q.GetOutputDocument(
		ctx,
		generated.GetOutputDocumentParams{
			CompanyID:    p.CompanyID,
			OutputID:     source.OutputID,
			OutputItemID: *c.OutputItemID,
		},
	)
	if e == nil {
		return existing.PageID, existing.CurrentRevisionID, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, e
	}
	title := []rune(source.Filename)
	if len(title) > 240 {
		title = title[:240]
	}
	page, revision, err := wiki.CreateCanvasPageInTx(ctx, q, wiki.CanvasPageInput{
		Principal: p, Title: string(title), Body: c.Body, SourceOutputID: &source.OutputID,
	})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	err = q.CreateOutputDocument(ctx, generated.CreateOutputDocumentParams{
		CompanyID: p.CompanyID, OutputID: source.OutputID, OutputItemID: *c.OutputItemID, PageID: page,
	})
	return page, revision, err
}

// OpenOutputDocument imports once, after rechecking authority under the same
// Output lock as sharing changes. Concurrent opens return the same saved page.
func (s *DefaultCanvasService) OpenOutputDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	ref OutputDocumentRef,
) (uuid.UUID, error) {
	output, item := ref.OutputID, ref.ItemID
	var source generated.GetOutputDocumentSourceRow
	var page uuid.UUID
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorizeOutputDocument(ctx, q, p, output, true); err != nil {
			return err
		}
		existing, err := q.GetOutputDocument(
			ctx,
			generated.GetOutputDocumentParams{CompanyID: p.CompanyID, OutputID: output, OutputItemID: item},
		)
		if err == nil {
			page = existing.PageID
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		source, err = q.GetOutputDocumentSource(
			ctx,
			generated.GetOutputDocumentSourceParams{CompanyID: p.CompanyID, OutputID: output, ID: item},
		)
		return lookup(err)
	})
	if err != nil || page != uuid.Nil {
		return page, err
	}
	body, err := s.readOutputMarkdown(
		ctx,
		p,
		outputDocumentFile{source.AttachmentID, source.Filename, source.ContentType, source.SizeBytes},
	)
	if err != nil {
		return uuid.Nil, err
	}
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorizeOutputDocument(ctx, q, p, output, true); err != nil {
			return err
		}
		existing, e := q.GetOutputDocument(
			ctx,
			generated.GetOutputDocumentParams{CompanyID: p.CompanyID, OutputID: output, OutputItemID: item},
		)
		if e == nil {
			page = existing.PageID
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		title := []rune(source.Filename)
		if len(title) > 240 {
			title = title[:240]
		}
		page, _, e = wiki.CreateCanvasPageInTx(
			ctx,
			q,
			wiki.CanvasPageInput{Principal: p, Title: string(title), Body: body, SourceOutputID: &output},
		)
		if e != nil {
			return e
		}
		return q.CreateOutputDocument(
			ctx,
			generated.CreateOutputDocumentParams{
				CompanyID:    p.CompanyID,
				OutputID:     output,
				OutputItemID: item,
				PageID:       page,
			},
		)
	})
	return page, mapWrite(err)
}

func authorizeOutputDocument(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	output uuid.UUID,
	write bool,
) (bool, error) {
	access, err := auth.OutputDocumentAccessInTx(ctx, q, p, output)
	if err != nil {
		return false, err
	}
	if !access.Read {
		return false, ErrNotFound
	}
	if write && !access.Edit {
		return false, ErrForbidden
	}
	return access.Edit, nil
}
