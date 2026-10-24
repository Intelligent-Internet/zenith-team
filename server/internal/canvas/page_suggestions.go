package canvas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
)

// Updates and removals target an identity; only insertions and moves consume a
// destination. Ignore unused placement metadata consistently in stale checks
// and review fingerprints.
func suggestionPlacementReferences(op wiki.BlockOperation) []string {
	if op.Type == "insert" || op.Type == "move" {
		return []string{op.BlockID, op.ParentID, op.AfterID, op.BeforeID}
	}
	return []string{op.BlockID}
}

// Compare the immutable base, current content and proposed operation at the
// block boundary. Shared-page edits never become accepted merely by comparison.
func suggestionApplicability(base, current wiki.BlockNode, op wiki.BlockOperation) string {
	original := wiki.FindBlock(base, op.BlockID)
	target := wiki.FindBlock(current, op.BlockID)
	if op.Type == "remove" && original != nil && target == nil {
		return "already_reflected"
	}
	if op.Type == "insert" && op.Content != nil {
		id, _ := op.Content.Attrs["blockId"].(string)
		if existing := wiki.FindBlock(current, id); existing != nil {
			if wiki.BlockHash(*existing) == wiki.BlockHash(*op.Content) &&
				suggestionAtDestination(current, id, op) {
				return "already_reflected"
			}
			return "needs_update"
		}
	}
	if op.Type == "move" && original != nil && target != nil &&
		wiki.BlockHash(*original) == wiki.BlockHash(*target) &&
		suggestionAtDestination(current, op.BlockID, op) {
		return "already_reflected"
	}
	for _, id := range suggestionPlacementReferences(op) {
		if id == "" {
			continue
		}
		if !suggestionPlacementUnchanged(base, current, id) {
			return "needs_update"
		}
	}
	if op.Type == "insert" {
		return "pending"
	}
	if original == nil || target == nil {
		return "needs_update"
	}
	if wiki.BlockHash(*target) == wiki.BlockHash(*original) {
		return "pending"
	}
	if op.Type == "update" && op.Content != nil && wiki.BlockHash(*target) == wiki.BlockHash(*op.Content) {
		return "already_reflected"
	}
	return "needs_update"
}

// A structural effect already present can be resolved without applying it twice.
func suggestionAtDestination(current wiki.BlockNode, id string, op wiki.BlockOperation) bool {
	placement, exists := locatePageBlock(current, id)
	if !exists || placement.parent != op.ParentID {
		return false
	}
	if op.AfterID != "" {
		return placement.previous == op.AfterID
	}
	if op.BeforeID != "" {
		return placement.next == op.BeforeID
	}
	return placement.next == ""
}

// New or removed siblings do not relocate an existing target or anchor. Only
// changing its parent or crossing a surviving original sibling invalidates it.
func suggestionPlacementUnchanged(base, current wiki.BlockNode, id string) bool {
	before, beforeExists := locatePageBlock(base, id)
	after, afterExists := locatePageBlock(current, id)
	if !beforeExists || !afterExists || before.parent != after.parent {
		return false
	}
	baseParent, currentParent := &base, &current
	if before.parent != "" {
		baseParent = wiki.FindBlock(base, before.parent)
		currentParent = wiki.FindBlock(current, after.parent)
	}
	positions := make(map[string]int, len(currentParent.Content))
	for i, child := range currentParent.Content {
		blockID, _ := child.Attrs["blockId"].(string)
		positions[blockID] = i
	}
	preceding := true
	for _, child := range baseParent.Content {
		blockID, _ := child.Attrs["blockId"].(string)
		if blockID == id {
			preceding = false
			continue
		}
		if position, exists := positions[blockID]; exists && (position < positions[id]) != preceding {
			return false
		}
	}
	return true
}

