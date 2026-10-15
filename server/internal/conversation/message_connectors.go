package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf16"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent/connectorselection"
	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

var ErrMessageConnectorSelection = errors.New("selected plugin is unavailable for one or more tagged agents")

type ConnectorReference struct {
	Start       int       `json:"start"`
	End         int       `json:"end"`
	ConnectorID uuid.UUID `json:"connectorId"`
}

func discussionConnectorReferences(references []discussionReference) []ConnectorReference {
	var result []ConnectorReference
	for _, ref := range references {
		if ref.Type == "connector" {
			result = append(
				result,
				ConnectorReference{Start: ref.Start, End: ref.End, ConnectorID: *ref.ConnectorID},
			)
		}
	}
	return result
}

func validateMessageConnectorReferences(content string, refs []ConnectorReference, skills []SkillReference) error {
	if len(refs) > 32 || len(skills) > 32 {
		return ErrInvalidInput
	}
	var references []discussionReference
	for _, ref := range refs {
		id := ref.ConnectorID
		references = append(
			references,
			discussionReference{Start: ref.Start, End: ref.End, Type: "connector", ConnectorID: &id},
		)
	}
	for _, ref := range skills {
		id, version := ref.SkillID, ref.VersionID
		references = append(
			references,
			discussionReference{
				Start:     ref.Start,
				End:       ref.End,
				Type:      "skill",
				SkillID:   &id,
				VersionID: &version,
			},
		)
	}
	slices.SortFunc(references, func(a, b discussionReference) int { return a.Start - b.Start })
	body := DiscussionBody{Version: 1, Markdown: content}
	for _, ref := range references {
		raw, err := json.Marshal(ref)
		if err != nil {
			return err
		}
		body.References = append(body.References, raw)
	}
	if _, err := decodeDiscussionReferences(body); err != nil {
		return ErrInvalidInput
	}
	return nil
}

type messageConnectorInput struct {
	Scope      messageSkillScope
	Content    string
	References []ConnectorReference
	AgentIDs   []uuid.UUID
}

func saveMessageConnectors(ctx context.Context, q *generated.Queries, input messageConnectorInput) error {
	if len(input.References) == 0 {
		return nil
	}
	if err := validateMessageConnectorReferences(input.Content, input.References, nil); err != nil {
		return err
	}
	scope := input.Scope
	principal := auth.UserPrincipal{CompanyID: scope.CompanyID, UserID: scope.UserID}
	catalog, err := connectorselection.ReadMessageConnectors(
		ctx,
		q,
		principal,
		connectorselection.MessageConnectorScope{ConversationID: scope.ConversationID},
	)
	if err != nil {
		return err
	}
	available := map[uuid.UUID]connectorselection.MessageConnector{}
	for _, item := range catalog {
		available[item.ID] = item
	}
	// Even a message without @Agent may carry a tag, but it grants no Run.
	targets := map[uuid.UUID]connectorselection.AgentMessageConnectors{}
	for _, id := range input.AgentIDs {
		if _, exists := targets[id]; exists {
			continue
		}
		connectors, err := connectorselection.ReadAgentMessageConnectors(ctx, q, principal, id)
		if err != nil {
			return err
		}
		targets[id] = connectors
	}
	text := utf16.Encode([]rune(input.Content))
	for i, ref := range input.References {
		candidate, ok := available[ref.ConnectorID]
		if !ok {
			return ErrMessageConnectorSelection
		}
		if string(utf16.Decode(text[ref.Start:ref.End])) != "$"+candidate.Name {
			return ErrInvalidInput
		}
		var selected generated.ListExecutableMessageConnectorsRow
		// The catalog ID names a reviewed provider contract, never an account.
		// Every invoked Agent must have it on this sender's own placement.
		if len(targets) == 0 {
			connectors, err := connectorselection.ReadAgentMessageConnectors(
				ctx,
				q,
				principal,
				candidate.EligibleAgentIDs[0],
			)
			if err != nil {
				return err
			}
			for _, item := range connectors.Items {
				if item.ID == ref.ConnectorID {
					selected = item
				}
			}
		}
		for id, connectors := range targets {
			if !slices.Contains(candidate.EligibleAgentIDs, id) {
				return ErrMessageConnectorSelection
			}
			found := false
			for _, item := range connectors.Items {
				if item.ID == ref.ConnectorID {
					if selected.ID != uuid.Nil &&
						(selected.PolicyRevision != item.PolicyRevision || selected.ContractDigest != item.ContractDigest) {
						return ErrMessageConnectorSelection
					}
					selected = item
					found = true
				}
			}
			if !found {
				return ErrMessageConnectorSelection
			}
		}
		if selected.ID == uuid.Nil {
			return ErrMessageConnectorSelection
		}
		err := q.InsertMessageConnector(ctx, generated.InsertMessageConnectorParams{
			CompanyID:        scope.CompanyID,
			ConversationID:   scope.ConversationID,
			RequestKey:       scope.RequestKey,
			SelectedByUserID: scope.UserID,
			Position: int32(
				i,
			),
			ConnectorID:    ref.ConnectorID,
			Name:           candidate.Name,
			PolicyRevision: selected.PolicyRevision,
			Source:         selected.Source,
			NativeID:       selected.NativeID,
			ContractDigest: selected.ContractDigest,
			StartOffset:    int32(ref.Start),
			EndOffset:      int32(ref.End),
		})
		if err != nil {
			return err
		}
	}
	for id, target := range targets {
		if err := q.InsertMessageConnectorBinding(ctx, generated.InsertMessageConnectorBindingParams{
			CompanyID:        scope.CompanyID,
			ConversationID:   scope.ConversationID,
			RequestKey:       scope.RequestKey,
			SelectedByUserID: scope.UserID,
			AgentID:          id,
			RuntimeID:        target.RuntimeID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func matchMessageConnectors(
	ctx context.Context,
	q *generated.Queries,
	scope messageSkillScope,
	refs []ConnectorReference,
) error {
	rows, err := q.ListMessageConnectors(
		ctx,
		generated.ListMessageConnectorsParams{
			CompanyID:        scope.CompanyID,
			ConversationID:   scope.ConversationID,
			RequestKey:       scope.RequestKey,
			SelectedByUserID: scope.UserID,
		},
	)
	if err != nil {
		return err
	}
	if len(rows) != len(refs) {
		return ErrConflict
	}
	for i, row := range rows {
		ref := refs[i]
		if row.ConnectorID != ref.ConnectorID || int(row.StartOffset) != ref.Start ||
			int(row.EndOffset) != ref.End {
			return ErrConflict
		}
	}
	return nil
}

func connectorTargetAgents(id *uuid.UUID) []uuid.UUID {
	if id == nil {
		return nil
	}
	return []uuid.UUID{*id}
}
