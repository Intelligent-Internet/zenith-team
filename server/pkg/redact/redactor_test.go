package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactorReplacesSecretLiteralsAndPreservesPaths(t *testing.T) {
	t.Parallel()
	redactor := NewRedactor(Options{
		SecretLiterals: []string{"run-capability-secret-token"},
	})
	input := "wrote /tmp/zenith-team-workspaces/run-1/src/main.go using run-capability-secret-token and password=hunter2"
	want := "wrote /tmp/zenith-team-workspaces/run-1/src/main.go using [redacted:literal] and password=[redacted:kv-secret]"
	if got := redactor.Redact(input); got != want {
		t.Fatalf("Redact() = %q, want %q", got, want)
	}
	if redactor.Version() != Version() {
		t.Fatalf("Redactor.Version() = %d, want %d", redactor.Version(), Version())
	}
}

func TestRedactorZeroValueAppliesSharedRuleset(t *testing.T) {
	t.Parallel()
	var redactor Redactor
	if got := redactor.Redact("AKIAIOSFODNN7EXAMPLE"); got != "[redacted:aws-key]" {
		t.Fatalf("zero Redactor = %q", got)
	}
}

// Literal guards keep idempotence: short values, bracketed values, and values
// colliding with placeholder text are skipped.
func TestRedactorSkipsUnsafeLiterals(t *testing.T) {
	t.Parallel()
	redactor := NewRedactor(Options{
		SecretLiterals: []string{"", "short", "redacted", "redacted:literal", "with[bracket]", "bearer-token"},
	})
	if len(redactor.literals) != 0 {
		t.Fatalf("unsafe literals were compiled: %d", len(redactor.literals))
	}
	input := "redacted short rel/path [redacted:literal]"
	if got := redactor.Redact(input); got != input {
		t.Fatalf("unsafe literals rewrote text: %q", got)
	}
}

func TestRedactorLongestSecretLiteralWins(t *testing.T) {
	t.Parallel()
	redactor := NewRedactor(Options{
		SecretLiterals: []string{"secret-a", "secret-a-extended"},
	})
	if got := redactor.Redact(
		"secret-a-extended plus /work/run-42/deep/child.txt",
	); got != "[redacted:literal] plus /work/run-42/deep/child.txt" {
		t.Fatalf("Redact() = %q", got)
	}
}

func TestRedactorIsIdempotentWithLiterals(t *testing.T) {
	t.Parallel()
	redactor := NewRedactor(Options{
		SecretLiterals: []string{"run-capability-secret-token"},
	})
	inputs := []string{
		"run-capability-secret-token",
		"/private/var/folders/ab/workspace/file.go",
		"password=run-capability-secret-token at /private/var/folders/ab/workspace",
		"mix AKIAIOSFODNN7EXAMPLE run-capability-secret-token alice@example.com",
		"token=run-capability-secret-token;next",
	}
	for _, input := range inputs {
		once := redactor.Redact(input)
		if twice := redactor.Redact(once); twice != once {
			t.Fatalf("Redactor is not idempotent:\ninput = %q\nonce  = %q\ntwice = %q", input, once, twice)
		}
		if strings.Contains(once, "run-capability-secret-token") {
			t.Fatalf("literal leaked: %q", once)
		}
		if strings.Contains(input, "/private/var/folders/ab/workspace") &&
			!strings.Contains(once, "/private/var/folders/ab/workspace") {
			t.Fatalf("path was not preserved: %q", once)
		}
	}
}

func TestRedactorValueWalksDecodedJSONShapes(t *testing.T) {
	t.Parallel()
	redactor := NewRedactor(Options{SecretLiterals: []string{"literal-secret-value"}})
	input := map[string]any{
		"text":    "literal-secret-value",
		"strings": []string{"a", "password=hunter2"},
		"nested": map[string]any{
			"list":   []any{"AKIAIOSFODNN7EXAMPLE", 42, true},
			"labels": map[string]string{"email": "alice@example.com"},
		},
		"number": json.Number("9007199254740991"),
		"none":   nil,
	}
	got, ok := redactor.Value(input).(map[string]any)
	if !ok {
		t.Fatal("Value() lost the map shape")
	}
	if got["text"] != "[redacted:literal]" {
		t.Fatalf("text = %v", got["text"])
	}
	if list := got["strings"].([]string); list[1] != "password=[redacted:kv-secret]" {
		t.Fatalf("strings = %v", list)
	}
	nested := got["nested"].(map[string]any)
	if items := nested["list"].([]any); items[0] != "[redacted:aws-key]" || items[1] != 42 || items[2] != true {
		t.Fatalf("nested list = %v", items)
	}
	if labels := nested["labels"].(map[string]string); labels["email"] != "alice@example.com" {
		t.Fatalf("labels = %v", labels)
	}
	if got["number"] != json.Number("9007199254740991") || got["none"] != nil {
		t.Fatalf("non-string values changed: %v %v", got["number"], got["none"])
	}
	if redactor.Value(nil) != nil {
		t.Fatal("Value(nil) != nil")
	}
	if _, stillMap := redactor.Value(map[string]any(nil)).(map[string]any); !stillMap {
		t.Fatal("Value(nil map) lost the map shape")
	}
}
