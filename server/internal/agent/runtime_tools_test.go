package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/pkg/protocol"
)

func TestNormalizeToolObservationsRejectsUnreviewedMetadata(t *testing.T) {
	t.Parallel()
	valid := protocol.ToolObservation{
		AgentID: uuid.NewString(), Key: "zenith-studio", Version: "0.1.11", State: "installed",
		AuthState: "needs_auth", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	for _, test := range []struct {
		name   string
		mutate func(*protocol.ToolObservation)
	}{
		{"unknown tool", func(v *protocol.ToolObservation) { v.Key = "shell" }},
		{"unknown release", func(v *protocol.ToolObservation) { v.Version = "latest" }},
		{"raw failure", func(v *protocol.ToolObservation) { v.ReasonCode = "secret token" }},
		{"Studio auth bypass", func(v *protocol.ToolObservation) { v.AuthState = "not_required" }},
		{"future observation", func(v *protocol.ToolObservation) { v.ObservedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := valid
			test.mutate(&value)
			if normalizeToolObservations([]protocol.ToolObservation{value}) == nil {
				t.Fatal("accepted invalid observation")
			}
		})
	}
	if err := normalizeToolObservations([]protocol.ToolObservation{valid}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRendererObservationRequiresNoAccount(t *testing.T) {
	t.Parallel()

	observation := protocol.ToolObservation{
		Key:        "hyperframes",
		Version:    "0.8.36",
		State:      "installed",
		AuthState:  "not_required",
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := normalizeToolObservations([]protocol.ToolObservation{observation}); err != nil {
		t.Fatal(err)
	}
	observation.AuthState = "authenticated"
	if normalizeToolObservations([]protocol.ToolObservation{observation}) == nil {
		t.Fatal("accepted invented renderer authentication")
	}
}

func TestToolObservationReleaseVersions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		version string
		want    error
	}{
		{"stable", "0.1.11", nil},
		{"published beta", "0.1.12-beta", nil},
		{"numbered beta", "0.1.12-beta.1", nil},
		{"build metadata", "0.1.12-beta.1+build.4", nil},
		{"missing patch", "0.1", ErrRuntimeInvalidInput},
		{"empty prerelease", "0.1.12-", ErrRuntimeInvalidInput},
		{"numeric prerelease leading zero", "0.1.12-01", ErrRuntimeInvalidInput},
		{"version prefix", "v0.1.12", ErrRuntimeInvalidInput},
		{"unsafe suffix", "0.1.12-beta/other", ErrRuntimeInvalidInput},
		{"oversize", "0.1.12-" + strings.Repeat("a", 128), ErrRuntimeInvalidInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeToolObservations([]protocol.ToolObservation{
				{
					AgentID:    uuid.NewString(),
					Key:        "zenith-studio",
					Version:    test.version,
					State:      "installed",
					AuthState:  "needs_auth",
					ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
					APIOrigin:  "http://localhost:8000",
				},
			})
			if !errors.Is(got, test.want) {
				t.Fatalf("normalizeToolObservations() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestToolObservationsRequireAgentForAuthentication(t *testing.T) {
	t.Parallel()
	a := protocol.ToolObservation{
		AgentID:    uuid.NewString(),
		Key:        "zenith-studio",
		Version:    "0.1.11",
		State:      "installed",
		AuthState:  "authenticated",
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	b := a
	b.AgentID = uuid.NewString()
	b.AuthState = "needs_auth"
	if err := normalizeToolObservations([]protocol.ToolObservation{a, b}); err != nil {
		t.Fatal(err)
	}
	if sameToolStates([]protocol.ToolObservation{a, b}, []protocol.ToolObservation{b, a}) != true {
		t.Fatal("order changed scoped states")
	}
	c := a
	c.AgentID = ""
	if normalizeToolObservations([]protocol.ToolObservation{c}) == nil {
		t.Fatal("unscoped account accepted")
	}
	if normalizeToolObservations([]protocol.ToolObservation{a, a}) == nil {
		t.Fatal("duplicate Agent account accepted")
	}
}
