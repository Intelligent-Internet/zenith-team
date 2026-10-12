package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	daemonpkg "boardly.dev/boardly/server/internal/daemon"
)

func newCompanyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "company",
		Short: "Work with companies and pick the default one",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List the companies the signed-in account belongs to",
		Args:  cobra.NoArgs,
		RunE:  runCompanyList,
	}
	get := &cobra.Command{
		Use:   "get [company-id]",
		Short: "Show one company (the selected one when no id is given)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runCompanyGet,
	}
	use := &cobra.Command{
		Use:   "use <company-id>",
		Short: "Save the default company for later commands",
		Args:  cobra.ExactArgs(1),
		RunE:  runCompanyUse,
	}
	operatorOnly(use)
	cmd.AddCommand(list, get, use)
	return cmd
}

func runCompanyList(cmd *cobra.Command, _ []string) error {
	context := resolveCommandContext()
	if context.mode == modeAgent {
		// A Run is bound to one Company; listing is the operator's view.
		return runCompanyGet(cmd, nil)
	}
	transport, err := newOperatorTransport()
	if err != nil {
		return err
	}
	client := &apiClient{context: context, transport: transport}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, "/companies", &raw); err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	return printCompanyTable(cmd.OutOrStdout(), raw)
}

func printCompanyTable(out io.Writer, raw json.RawMessage) error {
	var page struct {
		Items []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Slug       string `json:"slug"`
			TaskPrefix string `json:"taskPrefix"`
			Role       string `json:"role"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode companies: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(rows, []string{item.ID, item.Name, item.Slug, item.TaskPrefix, item.Role})
	}
	printTable(out, []string{"ID", "NAME", "SLUG", "TASK_PREFIX", "ROLE"}, rows)
	return nil
}

func runCompanyGet(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		if _, err := uuidArg("company id", args[0]); err != nil {
			return err
		}
		_ = cmd.Flags().Set("company", args[0])
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, "/companies/"+client.companyID, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, func(out io.Writer) error {
		var company struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Slug            string `json:"slug"`
			TaskPrefix      string `json:"taskPrefix"`
			Role            string `json:"role"`
			BusinessContext string `json:"businessContext"`
			Revision        int64  `json:"revision"`
		}
		if err := json.Unmarshal(raw, &company); err != nil {
			return fmt.Errorf("decode company: %w", err)
		}
		printKeyValues(out, [][2]string{
			{"id", company.ID},
			{"name", company.Name},
			{"slug", company.Slug},
			{"task prefix", company.TaskPrefix},
			{"role", dashIfEmpty(company.Role)},
			{"revision", fmt.Sprint(company.Revision)},
			{"business context", truncate(company.BusinessContext, 160)},
		})
		return nil
	})
}

func runCompanyUse(cmd *cobra.Command, args []string) error {
	companyID, err := uuidArg("company id", args[0])
	if err != nil {
		return err
	}
	cfg, err := daemonpkg.LoadCLIConfig()
	if err != nil {
		return err
	}
	cfg.CompanyID = companyID
	if err := daemonpkg.SaveCLIConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Default company set to %s\n", companyID)
	return nil
}
