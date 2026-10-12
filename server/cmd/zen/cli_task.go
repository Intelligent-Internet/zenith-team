package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// taskView is the subset of task.Response the tables render; JSON output
// prints the server body untouched.
type taskView struct {
	AssignmentStatus     string   `json:"assignmentStatus"`
	ScheduledAt          *string  `json:"scheduledAt"`
	ProjectID            string   `json:"projectId"`
	ID                   string   `json:"id"`
	Key                  string   `json:"key"`
	Title                string   `json:"title"`
	Description          string   `json:"description"`
	Status               string   `json:"status"`
	Priority             string   `json:"priority"`
	MilestoneType        string   `json:"milestoneType"`
	ConversationID       string   `json:"conversationId"`
	Number               int64    `json:"number"`
	Labels               []string `json:"labels"`
	DueDate              *string  `json:"dueDate"`
	BlockedByCount       int64    `json:"blockedByCount"`
	DependenciesResolved bool     `json:"dependenciesResolved"`
	Revision             int64    `json:"revision"`
	// Assignee is the member who owns the Task and runs it on their own
	// computer; ExecutedBy is the Agent doing the work, or the assignee again
	// when they do it themselves (ADR 0014).
	Assignee struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"assignee"`
	ExecutedBy struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"executedBy"`
	Execution struct {
		AssigneeReady bool    `json:"assigneeReady"`
		UnreadyReason *string `json:"unreadyReason"`
	} `json:"execution"`
}

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Work with tasks",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List tasks in the company",
		Args:  cobra.NoArgs,
		RunE:  runTaskList,
	}
	list.Flags().String("project", "", "filter by Project id or key")
	list.Flags().String("status", "", "comma-separated status filter (e.g. backlog,todo,in_progress)")
	list.Flags().String("priority", "", "comma-separated priority filter")
	list.Flags().String("assignee-user", "", "only tasks this member owns")
	list.Flags().String("executed-by-agent", "", "only tasks this agent does the work for")

	get := &cobra.Command{
		Use:   "get [task-id|task-key]",
		Short: "Show one task (the current Run's task when no id is given in Agent mode)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runTaskGet,
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a task",
		Long: "Creates a task owned by a member, who runs it on their own computer. " +
			"Inside a Run the task belongs to the member the Run works for, and the Run's own agent " +
			"does the work unless you name another agent or --executed-by-self. With --run-at or --cron " +
			"it waits for the scheduled time; otherwise it starts right away when the member has the agent set up. " +
			"CEO chat Runs require a schedule; immediate planned work uses Company Plan review. A Task Run files it in its " +
			"own Project, the only one it can look up.",
		Args: cobra.NoArgs,
		RunE: runTaskCreate,
	}
	create.Flags().
		String("project", "", "Project id or key (required, except inside a Task Run: that task's Project)")
	create.Flags().String("title", "", "task title (required)")
	addContentFlags(create, "description", "task description")
	create.Flags().String("milestone", "", "milestone type (required; see the company plan vocabulary)")
	create.Flags().
		String("key", "", "stable task key such as ship_release; omit it and the server mints the {prefix}-{number} label, a shape a chosen key may not take")
	create.Flags().String("status", "", "initial status (default backlog)")
	create.Flags().String("priority", "", "priority")
	create.Flags().String("labels", "", "comma-separated labels")
	create.Flags().String("due-date", "", "due date YYYY-MM-DD")
	create.Flags().String("run-at", "", "schedule one run at an RFC3339 timestamp, with whole-minute precision")
	create.Flags().String("cron", "", "repeat with a five-field cron expression (minimum five minutes)")
	create.Flags().String("time-zone", "", "IANA time zone for --cron, for example Asia/Ho_Chi_Minh")

	create.Flags().String("start-date", "", "start date YYYY-MM-DD")
	create.Flags().
		String("assignee-user", "", "the member who owns the task and runs it on their computer (default: you; another member needs tasks.assign; inside a Run requires a schedule and recipient acceptance)")
	create.Flags().
		String("executed-by-agent", "", "the agent that does the work; it starts right away when you are the assignee and have it set up")
	create.Flags().
		Bool("executed-by-self", false, "the assignee does the work themselves (no agent)")
	for _, required := range []string{"title", "milestone"} {
		_ = create.MarkFlagRequired(required)
	}

	update := &cobra.Command{
		Use:   "update <task-id|task-key>",
		Short: "Change a task (only the flags you pass change; the revision is fetched first)",
		Args:  cobra.ExactArgs(1),
		RunE:  runTaskUpdate,
	}
	update.Flags().String("title", "", "new title")
	addContentFlags(update, "description", "new description")
	update.Flags().String("status", "", "new status")
	update.Flags().String("priority", "", "new priority")
	update.Flags().String("labels", "", "comma-separated labels (replaces the set)")
	update.Flags().String("due-date", "", "due date YYYY-MM-DD; pass an empty string to clear")
	update.Flags().String("start-date", "", "start date YYYY-MM-DD; pass an empty string to clear")
	update.Flags().
		String("assignee-user", "", "hand the task to this member; it will run on their computer")
	update.Flags().String("executed-by-agent", "", "change the agent that does the work")
	update.Flags().Bool("executed-by-self", false, "the assignee does the work themselves (no agent)")
	update.Flags().
		Bool("confirm-handoff", false, "confirm a handoff of a task that has already run: its live runs are cancelled and the work starts fresh")
	update.Flags().String("milestone", "", "new milestone type")
	addRevisionFlag(update)

	start := &cobra.Command{
		Use:   "start <task-id|task-key>",
		Short: "Start an execution run for a task",
		Args:  cobra.ExactArgs(1),
		RunE:  runTaskStart,
	}
	addRevisionFlag(start)

	dependencies := &cobra.Command{
		Use:   "dependencies [task-id|task-key]",
		Short: "List the tasks a task depends on",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runTaskDependencies,
	}
	workspace := &cobra.Command{
		Use:   "workspace <task-id|task-key>",
		Short: "Show a task's workspace and conversation",
		Args:  cobra.ExactArgs(1),
		RunE:  runTaskWorkspace,
	}
	messages := &cobra.Command{
		Use:   "messages <task-id|task-key>",
		Short: "List the messages of a task's conversation",
		Args:  cobra.ExactArgs(1),
		RunE:  runTaskMessages,
	}
	messages.Flags().Int64("after-seq", 0, "only messages after this sequence number")
	messages.Flags().Int("limit", 50, "page size (1-100)")

	send := &cobra.Command{
		Use:   "send <task-id|task-key>",
		Short: "Send a message into a task's conversation (starts or queues a run)",
		Args:  cobra.ExactArgs(1),
		RunE:  runTaskSend,
	}
	addContentFlags(send, "content", "message text")

	fail := &cobra.Command{
		Use:   "fail",
		Short: "Declare the current Run's task a business failure (Agent mode)",
		Long: "Records an Agent-declared business failure for the task this Run executes, " +
			"proven by the Run itself. Use it only after a reasonable attempt and with evidence; " +
			"missing setup, a pending approval or an unanswered question is not a failure.",
		Args: cobra.NoArgs,
		RunE: runTaskFail,
	}
	addContentFlags(fail, "reason", "the evidence-based reason (required, at most 2000 characters)")

	cmd.AddCommand(list, get, create, update, start, dependencies, workspace, messages, send, fail)
	return cmd
}

