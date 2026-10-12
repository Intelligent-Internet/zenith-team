package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Inspect and control your schedules",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List your schedules",
		Args:  cobra.NoArgs,
		RunE:  runScheduleList,
	}
	get := &cobra.Command{
		Use:   "get <schedule-id>",
		Short: "Show one schedule",
		Args:  cobra.ExactArgs(1),
		RunE:  runScheduleGet,
	}
	preview := &cobra.Command{
		Use:   "preview <schedule-id>",
		Short: "Review the current definition, connections and upcoming times before resuming",
		Args:  cobra.ExactArgs(1),
		RunE:  runSchedulePreview,
	}
	runs := &cobra.Command{
		Use:   "runs <schedule-id>",
		Short: "List a schedule's firings",
		Args:  cobra.ExactArgs(1),
		RunE:  runScheduleRuns,
	}
	runNow := &cobra.Command{
		Use:   "run-now <schedule-id>",
		Short: "Fire a schedule immediately",
		Args:  cobra.ExactArgs(1),
		RunE:  runScheduleRunNow,
	}
	cmd.AddCommand(list, get, preview, runs, runNow)
	for _, operation := range []string{"pause", "resume", "cancel"} {
		operationCmd := &cobra.Command{
			Use:   operation + " <schedule-id>",
			Short: operation + " a schedule",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runScheduleLifecycle(cmd, args, operation)
			},
		}
		addRevisionFlag(operationCmd)
		if operation == "resume" {
			operationCmd.Flags().
				String("review-digest", "", "digest reviewed with zen schedule preview (required)")
		}
		cmd.AddCommand(operationCmd)
	}
	return cmd
}

func runSchedulePreview(cmd *cobra.Command, args []string) error {
	client, scheduleID, err := scheduleTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var definition map[string]json.RawMessage
	if err := client.get(ctx, client.companyPath("schedules", scheduleID), &definition); err != nil {
		return err
	}
	var sources []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(definition["sources"], &sources); err != nil {
		return fmt.Errorf("decode schedule sources: %w", err)
	}
	sourceIDs := make([]string, len(sources))
	for index, source := range sources {
		sourceIDs[index] = source.ID
	}
	body := map[string]any{"schemaVersion": 1, "sourceIds": sourceIDs}
	for _, field := range []string{"agentId", "name", "instructions", "presetKey", "trigger"} {
		body[field] = definition[field]
	}
	var preview json.RawMessage
	if err := client.post(ctx, client.companyPath("schedules", "preview"), body, &preview); err != nil {
		return err
	}
	result, err := json.Marshal(map[string]any{"schedule": definition, "preview": preview})
	if err != nil {
		return fmt.Errorf("encode schedule review: %w", err)
	}
	return printResult(cmd, result, nil)
}

func runScheduleList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("schedules"), &raw); err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	// The page is SchedulePage (docs/api/spec/schedules.yaml): the list
	// travels under `schedules`, and each row names its next and last run.
	var page struct {
		Schedules []struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Status    string  `json:"status"`
			AgentID   string  `json:"agentId"`
			NextRunAt *string `json:"nextRunAt"`
			LastRunAt *string `json:"lastRunAt"`
		} `json:"schedules"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode schedules: %w", err)
	}
	rows := make([][]string, 0, len(page.Schedules))
	for _, item := range page.Schedules {
		rows = append(rows, []string{
			item.ID, truncate(item.Name, 40), item.Status, shortID(item.AgentID),
			stringOrDash(item.NextRunAt), stringOrDash(item.LastRunAt),
		})
	}
	printTable(cmd.OutOrStdout(), []string{"ID", "NAME", "STATUS", "AGENT", "NEXT", "LAST"}, rows)
	return nil
}

func scheduleTarget(cmd *cobra.Command, args []string) (*apiClient, string, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	scheduleID, err := uuidArg("schedule id", args[0])
	if err != nil {
		return nil, "", err
	}
	return client, scheduleID, nil
}

func runScheduleGet(cmd *cobra.Command, args []string) error {
	client, scheduleID, err := scheduleTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("schedules", scheduleID), &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runScheduleRuns(cmd *cobra.Command, args []string) error {
	client, scheduleID, err := scheduleTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("schedules", scheduleID, "runs"), &raw); err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	return printJSON(cmd.OutOrStdout(), raw)
}

func runScheduleRunNow(cmd *cobra.Command, args []string) error {
	client, scheduleID, err := scheduleTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	path := client.companyPath("schedules", scheduleID, "runs")
	body := map[string]any{"schemaVersion": 1, "requestKey": client.bodyRequestKey("POST", path, nil)}
	var raw json.RawMessage
	if err := client.post(ctx, path, body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runScheduleLifecycle(cmd *cobra.Command, args []string, operation string) error {
	var digest string
	if operation == "resume" {
		digest, _ = cmd.Flags().GetString("review-digest")
		if digest == "" {
			return fmt.Errorf(
				"review with zen schedule preview %s, then resume with --review-digest and --revision from that review",
				args[0],
			)
		}
	}
	client, scheduleID, err := scheduleTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	path := client.companyPath("schedules", scheduleID)
	revision, err := resolveRevision(cmd, ctx, client, path, "")
	if err != nil {
		return err
	}
	var raw json.RawMessage
	body := map[string]any{"schemaVersion": 1, "expectedRevision": revision}
	if operation == "resume" {
		body["reviewDigest"] = digest
	}
	if err := client.post(ctx, path+"/"+operation, body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}
