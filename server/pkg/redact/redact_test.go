package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// ruleCases pairs every rule with representative positive matches. Each case
// is also fed to the idempotence property below.
var ruleCases = []struct {
	name  string
	input string
	want  string
}{
	{
		name:  "pem private key block",
		input: "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nabc/def+ghi=\n-----END RSA PRIVATE KEY-----\nafter",
		want:  "before\n[redacted:pem-block]\nafter",
	},
	{
		name:  "pem openssh block",
		input: "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaA==\n-----END OPENSSH PRIVATE KEY-----",
		want:  "[redacted:pem-block]",
	},
	{
		name:  "aws access key id",
		input: "creds AKIAIOSFODNN7EXAMPLE used",
		want:  "creds [redacted:aws-key] used",
	},
	{
		name:  "github classic token",
		input: "push with ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789 now",
		want:  "push with [redacted:github-token] now",
	},
	{
		name:  "github oauth token",
		input: "gho_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		want:  "[redacted:github-token]",
	},
	{
		name:  "github fine grained token",
		input: "github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn",
		want:  "[redacted:github-pat]",
	},
	{
		name:  "slack bot token",
		input: "xoxb-" + "123456789012-ABCDEFGHIJKLMNOPQRSTUVWX",
		want:  "[redacted:slack-token]",
	},
	{
		name:  "google api key",
		input: "key AIzaSyA1bC2dE3fG4hI5jK6lM7nO8pQ9rS0tU1v end",
		want:  "key [redacted:google-api-key] end",
	},
	{
		name:  "openai style key",
		input: "OPENAI sk-proj-AbCdEf0123456789AbCdEf0123456789 tail",
		want:  "OPENAI [redacted:sk-key] tail",
	},
	{
		name:  "anthropic style key",
		input: "sk-ant-api03-AbCdEf0123456789AbCdEf0123456789AA",
		want:  "[redacted:sk-key]",
	},
	{
		name:  "jwt triplet",
		input: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.TJVA95OrM7E2cBab30RMHrHDcEfxjoYZgeFONFh7HgQ",
		want:  "[redacted:jwt]",
	},
	{
		name:  "bearer header value",
		input: "Authorization: Bearer abcdef123456.token-value",
		want:  "Authorization: Bearer [redacted:bearer-token]",
	},
	{
		name:  "lowercase bearer",
		input: "authorization: bearer abcdefgh12345678",
		want:  "authorization: Bearer [redacted:bearer-token]",
	},
	{
		name:  "url userinfo credentials",
		input: "postgres://alice:s3cr3t-pass@db.internal:5432/boardly",
		want:  "postgres://[redacted:url-credentials]@db.internal:5432/boardly",
	},
	{
		name:  "generic key value secret",
		input: "export DB_PASSWORD=hunter2 && run",
		want:  "export DB_PASSWORD=[redacted:kv-secret] && run",
	},
	{
		name:  "json quoted api key keeps quotes",
		input: `{"api_key": "super-secret-value"}`,
		want:  `{"api_key": "[redacted:kv-secret]"}`,
	},
	{
		name:  "single quoted secret keeps quotes",
		input: "export SECRET='multi word value' done",
		want:  "export SECRET='[redacted:kv-secret]' done",
	},
	{
		name:  "yaml token",
		input: "github_token: ghx-not-a-known-shape-123",
		want:  "github_token: [redacted:kv-secret]",
	},
	{
		name:  "macos home path is preserved",
		input: "wrote /Users/alice/Working/project/main.go and left",
		want:  "wrote /Users/alice/Working/project/main.go and left",
	},
	{
		name:  "linux home path is preserved",
		input: "see /home/bob/.config/secrets.toml",
		want:  "see /home/bob/.config/secrets.toml",
	},
	{
		name:  "root path is preserved",
		input: "cache at /root/.cache/tool",
		want:  "cache at /root/.cache/tool",
	},
	{
		name:  "windows home path is preserved",
		input: `log C:\Users\alice\AppData\Local\tool.log end`,
		want:  `log C:\Users\alice\AppData\Local\tool.log end`,
	},
	{
		name:  "email address is preserved",
		input: "contact alice.smith+dev@example.co.uk today",
		want:  "contact alice.smith+dev@example.co.uk today",
	},
	{
		name:  "email survives beside credentials",
		input: "Draft for alice@example.com with password=hunter2 and Bearer abcdefgh12345678",
		want:  "Draft for alice@example.com with password=[redacted:kv-secret] and Bearer [redacted:bearer-token]",
	},
	{
		name:  "home path without email is preserved",
		input: "/Users/alice/mail",
		want:  "/Users/alice/mail",
	},
	{
		name:  "stacked shapes",
		input: "token=AKIAIOSFODNN7EXAMPLE via Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefgh1234 at /home/ci/build",
		want:  "token=[redacted:kv-secret] via Bearer [redacted:jwt] at /home/ci/build",
	},
	{
		name:  "plain text untouched",
		input: "release notes: bump minor version, fix flaky test",
		want:  "release notes: bump minor version, fix flaky test",
	},
	{
		name:  "safe markdown untouched",
		input: "## Result\n\n- built `server` binary\n- 3 tests passed\n\n```go\nfunc main() {}\n```",
		want:  "## Result\n\n- built `server` binary\n- 3 tests passed\n\n```go\nfunc main() {}\n```",
	},
	{
		name:  "short sk prefix untouched",
		input: "install sk-learn now",
		want:  "install sk-learn now",
	},
	{
		name:  "relative path untouched",
		input: "edited server/internal/run/events.go",
		want:  "edited server/internal/run/events.go",
	},
}

