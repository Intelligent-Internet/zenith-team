package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// agentView mirrors the server's agent object — the one wire type the list,
// the single read and every mutation return. JSON output prints the raw
// payload; the table reads these fields.
type agentView struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Title         string  `json:"title"`
	Provider      string  `json:"provider"`
	Model         *string `json:"model"`
	ThinkingLevel *string `json:"thinkingLevel"`
	ReportsTo     *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"reportsTo"`
	// Binding is how this Agent is set up on the caller's own computer, and
	// nobody else's (ADR 0014). It is null until they set it up.
	Binding *agentBindingView `json:"binding"`
	// RuntimeAvailable and UnavailableReason are the caller's verdict on
	// their own binding, not a fact about the Agent.
	RuntimeAvailable  bool    `json:"runtimeAvailable"`
	UnavailableReason *string `json:"unavailableReason"`
	Instructions      string  `json:"instructions"`
	AvatarKey         *string `json:"avatarKey"`
	AccentColor       *string `json:"accentColor"`
	MaxConcurrentRuns int32   `json:"maxConcurrentRuns"`
	ArchivedAt        *string `json:"archivedAt"`
	Revision          int64   `json:"revision"`
}

// agentBindingView is one member's placement of an Agent on one of their own
// runtimes.
type agentBindingView struct {
	ID      string `json:"id"`
	AgentID string `json:"agentId"`
	Runtime struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Status   string `json:"status"`
	} `json:"runtime"`
	CreatedAt string `json:"createdAt"`
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Work with agents",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List agents in the company",
		RunE:  runAgentList,
	}
	list.Flags().Bool("include-archived", false, "include archived agents")

	get := &cobra.Command{
		Use:   "get <agent-id>",
		Short: "Get agent details",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentGet,
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a new agent",
		RunE:  runAgentCreate,
	}
	create.Flags().String("name", "", "agent name (required)")
	create.Flags().String("title", "", "agent title (required)")
	create.Flags().
		String("provider", "", "provider this agent is written for: claude or codex (required; it cannot be changed later — use `zen agent copy`)")
	create.Flags().
		String("runtime-id", "", "also set the agent up on this runtime of yours (see `zen runtime list`); it must be the same provider")
	create.Flags().String("reports-to", "", "agent id this agent reports to (empty creates a root agent)")
	create.Flags().String("model", "", "model identifier; empty = provider default")
	create.Flags().String("thinking-level", "", "reasoning/effort level for the provider; empty = provider default")
	create.Flags().String("instructions", "", "agent instructions")
	create.Flags().
		String("avatar-key", "", "Fold look: fold-butter|tangerine|coral|orchid|lagoon|pistachio|periwinkle|cocoa; empty = chosen from the agent id")
	create.Flags().String("accent-color", "", "accent color as #RRGGBB; empty = default palette")
	create.Flags().Int32("max-concurrent-runs", 0, "task-lane run cap, 1-10 (default 3)")
	for _, required := range []string{"name", "title", "provider"} {
		_ = create.MarkFlagRequired(required)
	}

	// copy is the CLI half of Duplicate: it reads the source through the same
	// GET every client uses and assembles an ordinary create from its portable
	// fields, so the server sees one create and nothing copy-specific.
	copyCmd := &cobra.Command{
		Use:   "copy <source-agent-id>",
		Short: "Create a new agent from an existing one (optionally on another runtime)",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentCopy,
	}
	copyCmd.Flags().String("name", "", "name for the new agent (default: \"<source name> (copy)\")")
	copyCmd.Flags().String("title", "", "title for the new agent (default: the source title)")
	copyCmd.Flags().
		String("provider", "", "provider for the copy (default: the source provider); another provider drops the copied model and thinking-level unless you pass them")
	copyCmd.Flags().
		String("runtime-id", "", "also set the copy up on this runtime of yours (default: the one the source is set up on, when the provider is unchanged)")
	copyCmd.Flags().
		String("reports-to", "", "agent id the new agent reports to (default: the source's manager; a copy of the root reports to the root)")
	copyCmd.Flags().String("instructions", "", "override the copied instructions")
	copyCmd.Flags().
		String("model", "", "model identifier for the copy; pass an empty string for the runtime default")
	copyCmd.Flags().
		String("thinking-level", "", "reasoning/effort level for the copy; pass an empty string for the runtime default")
	copyCmd.Flags().String("avatar-key", "", "Fold look for the copy (fold-<palette>); pass an empty string to let the id choose")
	copyCmd.Flags().
		String("accent-color", "", "accent color for the copy as #RRGGBB; pass an empty string for the default palette")
	copyCmd.Flags().Int32("max-concurrent-runs", 0, "task-lane run cap, 1-10 (default: the source cap)")

	update := &cobra.Command{
		Use:   "update <agent-id>",
		Short: "Update an agent (only the flags you pass change; the current revision is fetched first)",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentUpdate,
	}
	update.Flags().String("name", "", "new name")
	update.Flags().String("title", "", "new title")
	update.Flags().String("reports-to", "", "new reports-to agent id")
	update.Flags().
		String("model", "", "new model identifier; pass an empty string to clear and use the runtime default")
	update.Flags().String("thinking-level", "", "new reasoning/effort level; pass an empty string to clear")
	update.Flags().String("instructions", "", "new instructions")
	update.Flags().String("avatar-key", "", "new Fold look (fold-<palette>); pass an empty string to clear")
	update.Flags().String("accent-color", "", "new accent color as #RRGGBB; pass an empty string to clear")

	archive := &cobra.Command{
		Use:   "archive <agent-id>",
		Short: "Archive an agent",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentArchive,
	}
	restore := &cobra.Command{
		Use:   "restore <agent-id>",
		Short: "Restore an archived agent",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentRestore,
	}

	usage := &cobra.Command{
		Use:   "usage <agent-id>",
		Short: "Show token usage for an agent",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentUsage,
	}
	usage.Flags().Int("days", 30, "number of days of usage to report (1-365)")
	usage.Flags().String("time-zone", "UTC", "IANA time zone for bucketing")
	usage.Flags().String("bucket", "day", "series bucket: day or month")

	// An Agent is a template; a binding is how one member runs it on one of
	// their own computers (ADR 0014). These three commands only ever touch
	// the caller's own setup.
	bind := &cobra.Command{
		Use:   "bind <agent-id>",
		Short: "Set an agent up on one of your runtimes (or move it to another one)",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentBind,
	}
	bind.Flags().String("runtime-id", "", "your runtime to run this agent on (required; see `zen runtime list`)")
	_ = bind.MarkFlagRequired("runtime-id")

	unbind := &cobra.Command{
		Use:   "unbind <agent-id>",
		Short: "Stop running an agent on your computer (queued runs of yours are cancelled)",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentUnbind,
	}

	bindings := &cobra.Command{
		Use:   "bindings",
		Short: "List the agents you have set up, and where they run",
		RunE:  runAgentBindings,
	}
	bindings.Flags().String("runtime-id", "", "only the agents you run on this runtime")

	cmd.AddCommand(list, get, create, copyCmd, update, archive, restore, usage, bind, unbind, bindings)
	return cmd
}

func runAgentBind(cmd *cobra.Command, args []string) error {
	client, agentID, err := agentCommandTarget(cmd, args)
	if err != nil {
		return err
	}
	runtimeID, _ := cmd.Flags().GetString("runtime-id")
	if _, err := uuidArg("--runtime-id", runtimeID); err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.put(
		ctx,
		client.companyPath("agents", agentID, "binding"),
		map[string]any{"runtimeId": runtimeID},
		&raw,
	); err != nil {
		return fmt.Errorf("set the agent up on your runtime: %w", err)
	}
	return printJSON(cmd.OutOrStdout(), raw)
}

func runAgentUnbind(cmd *cobra.Command, args []string) error {
	client, agentID, err := agentCommandTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	if err := client.delete(
		ctx, client.companyPath("agents", agentID, "binding"), nil, nil,
	); err != nil {
		return fmt.Errorf("remove the agent from your computer: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Agent %s no longer runs on your computer.\n", agentID)
	return nil
}

func runAgentBindings(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	path := client.companyPath("agent-bindings")
	if runtimeID, _ := cmd.Flags().GetString("runtime-id"); runtimeID != "" {
		if _, err := uuidArg("--runtime-id", runtimeID); err != nil {
			return err
		}
		path += "?runtime_id=" + url.QueryEscape(runtimeID)
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, path, &raw); err != nil {
		return fmt.Errorf("list your agent bindings: %w", err)
	}
	out := cmd.OutOrStdout()
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(out, raw)
	}
	var page struct {
		Items []agentBindingView `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode agent bindings: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(rows, []string{
			item.AgentID, item.Runtime.Name, item.Runtime.Provider, item.Runtime.Status,
		})
	}
	printTable(out, []string{"AGENT ID", "RUNTIME", "PROVIDER", "STATUS"}, rows)
	return nil
}

