package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newApprovalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approval",
		Short: "Founder approvals: list, inspect, approve, reject or cancel",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List approvals (pending by default)",
		Args:  cobra.NoArgs,
		RunE:  runApprovalList,
	}
	addHITLListFlags(list)
	get := &cobra.Command{
		Use:   "get <approval-id>",
		Short: "Show one approval with its preview",
		Args:  cobra.ExactArgs(1),
		RunE:  runApprovalGet,
	}
	cmd.AddCommand(list, get)
	for _, decision := range []string{"approve", "reject", "cancel"} {
		decisionCmd := &cobra.Command{
			Use:   decision + " <approval-id>",
			Short: strings.ToUpper(decision[:1]) + decision[1:] + " a pending approval",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runApprovalDecision(cmd, args, decision)
			},
		}
		decisionCmd.Flags().String("note", "", "decision note")
		addRevisionFlag(decisionCmd)
		cmd.AddCommand(decisionCmd)
	}
	return cmd
}

func runApprovalList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	query, err := hitlListQuery(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("approvals")+query, &raw); err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	var page struct {
		Items []struct {
			ID               string  `json:"id"`
			Kind             string  `json:"kind"`
			Status           string  `json:"status"`
			Title            string  `json:"title"`
			TaskID           *string `json:"taskId"`
			ExpiresAt        string  `json:"expiresAt"`
			RequestedByAgent struct {
				Name string `json:"name"`
			} `json:"requestedByAgent"`
		} `json:"items"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode approvals: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(rows, []string{item.ID, item.Kind, item.Status, truncate(item.Title, 48), dashIfEmpty(item.RequestedByAgent.Name), stringOrDash(item.TaskID), item.ExpiresAt})
	}
	printTable(cmd.OutOrStdout(), []string{"ID", "KIND", "STATUS", "TITLE", "AGENT", "TASK", "EXPIRES"}, rows)
	noteCursor(cmd, page.NextCursor)
	return nil
}

func runApprovalGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	approvalID, err := uuidArg("approval id", args[0])
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("approvals", approvalID), &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runApprovalDecision(cmd *cobra.Command, args []string, decision string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	approvalID, err := uuidArg("approval id", args[0])
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	path := client.companyPath("approvals", approvalID)
	revision, err := resolveRevision(cmd, ctx, client, path, "")
	if err != nil {
		return err
	}
	note, _ := cmd.Flags().GetString("note")
	var raw json.RawMessage
	if err := client.post(ctx, path+"/"+decision, map[string]any{"expectedRevision": revision, "decisionNote": note}, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}