func TestRedactRuleTable(t *testing.T) {
	t.Parallel()
	for _, testCase := range ruleCases {
		if got := Redact(testCase.input); got != testCase.want {
			t.Fatalf("%s: Redact(%q) = %q, want %q", testCase.name, testCase.input, got, testCase.want)
		}
	}
}

// Credentials is for human-authored prose: it removes credential formats and
// leaves contact details and ordinary words that merely name a secret alone.
func TestCredentialsRedactsCredentialFormatsOnly(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		input string
		want  string
	}{
		{"email survives", "Contact hr@acme.com for leave questions.", "Contact hr@acme.com for leave questions."},
		{"password heading survives", "Password: rotate every 90 days.", "Password: rotate every 90 days."},
		{"token word survives", "Tokenization: splits text into words.", "Tokenization: splits text into words."},
		{"aws key", "creds AKIAIOSFODNN7EXAMPLE used", "creds [redacted:aws-key] used"},
		{"github token", "push with ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789 now", "push with [redacted:github-token] now"},
		{"pem block", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaA==\n-----END OPENSSH PRIVATE KEY-----", "[redacted:pem-block]"},
		{"bearer", "Authorization: Bearer abcdefgh12345678", "Authorization: Bearer [redacted:bearer-token]"},
		{"url credentials", "postgres://alice:s3cret@db/x", "postgres://[redacted:url-credentials]@db/x"},
	} {
		got := Credentials(testCase.input)
		if got != testCase.want {
			t.Fatalf("%s: Credentials(%q) = %q, want %q", testCase.name, testCase.input, got, testCase.want)
		}
		if twice := Credentials(got); twice != got {
			t.Fatalf("%s: Credentials is not idempotent: %q -> %q", testCase.name, got, twice)
		}
	}
}

// Redact must be a fixed point after one application: the daemon redacts
// first and every server seam redacts again before commit.
func TestRedactIsIdempotentOnRuleTable(t *testing.T) {
	t.Parallel()
	for _, testCase := range ruleCases {
		once := Redact(testCase.input)
		if twice := Redact(once); twice != once {
			t.Fatalf("%s: Redact is not idempotent:\nonce  = %q\ntwice = %q", testCase.name, once, twice)
		}
	}
}

// Placeholder texts are closed under the ruleset: redacting any placeholder
// in neutral context never rewrites it, and even in key/value position — where
// a placeholder legitimately collapses to [redacted:kv-secret] — one more
// application changes nothing.
func TestRedactPlaceholdersAreFixedPoints(t *testing.T) {
	t.Parallel()
	for _, text := range placeholderTexts {
		for _, embedded := range []string{
			text,
			"prefix " + text + " suffix",
			text + text,
		} {
			if got := Redact(embedded); got != embedded {
				t.Fatalf("placeholder %q is not a fixed point: %q -> %q", text, embedded, got)
			}
		}
		for _, embedded := range []string{
			"password=" + text,
			`{"secret": "` + text + `"}`,
		} {
			once := Redact(embedded)
			if twice := Redact(once); twice != once {
				t.Fatalf("placeholder %q in key/value context is not idempotent: %q -> %q -> %q",
					text, embedded, once, twice)
			}
		}
	}
}

