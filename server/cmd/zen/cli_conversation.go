package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newConversationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "conversation",
		Aliases: []string{"conv"},
		Short:   "Work with conversations, their messages and transcripts",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List the direct conversations you can see",
		Args:  cobra.NoArgs,
		RunE:  runConversationList,
	}
	get := &cobra.Command{
		Use:   "get [conversation-id]",
		Short: "Show one conversation (the current Run's when no id is given in Agent mode)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runConversationGet,
	}
	create := &cobra.Command{
		Use:   "create",
		Short: "Start a direct conversation with an agent",
		Args:  cobra.NoArgs,
		RunE:  runConversationCreate,
	}
	create.Flags().String("agent", "", "agent id (required)")
	create.Flags().String("title", "", "conversation title")
	_ = create.MarkFlagRequired("agent")

	messages := &cobra.Command{
		Use:   "messages [conversation-id]",
		Short: "List what people and the agent said",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runConversationMessages,
	}
	messages.Flags().Int64("after-seq", 0, "only messages after this sequence number")
	messages.Flags().Int64("through-seq", 0, "newest messages at or below this sequence number (scroll back)")
	messages.Flags().Int("limit", 50, "page size (1-100)")

	transcript := &cobra.Command{
		Use:   "transcript [conversation-id]",
		Short: "List the durable provider transcript across every run of the conversation",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runConversationTranscript,
	}
	transcript.Flags().Int64("after-seq", 0, "only items after this sequence number")
	transcript.Flags().Int("limit", 200, "page size (1-200)")

	send := &cobra.Command{
		Use:   "send <conversation-id>",
		Short: "Send a message into a direct conversation (starts or queues a run)",
		Args:  cobra.ExactArgs(1),
		RunE:  runConversationSend,
	}
	addContentFlags(send, "content", "message text")

	cmd.AddCommand(list, get, create, messages, transcript, send)
	return cmd
}

// resolveConversationID defaults to the Run's own conversation in Agent
// mode; an operator must name one.
func resolveConversationID(client *apiClient, args []string) (string, error) {
	if len(args) == 0 {
		if client.context.mode == modeAgent {
			return client.context.run.ConversationID, nil
		}
		return "", usagef("a conversation id is required")
	}
	return uuidArg("conversation id", args[0])
}

// messageQuery builds the window query. afterSeq pages forward from a row
// already seen; throughSeq scrolls back to the newest rows at or below it,
// which is how an agent reads history older than its frozen snapshot.
func messageQuery(afterSeq, throughSeq int64, limit int) string {
	pairs := [][2]string{}
	if afterSeq > 0 {
		pairs = append(pairs, [2]string{"after_seq", strconv.FormatInt(afterSeq, 10)})
	}
	if throughSeq > 0 {
		pairs = append(pairs, [2]string{"through_seq", strconv.FormatInt(throughSeq, 10)})
	}
	if limit > 0 {
		pairs = append(pairs, [2]string{"limit", strconv.Itoa(limit)})
	}
	return queryString(pairs...)
}

