package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	daemonpkg "boardly.dev/boardly/server/internal/daemon"
)

type commerceDevLease interface {
	EnvironmentVariables() []string
	Renew(context.Context) error
	Close(context.Context) error
}

type commerceDevStreams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

var openCommerceDevLease = func(
	ctx context.Context,
	companyID string,
	site daemonpkg.CommerceDevManifestSite,
	ownerPID int,
) (commerceDevLease, error) {
	return daemonpkg.OpenCommerceDevLease(ctx, companyID, site, ownerPID)
}

var runCommerceDevChild = superviseCommerceDevChild

var removeCommerceDevChildHome = os.RemoveAll

func newCommerceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "commerce",
		Short: "Run and inspect Site Commerce integrations",
	}
	dev := &cobra.Command{
		Use:   "dev --manifest boardly-commerce.json -- <command...>",
		Short: "Run a local Site with the current Task's scoped signing lease",
		Args:  cobra.MinimumNArgs(1),
		RunE:  runCommerceDev,
	}
	dev.Flags().String("manifest", "boardly-commerce.json", "path to the Commerce manifest v2")
	cmd.AddCommand(dev)
	return cmd
}

func runCommerceDev(cmd *cobra.Command, args []string) (runErr error) {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manifestPath, _ := cmd.Flags().GetString("manifest")
	site, err := readCommerceDevManifest(manifestPath)
	if err != nil {
		return err
	}
	companyID, err := resolveCompanyID(cmd, resolveCommandContext())
	if err != nil {
		return err
	}
	lease, err := openCommerceDevLease(ctx, companyID, site, os.Getpid())
	if err != nil {
		return fmt.Errorf("open local Commerce signing lease: %w", err)
	}
	childBoardlyHome := ""
	defer func() {
		var cleanupErrors []error
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := lease.Close(cleanupContext); err != nil {
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("revoke local Commerce signing lease: %w", err),
			)
		}
		if childBoardlyHome != "" {
			if err := removeCommerceDevChildHome(childBoardlyHome); err != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("remove isolated commerce child home: %w", err),
				)
			}
		}
		runErr = errors.Join(append([]error{runErr}, cleanupErrors...)...)
	}()
	childBoardlyHome, err = os.MkdirTemp("", "boardly-commerce-child-")
	if err != nil {
		return fmt.Errorf("create isolated commerce child home: %w", err)
	}

	leaseEnvironment := append([]string(nil), lease.EnvironmentVariables()...)
	leaseEnvironment = append(leaseEnvironment, "BOARDLY_HOME="+childBoardlyHome)
	environment := mergeCommerceDevEnvironment(os.Environ(), leaseEnvironment)
	return runCommerceDevChild(ctx, args, environment, lease, commerceDevStreams{
		in:  cmd.InOrStdin(),
		out: cmd.OutOrStdout(),
		err: cmd.ErrOrStderr(),
	})
}

func readCommerceDevManifest(path string) (daemonpkg.CommerceDevManifestSite, error) {
	file, err := os.Open(path)
	if err != nil {
		return daemonpkg.CommerceDevManifestSite{}, fmt.Errorf("open Commerce manifest %q: %w", path, err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return daemonpkg.CommerceDevManifestSite{}, fmt.Errorf("read Commerce manifest %q: %w", path, err)
	}
	if len(raw) > 64<<10 {
		return daemonpkg.CommerceDevManifestSite{}, errors.New("Commerce manifest exceeds 64 KiB")
	}
	site, err := daemonpkg.ParseCommerceDevManifest(raw)
	if err != nil {
		return daemonpkg.CommerceDevManifestSite{}, fmt.Errorf("validate Commerce manifest %q: %w", path, err)
	}
	return site, nil
}

func mergeCommerceDevEnvironment(base, commerce []string) []string {
	merged := make([]string, 0, len(base)+len(commerce))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		// The wrapper consumes the Task bootstrap itself. Its child receives
		// only the lease-specific Commerce values appended below, never the
		// Boardly home, run proof, or daemon bootstrap authority.
		if strings.HasPrefix(strings.ToUpper(key), "BOARDLY_") {
			continue
		}
		merged = append(merged, entry)
	}
	return append(merged, commerce...)
}

func superviseCommerceDevChild(
	ctx context.Context,
	args []string,
	environment []string,
	lease commerceDevLease,
	streams commerceDevStreams,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	child := exec.CommandContext(context.WithoutCancel(ctx), args[0], args[1:]...)
	configureCommerceDevProcess(child)
	child.Env = environment
	child.Stdin = streams.in
	child.Stdout = streams.out
	child.Stderr = streams.err
	if err := child.Start(); err != nil {
		return fmt.Errorf("start Commerce development command: %w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case err := <-waited:
			if err != nil {
				return fmt.Errorf("Commerce development command exited: %w", err)
			}
			return nil
		case <-ticker.C:
			err := renewCommerceDevLease(ctx, lease)
			if err == nil {
				continue
			}
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				return stopCommerceDevChild(child, waited)
			}
			stopErr := stopCommerceDevChild(child, waited)
			return errors.Join(fmt.Errorf("renew local Commerce signing lease: %w", err), stopErr)
		case <-ctx.Done():
			return stopCommerceDevChild(child, waited)
		}
	}
}

func renewCommerceDevLease(ctx context.Context, lease commerceDevLease) error {
	renewContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return lease.Renew(renewContext)
}

func stopCommerceDevChild(child *exec.Cmd, waited <-chan error) error {
	if err := killCommerceDevProcess(child); err != nil {
		return fmt.Errorf("stop Commerce development process group: %w", err)
	}
	<-waited
	return nil
}