// Property: output is stable under re-application for adversarial
// concatenations of secrets, placeholders, separators, and prose.
func TestRedactIdempotencePropertyOverGeneratedCorpus(t *testing.T) {
	t.Parallel()
	atoms := []string{
		"AKIAIOSFODNN7EXAMPLE",
		"ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		"github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz",
		"xoxb-" + "123456789012-ABCDEFGHIJKLMNOPQRSTUVWX",
		"AIzaSyA1bC2dE3fG4hI5jK6lM7nO8pQ9rS0tU1v",
		"sk-proj-AbCdEf0123456789AbCdEf0123456789",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefgh1234",
		"Bearer abcdefgh12345678",
		"postgres://alice:s3cret@db/x",
		"password=hunter2",
		"api_key: \"quoted secret\"",
		"/Users/alice/Working/project",
		"/home/bob/.ssh/id_ed25519",
		"/root/.cache",
		`C:\Users\alice\secrets`,
		"alice@example.com",
		"-----BEGIN EC PRIVATE KEY-----\nAA==\n-----END EC PRIVATE KEY-----",
		"[redacted:kv-secret]", "[redacted:literal]",
		"plain words", "token:", "password =", "sk-", "ey.", "://", "@", "=", ": ",
		"\n", " ", "\"", "'", ",", ";", "]", "[", ">", "<",
	}
	random := rand.New(rand.NewSource(20260714))
	for sample := range 4000 {
		var builder strings.Builder
		pieces := 1 + random.Intn(8)
		for range pieces {
			builder.WriteString(atoms[random.Intn(len(atoms))])
		}
		input := builder.String()
		once := Redact(input)
		if twice := Redact(once); twice != once {
			t.Fatalf(
				"sample %d: Redact is not idempotent:\ninput = %q\nonce  = %q\ntwice = %q",
				sample,
				input,
				once,
				twice,
			)
		}
	}
}

func TestRedactIsDeterministic(t *testing.T) {
	t.Parallel()
	input := "password=hunter2 AKIAIOSFODNN7EXAMPLE /Users/alice/x alice@example.com"
	first := Redact(input)
	for range 8 {
		if got := Redact(input); got != first {
			t.Fatalf("Redact is not deterministic: %q then %q", first, got)
		}
	}
}

