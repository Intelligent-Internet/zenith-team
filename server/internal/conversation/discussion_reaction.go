package conversation

import (
	_ "embed"
	"strings"
)

// Exact Unicode aliases prevent two presentation forms from splitting a reaction.
// The immutable embedded catalog is also shared by web and desktop senders.
//
//go:embed emoji/unicode-16.tsv
var discussionEmojiCatalog string

func canonicalDiscussionEmoji(value string) (string, error) {
	if value == "" || len(value) > 64 || strings.ContainsAny(value, "\t\n") {
		return "", ErrDiscussionInvalid
	}
	key := "\n" + value + "\t"
	index := strings.Index(discussionEmojiCatalog, key)
	if index < 0 {
		return "", ErrDiscussionInvalid
	}
	canonical, _, _ := strings.Cut(discussionEmojiCatalog[index+len(key):], "\n")
	return canonical, nil
}
