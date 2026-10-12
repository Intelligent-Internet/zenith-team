package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// Body text can arrive three ways — inline, from a file, or from stdin —
// so an agent never has to fight shell quoting for a multi-line
// description or reason. Exactly one source may be given.
func addContentFlags(cmd *cobra.Command, name, usage string) {
	cmd.Flags().String(name, "", usage)
	cmd.Flags().String(name+"-file", "", usage+" read from a UTF-8 file")
	cmd.Flags().Bool(name+"-stdin", false, usage+" read from stdin")
}

// contentFlag resolves one of the three sources; ok is false when none was
// given, so callers decide whether the field is required.
func contentFlag(cmd *cobra.Command, name string) (value string, ok bool, err error) {
	inline, _ := cmd.Flags().GetString(name)
	file, _ := cmd.Flags().GetString(name + "-file")
	stdin, _ := cmd.Flags().GetBool(name + "-stdin")
	sources := 0
	if cmd.Flags().Changed(name) {
		sources++
	}
	if file != "" {
		sources++
	}
	if stdin {
		sources++
	}
	if sources > 1 {
		return "", false, usagef("pass only one of --%s, --%s-file or --%s-stdin", name, name, name)
	}
	switch {
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", false, fmt.Errorf("read --%s-file: %w", name, err)
		}
		return string(data), true, nil
	case stdin:
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4<<20))
		if err != nil {
			return "", false, fmt.Errorf("read --%s-stdin: %w", name, err)
		}
		return string(data), true, nil
	case cmd.Flags().Changed(name):
		return inline, true, nil
	}
	return "", false, nil
}

func uuidArg(what, value string) (string, error) {
	value = strings.TrimSpace(value)
	if _, err := uuid.Parse(value); err != nil {
		return "", usagef("%s %q is not a UUID", what, value)
	}
	return value, nil
}

// fetchRevision reads the current revision of one revisioned object so a
// mutation can name the state it saw; the server refuses a stale one.
func fetchRevision(ctx context.Context, client *apiClient, path string, field string) (int64, error) {
	var raw map[string]json.RawMessage
	if err := client.get(ctx, path, &raw); err != nil {
		return 0, err
	}
	object := raw
	if field != "" {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw[field], &nested); err != nil {
			return 0, fmt.Errorf("decode %s: %w", field, err)
		}
		object = nested
	}
	var revision int64
	if err := json.Unmarshal(object["revision"], &revision); err != nil {
		return 0, fmt.Errorf("decode revision from %s: %w", path, err)
	}
	return revision, nil
}

// revisionFlag lets a caller pin the revision it already holds instead of
// spending a read; 0 means "fetch it first".
func addRevisionFlag(cmd *cobra.Command) {
	cmd.Flags().Int64("revision", 0, "expected revision (fetched first when omitted)")
}

func resolveRevision(cmd *cobra.Command, ctx context.Context, client *apiClient, path, field string) (int64, error) {
	if revision, _ := cmd.Flags().GetInt64("revision"); revision > 0 {
		return revision, nil
	}
	return fetchRevision(ctx, client, path, field)
}

// csvFlag splits a comma-separated flag into trimmed non-empty parts.
func csvFlag(cmd *cobra.Command, name string) []string {
	raw, _ := cmd.Flags().GetString(name)
	var parts []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// queryString builds ?a=b&c=d from ordered pairs, skipping empty values.
func queryString(pairs ...[2]string) string {
	var parts []string
	for _, pair := range pairs {
		if pair[1] == "" {
			continue
		}
		parts = append(parts, pair[0]+"="+pair[1])
	}
	if len(parts) == 0 {
		return ""
	}
	return "?" + strings.Join(parts, "&")
}

// printResult renders a mutation or single-object result: JSON by default,
// or the caller's table when the operator asked for one.
func printResult(cmd *cobra.Command, raw json.RawMessage, table func(io.Writer) error) error {
	format, err := resolveOutput(cmd, outputJSON)
	if err != nil {
		return err
	}
	if format == outputJSON || table == nil {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	return table(cmd.OutOrStdout())
}
