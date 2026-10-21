package runfailure

import (
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// TestEveryReasonHasAMessage is the pin that makes the mapping single-owner:
// a reason added to the taxonomy without user-facing copy fails here rather
// than silently degrading to the unknown sentence in production.
func TestEveryReasonHasAMessage(t *testing.T) {
	t.Parallel()

	for _, reason := range AllReasons() {
		message, ok := reasonMessages[reason]
		if !ok {
			t.Errorf("reason %q has no user-facing message; add one to reasonMessages", reason)
			continue
		}
		if strings.TrimSpace(message) == "" {
			t.Errorf("reason %q has an empty message", reason)
		}
	}
}

func TestMessagesReadAsSentences(t *testing.T) {
	t.Parallel()

	for _, reason := range AllReasons() {
		message := Message(reason)
		if first := rune(message[0]); first < 'A' || first > 'Z' {
			t.Errorf("reason %q message does not start with a capital: %q", reason, message)
		}
		if !strings.HasSuffix(message, ".") {
			t.Errorf("reason %q message does not end with a full stop: %q", reason, message)
		}
	}
}

// TestMessageLeaksNoWireVocabulary guards the split this package exists to
// keep: the message is broadcast on the realtime socket, so it must not carry
// the raw classifier string or provider-shaped detail a person cannot act on.
func TestMessageLeaksNoWireVocabulary(t *testing.T) {
	t.Parallel()

	for _, reason := range AllReasons() {
		message := Message(reason)
		if strings.Contains(message, reason.String()) {
			t.Errorf("reason %q message embeds the wire reason: %q", reason, message)
		}
		if strings.Contains(message, "_") {
			t.Errorf("reason %q message contains a snake_case token: %q", reason, message)
		}
	}
}

func TestMessageFallsBackForUnknownReason(t *testing.T) {
	t.Parallel()

	got := Message(Reason("agent_error.invented_tomorrow"))
	if got != Message(ReasonAgentUnknown) {
		t.Fatalf("unrecognized reason = %q, want the unknown-reason sentence", got)
	}
}

// TestQuotaMessageDoesNotPromiseARetry pins the one distinction that matters
// most to a person staring at a failed run: whether waiting will help. Quota
// is the reason where the wrong answer wastes the most time.
func TestQuotaMessageDoesNotPromiseARetry(t *testing.T) {
	t.Parallel()

	message := Message(ReasonAgentProviderQuotaLimit)
	if !strings.Contains(message, "not help") {
		t.Fatalf("quota message should say retrying will not help, got %q", message)
	}
	if strings.Contains(Message(ReasonAgentProviderCapacityOrRateLimit), "not help") {
		t.Fatal("rate-limit message must not borrow the quota wording; it IS retryable")
	}
}

// TestMessageForNamesTheProvider pins the rule that a person reading a failed
// run must learn which provider failed, not "the agent provider". The generic
// form stays for callers without a provider.
func TestMessageForNamesTheProvider(t *testing.T) {
	t.Parallel()

	got := MessageFor(ReasonAgentProviderQuotaLimit, "codex")
	if !strings.HasPrefix(got, "Codex ") {
		t.Fatalf("MessageFor(quota, codex) = %q, want it to open with the provider name", got)
	}
	if strings.Contains(got, "agent provider") {
		t.Fatalf("MessageFor(quota, codex) still says 'agent provider': %q", got)
	}
	if got := MessageFor(ReasonAgentProviderAuthOrAccess, "claude"); !strings.Contains(got, "Claude") {
		t.Fatalf("MessageFor(auth, claude) = %q, want the provider name", got)
	}
	if got, want := MessageFor(
		ReasonAgentProviderQuotaLimit,
		"",
	), Message(
		ReasonAgentProviderQuotaLimit,
	); got != want {
		t.Fatalf("MessageFor with no provider = %q, want the generic sentence %q", got, want)
	}
	if got := MessageFor(
		ReasonAgentProviderQuotaLimit,
		"unheard-of",
	); !strings.HasPrefix(
		got,
		"The agent provider",
	) {
		t.Fatalf("MessageFor with an unknown provider = %q, want the generic form", got)
	}
}

func TestMessagesForEveryProviderReadAsSentences(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"", "claude", "codex"} {
		for _, reason := range AllReasons() {
			message := MessageFor(reason, provider)
			if first := rune(message[0]); first < 'A' || first > 'Z' {
				t.Errorf(
					"provider %q reason %q message does not start with a capital: %q",
					provider,
					reason,
					message,
				)
			}
			if strings.Contains(message, "{") {
				t.Errorf(
					"provider %q reason %q message leaked a template token: %q",
					provider,
					reason,
					message,
				)
			}
		}
	}
}

// TestDaemonLiteralReasonsHaveAMessage covers the two values the daemon writes
// that are not in the 22-value taxonomy, so they do not degrade to the
// unclassified sentence.
func TestDaemonLiteralReasonsHaveAMessage(t *testing.T) {
	t.Parallel()

	for _, reason := range []Reason{ReasonIdleWatchdog, ReasonCancelled} {
		if Message(reason) == Message(ReasonAgentUnknown) {
			t.Errorf("reason %q falls back to the unclassified sentence", reason)
		}
		if reason.IsAgentError() {
			t.Errorf("daemon disposition %q classified as an agent error", reason)
		}
		for _, canonical := range AllReasons() {
			if canonical == reason {
				t.Errorf("daemon disposition %q leaked into the 22-value taxonomy", reason)
			}
		}
	}
}

func TestProviderLabelsCoverSupportedProviders(t *testing.T) {
	t.Parallel()

	for _, provider := range protocol.SupportedProviders {
		if _, ok := providerLabels[provider]; !ok {
			t.Errorf("provider %q has no display label; add it to providerLabels", provider)
		}
	}
	if len(providerLabels) != len(protocol.SupportedProviders) {
		t.Errorf(
			"providerLabels has %d entries, protocol.SupportedProviders has %d",
			len(providerLabels),
			len(protocol.SupportedProviders),
		)
	}
}
