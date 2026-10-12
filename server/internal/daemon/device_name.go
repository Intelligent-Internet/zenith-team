package daemon

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// defaultDeviceName is the name a machine announces for itself when nobody
// set BOARDLY_DEVICE_NAME: on macOS the computer name the person gave it in
// System Settings ("Duong's MacBook"), because os.Hostname there is whatever
// the current network resolves — an ISP's reverse-DNS label on one Wi-Fi,
// "name.local" on another — and that string ends up in every roster and
// runtime picker. Elsewhere, and whenever the lookup fails, the hostname
// with any ".local" suffix trimmed.
func defaultDeviceName() string {
	if runtime.GOOS == "darwin" {
		if name := macComputerName(); name != "" {
			return name
		}
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "local-machine"
	}
	return strings.TrimSuffix(strings.TrimSpace(host), ".local")
}

// macComputerName reads the user-facing computer name; empty when scutil is
// unavailable or answers nothing usable. A var so tests can stub it.
var macComputerName = func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--get", "ComputerName").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