func runConversationList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("conversations"), &raw); err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	// A row carries the agent as a reference, null once the agent was
	// deleted (ConversationListItem.agent in docs/api/spec/conversations.yaml).
	var page struct {
		Items []struct {
			ID    string  `json:"id"`
			Kind  string  `json:"kind"`
			Title *string `json:"title"`
			Agent *struct {
				Name string `json:"name"`
			} `json:"agent"`
			TaskID    *string `json:"taskId"`
			UpdatedAt string  `json:"updatedAt"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode conversations: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		agent := "-"
		if item.Agent != nil {
			agent = dashIfEmpty(item.Agent.Name)
		}
		rows = append(rows, []string{
			item.ID, item.Kind, truncate(stringOrDash(item.Title), 48),
			agent, stringOrDash(item.TaskID), item.UpdatedAt,
		})
	}
	printTable(cmd.OutOrStdout(), []string{"ID", "KIND", "TITLE", "AGENT", "TASK", "UPDATED"}, rows)
	return nil
}

func runConversationGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	conversationID, err := resolveConversationID(client, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("conversations", conversationID), &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runConversationCreate(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	agentFlag, _ := cmd.Flags().GetString("agent")
	agentID, err := uuidArg("--agent", agentFlag)
	if err != nil {
		return err
	}
	body := map[string]any{"agentId": agentID}
	if title, _ := cmd.Flags().GetString("title"); strings.TrimSpace(title) != "" {
		body["title"] = strings.TrimSpace(title)
	}
	body["requestKey"] = client.bodyRequestKey(client.companyPath("conversations"), []byte(agentID))
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.post(ctx, client.companyPath("conversations"), body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

// printMessages renders a message page: a table for people, JSON for scripts.
func printMessages(cmd *cobra.Command, raw json.RawMessage) error {
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	var page struct {
		Items []struct {
			Seq     int64  `json:"seq"`
			Type    string `json:"type"`
			Content string `json:"content"`
			RunID   string `json:"runId"`
			Author  struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"author"`
			CreatedAt string `json:"createdAt"`
		} `json:"items"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode messages: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		author := item.Author.Name
		if author == "" {
			author = item.Author.Kind
		}
		rows = append(
			rows,
			[]string{
				strconv.FormatInt(item.Seq, 10),
				item.CreatedAt,
				dashIfEmpty(author),
				item.Type,
				truncate(item.Content, 100),
			},
		)
	}
	printTable(cmd.OutOrStdout(), []string{"SEQ", "AT", "AUTHOR", "TYPE", "CONTENT"}, rows)
	noteCursor(cmd, page.NextCursor)
	return nil
}

func runConversationMessages(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	conversationID, err := resolveConversationID(client, args)
	if err != nil {
		return err
	}
	afterSeq, _ := cmd.Flags().GetInt64("after-seq")
	throughSeq, _ := cmd.Flags().GetInt64("through-seq")
	limit, _ := cmd.Flags().GetInt("limit")
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	path := messagesPath("conversations", conversationID) + messageQuery(afterSeq, throughSeq, limit)
	if err := client.get(ctx, path, &raw); err != nil {
		return err
	}
	return printMessages(cmd, raw)
}

func runConversationTranscript(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	conversationID, err := resolveConversationID(client, args)
	if err != nil {
		return err
	}
	afterSeq, _ := cmd.Flags().GetInt64("after-seq")
	limit, _ := cmd.Flags().GetInt("limit")
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	path := client.companyPath("conversations", conversationID, "transcript") + messageQuery(afterSeq, 0, limit)
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
	// Rows are RunTranscriptEntry (docs/api/runs.md): a display role and the
	// provider-neutral parts; there is no type/payload pair on the wire.
	var page struct {
		Items []struct {
			Seq     int64           `json:"seq"`
			RunID   string          `json:"runId"`
			ItemKey string          `json:"itemKey"`
			Role    string          `json:"role"`
			Parts   json.RawMessage `json:"parts"`
		} `json:"items"`
		HasMore bool `json:"hasMore"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode transcript: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(rows, []string{
			strconv.FormatInt(item.Seq, 10), shortID(item.RunID), dashIfEmpty(item.Role),
			dashIfEmpty(item.ItemKey), truncate(string(item.Parts), 90),
		})
	}
	printTable(cmd.OutOrStdout(), []string{"SEQ", "RUN", "ROLE", "ITEM", "PARTS"}, rows)
	// The next page continues from the last row returned. throughSeq is the
	// conversation head when the page began, so continuing from it would
	// skip every row between.
	if page.HasMore && len(page.Items) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "more items: add --after-seq %d\n", page.Items[len(page.Items)-1].Seq)
	}
	return nil
}

func runConversationSend(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	conversationID, err := uuidArg("conversation id", args[0])
	if err != nil {
		return err
	}
	content, given, err := contentFlag(cmd, "content")
	if err != nil {
		return err
	}
	if !given || strings.TrimSpace(content) == "" {
		return usagef("--content, --content-file or --content-stdin is required")
	}
	path := messagesPath("conversations", conversationID)
	body := map[string]any{"requestKey": client.bodyRequestKey(path, []byte(content)), "content": content}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.post(ctx, path, body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}