// Only writes to the same subtree or changes that invalidate a placement
// reference depend on one another. Sharing a parent or insertion slot is not
// enough: each independently authored block remains independently reviewable.
func suggestionScopesOverlap(root wiki.BlockNode, a, b wiki.BlockOperation) bool {
	for _, pair := range [][2]wiki.BlockOperation{{a, b}, {b, a}} {
		writer, other := pair[0], pair[1]
		target := wiki.FindBlock(root, writer.BlockID)
		if writer.Type == "insert" {
			target = writer.Content
		}
		if target == nil {
			continue
		}
		otherID := other.BlockID
		if other.Type == "insert" && other.Content != nil {
			otherID, _ = other.Content.Attrs["blockId"].(string)
		}
		if otherID != "" && wiki.FindBlock(*target, otherID) != nil {
			return true
		}
		// Placement fields on updates/removals do not describe content being edited.
		if other.Type != "insert" && other.Type != "move" {
			continue
		}
		if other.ParentID != "" && wiki.FindBlock(*target, other.ParentID) != nil {
			return true
		}
		for _, anchor := range []string{other.AfterID, other.BeforeID} {
			if anchor == "" {
				continue
			}
			// Updating an anchor's text preserves its identity and sibling position.
			if writer.Type == "update" && anchor == writer.BlockID {
				continue
			}
			if wiki.FindBlock(*target, anchor) != nil {
				return true
			}
		}
	}
	return false
}

// PageSuggestion exposes an operation and its current applicability without
// inserting pending content into the shared document.
type PageSuggestion struct {
	CRDTUpdate         []byte          `json:"crdtUpdate,omitempty"`
	Index              int             `json:"index"`
	Type               string          `json:"type"`
	BlockID            string          `json:"blockId"`
	Status             string          `json:"status"`
	Current            *wiki.BlockNode `json:"current,omitempty"`
	CurrentHash        string          `json:"currentHash"`
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
	updates     [][]byte
}

type pageSuggestionSnapshot struct {
	root              wiki.BlockNode
	state             []byte
	theme             string
	structureSequence int64
}

