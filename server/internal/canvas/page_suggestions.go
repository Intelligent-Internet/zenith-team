package canvas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
)

func suggestionApplicability(base, current wiki.BlockNode, op wiki.BlockOperation) string {
	for _, id := range []string{op.BlockID, op.ParentID, op.AfterID, op.BeforeID} {
		if id == "" {
			continue
		}
		beforePlacement, beforeExists := locatePageBlock(base, id)
		afterPlacement, afterExists := locatePageBlock(current, id)
		if !beforeExists || !afterExists || beforePlacement != afterPlacement {
			return "needs_update"
		}
	}
	if op.Type == "insert" {
		return "pending"
	}
	target := wiki.FindBlock(current, op.BlockID)
	if target == nil {
		return "needs_update"
	}
	if wiki.BlockHash(*target) == op.ExpectedHash {
		return "pending"
	}
	if op.Type == "update" && op.Content != nil && wiki.BlockHash(*target) == wiki.BlockHash(*op.Content) {
		return "already_reflected"
	}
	return "needs_update"
}

func suggestionScopesOverlap(root wiki.BlockNode, a, b wiki.BlockOperation) bool {
	if a.Type == "insert" && b.Type == "insert" && a.ParentID == b.ParentID && a.AfterID == b.AfterID &&
		a.BeforeID == b.BeforeID {
		return true
	}
	for _, pair := range [][2]wiki.BlockOperation{{a, b}, {b, a}} {
		target := wiki.FindBlock(root, pair[0].BlockID)
		if target == nil {
			continue
		}
		for _, id := range []string{pair[1].BlockID, pair[1].ParentID, pair[1].AfterID, pair[1].BeforeID} {
			if id != "" && wiki.FindBlock(*target, id) != nil {
				return true
			}
		}
	}
	return false
}

// Placement dependencies keep a proposal's operations together, but do not
// necessarily make independently authored changes competing versions.
func suggestionOperationsConflict(root wiki.BlockNode, a, b wiki.BlockOperation) bool {
	if a.Type == "insert" && b.Type == "insert" && a.Content != nil && b.Content != nil {
		aID, _ := a.Content.Attrs["blockId"].(string)
		bID, _ := b.Content.Attrs["blockId"].(string)
		if aID != "" && bID != "" {
			return aID == bID
		}
	}
	for _, pair := range [][2]wiki.BlockOperation{{a, b}, {b, a}} {
		update, insert := pair[0], pair[1]
		if update.Type == "update" && insert.Type == "insert" && update.BlockID != "" &&
			update.BlockID != insert.ParentID && (update.BlockID == insert.AfterID || update.BlockID == insert.BeforeID) {
			return false
		}
	}
	return suggestionScopesOverlap(root, a, b)
}

// PageSuggestion exposes an operation and its current applicability without
// inserting pending content into the shared document.
type PageSuggestion struct {
	Index              int             `json:"index"`
	Type               string          `json:"type"`
	BlockID            string          `json:"blockId"`
	Status             string          `json:"status"`
	Before             *wiki.BlockNode `json:"before,omitempty"`
	After              *wiki.BlockNode `json:"after,omitempty"`
	Group              []int           `json:"group"`
	CompetingChangeIDs []uuid.UUID     `json:"competingChangeIds"`
}

type PageChangeReviewInput struct {
	RequestKey  uuid.UUID `json:"requestKey"         binding:"required"`
	Fingerprint string    `json:"fingerprint"        binding:"required,len=64"`
	Action      string    `json:"action"             binding:"required,oneof=apply reject undo request_changes resolve"`
	Indexes     []int     `json:"indexes,omitempty"  binding:"max=10000,dive,min=0"`
	Feedback    string    `json:"feedback,omitempty" binding:"max=4000"`
}

type suggestionReviewState struct {
	suggestions []PageSuggestion
	operations  []wiki.BlockOperation
	decisions   map[int]string
	fingerprint string
}

