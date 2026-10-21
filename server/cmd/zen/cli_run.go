package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// runView is the subset of the Run contract the tables render
// (docs/api/spec/runs.yaml `Run`). A Run's only timestamps are createdAt,
// updatedAt — when it entered its current status — and fireAt; it names its
// agent by id alone, which a client joins against the roster itself.
type runView struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	AgentID        string  `json:"agentId"`
	TaskID         *string `json:"taskId"`
	ConversationID string  `json:"conversationId"`
	RuntimeID      *string `json:"runtimeId"`
	Source         *struct {
		Key   *string `json:"key"`
		Title *string `json:"title"`
	} `json:"source"`
	FailureReason  *string `json:"failureReason"`
	FailureMessage *string `json:"failureMessage"`
	Error          *string `json:"error"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
	FireAt         *string `json:"fireAt"`
}

// agentNames is the roster join: agent id to name, for the AGENT cells.
type agentNames map[string]string

// cell renders the agent for a table row: the name when the roster has it,
// otherwise enough of the id to tell rows apart.
func (n agentNames) cell(agentID string) string {
	if name := n[agentID]; name != "" {
		return name
	}
	return shortID(agentID)
}

// label renders the agent for a single-object view: `name (id)`, or the
// bare id when the roster does not name it.
func (n agentNames) label(agentID string) string {
	if name := n[agentID]; name != "" {
		return name + " (" + agentID + ")"
	}
	return agentID
}

// fetchAgentNames reads the company roster once, archived agents included,
// so a page of runs joins its agent ids in one request. It is only a table
// decoration: a roster the caller cannot read leaves the ids in the cells
// and says why on stderr, because the runs were already fetched and must
// still print.
func fetchAgentNames(ctx context.Context, cmd *cobra.Command, client *apiClient) agentNames {
	var page struct {
		Items []agentView `json:"items"`
	}
	if err := client.get(ctx, client.companyPath("agents")+"?include_archived=true", &page); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "agent names unavailable, showing ids: %v\n", err)
		return nil
	}
	names := make(agentNames, len(page.Items))
	for _, agent := range page.Items {
		names[agent.ID] = agent.Name
	}
	return names
}

func (v runView) row(names agentNames) []string {
	return []string{
		v.ID, v.Status, names.cell(v.AgentID), stringOrDash(v.TaskID), shortID(v.ConversationID),
		v.CreatedAt, v.UpdatedAt, stringOrDash(v.FailureReason),
	}
}

// sourceLabel renders the enqueue-time source as `key title`; either part
// may be null, and the whole source is null for a Run that predates it.
func (v runView) sourceLabel() string {
	if v.Source == nil {
		return "-"
	}
	return dashIfEmpty(strings.TrimSpace(stringOrEmpty(v.Source.Key) + " " + stringOrEmpty(v.Source.Title)))
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func printRunTable(out io.Writer, raw json.RawMessage, names agentNames) error {
	var page struct {
		Items []runView `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode runs: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(rows, item.row(names))
	}
	headers := []string{"ID", "STATUS", "AGENT", "TASK", "CONVERSATION", "CREATED", "UPDATED", "FAILURE"}
	printTable(out, headers, rows)
	return nil
}

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Inspect and cancel runs; show the current Run's identity",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List runs in the company, newest first",
		Args:  cobra.NoArgs,
		RunE:  runRunList,
	}
	list.Flags().String("status", "", "comma-separated status filter")
	list.Flags().Bool("active", false, "only runs that are not finished")
	list.Flags().String("task", "", "only runs of this task id")
	list.Flags().String("conversation", "", "only runs of this conversation id")
	list.Flags().String("agent", "", "only runs of this agent id")
	list.Flags().String("runtime", "", "only runs claimed by this runtime id")
	list.Flags().Int("limit", 50, "page size (1-100)")
	list.Flags().String("cursor", "", "cursor from a previous page")

	get := &cobra.Command{
		Use:   "get [run-id]",
		Short: "Show one run (the current Run when no id is given in Agent mode)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runRunGet,
	}
	cancel := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a queued or running run",
		Args:  cobra.ExactArgs(1),
		RunE:  runRunCancel,
	}
	context := &cobra.Command{
		Use:   "context",
		Short: "Show the identity this shell is bound to (Agent mode) and the commands it may use",
		Args:  cobra.NoArgs,
		RunE:  runRunContext,
	}
	cmd.AddCommand(list, get, cancel, context)
	return cmd
}