func runAgentList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	// The Agent collection is bounded: it takes no limit or cursor, and its
	// only filters are the snake_case query parameters the API documents.
	path := client.companyPath("agents")
	if includeArchived, _ := cmd.Flags().GetBool("include-archived"); includeArchived {
		path += "?include_archived=true"
	}

	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, path, &raw); err != nil {
		return fmt.Errorf("list agents: %w", err)
	}

	out := cmd.OutOrStdout()
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(out, raw)
	}
	var page struct {
		Items []agentView `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode agents: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, view := range page.Items {
		archived := "no"
		if view.ArchivedAt != nil {
			archived = "yes"
		}
		// "On your computer" is the caller's own setup: the runtime they
		// bound this Agent to, and whether it can run right now.
		yourRuntime := "not set up"
		if view.Binding != nil {
			yourRuntime = view.Binding.Runtime.Name
			if !view.RuntimeAvailable {
				yourRuntime += " (unavailable)"
			}
		}
		// The root Agent reports to nobody: it is the CEO of the reporting tree.
		reportsTo := "(root)"
		if view.ReportsTo != nil {
			reportsTo = view.ReportsTo.Name
		}
		rows = append(rows, []string{
			view.ID, view.Name, view.Title, reportsTo, view.Provider,
			stringOrDash(view.Model), yourRuntime, archived,
		})
	}
	printTable(
		out,
		[]string{"ID", "NAME", "TITLE", "REPORTS TO", "PROVIDER", "MODEL", "YOUR RUNTIME", "ARCHIVED"},
		rows,
	)
	return nil
}

func runAgentGet(cmd *cobra.Command, args []string) error {
	client, agentID, err := agentCommandTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("agents", agentID), &raw); err != nil {
		return fmt.Errorf("get agent: %w", err)
	}
	return printJSON(cmd.OutOrStdout(), raw)
}

func runAgentCreate(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	flag := func(name string) string { value, _ := cmd.Flags().GetString(name); return value }
	provider := flag("provider")
	if provider != "claude" && provider != "codex" {
		return usagef("--provider must be claude or codex (got %q)", provider)
	}
	body := map[string]any{
		"name":          flag("name"),
		"title":         flag("title"),
		"provider":      provider,
		"model":         nullableFlag(cmd, "model"),
		"thinkingLevel": nullableFlag(cmd, "thinking-level"),
	}
	// The runtime is optional: without it the Agent exists as a template
	// nobody runs yet, and `zen agent bind` sets it up later (ADR 0014).
	if runtimeID := flag("runtime-id"); runtimeID != "" {
		if _, err := uuidArg("--runtime-id", runtimeID); err != nil {
			return err
		}
		body["runtimeId"] = runtimeID
	}
	if reportsTo := flag("reports-to"); reportsTo != "" {
		if _, err := uuidArg("--reports-to", reportsTo); err != nil {
			return err
		}
		body["reportsToAgentId"] = reportsTo
	}
	if instructions := flag("instructions"); instructions != "" {
		body["instructions"] = instructions
	}
	for flagName, field := range map[string]string{"avatar-key": "avatarKey", "accent-color": "accentColor"} {
		if value := flag(flagName); value != "" {
			body[field] = value
		}
	}
	if cmd.Flags().Changed("max-concurrent-runs") {
		capValue, err := maxConcurrentRunsFlag(cmd)
		if err != nil {
			return err
		}
		body["maxConcurrentRuns"] = capValue
	}

	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.post(ctx, client.companyPath("agents"), body, &raw); err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	return printJSON(cmd.OutOrStdout(), raw)
}

// runAgentCopy reads the source agent and turns its portable fields into a
// create request. Model and thinking level are one provider's vocabulary, so
// they travel only when the copy keeps the source provider; a copy onto the
// other provider starts from its defaults unless the flags say otherwise.
// Copying is also how an Agent changes provider, since a template's provider
// is fixed (ADR 0014). A copy of the root reports to the root: a company has
// one.
func runAgentCopy(cmd *cobra.Command, args []string) error {
	client, sourceID, err := agentCommandTarget(cmd, args)
	if err != nil {
		return err
	}
	flag := func(name string) string { value, _ := cmd.Flags().GetString(name); return value }

	ctx, cancel := apiContext()
	defer cancel()
	var source agentView
	if err := client.get(ctx, client.companyPath("agents", sourceID), &source); err != nil {
		return fmt.Errorf("get source agent: %w", err)
	}

	provider := source.Provider
	if cmd.Flags().Changed("provider") {
		if provider = flag("provider"); provider != "claude" && provider != "codex" {
			return usagef("--provider must be claude or codex (got %q)", provider)
		}
	}
	sameProvider := provider == source.Provider
	runtimeID := ""
	if sameProvider && source.Binding != nil {
		runtimeID = source.Binding.Runtime.ID
	}
	if cmd.Flags().Changed("runtime-id") {
		if runtimeID, err = uuidArg("--runtime-id", flag("runtime-id")); err != nil {
			return err
		}
	}

	name := source.Name + " (copy)"
	if cmd.Flags().Changed("name") {
		if name = flag("name"); name == "" {
			return usagef("--name must not be empty")
		}
	}
	title := source.Title
	if cmd.Flags().Changed("title") {
		if title = flag("title"); title == "" {
			return usagef("--title must not be empty")
		}
	}
	reportsTo := sourceID
	if source.ReportsTo != nil {
		reportsTo = source.ReportsTo.ID
	}
	if cmd.Flags().Changed("reports-to") {
		if reportsTo, err = uuidArg("--reports-to", flag("reports-to")); err != nil {
			return err
		}
	}

	body := map[string]any{
		"name":             name,
		"title":            title,
		"provider":         provider,
		"reportsToAgentId": reportsTo,
		"instructions":     source.Instructions,
		"model":            nil,
		"thinkingLevel":    nil,
	}
	if runtimeID != "" {
		body["runtimeId"] = runtimeID
	}
	if cmd.Flags().Changed("instructions") {
		body["instructions"] = flag("instructions")
	}
	// The required nullable keys: an explicit flag wins, the source value
	// travels within its own runtime, and a fork starts from the defaults.
	for flagName, field := range map[string]string{"model": "model", "thinking-level": "thinkingLevel"} {
		if cmd.Flags().Changed(flagName) {
			body[field] = nullableFlag(cmd, flagName)
		}
	}
	if sameProvider {
		if !cmd.Flags().Changed("model") && source.Model != nil {
			body["model"] = *source.Model
		}
		if !cmd.Flags().Changed("thinking-level") && source.ThinkingLevel != nil {
			body["thinkingLevel"] = *source.ThinkingLevel
		}
	}
	// Appearance travels with the copy; an explicit empty flag clears it.
	for flagName, field := range map[string]string{"avatar-key": "avatarKey", "accent-color": "accentColor"} {
		if cmd.Flags().Changed(flagName) {
			body[field] = nullableFlag(cmd, flagName)
		}
	}
	if !cmd.Flags().Changed("avatar-key") && source.AvatarKey != nil {
		body["avatarKey"] = *source.AvatarKey
	}
	if !cmd.Flags().Changed("accent-color") && source.AccentColor != nil {
		body["accentColor"] = *source.AccentColor
	}
	if cmd.Flags().Changed("max-concurrent-runs") {
		capValue, err := maxConcurrentRunsFlag(cmd)
		if err != nil {
			return err
		}
		body["maxConcurrentRuns"] = capValue
	} else if source.MaxConcurrentRuns > 0 {
		body["maxConcurrentRuns"] = source.MaxConcurrentRuns
	}

	var raw json.RawMessage
	if err := client.post(ctx, client.companyPath("agents"), body, &raw); err != nil {
		return fmt.Errorf("copy agent: %w", err)
	}
	return printJSON(cmd.OutOrStdout(), raw)
}

// maxConcurrentRunsFlag reads the task-lane cap flag and applies the API's
// 1..10 rule before any request is made, so a typo fails locally.
func maxConcurrentRunsFlag(cmd *cobra.Command) (int32, error) {
	capValue, _ := cmd.Flags().GetInt32("max-concurrent-runs")
	if capValue < 1 || capValue > 10 {
		return 0, usagef("--max-concurrent-runs must be between 1 and 10 (got %d)", capValue)
	}
	return capValue, nil
}

func runAgentUpdate(cmd *cobra.Command, args []string) error {
	client, agentID, err := agentCommandTarget(cmd, args)
	if err != nil {
		return err
	}

	ctx, cancel := apiContext()
	defer cancel()
	revision, err := fetchAgentRevision(ctx, client, agentID)
	if err != nil {
		return err
	}

	body := map[string]any{"expectedRevision": revision}
	for flagName, field := range map[string]string{
		"name":         "name",
		"title":        "title",
		"instructions": "instructions",
		"reports-to":   "reportsToAgentId",
	} {
		if cmd.Flags().Changed(flagName) {
			value, _ := cmd.Flags().GetString(flagName)
			body[field] = value
		}
	}
	// model and thinking-level are nullable: an explicit empty string clears
	// the override so the runtime default applies again.
	if cmd.Flags().Changed("model") {
		body["model"] = nullableFlag(cmd, "model")
	}
	if cmd.Flags().Changed("thinking-level") {
		body["thinkingLevel"] = nullableFlag(cmd, "thinking-level")
	}
	// Appearance is nullable the same way: an empty string clears it.
	if cmd.Flags().Changed("avatar-key") {
		body["avatarKey"] = nullableFlag(cmd, "avatar-key")
	}
	if cmd.Flags().Changed("accent-color") {
		body["accentColor"] = nullableFlag(cmd, "accent-color")
	}
	if len(body) == 1 {
		return usagef("nothing to update: pass at least one field flag")
	}

	var raw json.RawMessage
	if err := client.patch(ctx, client.companyPath("agents", agentID), body, &raw); err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	return printJSON(cmd.OutOrStdout(), raw)
}

func runAgentArchive(cmd *cobra.Command, args []string) error {
	return mutateAgentArchiveState(cmd, args, "archive")
}

func runAgentRestore(cmd *cobra.Command, args []string) error {
	return mutateAgentArchiveState(cmd, args, "restore")
}

func mutateAgentArchiveState(cmd *cobra.Command, args []string, action string) error {
	client, agentID, err := agentCommandTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	revision, err := fetchAgentRevision(ctx, client, agentID)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := client.post(ctx, client.companyPath("agents", agentID, action),
		map[string]any{"expectedRevision": revision}, &raw); err != nil {
		return fmt.Errorf("%s agent: %w", action, err)
	}
	return printJSON(cmd.OutOrStdout(), raw)
}

func runAgentUsage(cmd *cobra.Command, args []string) error {
	client, agentID, err := agentCommandTarget(cmd, args)
	if err != nil {
		return err
	}
	days, _ := cmd.Flags().GetInt("days")
	if days < 1 || days > 365 {
		return usagef("--days must be between 1 and 365")
	}
	timeZone, _ := cmd.Flags().GetString("time-zone")
	// The route rejects the literal "Local" and any name the tz database
	// does not know; checking here turns a typo into a usage error before
	// any request is made.
	location, err := time.LoadLocation(timeZone)
	if err != nil || timeZone == "" || timeZone == "Local" {
		return usagef("--time-zone must be an IANA zone name")
	}
	bucket, _ := cmd.Flags().GetString("bucket")
	if bucket != "day" && bucket != "month" {
		return usagef("--bucket must be day or month")
	}

	// The route takes snake_case names and both bounds must be exact local
	// midnights in the zone (docs/api/activity-and-usage.md). The window
	// ends at the next local midnight so today is included, and it is
	// half-open: start_at inclusive, end_at exclusive.
	today := time.Now().In(location)
	endAt := time.Date(today.Year(), today.Month(), today.Day()+1, 0, 0, 0, 0, location)
	startAt := endAt.AddDate(0, 0, -days)
	query := url.Values{}
	query.Set("start_at", startAt.UTC().Format(time.RFC3339))
	query.Set("end_at", endAt.UTC().Format(time.RFC3339))
	query.Set("time_zone", timeZone)
	query.Set("bucket", bucket)
	path := client.companyPath("agents", agentID, "usage") + "?" + query.Encode()

	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, path, &raw); err != nil {
		return fmt.Errorf("get agent usage: %w", err)
	}

	out := cmd.OutOrStdout()
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(out, raw)
	}
	var report struct {
		Totals struct {
			InputTokens  string `json:"inputTokens"`
			OutputTokens string `json:"outputTokens"`
			RunCount     string `json:"runCount"`
		} `json:"totals"`
		Models []struct {
			Provider             string  `json:"provider"`
			Model                string  `json:"model"`
			InputTokens          string  `json:"inputTokens"`
			OutputTokens         string  `json:"outputTokens"`
			ProviderCostUSDTicks *string `json:"providerCostUsdTicks"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return fmt.Errorf("decode usage report: %w", err)
	}
	rows := make([][]string, 0, len(report.Models))
	for _, entry := range report.Models {
		rows = append(rows, []string{
			entry.Provider, entry.Model, entry.InputTokens, entry.OutputTokens,
			formatCostUSDTicks(entry.ProviderCostUSDTicks),
		})
	}
	printTable(out, []string{"PROVIDER", "MODEL", "INPUT_TOKENS", "OUTPUT_TOKENS", "COST_USD"}, rows)
	fmt.Fprintf(out, "\nruns: %s, input: %s, output: %s\n",
		report.Totals.RunCount, report.Totals.InputTokens, report.Totals.OutputTokens)
	return nil
}

