package daemon

import (
	"runtime"
	"strings"
	"testing"
)

func TestDefaultDeviceNamePrefersTheComputerNameOnMac(t *testing.T) {
	previous := macComputerName
	t.Cleanup(func() { macComputerName = previous })

	macComputerName = func() string { return "Alex's MacBook" }
	got := defaultDeviceName()
	if runtime.GOOS == "darwin" && got != "Alex's MacBook" {
		t.Fatalf("defaultDeviceName = %q, want the computer name", got)
	}

	macComputerName = func() string { return "" }
	got = defaultDeviceName()
	if got == "" || strings.HasSuffix(got, ".local") {
		t.Fatalf("defaultDeviceName fallback = %q, want a hostname without .local", got)
	}
}