// resolveTaskID is the id of the Task a command acts on. A UUID is the id,
// and in Agent mode the Run's own Task needs no request. Any other name is
// read with GET /tasks/{name}, as `zen task get` reads it, and the id comes
// from the server's answer.
func resolveTaskID(ctx context.Context, client *apiClient, args []string) (string, error) {
	name, err := taskReference(client, args)
	if err != nil {
		return "", err
	}
	if _, err := uuid.Parse(name); err == nil {
		return name, nil
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := client.get(ctx, client.companyPath("tasks", name), &task); err != nil {
		return "", relayRunScopeRefusal(err)
	}
	return task.ID, nil
}

// taskReference names a Task the way the server's Task reads take it: its
// id, its key, or the {prefix}-{number} label every surface displays. The
// server looks keys and labels up itself, so the CLI sends the name as
// given and a name that is no Task fails with the server's code and
// request id. In Agent mode a Run lists only its own Tasks, so a
// colleague's Task could not be looked up here anyway; the server refuses
// it instead. The Run's own Task needs no request, named or not.
//
// The name becomes one segment of the request path, and the server routes
// on the decoded path, so a slash or a dot segment would land on another
// route — the Task's workspace, its results. Such a name is refused here as
// a usage error in both modes. An operator's name is escaped, so any other
// key the server allows (a space, a letter like é) arrives as given. In
// Agent mode the daemon refuses any percent sign in a channel path, so a
// name that would need escaping is refused too. Such a Task is still named
// by its id or its {prefix}-{number} label.
func taskReference(client *apiClient, args []string) (string, error) {
	run := client.context.run
	own := client.context.mode == modeAgent && run.TaskID != ""
	if len(args) == 0 {
		if own {
			return run.TaskID, nil
		}
		return "", usagef("a task id or key is required")
	}
	value := strings.TrimSpace(args[0])
	if value == "" {
		return "", usagef("a task id or key is required")
	}
	if own && strings.EqualFold(value, run.TaskKey) {
		return run.TaskID, nil
	}
	escaped := url.PathEscape(value)
	if value == "." || value == ".." || strings.Contains(value, "/") ||
		(client.context.mode == modeAgent && escaped != value) {
		return "", usagef("%q cannot name a task: use its id or its label, such as ENG-12", value)
	}
	return escaped, nil
}

// relayRunScopeRefusal keeps a Run's refused read — a Task that is not its
// own, or another Project read by a Task Run — to the server's
// sentence, "Permission denied. You can only access your own Tasks.", with
// no request line in front, so `zen task` and `zen project` print it the
// same way. The code and exit code are unchanged.
func relayRunScopeRefusal(err error) error {
	var refusal *apiError
	if !errors.As(err, &refusal) || refusal.Code != "task_outside_run_scope" || refusal.ServerDetail == "" {
		return err
	}
	relayed := *refusal
	relayed.Detail = refusal.ServerDetail
	return &relayed
}

func runTaskList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	assigneeID, _ := cmd.Flags().GetString("assignee-user")
	if assigneeID != "" {
		if _, err := uuidArg("--assignee-user", assigneeID); err != nil {
			return err
		}
	}
	executedByAgentID, _ := cmd.Flags().GetString("executed-by-agent")
	if executedByAgentID != "" {
		if _, err := uuidArg("--executed-by-agent", executedByAgentID); err != nil {
			return err
		}
	}
	status, _ := cmd.Flags().GetString("status")
	priority, _ := cmd.Flags().GetString("priority")
	ctx, cancel := apiContext()
	defer cancel()
	projectID := ""
	if value, _ := cmd.Flags().GetString("project"); value != "" {
		project, err := resolveProject(ctx, client, value)
		if err != nil {
			return err
		}
		projectID = project.ID
	}
	path := client.companyPath("tasks") + queryString(
		[2]string{"project_id", projectID},
		[2]string{"status", status},
		[2]string{"priority", priority},
		[2]string{"assignee_id", assigneeID},
		[2]string{"executed_by_agent_id", executedByAgentID},
	)
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
		Items []taskView `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode tasks: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		blocked := "-"
		if item.BlockedByCount > 0 {
			blocked = strconv.FormatInt(item.BlockedByCount, 10)
		}
		executedBy := dashIfEmpty(item.ExecutedBy.Name)
		if item.ExecutedBy.Kind == "user" {
			executedBy = "(themselves)"
		}
		rows = append(rows, []string{
			item.Key, truncate(item.Title, 48), item.Status, item.Priority,
			dashIfEmpty(item.Assignee.Name), executedBy, blocked, item.ID,
		})
	}
	printTable(
		cmd.OutOrStdout(),
		[]string{"KEY", "TITLE", "STATUS", "PRIORITY", "ASSIGNEE", "EXECUTED BY", "BLOCKED_BY", "ID"},
		rows,
	)
	return nil
}

func printTaskDetail(out io.Writer, raw json.RawMessage) error {
	var item taskView
	if err := json.Unmarshal(raw, &item); err != nil {
		return fmt.Errorf("decode task: %w", err)
	}
	printKeyValues(out, [][2]string{
		{"key", item.Key},
		{"id", item.ID},
		{"title", item.Title},
		{"status", item.Status},
		{"priority", item.Priority},
		{"milestone", item.MilestoneType},
		{
			"assignee",
			dashIfEmpty(item.Assignee.Name) + " (" + item.Assignee.ID + ")",
		},
		{
			"executed by",
			dashIfEmpty(
				item.ExecutedBy.Name,
			) + " (" + item.ExecutedBy.Kind + " " + item.ExecutedBy.ID + ")",
		},
		{"ready to run", taskReadiness(item)},
		{"conversation", item.ConversationID},
		{"labels", dashIfEmpty(strings.Join(item.Labels, ", "))},
		{"due", stringOrDash(item.DueDate)},
		{"run at", stringOrDash(item.ScheduledAt)},
		{"assignment", item.AssignmentStatus},
		{"blocked by", strconv.FormatInt(item.BlockedByCount, 10)},
		{"dependencies resolved", strconv.FormatBool(item.DependenciesResolved)},
		{"revision", strconv.FormatInt(item.Revision, 10)},
	})
	if strings.TrimSpace(item.Description) != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, item.Description)
	}
	return nil
}

func runTaskGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	reference, err := taskReference(client, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("tasks", reference), &raw); err != nil {
		return relayRunScopeRefusal(err)
	}
	return printResult(cmd, raw, func(out io.Writer) error { return printTaskDetail(out, raw) })
}

// taskReadiness says whether the Task can start on its assignee's computer,
// in the assignee's own words, without naming that computer to anybody else
// (ADR 0014).
func taskReadiness(item taskView) string {
	if item.Execution.AssigneeReady {
		return "yes"
	}
	reasons := map[string]string{
		"not_bound":           "the assignee has not set the agent up on a computer of theirs",
		"runtime_offline":     "the assignee's computer is offline",
		"runtime_unusable":    "the assignee's computer cannot run it",
		"runtime_signed_out":  "the assignee is signed out of the provider",
		"agent_archived":      "the agent is archived",
		"assignee_inactive":   "the assignee is no longer an active member",
		"assignee_cannot_run": "the assignee may not run tasks in this work",
	}
	if item.Execution.UnreadyReason == nil {
		return "no"
	}
	if reason, known := reasons[*item.Execution.UnreadyReason]; known {
		return "no — " + reason
	}
	return "no — " + *item.Execution.UnreadyReason
}

// executedByFromFlags settles who does the work of a new Task: the named
// Agent or — in Agent mode with neither flag — the Agent running this
// session, which is the one that would pick the work up. Nil leaves it to
// the server, whose default is the assignee doing it themselves.
func executedByFromFlags(cmd *cobra.Command, client *apiClient) (map[string]string, error) {
	agentFlag, _ := cmd.Flags().GetString("executed-by-agent")
	agentFlag = strings.TrimSpace(agentFlag)
	self, _ := cmd.Flags().GetBool("executed-by-self")
	if agentFlag != "" && self {
		return nil, usagef("pass only one of --executed-by-agent or --executed-by-self")
	}
	switch {
	case agentFlag != "":
		agentID, err := uuidArg("--executed-by-agent", agentFlag)
		if err != nil {
			return nil, err
		}
		return map[string]string{"kind": "agent", "id": agentID}, nil
	case !self && client.context.mode == modeAgent && client.context.run.AgentID != "":
		return map[string]string{"kind": "agent", "id": client.context.run.AgentID}, nil
	}
	return nil, nil
}

// taskAssigneeUserID answers who will hold the Task after this update: the
// assignee the same body sets, else the one it already has.
func taskAssigneeUserID(
	ctx context.Context,
	cmd *cobra.Command,
	client *apiClient,
	body map[string]any,
	path string,
) (string, error) {
	if assignee, ok := body["assignee"].(map[string]string); ok {
		return assignee["id"], nil
	}
	var current taskView
	if err := client.get(ctx, path, &current); err != nil {
		return "", err
	}
	if current.Assignee.ID == "" {
		return "", fmt.Errorf("read the task assignee")
	}
	return current.Assignee.ID, nil
}

func runTaskCreate(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	flag := func(name string) string { value, _ := cmd.Flags().GetString(name); return strings.TrimSpace(value) }
	description, _, err := contentFlag(cmd, "description")
	if err != nil {
		return err
	}
	// A Task Run files follow-up work in its own Project, the only one it
	// can look up.
	projectValue := flag("project")
	if projectValue == "" && client.context.mode == modeAgent {
		projectValue = client.context.run.ProjectID
	}
	if projectValue == "" {
		return usagef("--project is required: name the Project id or key")
	}
	body := map[string]any{
		"title":         flag("title"),
		"milestoneType": flag("milestone"),
	}
	runAt, expression, zone := flag("run-at"), flag("cron"), flag("time-zone")
	if runAt != "" && expression != "" {
		return usagef("pass only one of --run-at or --cron")
	}
	switch {
	case runAt != "":
		at, err := time.Parse(time.RFC3339, runAt)
		if err != nil || at.Second() != 0 || at.Nanosecond() != 0 {
			return usagef("--run-at must be an RFC3339 timestamp with whole-minute precision")
		}
		if zone != "" {
			return usagef("--time-zone is for --cron; --run-at already includes its offset")
		}
		body["schedule"] = map[string]any{"type": "once", "runAt": at.UTC().Format(time.RFC3339)}
	case expression != "":
		if zone == "" {
			return usagef("--cron requires --time-zone")
		}
		body["schedule"] = map[string]any{"type": "cron", "expression": expression, "timeZone": zone}
	case zone != "":
		return usagef("--time-zone requires --cron")
	}
	// The assignee is always a member (ADR 0014). Unnamed, the server makes
	// it the caller: the signed-in member, or in Agent mode the member the
	// Run works for, whose daemon session carries the request. Naming a
	// colleague needs tasks.assign, which the server checks; a Run never
	// names one, since the work it files is its member's (D6).
	if assigneeUserID := flag("assignee-user"); assigneeUserID != "" {
		if client.context.mode == modeAgent && flag("run-at") == "" && flag("cron") == "" {
			return usagef(
				"--assignee-user is not available inside a Run: the task belongs to the member the Run works for",
			)
		}
		if _, err := uuidArg("--assignee-user", assigneeUserID); err != nil {
			return err
		}
		body["assignee"] = map[string]string{"kind": "user", "id": assigneeUserID}
	}
	executedBy, err := executedByFromFlags(cmd, client)
	if err != nil {
		return err
	}
	if executedBy != nil {
		body["executedBy"] = executedBy
	}
	ctx, cancel := apiContext()
	defer cancel()
	project, err := resolveProject(ctx, client, projectValue)
	if err != nil {
		return err
	}
	body["projectId"] = project.ID
	if description != "" {
		body["description"] = description
	}
	for flagName, field := range map[string]string{"status": "status", "priority": "priority", "due-date": "dueDate", "start-date": "startDate"} {
		if value := flag(flagName); value != "" {
			body[field] = value
		}
	}
	if labels := csvFlag(cmd, "labels"); len(labels) != 0 {
		body["labels"] = labels
	}
	// Without --key the server mints {prefix}-{number} from the number it
	// allocates, so the key and the label on the board are one task; a
	// client-side guess would race the allocation.
	if key := flag("key"); key != "" {
		body["key"] = key
	}
	var raw json.RawMessage
	if err := client.post(ctx, client.companyPath("tasks"), body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, func(out io.Writer) error { return printTaskDetail(out, raw) })
}

func runTaskUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	taskID, err := resolveTaskID(ctx, client, args)
	if err != nil {
		return err
	}
	path := client.companyPath("tasks", taskID)
	body := map[string]any{}
	flag := func(name string) string { value, _ := cmd.Flags().GetString(name); return value }
	for flagName, field := range map[string]string{
		"title": "title", "status": "status", "priority": "priority",
		"milestone": "milestoneType",
	} {
		if cmd.Flags().Changed(flagName) {
			body[field] = flag(flagName)
		}
	}
	if description, given, err := contentFlag(cmd, "description"); err != nil {
		return err
	} else if given {
		body["description"] = description
	}
	if cmd.Flags().Changed("labels") {
		labels := csvFlag(cmd, "labels")
		if labels == nil {
			labels = []string{}
		}
		body["labels"] = labels
	}
	for flagName, field := range map[string]string{"due-date": "dueDate", "start-date": "startDate"} {
		if cmd.Flags().Changed(flagName) {
			if value := strings.TrimSpace(flag(flagName)); value == "" {
				body[field] = nil
			} else {
				body[field] = value
			}
		}
	}
	if value := strings.TrimSpace(flag("assignee-user")); value != "" {
		userID, err := uuidArg("--assignee-user", value)
		if err != nil {
			return err
		}
		body["assignee"] = map[string]string{"kind": "user", "id": userID}
	}
	switch {
	case cmd.Flags().Changed("executed-by-agent") && cmd.Flags().Changed("executed-by-self"):
		return usagef("pass only one of --executed-by-agent or --executed-by-self")
	case cmd.Flags().Changed("executed-by-agent"):
		agentID, err := uuidArg("--executed-by-agent", strings.TrimSpace(flag("executed-by-agent")))
		if err != nil {
			return err
		}
		body["executedBy"] = map[string]string{"kind": "agent", "id": agentID}
	case cmd.Flags().Changed("executed-by-self"):
		// Whoever ends up holding the Task does the work: the assignee this
		// same write sets, or the one it already has.
		assigneeID, err := taskAssigneeUserID(ctx, cmd, client, body, path)
		if err != nil {
			return err
		}
		body["executedBy"] = map[string]string{"kind": "user", "id": assigneeID}
	}
	if confirm, _ := cmd.Flags().GetBool("confirm-handoff"); confirm {
		body["confirmHandoff"] = true
	}
	if len(body) == 0 {
		return usagef("nothing to update: pass at least one field flag")
	}
	revision, err := resolveRevision(cmd, ctx, client, path, "")
	if err != nil {
		return err
	}
	body["expectedRevision"] = revision
	var raw json.RawMessage
	if err := client.patch(ctx, path, body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, func(out io.Writer) error { return printTaskDetail(out, raw) })
}

func runTaskStart(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	taskID, err := resolveTaskID(ctx, client, args)
	if err != nil {
		return err
	}
	revision, err := resolveRevision(cmd, ctx, client, client.companyPath("tasks", taskID), "")
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := client.post(
		ctx,
		client.companyPath("tasks", taskID, "runs"),
		map[string]any{"expectedRevision": revision},
		&raw,
	); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runTaskDependencies(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	reference, err := taskReference(client, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("tasks", reference, "dependencies"), &raw); err != nil {
		return relayRunScopeRefusal(err)
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	// The wire shape is task.DependencySet (docs/api/tasks.md): both
	// directions, each edge embedding the other task. A prerequisite is
	// resolved once it is done; a dependent has no such state to show.
	var set struct {
		BlockedBy []struct {
			Task taskView `json:"task"`
		} `json:"blockedBy"`
		Blocks []struct {
			Task taskView `json:"task"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("decode task dependencies: %w", err)
	}
	rows := make([][]string, 0, len(set.BlockedBy)+len(set.Blocks))
	for _, edge := range set.BlockedBy {
		prerequisite := edge.Task
		resolved := strconv.FormatBool(prerequisite.Status == "done")
		rows = append(rows, []string{
			"blocked by", prerequisite.Key, truncate(prerequisite.Title, 48),
			prerequisite.Status, resolved, prerequisite.ID,
		})
	}
	for _, edge := range set.Blocks {
		dependent := edge.Task
		rows = append(rows, []string{
			"blocks", dependent.Key, truncate(dependent.Title, 48), dependent.Status, "-", dependent.ID,
		})
	}
	printTable(cmd.OutOrStdout(), []string{"DIRECTION", "KEY", "TITLE", "STATUS", "RESOLVED", "ID"}, rows)
	return nil
}

