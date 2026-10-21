//go:build darwin

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

const appPreviewSupervisorScript = `
trap ':' TERM
"$@" &
root=$!
while kill -0 "$root" 2>/dev/null; do
  wait "$root"
  status=$?
done
printf x >&3
while :; do
  sleep 2147483647 &
  wait $!
done
exit "$status"
`

type unixAppPreviewBackend struct {
	lsofPath string
	client   *http.Client
}

type unixAppPreviewTree struct {
	process  *os.Process
	groupID  int
	rootExit <-chan error
	done     <-chan error

	mu      sync.Mutex
	stopped bool
}

func newPlatformAppPreviewBackend() appPreviewBackend {
	lsofPath, _ := exec.LookPath("lsof")
	return &unixAppPreviewBackend{
		lsofPath: lsofPath,
		client: &http.Client{
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (b *unixAppPreviewBackend) CheckSupported() error {
	if b.lsofPath == "" {
		return errAppPreviewPlatformUnsupported
	}
	return nil
}

// Launch starts each command under a small shell supervisor in its own
// process group. With a sandbox, sandbox-exec starts the supervisor and
// replaces itself with it, so the group, the pid and fd 3 stay the
// supervisor's and every process the command starts inherits the sandbox.
func (b *unixAppPreviewBackend) Launch(
	ctx context.Context,
	manifest appPreviewManifest,
	workingDirectories []string,
	sandbox *agent.Sandbox,
) ([]appPreviewTree, error) {
	if err := b.CheckSupported(); err != nil {
		return nil, err
	}
	if len(workingDirectories) != len(manifest.Commands) {
		return nil, errAppPreviewInvalid
	}
	trees := make([]appPreviewTree, 0, len(manifest.Commands))
	for index, command := range manifest.Commands {
		if err := ctx.Err(); err != nil {
			return b.abortLaunch(trees, err)
		}
		rootReader, rootWriter, err := os.Pipe()
		if err != nil {
			_ = b.stopPartial(trees)
			return trees, fmt.Errorf("create app preview root monitor: %w", err)
		}
		arguments := []string{"-c", appPreviewSupervisorScript, "zenith-app-preview", command.Command}
		arguments = append(arguments, command.Args...)
		name, argv := sandbox.Command("/bin/sh", arguments)
		helper := exec.CommandContext(context.WithoutCancel(ctx), name, argv...)
		helper.Dir = workingDirectories[index]
		helper.Env = appPreviewEnvironment(command.Port)
		helper.ExtraFiles = []*os.File{rootWriter}
		helper.Stdout = io.Discard
		helper.Stderr = io.Discard
		helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := ctx.Err(); err != nil {
			rootReader.Close()
			rootWriter.Close()
			return b.abortLaunch(trees, err)
		}
		if err := helper.Start(); err != nil {
			rootReader.Close()
			rootWriter.Close()
			_ = b.stopPartial(trees)
			return trees, fmt.Errorf("start app preview command: %w", err)
		}
		rootWriter.Close()
		rootExit := make(chan error, 1)
		done := make(chan error, 1)
		go observeAppPreviewRoot(rootReader, rootExit)
		go func() {
			done <- helper.Wait()
			close(done)
		}()
		trees = append(trees, &unixAppPreviewTree{
			process: helper.Process, groupID: helper.Process.Pid,
			rootExit: rootExit, done: done,
		})
		if err := ctx.Err(); err != nil {
			return b.abortLaunch(trees, err)
		}
	}
	return trees, nil
}

func (b *unixAppPreviewBackend) abortLaunch(
	trees []appPreviewTree,
	cause error,
) ([]appPreviewTree, error) {
	cleanupErr := b.stopPartial(trees)
	return trees, errors.Join(
		fmt.Errorf("launch app preview command: %w", cause),
		cleanupErr,
	)
}

func observeAppPreviewRoot(reader *os.File, result chan<- error) {
	defer reader.Close()
	var marker [1]byte
	_, err := reader.Read(marker[:])
	if err == nil {
		err = errors.New("app preview command exited")
	}
	result <- err
	close(result)
}

func (b *unixAppPreviewBackend) Observe(
	ctx context.Context,
	port int,
	tree appPreviewTree,
) appPreviewEndpointObservation {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	before := b.listenerOwnership(probeCtx, port, tree)
	if before != appPreviewOwnershipOwned {
		return appPreviewEndpointObservation{Ownership: before}
	}
	httpReady := b.httpReady(probeCtx, port)
	after := b.listenerOwnership(probeCtx, port, tree)
	if after != appPreviewOwnershipOwned {
		return appPreviewEndpointObservation{Ownership: after}
	}
	return appPreviewEndpointObservation{Ownership: appPreviewOwnershipOwned, HTTP: httpReady}
}

func (b *unixAppPreviewBackend) listenerOwnership(
	ctx context.Context,
	port int,
	tree appPreviewTree,
) appPreviewOwnership {
	pids, err := b.listenerPIDs(ctx, port)
	if err != nil {
		return appPreviewOwnershipUnknown
	}
	if len(pids) == 0 {
		return appPreviewOwnershipAbsent
	}
	if tree == nil {
		return appPreviewOwnershipForeign
	}
	ownedTree, ok := tree.(*unixAppPreviewTree)
	if !ok {
		return appPreviewOwnershipUnknown
	}
	owned := 0
	for _, pid := range pids {
		groupID, err := syscall.Getpgid(pid)
		if err != nil {
			return appPreviewOwnershipUnknown
		}
		if groupID == ownedTree.groupID {
			owned++
		}
	}
	if owned == len(pids) {
		return appPreviewOwnershipOwned
	}
	return appPreviewOwnershipForeign
}

func (b *unixAppPreviewBackend) listenerPIDs(ctx context.Context, port int) ([]int, error) {
	command := exec.CommandContext(
		ctx, b.lsofPath, "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fp",
	)
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(output) == 0 && len(exitErr.Stderr) == 0 {
			return nil, nil
		}
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, "p") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(line, "p"))
		if err != nil || pid < 1 {
			return nil, errAppPreviewUnavailable
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func (b *unixAppPreviewBackend) httpReady(ctx context.Context, port int) bool {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil,
	)
	if err != nil {
		return false
	}
	response, err := b.client.Do(request)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	_ = response.Body.Close()
	return true
}

func (b *unixAppPreviewBackend) Stop(ctx context.Context, trees []appPreviewTree) error {
	owned := make([]*unixAppPreviewTree, 0, len(trees))
	var cleanupErrors []error
	for _, tree := range trees {
		value, ok := tree.(*unixAppPreviewTree)
		if !ok {
			cleanupErrors = append(cleanupErrors, errAppPreviewCleanupBlocked)
			continue
		}
		value.mu.Lock()
		stopped := value.stopped
		value.mu.Unlock()
		if stopped {
			continue
		}
		alive, err := appPreviewAnchorAlive(value)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if !alive {
			value.mu.Lock()
			value.stopped = true
			value.mu.Unlock()
			continue
		}
		owned = append(owned, value)
	}
	for _, tree := range owned {
		if err := syscall.Kill(-tree.groupID, syscall.SIGTERM); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf(
				"%w: graceful termination: %w",
				errAppPreviewCleanupBlocked,
				err,
			))
		}
	}
	grace := time.NewTimer(3 * time.Second)
	defer grace.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if appPreviewTreesStopped(owned) {
			return errors.Join(cleanupErrors...)
		}
		select {
		case <-ctx.Done():
			cleanupErrors = append(cleanupErrors, fmt.Errorf(
				"%w: %w",
				errAppPreviewCleanupBlocked,
				ctx.Err(),
			))
			return errors.Join(cleanupErrors...)
		case <-grace.C:
			remaining := owned[:0]
			for _, tree := range owned {
				tree.mu.Lock()
				stopped := tree.stopped
				tree.mu.Unlock()
				if stopped {
					continue
				}
				alive, err := appPreviewAnchorAlive(tree)
				if err != nil {
					cleanupErrors = append(cleanupErrors, err)
					continue
				}
				if !alive {
					tree.mu.Lock()
					tree.stopped = true
					tree.mu.Unlock()
					continue
				}
				if err = syscall.Kill(-tree.groupID, syscall.SIGKILL); err != nil &&
					!errors.Is(err, syscall.ESRCH) {
					cleanupErrors = append(cleanupErrors, fmt.Errorf(
						"%w: forced termination: %w",
						errAppPreviewCleanupBlocked,
						err,
					))
					continue
				}
				remaining = append(remaining, tree)
			}
			owned = remaining
			grace.Reset(time.Hour)
		case <-poll.C:
		}
	}
}

