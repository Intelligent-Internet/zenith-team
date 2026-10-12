package company

import (
	"fmt"
	"log/slog"

	"boardly.dev/boardly/server/pkg/redact"
)

func (CreateLocalDirectoryResourceInput) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "company.CreateLocalDirectoryResourceInput")
}

func (CreateLocalDirectoryResourceInput) GoString() string {
	return redact.TypeString("company.CreateLocalDirectoryResourceInput")
}

func (CreateLocalDirectoryResourceInput) LogValue() slog.Value {
	return redact.TypeLogValue("company.CreateLocalDirectoryResourceInput")
}

func (resourceRecord) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "company.resourceRecord")
}
func (resourceRecord) GoString() string     { return redact.TypeString("company.resourceRecord") }
func (resourceRecord) LogValue() slog.Value { return redact.TypeLogValue("company.resourceRecord") }
