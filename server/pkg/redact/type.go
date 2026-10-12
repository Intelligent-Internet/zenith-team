package redact

import (
	"fmt"
	"io"
	"log/slog"
)

// TypeString returns the deterministic placeholder a credential-bearing type
// prints instead of its fields: "<pkg.Type>([REDACTED])". Such types implement
// fmt.Formatter, fmt.GoStringer, and slog.LogValuer on these three helpers so
// no %v/%#v/slog rendering path can leak contents.
func TypeString(qualifiedName string) string {
	return qualifiedName + "([REDACTED])"
}

// TypeFormat writes TypeString onto a fmt.Formatter's state.
func TypeFormat(state fmt.State, qualifiedName string) {
	_, _ = io.WriteString(state, TypeString(qualifiedName))
}

// TypeLogValue returns TypeString as the type's slog value.
func TypeLogValue(qualifiedName string) slog.Value {
	return slog.StringValue(TypeString(qualifiedName))
}