func pageSuggestionState(
	row generated.ProjectPageChange,
	document pageSuggestionSnapshot,
	peers []generated.ProjectPageChange,
) (suggestionReviewState, error) {
	current, currentTheme := document.root, document.theme
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
	if len(row.CrdtUpdates) > 0 {
		if err := json.Unmarshal(row.CrdtUpdates, &state.updates); err != nil {
			return state, err
		}
	}
	if row.AfterTheme.Valid {
		state.operations = []wiki.BlockOperation{{Type: "theme"}}
	}
	// Decode each competing snapshot once, rather than once per operation.
	type peerSuggestion struct {
		id         uuid.UUID
		theme      bool
		base       wiki.BlockNode
		operations []wiki.BlockOperation
		decisions  map[int]string
	}
	var competing []peerSuggestion
	for _, peer := range peers {
		if peer.ID == row.ID || peer.ActorUserID != row.ActorUserID {
			continue
		}
		parsed := peerSuggestion{id: peer.ID, theme: peer.AfterTheme.Valid}
		if err := json.Unmarshal(peer.BeforeContent, &parsed.base); err != nil {
			return state, err
		}
		if err := json.Unmarshal(peer.Operations, &parsed.operations); err != nil {
			return state, err
		}
		if err := json.Unmarshal(peer.Decisions, &parsed.decisions); err != nil {
			return state, err
		}
		competing = append(competing, parsed)
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
		if target := wiki.FindBlock(current, op.BlockID); target != nil {
			suggestion.CurrentHash = wiki.BlockHash(*target)
			suggestion.Current = target
			if suggestion.Status != "already_reflected" && i < len(state.updates) &&
				len(state.updates[i]) > 0 &&
				len(document.state) > 0 {
				// These are the same immutable bytes approval will merge. Concurrent
				// rewrites are CRDT operations, not conflicts between JSON snapshots.
				if suggestion.Before == nil ||
					!wiki.IsTextBlockUpdate(*suggestion.Before, *target) ||
					!suggestionPlacementUnchanged(
						base,
						current,
						op.BlockID,
					) || operationProtected(current, op) {
					suggestion.Status = "needs_update"
				} else {
					_, merged, _, err := wiki.MergeBlocks(document.state, state.updates[i])
					if err != nil {
						return state, err
					}
					suggestion.After = wiki.FindBlock(merged, op.BlockID)
					suggestion.Status = "pending"
					// A recreated XML target has the same block ID but cannot receive
					// updates addressed to the retired text. Do not silently accept it.
					if suggestion.After == nil ||
						wiki.BlockHash(*suggestion.After) == wiki.BlockHash(*target) {
						suggestion.Status = "needs_update"
					}
				}
			}

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
			if decision == "already_reflected" {
				suggestion.Status = "resolved"
			}
		}
		if state.decisions[i] == "" {
			switch row.Status {
			case "applied", "rejected", "undone", "resolved", "superseded":
				suggestion.Status = row.Status
			}
		}
		if row.Status == "needs_update" && state.decisions[i] == "" {
			suggestion.Status = "needs_update"
		}
		for j, other := range state.operations {
			if i != j && state.decisions[i] == "" && state.decisions[j] == "" &&
				suggestionScopesOverlap(base, op, other) {
				suggestion.Group = append(suggestion.Group, j)
			}
		}
		for _, peer := range competing {
			if op.Type == "theme" && peer.theme {
				suggestion.CompetingChangeIDs = append(suggestion.CompetingChangeIDs, peer.id)
				continue
			}
			for j, other := range peer.operations {
				// A moved block can belong to different subtrees in the two
				// snapshots. Compare both so each version reports the same conflict.
				if peer.decisions[j] == "" &&
					(suggestionScopesOverlap(base, op, other) || suggestionScopesOverlap(peer.base, op, other)) {
					suggestion.CompetingChangeIDs = append(suggestion.CompetingChangeIDs, peer.id)
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
	if !row.AfterTheme.Valid {
		currentTheme = ""
	}
	context := make(map[string]any)
	for _, op := range state.operations {
		for _, id := range suggestionPlacementReferences(op) {
			if id == "" {
				continue
			}
			placement, exists := locatePageBlock(current, id)
			if suggestionPlacementUnchanged(base, current, id) {
				context[id] = true
			} else {
				context[id] = []any{exists, placement.parent, placement.previous, placement.next}
			}
		}
	}
	payload, _ := json.Marshal(struct {
		Context     map[string]any   `json:"context"`
		Theme       string           `json:"theme"`
		ID          uuid.UUID        `json:"id"`
		Status      string           `json:"status"`
		Decisions   map[int]string   `json:"decisions"`
		Suggestions []PageSuggestion `json:"suggestions"`
	}{context, currentTheme, row.ID, row.Status, state.decisions, state.suggestions})
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
			(suggestion.Status != "pending" && suggestion.Status != "already_reflected") {
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

// Mixed final decisions describe a resolved proposal, not a wholesale rejection.
func suggestionDecisionStatus(count int, decisions map[int]string, fallback string) string {
	unresolved, applied, resolved := 0, 0, false
	for index := range count {
		switch decisions[index] {
		case "":
			unresolved++
		case "applied":
			applied++
		case "resolved", "already_reflected":
			resolved = true
		}
	}
	if unresolved > 0 {
		if applied > 0 {
			return "partially_applied"
		}
		return "proposed"
	}
	if count > 0 && applied == count {
		return "applied"
	}
	if applied > 0 || resolved {
		return "resolved"
	}
	return fallback
}

// Partial acceptance must keep the proposal's insertion order even when the
// member reviews siblings in a different order. Accepted inserts and earlier
// inserts in this request can anchor siblings from the same original slot.
func insertionBesideAcceptedSibling(
	state suggestionReviewState,
	current wiki.BlockNode,
	index int,
	selected []int,
) wiki.BlockOperation {
	op := state.operations[index]
	for distance := 1; distance < len(state.operations); distance++ {
		for _, candidate := range []int{index - distance, index + distance} {
			if candidate < 0 || candidate >= len(state.operations) {
				continue
			}
			// Earlier operations in this request will exist when this insert runs.
			planned := candidate < index && slices.Contains(selected, candidate)
			if !planned && state.decisions[candidate] != "applied" {
				continue
			}
			sibling := state.operations[candidate]
			if sibling.Type != "insert" || sibling.Content == nil || sibling.ParentID != op.ParentID ||
				sibling.AfterID != op.AfterID ||
				sibling.BeforeID != op.BeforeID {
				continue
			}
			id, _ := sibling.Content.Attrs["blockId"].(string)
			placement, exists := locatePageBlock(current, id)
			if !planned && (!exists || placement.parent != op.ParentID) {
				continue
			}
			preceding := candidate < index
			if op.AfterID != "" {
				preceding = !preceding
			}
			op.AfterID, op.BeforeID = "", ""
			if preceding {
				op.AfterID = id
			} else {
				op.BeforeID = id
			}
			return op
		}
	}
	return op
}
