package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The subprocess is a framing fixture, not evidence of Studio authentication.
func TestStudioBridgeChild(_ *testing.T) {
	if os.Getenv("STUDIO_BRIDGE_TEST_CHILD") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if request["operation"] == "stall" {
			time.Sleep(time.Second)
			continue
		}
		b, _ := json.Marshal(
			map[string]any{
				"protocolVersion": 1,
				"id":              request["id"],
				"ok":              true,
				"data":            map[string]any{"value": "ok"},
			},
		)
		fmt.Println(string(b))
	}
	os.Exit(0)
}

func TestStudioBridgeDeadlineKillsOwnedProcess(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStudioBridgeChild$")
	cmd.Env = append(os.Environ(), "STUDIO_BRIDGE_TEST_CHILD=1")
	b, err := startStudioBridge(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = b.call(
		ctx,
		map[string]any{"protocolVersion": 1, "id": uuid.New(), "operation": "probe"},
	); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if _, err = b.call(
		short,
		map[string]any{"protocolVersion": 1, "id": uuid.New(), "operation": "stall"},
	); err == nil {
		t.Fatal("deadline accepted")
	}
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("child not reaped")
	}
	if _, err = b.call(ctx, map[string]any{"id": uuid.New()}); err == nil {
		t.Fatal("reused timed-out pipe")
	}
}
