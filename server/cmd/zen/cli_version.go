package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the zen version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := resolveOutput(cmd, outputTable)
			if err != nil {
				return err
			}
			if format == outputJSON {
				return printJSON(cmd.OutOrStdout(), map[string]string{
					"version": version,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				})
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"zen %s (%s, %s/%s)\n",
				version,
				runtime.Version(),
				runtime.GOOS,
				runtime.GOARCH,
			)
			return nil
		},
	}
}
