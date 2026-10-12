package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

func newAnalyticsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "analytics", Short: "Read usage analytics"}
	for _, child := range []*cobra.Command{
		newAnalyticsSummaryCmd(), newAnalyticsBreakdownCmd(), newAnalyticsFailuresCmd(),
	} {
		cmd.AddCommand(child)
	}
	return cmd
}

func analyticsFlags(cmd *cobra.Command) {
	cmd.Flags().String("scope", "mine", "analytics scope: mine or company")
	cmd.Flags().Int("days", 30, "number of calendar days (1-367)")
	cmd.Flags().String("start-at", "", "explicit RFC3339 start at local midnight")
	cmd.Flags().String("end-at", "", "explicit RFC3339 end at local midnight")
	cmd.Flags().String("time-zone", "UTC", "IANA time zone")
	cmd.Flags().String("provider", "", "provider filter: claude or codex")
	cmd.Flags().String("model", "", "exact model filter")
	cmd.Flags().String("agent-id", "", "Agent UUID filter")
	cmd.Flags().String("member-id", "", "member UUID filter (company scope)")
	cmd.Flags().String("project-id", "", "Project UUID filter")
	cmd.Flags().String("task-id", "", "Task UUID filter")
	cmd.Flags().String("runtime-id", "", "Runtime UUID filter")
	cmd.Flags().String("run-type", "", "run type filter")
	cmd.Flags().Bool("json", false, "print JSON (same as --output json)")
}

func newAnalyticsSummaryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "summary", Short: "Show analytics summary", RunE: runAnalyticsSummary}
	analyticsFlags(cmd)
	return cmd
}

func newAnalyticsBreakdownCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "breakdown", Short: "Show analytics dimension breakdown", RunE: runAnalyticsBreakdown}
	analyticsFlags(cmd)
	cmd.Flags().String("by", "agent", "dimension: agent, model, member, project, task, run_type, runtime")
	cmd.Flags().String("sort", "tokens", "sort: tokens, cost, runs, run_time, failures")
	cmd.Flags().Int("limit", 25, "rows to return (1-100)")
	cmd.Flags().String("cursor", "", "continuation cursor")
	return cmd
}

func newAnalyticsFailuresCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "failures", Short: "Show analytics failures", RunE: runAnalyticsFailures}
	analyticsFlags(cmd)
	return cmd
}

func runAnalyticsSummary(cmd *cobra.Command, _ []string) error {
	return runAnalyticsGet(cmd, "summary", nil, func(out []byte) error {
		var report struct {
			Scope         string               `json:"scope"`
			PeriodStartAt string               `json:"periodStartAt"`
			PeriodEndAt   string               `json:"periodEndAt"`
			Current       analyticsMetricsView `json:"current"`
			Comparison    analyticsMetricsView `json:"comparison"`
		}
		if err := json.Unmarshal(out, &report); err != nil {
			return err
		}
		printKeyValues(cmd.OutOrStdout(), [][2]string{
			{"Scope", report.Scope}, {"Window", report.PeriodStartAt + " → " + report.PeriodEndAt},
			{"Runs", report.Current.Runs.Total}, {"Completed", report.Current.Runs.Completed},
			{"Failed", report.Current.Runs.Failed}, {"Input tokens", report.Current.Tokens.Input},
			{"Output tokens", report.Current.Tokens.Output}, {"Reported USD ticks", dashIfEmpty(pointerValue(report.Current.Cost.ReportedUsdTicks))},
			{"Estimated USD ticks", dashIfEmpty(pointerValue(report.Current.Cost.EstimatedUsdTicks))},
		})
		return nil
	})
}

