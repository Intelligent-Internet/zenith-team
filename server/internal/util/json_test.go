package util

import (
	"bytes"
	"testing"
)

type strictJSONFixture struct {
	Name   string            `json:"name"`
	Nested strictJSONNested  `json:"nested"`
	Labels map[string]string `json:"labels"`
}

type strictJSONNested struct {
	Enabled bool `json:"enabled"`
}

func TestBindStrictJSONRejectsAmbiguousOrAdditiveInput(t *testing.T) {
	t.Parallel()

	valid := []byte(`{"name":"Zenith Team","nested":{"enabled":true},"labels":{"env":"test"}}`)
	var decoded strictJSONFixture
	if err := BindStrictJSON(bytes.NewReader(valid), &decoded); err != nil {
		t.Fatalf("BindStrictJSON(valid) error = %v", err)
	}
	if decoded.Name != "Zenith Team" || !decoded.Nested.Enabled || decoded.Labels["env"] != "test" {
		t.Fatalf("decoded = %#v", decoded)
	}
	for name, body := range map[string][]byte{
		"empty":              nil,
		"null":               []byte(`null`),
		"array":              []byte(`[]`),
		"unknown":            []byte(`{"name":"a","nested":{"enabled":true},"labels":{},"secret":"x"}`),
		"nested unknown":     []byte(`{"name":"a","nested":{"enabled":true,"extra":1},"labels":{}}`),
		"wrong nested shape": []byte(`{"name":"a","nested":true,"labels":{}}`),
		"trailing":           []byte(`{"name":"a","nested":{"enabled":true},"labels":{}} {}`),
		"invalid UTF-8":      []byte("{\"name\":\"bad\xff\",\"nested\":{\"enabled\":true},\"labels\":{}}"),
	} {
		t.Run(name, func(t *testing.T) {
			var destination strictJSONFixture
			if err := BindStrictJSON(bytes.NewReader(body), &destination); err == nil {
				t.Fatal("BindStrictJSON() error = nil")
			}
		})
	}
}

func TestBindStrictJSONRequiresPointerToStruct(t *testing.T) {
	t.Parallel()

	body := []byte(`{"name":"Zenith Team","nested":{"enabled":true},"labels":{}}`)
	if err := BindStrictJSON(bytes.NewReader(body), strictJSONFixture{}); err == nil {
		t.Fatal("BindStrictJSON(value) error = nil")
	}
	if err := BindStrictJSON(bytes.NewReader(body), (*strictJSONFixture)(nil)); err == nil {
		t.Fatal("BindStrictJSON(nil pointer) error = nil")
	}
}

// The two entry points differ in exactly one input. Everything else BindJSON
// refuses, it refuses for the same reason BindStrictJSON does, so a caller
// choosing leniency is not also opting out of the other guards.
func TestBindJSONToleratesOnlyUnknownFields(t *testing.T) {
	t.Parallel()

	undeclared := []byte(`{"name":"a","nested":{"enabled":true},"labels":{},"secret":"x"}`)
	var lenient strictJSONFixture
	if err := BindJSON(bytes.NewReader(undeclared), &lenient); err != nil {
		t.Fatalf("BindJSON(undeclared field) error = %v", err)
	}
	if lenient.Name != "a" || !lenient.Nested.Enabled {
		t.Fatalf("decoded = %#v", lenient)
	}
	if err := BindStrictJSON(bytes.NewReader(undeclared), &strictJSONFixture{}); err == nil {
		t.Fatal("BindStrictJSON(undeclared field) error = nil")
	}

	for name, body := range map[string][]byte{
		"null":          []byte(`null`),
		"array":         []byte(`[]`),
		"trailing":      []byte(`{"name":"a"} {}`),
		"invalid UTF-8": []byte("{\"name\":\"bad\xff\"}"),
	} {
		t.Run(name, func(t *testing.T) {
			var destination strictJSONFixture
			if err := BindJSON(bytes.NewReader(body), &destination); err == nil {
				t.Fatal("BindJSON() error = nil; leniency must stop at unknown fields")
			}
		})
	}
}
