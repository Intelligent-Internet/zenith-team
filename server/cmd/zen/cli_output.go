package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Output contract: results go to stdout, diagnostics and progress go to
// stderr, and `--output json` prints the server body untouched so nothing
// the CLI does not yet know about is dropped. Lists default to a table;
// single objects and mutation results default to JSON, because a create or
// update result is what a script feeds into its next command.
const (
	outputTable = "table"
	outputJSON  = "json"
)

func resolveOutput(cmd *cobra.Command, defaultFormat string) (string, error) {
	value, _ := cmd.Flags().GetString("output")
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "":
		return defaultFormat, nil
	case outputTable, outputJSON:
		return value, nil
	}
	return "", usagef("--output must be table or json, not %q", value)
}

func printJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printTable(out io.Writer, headers []string, rows [][]string) {
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(table, strings.Join(row, "\t"))
	}
	table.Flush()
}

// printKeyValues renders one object as aligned `key: value` lines.
func printKeyValues(out io.Writer, rows [][2]string) {
	width := 0
	for _, row := range rows {
		if len(row[0]) > width {
			width = len(row[0])
		}
	}
	for _, row := range rows {
		fmt.Fprintf(out, "%-*s  %s\n", width+1, row[0]+":", row[1])
	}
}

// stringOrDash renders an optional table cell.
func stringOrDash(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	return *value
}

func dashIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// truncate clips a cell so a table stays readable; JSON output is never
// truncated.
func truncate(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

// shortID keeps the leading eight characters of a UUID, enough to tell rows
// apart in a table; `--output json` carries the full identity.
func shortID(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func timeOrDash(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04")
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04")
}

// noteCursor tells an operator how to fetch the next page. It goes to
// stderr so JSON on stdout stays parseable.
func noteCursor(cmd *cobra.Command, cursor *string) {
	if cursor == nil || strings.TrimSpace(*cursor) == "" {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "more results: add --cursor %s\n", *cursor)
}