func runTaskWorkspace(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	taskID, err := resolveTaskID(ctx, client, args)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("tasks", taskID, "workspace"), &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runTaskMessages(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	taskID, err := resolveTaskID(ctx, client, args)
	if err != nil {
		return err
	}
	afterSeq, _ := cmd.Flags().GetInt64("after-seq")
	limit, _ := cmd.Flags().GetInt("limit")
	path := messagesPath("tasks", taskID) + messageQuery(afterSeq, 0, limit)
	var raw json.RawMessage
	if err := client.get(ctx, path, &raw); err != nil {
		return err
	}
	return printMessages(cmd, raw)
}

func runTaskSend(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
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
	ctx, cancel := apiContext()
	defer cancel()
	taskID, err := resolveTaskID(ctx, client, args)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	body := map[string]any{
		"requestKey": client.bodyRequestKey("POST", messagesPath("tasks", taskID), []byte(content)),
		"content":    content,
	}
	if err := client.post(ctx, messagesPath("tasks", taskID), body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runTaskFail(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if client.context.mode != modeAgent {
		return &modeError{msg: "`zen task fail` is proven by a Run and only works in Agent mode; " +
			"an operator changes a task with `zen task update`"}
	}
	if client.context.run.TaskID == "" {
		return &modeError{msg: "this Run belongs to a direct conversation, not a task"}
	}
	reason, given, err := contentFlag(cmd, "reason")
	if err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if !given || reason == "" {
		return usagef("--reason is required")
	}
	if len([]rune(reason)) > 2000 {
		return usagef("--reason must be at most 2000 characters")
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.post(
		ctx,
		client.companyPath("task-failures"),
		map[string]any{"reason": reason},
		&raw,
	); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}