func runRunList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	flag := func(name string) string { value, _ := cmd.Flags().GetString(name); return strings.TrimSpace(value) }
	for _, name := range []string{"task", "conversation", "agent", "runtime"} {
		if value := flag(name); value != "" {
			if _, err := uuidArg("--"+name, value); err != nil {
				return err
			}
		}
	}
	limit, _ := cmd.Flags().GetInt("limit")
	active := ""
	if value, _ := cmd.Flags().GetBool("active"); value {
		active = "true"
	}
	path := client.companyPath("runs") + queryString(
		[2]string{"status", flag("status")},
		[2]string{"active", active},
		[2]string{"task_id", flag("task")},
		[2]string{"conversation_id", flag("conversation")},
		[2]string{"agent_id", flag("agent")},
		[2]string{"runtime_id", flag("runtime")},
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
	if err := printRunTable(cmd.OutOrStdout(), raw, fetchAgentNames(ctx, cmd, client)); err != nil {
		return err
	}
	var page struct {
		NextCursor *string `json:"nextCursor"`
	}
	_ = json.Unmarshal(raw, &page)
	noteCursor(cmd, page.NextCursor)
	return nil
}

func runRunGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	var runID string
	switch {
	case len(args) == 1:
		if runID, err = uuidArg("run id", args[0]); err != nil {
			return err
		}
	case client.context.mode == modeAgent:
		runID = client.context.run.RunID
	default:
		return usagef("a run id is required")
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("runs", runID), &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, func(out io.Writer) error {
		var view runView
		if err := json.Unmarshal(raw, &view); err != nil {
			return fmt.Errorf("decode run: %w", err)
		}
		names := fetchAgentNames(ctx, cmd, client)
		printKeyValues(out, [][2]string{
			{"id", view.ID},
			{"status", view.Status},
			{"agent", names.label(view.AgentID)},
			{"task", stringOrDash(view.TaskID)},
			{"conversation", view.ConversationID},
			{"runtime", stringOrDash(view.RuntimeID)},
			{"source", view.sourceLabel()},
			{"created", view.CreatedAt},
			{"updated", view.UpdatedAt},
			{"fire at", stringOrDash(view.FireAt)},
			{"failure", stringOrDash(view.FailureReason)},
			{"message", stringOrDash(view.FailureMessage)},
			{"error", stringOrDash(view.Error)},
		})
		return nil
	})
}

func runRunCancel(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	runID, err := uuidArg("run id", args[0])
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.post(ctx, client.companyPath("runs", runID, "cancel"), nil, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

// runRunContext needs no network: run.json is the daemon-written identity.
func runRunContext(cmd *cobra.Command, _ []string) error {
	context := resolveCommandContext()
	if context.mode != modeAgent {
		return &modeError{
			msg: "not inside a Zenith Team Run: this is an operator shell (see `zen auth status`)",
		}
	}
	if context.channelErr != nil {
		return context.channelErr
	}
	format, err := resolveOutput(cmd, outputJSON)
	if err != nil {
		return err
	}
	run := context.run
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), struct {
			Mode             string   `json:"mode"`
			RunID            string   `json:"runId"`
			CompanyID        string   `json:"companyId"`
			AgentID          string   `json:"agentId"`
			AgentName        string   `json:"agentName,omitempty"`
			TaskID           string   `json:"taskId,omitempty"`
			TaskKey          string   `json:"taskKey,omitempty"`
			ProjectID        string   `json:"projectId,omitempty"`
			ProjectKey       string   `json:"projectKey,omitempty"`
			ConversationID   string   `json:"conversationId"`
			ConversationKind string   `json:"conversationKind"`
			Commands         []string `json:"commands"`
		}{
			Mode: "agent", RunID: run.RunID, CompanyID: run.CompanyID, AgentID: run.AgentID,
			AgentName: run.AgentName, TaskID: run.TaskID, TaskKey: run.TaskKey,
			ProjectID: run.ProjectID, ProjectKey: run.ProjectKey,
			ConversationID: run.ConversationID, ConversationKind: run.ConversationKind,
			Commands: run.Commands,
		})
	}
	out := cmd.OutOrStdout()
	printKeyValues(out, [][2]string{
		{"mode", "agent"},
		{"run", run.RunID},
		{"company", run.CompanyID},
		{"agent", dashIfEmpty(run.AgentName) + " (" + run.AgentID + ")"},
		{"task", dashIfEmpty(run.TaskKey) + " " + dashIfEmpty(run.TaskID)},
		{"project", dashIfEmpty(run.ProjectKey) + " " + dashIfEmpty(run.ProjectID)},
		{"conversation", run.ConversationID + " (" + run.ConversationKind + ")"},
	})
	fmt.Fprintln(out, "commands this Run may use:")
	for _, command := range run.Commands {
		fmt.Fprintln(out, "  "+command)
	}
	return nil
}
