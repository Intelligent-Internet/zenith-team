package redact

import (
	"regexp"
	"sort"
	"strings"
)

// literalRuleID names the placeholder used for exact secret literals layered
// on top of the shared ruleset (Run capability tokens and similar values the
// producer knows verbatim).
const literalRuleID = "literal"

// minimumSecretLiteralBytes rejects short literals whose replacement could
// collide with placeholder text and break idempotence.
const minimumSecretLiteralBytes = 8

// Redactor layers producer-specific exact literals — the Run capability
// token and similar values — over the shared versioned ruleset.
// The zero value applies the shared ruleset alone, which is exactly what the
// server-before-commit seams use.
type Redactor struct {
	literals []literalRule
}

type literalRule struct {
	length      int
	pattern     *regexp.Regexp
	replacement string
}

// Options declares the exact literals one Redactor layers over the ruleset.
type Options struct {
	// SecretLiterals are exact sensitive strings replaced with
	// [redacted:literal]. Empty, short, or placeholder-colliding values are
	// skipped so idempotence always holds.
	SecretLiterals []string
}

// NewRedactor compiles the literal layer. Longer literals replace first so a
// literal containing another literal never leaves a partial rewrite behind.
func NewRedactor(options Options) Redactor {
	redactor := Redactor{}
	for _, value := range options.SecretLiterals {
		if !safeSecretLiteral(value) {
			continue
		}
		redactor.literals = append(redactor.literals, literalRule{
			length:      len(value),
			pattern:     regexp.MustCompile(regexp.QuoteMeta(value)),
			replacement: placeholder(literalRuleID),
		})
	}
	sort.SliceStable(redactor.literals, func(left, right int) bool {
		return redactor.literals[left].length > redactor.literals[right].length
	})
	return redactor
}

// safeSecretLiteral admits only literals whose exact replacement cannot
// re-trigger a rewrite: long enough to be a secret, free of the placeholder
// bracket alphabet, and never a substring of any placeholder text.
func safeSecretLiteral(value string) bool {
	if len(value) < minimumSecretLiteralBytes || strings.ContainsAny(value, "[]<>") {
		return false
	}
	for _, text := range placeholderTexts {
		if strings.Contains(text, value) {
			return false
		}
	}
	return true
}

// Version reports the shared ruleset version this Redactor applies.
func (Redactor) Version() int { return rulesetVersion }

// Redact replaces the exact literals first, then applies the shared
// versioned ruleset. Like the package-level Redact it is idempotent.
func (r Redactor) Redact(text string) string {
	for _, literal := range r.literals {
		text = literal.pattern.ReplaceAllString(text, literal.replacement)
	}
	return Redact(text)
}

// Value deep-redacts every string reachable through the common decoded-JSON
// shapes (maps, slices, strings), preserves nil containers, and returns other
// values unchanged.
func (r Redactor) Value(value any) any {
	switch value := value.(type) {
	case string:
		return r.Redact(value)
	case map[string]any:
		if value == nil {
			return map[string]any(nil)
		}
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = r.Value(item)
		}
		return result
	case map[string]string:
		if value == nil {
			return map[string]string(nil)
		}
		result := make(map[string]string, len(value))
		for key, item := range value {
			result[key] = r.Redact(item)
		}
		return result
	case []any:
		if value == nil {
			return []any(nil)
		}
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = r.Value(item)
		}
		return result
	case []string:
		if value == nil {
			return []string(nil)
		}
		result := make([]string, len(value))
		for index, item := range value {
			result[index] = r.Redact(item)
		}
		return result
	default:
		return value
	}
}
