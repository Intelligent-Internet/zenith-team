package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// presentationUIEvidence keeps source authority beside the immutable evaluations.
// Project dependencies survive source deletion: replay must not infer permission
// from a captured title, a current Task row, or an Output grant.
type presentationUIEvidence struct {
	Documents map[string]wiki.UICapture `json:"documents"`
	Projects  []uuid.UUID               `json:"projects"`
}

type presentationUIInput struct {
	Documents map[string]wiki.BlockNode
	Selected  PresentationSelections
}

func capturePresentationUI(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	input presentationUIInput,
) (presentationUIEvidence, error) {
	selected, err := json.Marshal(input.Selected)
	if err != nil || len(selected) > 1<<20 {
		return presentationUIEvidence{}, ErrInvalid
	}
	evidence := presentationUIEvidence{
		Documents: map[string]wiki.UICapture{},
		Projects:  []uuid.UUID{},
	}
	// Share identical observations across scopes and included documents. Resolution
	// remains inside the caller's repeatable-read transaction.
	snapshots := map[string]wiki.UISourceSnapshot{}
	projects := map[uuid.UUID]bool{}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	resolve := func(_, _ string, descriptor map[string]any) (wiki.UISourceSnapshot, error) {
		key, err := json.Marshal(descriptor)
		if err != nil {
			return wiki.UISourceSnapshot{}, ErrInvalid
		}
		if snapshot, exists := snapshots[string(key)]; exists {
			return snapshot, nil
		}
		// The document compiler owns descriptor validation. This adapter only
		// dispatches the closed source type to its authorized domain query.
		if descriptor["type"] != "tasks" {
			return wiki.UISourceSnapshot{}, ErrInvalid
		}
		project, err := uuid.Parse(fmt.Sprint(descriptor["projectId"]))
		if err != nil {
			return wiki.UISourceSnapshot{}, ErrInvalid
		}
		if !projects[project] {
			if _, err = authorize(ctx, q, principal, project, false); err != nil {
				return wiki.UISourceSnapshot{}, err
			}
			projects[project] = true
		}
		limit := 1000
		if number, ok := descriptor["limit"].(float64); ok {
			limit = int(number)
		}
		rows, err := q.ListIntelligentCaptureTasks(ctx, generated.ListIntelligentCaptureTasksParams{
			CompanyID: principal.CompanyID, ProjectID: project,
			Filter: fmt.Sprint(descriptor["filter"]), RowLimit: int32(limit + 1),
		})
		if err != nil {
			return wiki.UISourceSnapshot{}, err
		}
		complete := len(rows) <= limit
		if !complete {
			rows = rows[:limit]
		}
		values := make([]OverviewTask, 0, len(rows))
		versions := make([][2]any, 0, len(rows))
		for _, row := range rows {
			values = append(values, OverviewTask{
				ID: row.ID, Title: row.Title, Status: row.Status,
				AssigneeUserID: row.AssigneeUserID, AssigneeName: row.AssigneeName,
				AgentID: outputSource(row.ExecutedByAgentID), AgentName: row.AgentName,
				Revision: row.Version,
			})
			versions = append(versions, [2]any{row.ID, row.Version})
		}
		version, err := json.Marshal(versions)
		if err != nil {
			return wiki.UISourceSnapshot{}, err
		}
		snapshot := wiki.UISourceSnapshot{
			Value: values, Version: string(version), ObservedAt: observedAt,
			Coverage: &wiki.UISourceCoverage{RowCount: len(values), Limit: limit, Complete: complete},
		}
		snapshots[string(key)] = snapshot
		return snapshot, nil
	}
	keys := make([]string, 0, len(input.Documents))
	for key := range input.Documents {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	evidenceBytes := 0
	for _, key := range keys {
		document := input.Documents[key]
		if wiki.UIDocumentSchema(document) < 2 {
			if len(input.Selected[key]) > 0 {
				return evidence, ErrInvalid
			}
			continue
		}
		capture, err := wiki.CaptureUI(
			document,
			wiki.UICaptureInput{Selected: input.Selected[key], Resolve: resolve},
		)
		if err != nil {
			return evidence, fmt.Errorf("%w: reactive capture: %w", ErrInvalid, err)
		}
		raw, err := json.Marshal(capture)
		if err != nil {
			return evidence, err
		}
		evidenceBytes += len(raw)
		if evidenceBytes > 64<<20 {
			return evidence, ErrInvalid
		}
		evidence.Documents[key] = capture
	}
	for key := range input.Selected {
		if _, exists := input.Documents[key]; !exists {
			return evidence, ErrInvalid
		}
	}
	for project := range projects {
		evidence.Projects = append(evidence.Projects, project)
	}
	slices.SortFunc(evidence.Projects, func(a, b uuid.UUID) int {
		return bytes.Compare(a[:], b[:])
	})
	return evidence, nil
}

// authorizePresentationUI checks frozen dependencies without fetching newer data.
// Failure hides the derived evidence as a whole; individual computed values may
// depend transitively on several sources and cannot safely be partially redacted.
func authorizePresentationUI(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	evidence presentationUIEvidence,
) error {
	for _, project := range evidence.Projects {
		if _, err := authorize(ctx, q, principal, project, false); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrForbidden
			}
			return err
		}
	}
	return nil
}

func authorizeStoredPresentationUI(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	raw []byte,
) error {
	var evidence presentationUIEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return err
	}
	return authorizePresentationUI(ctx, q, principal, evidence)
}

// Observation times and the hash derived from a native capture's display
// timestamps cannot invalidate an otherwise identical reviewed preview.
func presentationUIFingerprint(evidence presentationUIEvidence) presentationUIEvidence {
	result := presentationUIEvidence{
		Documents: make(map[string]wiki.UICapture, len(evidence.Documents)),
		Projects:  evidence.Projects,
	}
	for key, capture := range evidence.Documents {
		capture.DefinitionHash = ""
		capture.Sources = slices.Clone(capture.Sources)
		for index := range capture.Sources {
			capture.Sources[index].Snapshot.ObservedAt = ""
		}
		result.Documents[key] = capture
	}
	return result
}

func mergePresentationUIProjects(first, second []uuid.UUID) []uuid.UUID {
	result := append(slices.Clone(first), second...)
	slices.SortFunc(result, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(result)
}