func TestDiagnosticRedactsBoundsAndPreservesUTF8(t *testing.T) {
	t.Parallel()

	input := "Authorization: Bearer super-secret-token\n" + strings.Repeat("界", 2<<20)
	got := Diagnostic(input)
	if len(got) > maxDiagnosticBytes {
		t.Fatalf("Diagnostic() bytes = %d, want <= %d", len(got), maxDiagnosticBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("Diagnostic() returned invalid UTF-8")
	}
	if strings.Contains(got, "super-secret-token") {
		t.Fatalf("Diagnostic() leaked secret: %q", got[:128])
	}
	if !strings.HasSuffix(got, "[truncated:diagnostic]") {
		t.Fatalf("Diagnostic() has no truncation marker: %q", got[len(got)-64:])
	}
	if twice := Diagnostic(got); twice != got {
		t.Fatal("Diagnostic() is not idempotent")
	}
	structured := Diagnostic(`{"token":["alpha-secret","beta-secret"],"message":"failed"}`)
	if strings.Contains(structured, "alpha-secret") || strings.Contains(structured, "beta-secret") {
		t.Fatalf("Diagnostic() leaked structured secret values: %s", structured)
	}
}

func TestDiagnosticStillRedactsEmailAddresses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain diagnostic",
			input: "Failed for alice@example.com with password=hunter2",
			want:  "Failed for [redacted:email] with password=[redacted:kv-secret]",
		},
		{
			name:  "structured diagnostic",
			input: `{"email":"alice@example.com","password":"hunter2"}`,
			want:  `{"email":"[redacted:email]","password":"[redacted:json-secret]"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Diagnostic(test.input)
			if got != test.want {
				t.Fatalf("Diagnostic() = %q, want %q", got, test.want)
			}
			if twice := Diagnostic(got); twice != got {
				t.Fatalf("Diagnostic() is not idempotent: %q -> %q", got, twice)
			}
		})
	}
}

// The version and the exact ruleset move together: changing any rule id,
// pattern, placeholder, or the rule order requires bumping Version and
// repinning this fingerprint in the same commit.
func TestVersionPinsExactRuleset(t *testing.T) {
	t.Parallel()
	if Version() != 4 {
		t.Fatalf("Version() = %d, want 4 (bump the pinned fingerprint alongside any version change)", Version())
	}
	hash := sha256.New()
	for _, rule := range rules {
		hash.Write([]byte(rule.id))
		hash.Write([]byte{0})
		hash.Write([]byte(rule.pattern.String()))
		hash.Write([]byte{0})
		hash.Write([]byte(rule.replacement))
		hash.Write([]byte{0})
	}
	hash.Write([]byte("diagnostic\x00"))
	hash.Write([]byte(diagnosticEmailRule.id))
	hash.Write([]byte{0})
	hash.Write([]byte(diagnosticEmailRule.pattern.String()))
	hash.Write([]byte{0})
	hash.Write([]byte(diagnosticEmailRule.replacement))
	hash.Write([]byte{0})
	hash.Write([]byte(jsonSensitiveKeyPattern.String()))
	hash.Write([]byte{0})
	const pinned = "48afc0c8b58ce86cccebf9663fda3c53b33b1646eb4c7583129c0c09f85b10f9"
	if got := hex.EncodeToString(hash.Sum(nil)); got != pinned {
		t.Fatalf(
			"ruleset fingerprint = %s, pinned %s: rules changed — bump rulesetVersion and repin",
			got,
			pinned,
		)
	}
}

func TestJSONRedactsStringsAndPreservesStructure(t *testing.T) {
	t.Parallel()
	input := []byte(
		`{"command":"export API_KEY=sk-proj-AbCdEf0123456789AbCdEf0123456789","cwd":"/Users/alice/project","count":3,"ok":true,"password":"hunter2","apiKey":"short-secret","nested":{"emails":["alice@example.com",7],"refresh_token":{"raw":"tiny"}},"big":9007199254740991}`,
	)
	got := JSON(input)
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("redacted JSON is invalid: %v\n%s", err, got)
	}
	text := string(got)
	for _, leaked := range []string{"sk-proj-", "hunter2", "short-secret", "tiny"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("redacted JSON leaks %q: %s", leaked, text)
		}
	}
	for _, kept := range []string{`"count":3`, `"ok":true`, `"big":9007199254740991`, "[redacted:kv-secret]", "[redacted:json-secret]", "/Users/alice/project", "alice@example.com", "7"} {
		if !strings.Contains(text, kept) {
			t.Fatalf("redacted JSON lost %q: %s", kept, text)
		}
	}
	if twice := JSON(got); string(twice) != text {
		t.Fatalf("JSON redaction is not idempotent:\nonce  = %s\ntwice = %s", text, twice)
	}
}

func TestJSONOrTextRedactsSensitiveStructuredValuesAndCanonicalizes(t *testing.T) {
	t.Parallel()

	input := ` { "safe" : 1, "token" : ["alpha", {"nested":"beta"}], "password" : {"raw":"hunter2"} } `
	got := JSONOrText(input)
	if got != `{"password":"[redacted:json-secret]","safe":1,"token":"[redacted:json-secret]"}` {
		t.Fatalf("JSONOrText() = %s", got)
	}
	if twice := JSONOrText(got); twice != got {
		t.Fatalf("JSONOrText is not idempotent:\nonce  = %s\ntwice = %s", got, twice)
	}
	if plain := JSONOrText("password=hunter2"); plain != "password=[redacted:kv-secret]" {
		t.Fatalf("plain JSONOrText() = %q", plain)
	}
}

func TestJSONKeepsCleanAndInvalidInputBytes(t *testing.T) {
	t.Parallel()
	clean := []byte(`{"status":"completed","turnIndex":1}`)
	if got := JSON(clean); string(got) != string(clean) {
		t.Fatalf("clean JSON was rewritten: %s -> %s", clean, got)
	}
	invalid := []byte(`{"unterminated":`)
	if got := JSON(invalid); string(got) != string(invalid) {
		t.Fatalf("invalid JSON was rewritten: %s -> %s", invalid, got)
	}
}
