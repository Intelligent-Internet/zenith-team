package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var ErrMessageSkillSelection = errors.New("message skill selection is unavailable or ambiguous")

type messageSkillScope struct {
	CompanyID, ConversationID, UserID, RequestKey uuid.UUID
}

func discussionSkillReferences(references []discussionReference) []SkillReference {
	var result []SkillReference
	for _, ref := range references {
		if ref.Type == "skill" {
			result = append(
				result,
				SkillReference{
					Start:     ref.Start,
					End:       ref.End,
					SkillID:   *ref.SkillID,
					VersionID: *ref.VersionID,
				},
			)
		}
	}
	return result
}

func validateMessageSkillReferences(content string, refs []SkillReference) error {
	if len(refs) > 32 {
		return ErrInvalidInput
	}
	body := DiscussionBody{Version: 1, Markdown: content}
	for _, ref := range refs {
		id, version := ref.SkillID, ref.VersionID
		encoded, err := json.Marshal(
			discussionReference{
				Start:     ref.Start,
				End:       ref.End,
				Type:      "skill",
				SkillID:   &id,
				VersionID: &version,
			},
		)
		if err != nil {
			return err
		}
		body.References = append(body.References, encoded)
	}
	if _, err := decodeDiscussionReferences(body); err != nil {
		return ErrInvalidInput
	}
	return nil
}

// The send receipt pins versions before any invocation is promoted. Every
// Agent tagged by the message, including one queued offline, gets these bytes.
func saveMessageSkills(
	ctx context.Context,
	q *generated.Queries,
	scope messageSkillScope,
	content string,
	refs []SkillReference,
) error {
	if err := validateMessageSkillReferences(content, refs); err != nil {
		return err
	}
	names := map[string]uuid.UUID{}
	text := utf16.Encode([]rune(content))
	for i, ref := range refs {
		row, err := q.GetSelectableMessageSkill(
			ctx,
			generated.GetSelectableMessageSkillParams{
				ConversationID: scope.ConversationID,
				CompanyID:      scope.CompanyID,
				ID:             ref.SkillID,
				VersionID:      ref.VersionID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMessageSkillSelection
		}
		if err != nil {
			return err
		}
		if string(utf16.Decode(text[ref.Start:ref.End])) != "$"+row.Name {
			return ErrInvalidInput
		}
		// Two different packages cannot occupy the same local skill directory.
		if version, ok := names[row.Name]; ok && version != row.ID {
			return ErrMessageSkillSelection
		}
		names[row.Name] = row.ID
		if err := q.InsertMessageSkill(
			ctx,
			generated.InsertMessageSkillParams{
				CompanyID:        scope.CompanyID,
				ConversationID:   scope.ConversationID,
				SelectedByUserID: scope.UserID,
				RequestKey:       scope.RequestKey,
				Position:         int32(i),
				SkillID:          ref.SkillID,
				VersionID:        ref.VersionID,
				StartOffset:      int32(ref.Start),
				EndOffset:        int32(ref.End),
			},
		); err != nil {
			return err
		}
	}
	return nil
}

func matchMessageSkills(
	ctx context.Context,
	q *generated.Queries,
	scope messageSkillScope,
	refs []SkillReference,
) error {
	rows, err := q.ListMessageSkills(
		ctx,
		generated.ListMessageSkillsParams{
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
		if row.SkillID != ref.SkillID || row.VersionID != ref.VersionID || int(row.StartOffset) != ref.Start ||
			int(row.EndOffset) != ref.End {
			return ErrConflict
		}
	}
	return nil
}
