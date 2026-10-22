package canvas

import "github.com/Intelligent-Internet/zenith-team/server/internal/wiki"

// Approvals and captured presentations consume the same stored offline updates.
// Structural operations retain their ordinary scope, hash and placement guards.
func applyPageSuggestions(saved []byte, review suggestionReviewState, indexes []int) ([]byte, wiki.BlockNode, error) {
	state, root, _, err := wiki.MergeBlocks(saved, nil)
	if err != nil {
		return nil, root, err
	}
	for _, index := range indexes {
		if review.suggestions[index].Status == "already_reflected" {
			continue
		}
		if index < len(review.updates) && len(review.updates[index]) > 0 {
			state, root, _, err = wiki.MergeBlocks(state, review.updates[index])
		} else {
			op := review.operations[index]
			if op.Type == "theme" || review.suggestions[index].Status == "already_reflected" {
				continue
			}
			if op.Type == "insert" {
				op = insertionBesideAcceptedSibling(review, root, index, indexes)
			}
			state, root, err = wiki.ApplyBlockOperations(state, []wiki.BlockOperation{op})
		}
		if err != nil {
			return nil, root, err
		}
	}
	return state, root, nil
}