func formatCostUSDTicks(ticks *string) string {
	if ticks == nil {
		return "-"
	}
	value, ok := new(big.Int).SetString(*ticks, 10)
	if !ok || value.Sign() < 0 {
		return *ticks
	}
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(value, big.NewInt(10_000_000_000), fraction)
	fractionText := fraction.String()
	fractionText = strings.Repeat("0", 10-len(fractionText)) + fractionText
	fractionText = strings.TrimRight(fractionText, "0")
	if fractionText == "" {
		return whole.String()
	}
	return whole.String() + "." + fractionText
}

// agentCommandTarget resolves the API client and validates the positional
// agent id shared by every single-agent subcommand.
func agentCommandTarget(cmd *cobra.Command, args []string) (*apiClient, string, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	agentID, err := uuidArg("agent id", args[0])
	if err != nil {
		return nil, "", err
	}
	return client, agentID, nil
}

func fetchAgentRevision(ctx context.Context, client *apiClient, agentID string) (int64, error) {
	var view agentView
	if err := client.get(ctx, client.companyPath("agents", agentID), &view); err != nil {
		return 0, fmt.Errorf("get agent: %w", err)
	}
	return view.Revision, nil
}

// nullableFlag maps an unset or empty string flag to JSON null, which the
// agent API reads as "use the runtime default".
func nullableFlag(cmd *cobra.Command, name string) any {
	value, _ := cmd.Flags().GetString(name)
	if value == "" {
		return nil
	}
	return value
}
