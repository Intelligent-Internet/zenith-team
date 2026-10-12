package execution

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/contentbundle"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func runAgentSkillRefs(
	ctx context.Context,
	q *generated.Queries,
	companyID, agentID uuid.UUID,
) ([]contentbundle.SkillRef, error) {
	rows, err := q.ListRunAgentSkills(
		ctx,
		generated.ListRunAgentSkillsParams{CompanyID: companyID, AgentID: agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("freeze Agent skills: %w", err)
	}
	var refs []contentbundle.SkillRef
	for _, row := range rows {
		refs = append(
			refs,
			contentbundle.SkillRef{
				ID:          row.ID.String(),
				Source:      "company",
				Name:        row.Name,
				Description: row.Description,
				Hash:        row.ContentHash,
				SizeBytes:   row.SizeBytes,
				FileCount:   int(row.FileCount),
			},
		)
	}
	return refs, nil
}

func freezeRunSkills(ctx context.Context, q *generated.Queries, run generated.Run) error {
	var doc struct {
		Agent struct {
			SkillRefs []contentbundle.SkillRef `json:"skillRefs"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(run.Context, &doc); err != nil {
		return err
	}
	for _, ref := range doc.Agent.SkillRefs {
		id, err := uuid.Parse(ref.ID)
		if err != nil {
			return err
		}
		if err := q.FreezeRunSkill(
			ctx,
			generated.FreezeRunSkillParams{CompanyID: run.CompanyID, RunID: run.ID, VersionID: id},
		); err != nil {
			return err
		}
	}
	return nil
}