func appPreviewAnchorAlive(tree *unixAppPreviewTree) (bool, error) {
	groupID, err := syscall.Getpgid(tree.process.Pid)
	if err == nil {
		if groupID != tree.groupID {
			return false, fmt.Errorf(
				"%w: supervisor identity was lost",
				errAppPreviewCleanupBlocked,
			)
		}
		return true, nil
	}
	if groupErr := syscall.Kill(-tree.groupID, 0); errors.Is(groupErr, syscall.ESRCH) {
		return false, nil
	}
	return false, fmt.Errorf("%w: supervisor identity was lost", errAppPreviewCleanupBlocked)
}

func appPreviewTreesStopped(trees []*unixAppPreviewTree) bool {
	allStopped := true
	for _, tree := range trees {
		tree.mu.Lock()
		if !tree.stopped {
			select {
			case <-tree.done:
				if err := syscall.Kill(-tree.groupID, 0); errors.Is(err, syscall.ESRCH) {
					tree.stopped = true
				} else {
					allStopped = false
				}
			default:
				allStopped = false
			}
		}
		tree.mu.Unlock()
	}
	return allStopped
}

func (b *unixAppPreviewBackend) stopPartial(trees []appPreviewTree) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return b.Stop(ctx, trees)
}

func (tree *unixAppPreviewTree) RootExit() <-chan error { return tree.rootExit }

func appPreviewEnvironment(port int) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "PORT" && key != "HOST" {
			environment = append(environment, value)
		}
	}
	return append(environment, "HOST=127.0.0.1", "PORT="+strconv.Itoa(port))
}
