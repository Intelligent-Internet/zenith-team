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

// Message selections override an installed skill with the same name only for
// this Run. They never mutate the Agent configuration.
func messageRunSkills(ctx context.Context, q *generated.Queries, in RunTrigger, installed []contentbundle.SkillRef) ([]contentbundle.SkillRef, []string, error) {
	var rows []generated.SkillVersion
	var err error
	if in.InvocationID.Valid {
		rows, err = q.ListInvocationSkillVersions(ctx, generated.ListInvocationSkillVersionsParams{CompanyID: in.CompanyID, ID: in.InvocationID.UUID})
	} else if in.SkillRequestKey != uuid.Nil {
		rows, err = q.ListMessageSkillVersions(ctx, generated.ListMessageSkillVersionsParams{CompanyID: in.CompanyID, ConversationID: in.ConversationID, RequestKey: in.SkillRequestKey, SelectedByUserID: in.ActorUserID})
	}
	if err != nil {
		return nil, nil, err
	}
	var keys []string
	for _, row := range rows {
		ref := contentbundle.SkillRef{ID: row.ID.String(), Source: "company", Name: row.Name, Description: row.Description, Hash: row.ContentHash, SizeBytes: row.SizeBytes, FileCount: int(row.FileCount)}
		replaced := false
		for i, current := range installed {
			if current.Name == ref.Name {
				installed[i] = ref
				replaced = true
				break
			}
		}
		if !replaced {
			installed = append(installed, ref)
		}
		keys = append(keys, ref.Name)
	}
	return installed, keys, nil
}
