package protocol

import "strings"

// MaxJSONSafeInteger is the largest integer JSON (and JavaScript's Number)
// represents exactly. Every wire-facing integer — revisions, sequence
// numbers, access versions, cursors — is capped to it so clients never
// receive a value they silently round.
const MaxJSONSafeInteger int64 = 9007199254740991

func TrimContractWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch {
		case character >= '\u0009' && character <= '\u000d':
			return true
		case character == '\u0020', character == '\u0085', character == '\u00a0',
			character == '\u1680', character == '\u2028', character == '\u2029',
			character == '\u202f', character == '\u205f', character == '\u3000',
			character == '\ufeff':
			return true
		case character >= '\u2000' && character <= '\u200a':
			return true
		default:
			return false
		}
	})
}
