package main

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"
)

func newMailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mail",
		Short: "Agent Mail: prepare reviewable drafts from the current Run (Agent mode)",
	}
	draft := &cobra.Command{
		Use:   "draft",
		Short: "Create one reviewable email draft in this Run's conversation; nothing is sent until the founder approves",
		Args:  cobra.NoArgs,
		RunE:  runMailDraft,
	}
	draft.Flags().StringArray("to", nil, "recipient address (repeatable, required)")
	draft.Flags().String("subject", "", "subject (required, at most 200 characters)")
	addContentFlags(draft, "text", "plain-text body (required)")
	_ = draft.MarkFlagRequired("subject")
	cmd.AddCommand(draft)
	return cmd
}

func runMailDraft(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if client.context.mode != modeAgent {
		return &modeError{msg: "`zen mail draft` is proven by a Run and only works in Agent mode"}
	}
	recipients, _ := cmd.Flags().GetStringArray("to")
	if len(recipients) == 0 {
		return usagef("at least one --to is required")
	}
	subject, _ := cmd.Flags().GetString("subject")
	if strings.TrimSpace(subject) == "" {
		return usagef("--subject is required")
	}
	text, given, err := contentFlag(cmd, "text")
	if err != nil {
		return err
	}
	if !given || strings.TrimSpace(text) == "" {
		return usagef("--text, --text-file or --text-stdin is required")
	}
	deliveries := make([]map[string]string, 0, len(recipients))
	for _, recipient := range recipients {
		deliveries = append(deliveries, map[string]string{"to": strings.TrimSpace(recipient)})
	}
	body := map[string]any{"drafts": []map[string]any{{
		"subject":    strings.TrimSpace(subject),
		"text":       text,
		"deliveries": deliveries,
	}}}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	path := client.companyPath("conversations", client.context.run.ConversationID, "mail-draft-groups")
	if err := client.post(ctx, path, body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}