func pageSuggestionState(
	row generated.ProjectPageChange,
	current wiki.BlockNode,
	currentTheme string,
	peers []generated.ProjectPageChange,
) (suggestionReviewState, error) {
	state := suggestionReviewState{decisions: map[int]string{}, suggestions: []PageSuggestion{}}
	var base wiki.BlockNode
	if err := json.Unmarshal(row.BeforeContent, &base); err != nil {
		return state, err
	}
	if err := json.Unmarshal(row.Operations, &state.operations); err != nil {
		return state, err
	}
	if err := json.Unmarshal(row.Decisions, &state.decisions); err != nil {
		return state, err
	}
	if row.AfterTheme.Valid {
		state.operations = []wiki.BlockOperation{{Type: "theme"}}
	}
	for i, op := range state.operations {
		suggestion := PageSuggestion{
			Index:              i,
			Type:               op.Type,
			BlockID:            op.BlockID,
			Status:             suggestionApplicability(base, current, op),
			Before:             wiki.FindBlock(base, op.BlockID),
			After:              op.Content,
			Group:              []int{i},
			CompetingChangeIDs: []uuid.UUID{},
		}
		if op.Type == "insert" && op.Content != nil {
			suggestion.BlockID, _ = op.Content.Attrs["blockId"].(string)
		}
		if op.Type == "theme" {
			suggestion.Status = "needs_update"
			if currentTheme == row.BeforeTheme.String {
				suggestion.Status = "pending"
			}
			if currentTheme == row.AfterTheme.String {
				suggestion.Status = "already_reflected"
			}
		}
		if decision := state.decisions[i]; decision != "" {
			suggestion.Status = decision
		}
		if row.Status == "superseded" && state.decisions[i] == "" {
			suggestion.Status = "superseded"
		}
		if row.Status == "needs_update" && state.decisions[i] == "" {
			suggestion.Status = "needs_update"
		}
		for j, other := range state.operations {
			if i != j && suggestionScopesOverlap(base, op, other) {
				suggestion.Group = append(suggestion.Group, j)
			}
		}
		for _, peer := range peers {
			if peer.ID == row.ID {
				continue
			}
			if op.Type == "theme" && peer.AfterTheme.Valid {
				suggestion.CompetingChangeIDs = append(suggestion.CompetingChangeIDs, peer.ID)
				continue
			}
			var ops []wiki.BlockOperation
			var decisions map[int]string
			if err := json.Unmarshal(peer.Operations, &ops); err != nil {
				return state, err
			}
			if err := json.Unmarshal(peer.Decisions, &decisions); err != nil {
				return state, err
			}
			for j, other := range ops {
				if decisions[j] == "" && suggestionOperationsConflict(base, op, other) {
					suggestion.CompetingChangeIDs = append(suggestion.CompetingChangeIDs, peer.ID)
					break
				}
			}
		}
		state.suggestions = append(state.suggestions, suggestion)
	}
	// Close dependency groups transitively, with deterministic ordering.
	for i := range state.suggestions {
		selected := map[int]bool{i: true}
		for changed := true; changed; {
			changed = false
			for j := range selected {
				for _, k := range state.suggestions[j].Group {
					if !selected[k] {
						selected[k] = true
						changed = true
					}
				}
			}
		}
		state.suggestions[i].Group = nil
		for j := range state.suggestions {
			if selected[j] {
				state.suggestions[i].Group = append(state.suggestions[i].Group, j)
			}
		}
	}
	payload, _ := json.Marshal(struct {
		ID          uuid.UUID        `json:"id"`
		Status      string           `json:"status"`
		Decisions   map[int]string   `json:"decisions"`
		Suggestions []PageSuggestion `json:"suggestions"`
	}{row.ID, row.Status, state.decisions, state.suggestions})
	hash := sha256.Sum256(payload)
	state.fingerprint = hex.EncodeToString(hash[:])
	return state, nil
}

func suggestionSelection(state suggestionReviewState, in PageChangeReviewInput) ([]int, error) {
	selected := map[int]bool{}
	for _, index := range in.Indexes {
		if index < 0 || index >= len(state.operations) || selected[index] {
			return nil, ErrInvalid
		}
		selected[index] = true
	}
	if len(in.Indexes) == 0 {
		for i := range state.operations {
			if state.decisions[i] == "" {
				selected[i] = true
			}
		}
	}
	indexes := []int{}
	for i, suggestion := range state.suggestions {
		if !selected[i] {
			continue
		}
		if state.decisions[i] != "" {
			return nil, ErrConflict
		}
		for _, j := range suggestion.Group {
			if state.decisions[j] == "" && !selected[j] {
				return nil, ErrConflict
			}
		}
		if in.Action == "apply" &&
			(suggestion.Status != "pending" || len(in.Indexes) == 0 && len(suggestion.CompetingChangeIDs) > 0) {
			return nil, ErrConflict
		}
		if in.Action == "resolve" && suggestion.Status != "already_reflected" {
			return nil, ErrConflict
		}
		indexes = append(indexes, i)
	}
	if len(indexes) == 0 && len(state.operations) > 0 {
		return nil, ErrConflict
	}
	return indexes, nil
}
