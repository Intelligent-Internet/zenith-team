package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newInboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Your notification inbox: list, count, read and archive items",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List inbox items (active by default)",
		Args:  cobra.NoArgs,
		RunE:  runInboxList,
	}
	list.Flags().String("status", "", "active, unread, archived or all (default active)")
	list.Flags().String("kind", "", "only items of this kind")
	list.Flags().Int("limit", 50, "page size (1-100)")
	list.Flags().String("cursor", "", "cursor from a previous page")
	counts := &cobra.Command{
		Use:   "counts",
		Short: "Show the unread count",
		Args:  cobra.NoArgs,
		RunE:  runInboxCounts,
	}
	readAll := &cobra.Command{
		Use:   "read-all",
		Short: "Mark every item read",
		Args:  cobra.NoArgs,
		RunE:  runInboxReadAll,
	}
	cmd.AddCommand(list, counts, readAll)
	for _, mutation := range []string{"read", "unread", "archive", "restore"} {
		mutationCmd := &cobra.Command{
			Use:   mutation + " <item-id>",
			Short: "Mark one item " + mutation,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runInboxMutation(cmd, args, mutation)
			},
		}
		addRevisionFlag(mutationCmd)
		cmd.AddCommand(mutationCmd)
	}
	return cmd
}

func runInboxList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	flag := func(name string) string { value, _ := cmd.Flags().GetString(name); return strings.TrimSpace(value) }
	limit, _ := cmd.Flags().GetInt("limit")
	path := client.companyPath("inbox") + queryString(
		[2]string{"status", flag("status")},
		[2]string{"kind", flag("kind")},
		[2]string{"limit", strconv.Itoa(limit)},
		[2]string{"cursor", flag("cursor")},
	)
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, path, &raw); err != nil {
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
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Title     string `json:"title"`
			Unread    bool   `json:"unread"`
			CreatedAt string `json:"createdAt"`
		} `json:"items"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode inbox: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		unread := ""
		if item.Unread {
			unread = "*"
		}
		rows = append(rows, []string{unread, item.ID, item.Kind, truncate(item.Title, 60), item.CreatedAt})
	}
	printTable(cmd.OutOrStdout(), []string{"", "ID", "KIND", "TITLE", "CREATED"}, rows)
	noteCursor(cmd, page.NextCursor)
	return nil
}

func runInboxCounts(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("inbox", "counts"), &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runInboxReadAll(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.post(ctx, client.companyPath("inbox", "read-all"), map[string]any{}, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runInboxMutation(cmd *cobra.Command, args []string, mutation string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	itemID, err := uuidArg("item id", args[0])
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	path := client.companyPath("inbox", itemID)
	revision, err := resolveRevision(cmd, ctx, client, path, "")
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := client.post(ctx, path+"/"+mutation, map[string]any{"expectedRevision": revision}, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}