func runAnalyticsBreakdown(cmd *cobra.Command, _ []string) error {
	by, _ := cmd.Flags().GetString("by")
	sort, _ := cmd.Flags().GetString("sort")
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > 100 {
		return usagef("--limit must be between 1 and 100")
	}
	query := func(values url.Values) {
		values.Set("by", by)
		values.Set("sort", sort)
		values.Set("limit", fmt.Sprint(limit))
		if cursor, _ := cmd.Flags().GetString("cursor"); cursor != "" {
			values.Set("cursor", cursor)
		}
	}
	return runAnalyticsGet(cmd, "breakdown", query, func(out []byte) error {
		var report struct {
			Rows []struct {
				Key struct {
					Label string `json:"label"`
				} `json:"key"`
				Runs struct {
					Total  string `json:"total"`
					Failed string `json:"failed"`
				} `json:"runs"`
				Tokens struct {
					Input  string `json:"input"`
					Output string `json:"output"`
				} `json:"tokens"`
				Cost struct {
					Reported  *string `json:"reportedUsdTicks"`
					Estimated *string `json:"estimatedUsdTicks"`
				} `json:"cost"`
			} `json:"rows"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(out, &report); err != nil {
			return err
		}
		rows := make([][]string, 0, len(report.Rows))
		for _, row := range report.Rows {
			rows = append(rows, []string{row.Key.Label, row.Runs.Total, row.Runs.Failed, row.Tokens.Input, row.Tokens.Output, dashIfEmpty(pointerValue(row.Cost.Reported)), dashIfEmpty(pointerValue(row.Cost.Estimated))})
		}
		printTable(cmd.OutOrStdout(), []string{"NAME", "RUNS", "FAILED", "INPUT", "OUTPUT", "REPORTED TICKS", "ESTIMATED TICKS"}, rows)
		noteCursor(cmd, report.NextCursor)
		return nil
	})
}

func runAnalyticsFailures(cmd *cobra.Command, _ []string) error {
	return runAnalyticsGet(cmd, "failures", nil, func(out []byte) error {
		var report struct {
			Succeeded string            `json:"succeeded"`
			ByClass   map[string]string `json:"byClass"`
			ByCode    map[string]string `json:"byCode"`
		}
		if err := json.Unmarshal(out, &report); err != nil {
			return err
		}
		rows := make([][]string, 0, len(report.ByClass))
		for class, count := range report.ByClass {
			rows = append(rows, []string{class, count})
		}
		printKeyValues(cmd.OutOrStdout(), [][2]string{{"Succeeded", report.Succeeded}})
		printTable(cmd.OutOrStdout(), []string{"FAILURE CLASS", "COUNT"}, rows)
		return nil
	})
}

type analyticsMetricsView struct {
	Tokens struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	} `json:"tokens"`
	Cost struct {
		ReportedUsdTicks  *string `json:"reportedUsdTicks"`
		EstimatedUsdTicks *string `json:"estimatedUsdTicks"`
	} `json:"cost"`
	Runs struct {
		Total     string `json:"total"`
		Completed string `json:"completed"`
		Failed    string `json:"failed"`
	} `json:"runs"`
}

func runAnalyticsGet(cmd *cobra.Command, endpoint string, extra func(url.Values), table func([]byte) error) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	query, err := analyticsQueryValues(cmd)
	if err != nil {
		return err
	}
	if extra != nil {
		extra(query)
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("analytics", endpoint)+"?"+query.Encode(), &raw); err != nil {
		return fmt.Errorf("get analytics %s: %w", endpoint, err)
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	return table(raw)
}

func analyticsQueryValues(cmd *cobra.Command) (url.Values, error) {
	scope, _ := cmd.Flags().GetString("scope")
	if scope != "mine" && scope != "company" {
		return nil, usagef("--scope must be mine or company")
	}
	zone, _ := cmd.Flags().GetString("time-zone")
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return nil, usagef("--time-zone must be an IANA zone name")
	}
	days, _ := cmd.Flags().GetInt("days")
	if days < 1 || days > 367 {
		return nil, usagef("--days must be between 1 and 367")
	}
	today := time.Now().In(loc)
	end := time.Date(today.Year(), today.Month(), today.Day()+1, 0, 0, 0, 0, loc)
	start := end.AddDate(0, 0, -days)
	if value, _ := cmd.Flags().GetString("start-at"); value != "" {
		start, err = time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, usagef("--start-at must be RFC3339")
		}
	}
	if value, _ := cmd.Flags().GetString("end-at"); value != "" {
		end, err = time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, usagef("--end-at must be RFC3339")
		}
	}
	values := url.Values{"scope": {scope}, "start_at": {start.UTC().Format(time.RFC3339Nano)}, "end_at": {end.UTC().Format(time.RFC3339Nano)}, "time_zone": {zone}}
	for _, flag := range []struct{ name, query string }{{"provider", "provider"}, {"model", "model"}, {"agent-id", "agent_id"}, {"member-id", "member_id"}, {"project-id", "project_id"}, {"task-id", "task_id"}, {"runtime-id", "runtime_id"}, {"run-type", "run_type"}} {
		if value, _ := cmd.Flags().GetString(flag.name); value != "" {
			values.Set(flag.query, value)
		}
	}
	return values, nil
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
