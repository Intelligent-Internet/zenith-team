package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	daemonpkg "github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

func newMigrateRunHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate-run-history",
		Short: "Convert retained workspace Run identities from a database mapping (daemon stopped)",
		Args:  cobra.NoArgs,
		RunE:  runMigrateRunHistory,
	}
	flags := cmd.Flags()
	flags.String("mapping", "", "JSON object mapping old decimal Run IDs to database UUIDs")
	flags.String("daemon-id", "", "existing daemon identity that authenticated the sidecars")
	flags.String("workspaces-root", "", "existing daemon workspace root")
	flags.Int("health-port", daemonHealthPort(), "daemon health port to hold while offline")
	flags.Bool("apply", false, "apply the reviewed conversion; otherwise print the proposed changes")
	for _, name := range []string{"mapping", "daemon-id", "workspaces-root"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func runMigrateRunHistory(cmd *cobra.Command, _ []string) error {
	mappingPath, _ := cmd.Flags().GetString("mapping")
	daemonID, _ := cmd.Flags().GetString("daemon-id")
	root, _ := cmd.Flags().GetString("workspaces-root")
	port, _ := cmd.Flags().GetInt("health-port")
	apply, _ := cmd.Flags().GetBool("apply")
	if daemonID == "" || port < 1 || port > 65535 {
		return errors.New("existing daemon identity and health port are required")
	}
	root, err := daemonpkg.ResolveWorkspacesRoot(root)
	if err != nil {
		return err
	}
	file, err := os.Open(mappingPath)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<20))
	var mappings map[string]uuid.UUID
	if err := decoder.Decode(&mappings); err != nil {
		return fmt.Errorf("read Run identity mapping: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Run identity mapping contains trailing data")
	}
	// The daemon reserves this same port before accepting execution. Holding it
	// closes the check/start race while managed Git mutations hold their file lock.
	lease, err := (&net.ListenConfig{}).Listen(cmd.Context(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("stop the daemon before workspace migration: %w", err)
	}
	defer lease.Close()
	changes, err := daemonpkg.MigrateWorkspaceRunIdentities(cmd.Context(), root, daemonID, mappings, apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(changes)
}
