package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"boardly.dev/boardly/server/pkg/protocol"
)

func newRuntimeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runtime",
		Short: "Work with the machines (runtimes) registered in the company",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List runtimes registered in the company",
		Args:  cobra.NoArgs,
		RunE:  runRuntimeList,
	}
	get := &cobra.Command{
		Use:   "get <runtime-id>",
		Short: "Show one runtime",
		Args:  cobra.ExactArgs(1),
		RunE:  runRuntimeGet,
	}
	rename := &cobra.Command{
		Use:   "rename <runtime-id> <name>",
		Short: "Give a runtime a custom name (an empty name restores the daemon's own)",
		Args:  cobra.ExactArgs(2),
		RunE:  runRuntimeRename,
	}
	rename.Flags().Bool("apply-to-machine", false, "rename every runtime of the same machine")
	remove := &cobra.Command{
		Use:   "remove <runtime-id> [--target <runtime-id>]",
		Short: "Remove a runtime and move its agents to another runtime",
		Args:  cobra.ExactArgs(1),
		RunE:  runRuntimeRemove,
	}
	remove.Flags().
		String("target", "", "another runtime of yours, same provider, to move your agents to; without it they stop running here and your queued runs are cancelled")
	operatorOnly(rename)
	operatorOnly(remove)
	cmd.AddCommand(list, get, rename, remove)
	return cmd
}

type runtimeView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CustomName *string `json:"customName"`
	Provider   string  `json:"provider"`
	Status     string  `json:"status"`
	Owner      *struct {
		Name string `json:"name"`
	} `json:"owner"`
	LastSeenAt *string `json:"lastSeenAt"`
	// SignIn is the daemon's last sign-in verdict for the provider CLI, or
	// nil when no registration has carried one.
	SignIn          *protocol.RuntimeSignIn `json:"signIn"`
	ProviderVersion string                  `json:"providerVersion"`
	CLIVersion      string                  `json:"cliVersion"`
	LaunchedBy      string                  `json:"launchedBy"`
	DeviceInfo      string                  `json:"deviceInfo"`
}

// signInLine is the sign-in verdict with the daemon's sentence and the
// moment it was checked, or a dash when no registration has carried one.
func (v runtimeView) signInLine() string {
	if v.SignIn == nil {
		return "-"
	}
	line := v.SignIn.State
	if v.SignIn.Detail != "" {
		line += " — " + v.SignIn.Detail
	}
	if v.SignIn.CheckedAt != "" {
		line += " (checked " + v.SignIn.CheckedAt + ")"
	}
	return line
}

// displayName is what somebody renamed the machine to when set, else the
// daemon's own name — the roster and every picker show one name.
func (v runtimeView) displayName() string {
	if v.CustomName != nil && strings.TrimSpace(*v.CustomName) != "" {
		return *v.CustomName
	}
	return v.Name
}

func runRuntimeList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("runtimes"), &raw); err != nil {
		return fmt.Errorf("list runtimes: %w", err)
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	// The server's `{items:[Runtime]}` page (docs/api/runs.md), decoded into a
	// local shape on purpose: the CLI must never link server/internal/agent.
	var page struct {
		Items []runtimeView `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode runtimes: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, runtime := range page.Items {
		var ownerName *string
		if runtime.Owner != nil {
			ownerName = &runtime.Owner.Name
		}
		var signIn *string
		if runtime.SignIn != nil {
			signIn = &runtime.SignIn.State
		}
		rows = append(rows, []string{
			runtime.ID, runtime.displayName(), runtime.Provider, runtime.Status,
			stringOrDash(signIn), stringOrDash(ownerName), stringOrDash(runtime.LastSeenAt),
		})
	}
	printTable(
		cmd.OutOrStdout(),
		[]string{"ID", "NAME", "PROVIDER", "STATUS", "SIGN_IN", "OWNER", "LAST_SEEN"},
		rows,
	)
	return nil
}

func runtimeTarget(cmd *cobra.Command, args []string) (*apiClient, string, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	runtimeID, err := uuidArg("runtime id", args[0])
	if err != nil {
		return nil, "", err
	}
	return client, runtimeID, nil
}

func runRuntimeGet(cmd *cobra.Command, args []string) error {
	client, runtimeID, err := runtimeTarget(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("runtimes", runtimeID), &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, func(out io.Writer) error {
		var view runtimeView
		if err := json.Unmarshal(raw, &view); err != nil {
			return fmt.Errorf("decode runtime: %w", err)
		}
		printKeyValues(out, [][2]string{
			{"id", view.ID},
			{"name", view.displayName()},
			{"provider", view.Provider + " " + dashIfEmpty(view.ProviderVersion)},
			{"status", view.Status},
			{"sign-in", view.signInLine()},
			{"zen version", dashIfEmpty(view.CLIVersion)},
			{"launched by", dashIfEmpty(view.LaunchedBy)},
			{"device", dashIfEmpty(view.DeviceInfo)},
			{"last seen", stringOrDash(view.LastSeenAt)},
		})
		return nil
	})
}

func runRuntimeRename(cmd *cobra.Command, args []string) error {
	client, runtimeID, err := runtimeTarget(cmd, args)
	if err != nil {
		return err
	}
	applyToMachine, _ := cmd.Flags().GetBool("apply-to-machine")
	var customName *string
	if name := strings.TrimSpace(args[1]); name != "" {
		customName = &name
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	body := map[string]any{"customName": customName, "applyToMachine": applyToMachine}
	if err := client.patch(ctx, client.companyPath("runtimes", runtimeID), body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runRuntimeRemove(cmd *cobra.Command, args []string) error {
	client, runtimeID, err := runtimeTarget(cmd, args)
	if err != nil {
		return err
	}
	// The body names where this machine's agents go, and nothing else: the
	// runtime is the caller's own, so there is no plan to agree on with
	// anybody (ADR 0014). Without a target they simply stop running here.
	var target any
	if cmd.Flags().Changed("target") {
		targetFlag, _ := cmd.Flags().GetString("target")
		targetID, err := uuidArg("--target", targetFlag)
		if err != nil {
			return err
		}
		target = targetID
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	body := map[string]any{"targetRuntimeId": target}
	if err := client.post(ctx, client.companyPath("runtimes", runtimeID, "remove"), body, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}
