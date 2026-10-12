package util

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

type renderNested struct {
	ID string `json:"id" validate:"required,uuid"`
}

type renderProbe struct {
	ID      string       `json:"id"               validate:"required,uuid"`
	Name    string       `json:"name"             validate:"required"`
	Kind    string       `json:"kind"             validate:"required,oneof=user agent"`
	Version int64        `json:"expectedRevision" validate:"min=1"`
	IDs     []string     `json:"runtimeIds"       validate:"max=2,dive,uuid"`
	CEO     renderNested `json:"ceo"`
	Secret  string       `json:"secret"           validate:"omitempty,uuid"`
}

func TestRenderAggregatesJSONFieldMessages(t *testing.T) {
	t.Parallel()
	v := newTestValidator()

	err := v.Struct(renderProbe{
		Kind: "robot",
		IDs:  []string{"01980000-0000-7000-8000-000000000000", "nope", "also-nope"},
	})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	got := RenderValidationErrors(err, 1024)
	want := strings.Join([]string{
		"id is required",
		"name is required",
		"kind must be one of user, agent",
		"expectedRevision must be at least 1",
		"runtimeIds must be at most 2 items",
		"ceo.id is required",
	}, "; ")
	if got != want {
		t.Fatalf("RenderValidationErrors() = %q, want %q", got, want)
	}
}

func TestRenderIndexesSliceElements(t *testing.T) {
	t.Parallel()
	v := newTestValidator()

	err := v.Struct(renderProbe{
		ID:      "01980000-0000-7000-8000-000000000000",
		Name:    "x",
		Kind:    "user",
		Version: 1,
		IDs:     []string{"01980000-0000-7000-8000-000000000000", "nope"},
		CEO:     renderNested{ID: "01980000-0000-7000-8000-000000000000"},
	})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	got := RenderValidationErrors(err, 1024)
	if got != "runtimeIds[1] must be a canonical UUID" {
		t.Fatalf("RenderValidationErrors() = %q", got)
	}
}

func TestRenderNeverEchoesSubmittedValues(t *testing.T) {
	t.Parallel()
	v := newTestValidator()

	const secret = "super-secret-desktop-token"
	err := v.Struct(renderProbe{
		ID:      "01980000-0000-7000-8000-000000000000",
		Name:    "x",
		Kind:    "user",
		Version: 1,
		CEO:     renderNested{ID: "01980000-0000-7000-8000-000000000000"},
		Secret:  secret,
	})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	got := RenderValidationErrors(err, 1024)
	if strings.Contains(got, secret) {
		t.Fatalf("RenderValidationErrors() leaked submitted value: %q", got)
	}
	if got != "secret must be a canonical UUID" {
		t.Fatalf("RenderValidationErrors() = %q", got)
	}
}

func TestRenderNamesTheCrossFieldTags(t *testing.T) {
	t.Parallel()
	// The at-least-one and exactly-one rules are built-in tags now, not
	// struct-level callbacks, so Render has to phrase what the library reports.
	type patchProbe struct {
		Name        *string `validate:"required_without_all=Description Notes"`
		Description *string
		Notes       *string
	}
	v := newTestValidator()

	err := v.Struct(patchProbe{})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if got := RenderValidationErrors(
		err,
		1024,
	); got != "Name is required when none of its alternatives are provided" {
		t.Fatalf("RenderValidationErrors() = %q", got)
	}
}

func TestRenderFallsBackForNonValidationErrors(t *testing.T) {
	t.Parallel()
	if got := RenderValidationErrors(errors.New("boom"), 1024); got != "validation failed" {
		t.Fatalf("RenderValidationErrors() = %q", got)
	}
	if got := RenderValidationErrors(nil, 1024); got != "validation failed" {
		t.Fatalf("RenderValidationErrors(nil) = %q", got)
	}
}

func TestTruncateTextRespectsRuneBoundaries(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("é", 300) // 600 bytes
	got := truncateText(value, 101)
	if len(got) > 101 {
		t.Fatalf("len = %d, want <= 101", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("missing ellipsis: %q", got[len(got)-6:])
	}
	if !strings.HasPrefix(got, "é") || strings.ContainsRune(got, '�') {
		t.Fatalf("invalid truncation: %q", got[:12])
	}
	if truncateText("short", 1024) != "short" {
		t.Fatal("short values must pass through")
	}
}

// newTestValidator builds a bare instance and applies ConfigureValidator to
// it. The process validator is gin's, which reads the binding tag; these tests
// probe rendering rather than the wiring, so their fixtures use the default
// validate tag.
func newTestValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	ConfigureValidator(v)
	return v
}

// The `uuid` tag is lowercase-only, which is the difference between it and
// `uuid_rfc4122` — neither checks the RFC-4122 version or variant, despite the
// name. Lowercase is the canonical output form and the only one the server
// mints, so the wire contract says canonical and the rendered message says so
// too.
//
// util.ParseGoUUID is deliberately more forgiving and still accepts uppercase;
// it guards writes rather than the wire, and is reached from path parameters
// that carry no tag.
func TestUUIDTagAcceptsCanonicalFormOnly(t *testing.T) {
	t.Parallel()

	type probe struct {
		ID string `validate:"uuid"`
	}
	v := newTestValidator()

	const canonical = "0198aa00-0000-7000-8000-00000000000a"
	if err := v.Struct(probe{ID: canonical}); err != nil {
		t.Fatalf("canonical form rejected: %v", err)
	}
	if err := v.Struct(probe{ID: strings.ToUpper(canonical)}); err == nil {
		t.Error("uppercase accepted; the tag is meant to pin the canonical form")
	}
	if _, err := ParseGoUUID(strings.ToUpper(canonical)); err != nil {
		t.Errorf("ParseGoUUID rejected uppercase: %v — path parameters rely on it", err)
	}
}
