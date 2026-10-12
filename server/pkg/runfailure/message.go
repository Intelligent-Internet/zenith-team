package runfailure

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Message returns the user-facing sentence for a failure reason when the
// provider is not known. MessageFor is the form every surface that knows the
// run's provider should use.
//
// A Reason is a classifier: it is stable, machine-readable, and deliberately
// terse. It is not something to show a person. The raw provider text is not
// showable as-is either — it is free-form, sometimes several hundred
// characters of JSON — so without this mapping every surface that wants to
// explain a failed run has to invent its own copy from an enum, and they
// drift. Detail is the bounded, cleaned form of that raw text and rides
// beside the sentence, never inside it.
//
// This is that copy, owned once. The rules it follows:
//
//   - Say what happened, then who can act. A person reading it should know
//     whether to wait, to fix something, or to do nothing.
//   - Name the provider when the caller knows it ("Codex", "Claude"); fall
//     back to "the agent provider" otherwise. Owner decision 2026-09-16: a
//     founder must learn which account or CLI to fix without opening logs.
//   - No blame, no links, no ids. Surfaces add context; this is the sentence.
//   - Never embed provider text. Detail carries it separately, so a surface
//     can choose to show the sentence alone.
//
// An unrecognized reason returns the same sentence as ReasonAgentUnknown, so
// a reason added to the taxonomy without a message here degrades to a vague
// but honest string rather than an empty one. TestEveryReasonHasAMessage
// keeps that from being the normal case.
func Message(r Reason) string {
	return MessageFor(r, "")
}

// MessageFor renders the sentence for a reason, naming the provider when it
// is one Zenith Team knows (the wire vocabulary "claude" | "codex"). Any other
// value, including empty, renders the generic form.
func MessageFor(r Reason, provider string) string {
	template, ok := reasonMessages[r]
	if !ok {
		template = reasonMessages[ReasonAgentUnknown]
	}
	return capitalizeFirst(strings.ReplaceAll(template, providerToken, ProviderLabel(provider)))
}

// ProviderLabel is the display name of a provider ("Codex", "Claude"), or
// "the agent provider" for any other value, including empty. Other refusal
// copy that names the provider uses it so every surface spells it alike.
func ProviderLabel(provider string) string {
	name, known := providerLabels[strings.ToLower(strings.TrimSpace(provider))]
	if !known {
		return genericProviderLabel
	}
	return name
}

// providerToken is the placeholder reasonMessages use wherever the provider
// is named. Every sentence that starts with it is capitalized after
// substitution, which is what lets one template serve "Codex …" and "The
// agent provider …".
const (
	providerToken        = "{provider}"
	genericProviderLabel = "the agent provider"
)

// providerLabels is the display form of protocol.SupportedProviders. It is
// spelled here rather than imported so this package stays dependency-free;
// TestProviderLabelsCoverSupportedProviders pins the two vocabularies
// together.
var providerLabels = map[string]string{
	"claude": "Claude",
	"codex":  "Codex",
}

func capitalizeFirst(sentence string) string {
	first, size := utf8.DecodeRuneInString(sentence)
	if first == utf8.RuneError || unicode.IsUpper(first) {
		return sentence
	}
	return string(unicode.ToUpper(first)) + sentence[size:]
}

// reasonMessages is keyed by every Reason in allReasons, plus the two
// literals the daemon writes outside the taxonomy (idle_watchdog when the
// agent went quiet, cancelled when a stop was acknowledged as a failure). The
// ordering mirrors allReasons so the two stay diffable side by side.
var reasonMessages = map[Reason]string{
	// Platform / scheduler side.
	ReasonQueuedExpired: "This run waited too long for a machine to pick it up and was cancelled. " +
		"Start the Zenith Team daemon on the machine that runs your agents, then try again.",
	ReasonRuntimeOffline: "The machine running this agent went offline before the run finished. " +
		"Bring it back online and try again.",
	ReasonRuntimeRecovery: "The daemon restarted while this run was in flight, so the run was ended. " +
		"Nothing was lost — try again.",
	ReasonTimeout: "This run ran longer than its time limit and was stopped.",
	ReasonIterationLimit: "The agent reached the maximum number of steps allowed for one run. " +
		"Narrowing the request usually lets it finish.",
	ReasonAgentBlocked:      "The agent stopped to wait for a person. It is not an error.",
	ReasonAPIInvalidRequest: providerToken + " rejected this request as malformed, so the run could not continue.",
	ReasonSkillBundleUnavailable: "The daemon could not download this agent's skills, so the agent never started. " +
		"This is usually a network or proxy problem on that machine; retrying is cheap.",
	ReasonRunContextInvalid: "Zenith Team handed this run a context document the machine running this agent could not accept, " +
		"so the agent never started. Send the message again; if it keeps happening, the server and the daemon are out of step.",
	ReasonNoEligibleResolver: "The agent asked for a decision that nobody who can see this work may make, so the run stopped. " +
		"Nothing is wrong with " + providerToken + ". Ask someone who manages this work to let a person who can see it decide, " +
		"then reply in the chat to go on.",

	// Agent process side: provider errors.
	ReasonAgentProviderAuthOrAccess: providerToken + " rejected the credentials on the machine running this agent. " +
		"Sign in to " + providerToken + " again on that machine, then try the run.",
	ReasonAgentProviderQuotaLimit: providerToken + " has hit its usage limit or run out of credit. " +
		"Retrying will not help until the plan or balance is topped up.",
	ReasonAgentProviderCapacityOrRateLimit: providerToken + " is rate-limiting or out of capacity right now. " +
		"This usually clears on its own — try again shortly.",
	ReasonAgentProviderServerError: providerToken + " returned a server error. " +
		"This is on their side and usually clears on its own.",
	ReasonAgentProviderNetwork: "The connection to " + providerToken + " dropped mid-run. " +
		"Check the network on the machine running this agent, then try again.",

	// Agent process side: agent / runner errors.
	ReasonAgentProcessFailure:           "The agent process stopped unexpectedly before it finished.",
	ReasonAgentEmptyOrUnparseableOutput: "The agent finished without producing any output this run could use.",
	ReasonAgentTimeout:                  "The agent ran longer than its time limit and was stopped.",
	ReasonAgentContextOverflow: "This conversation grew past what the model can hold in one run. " +
		"Starting a fresh conversation, or a shorter request, will get past it.",

	// Agent process side: configuration and runtime.
	ReasonAgentMissingConfig: "The agent is missing configuration it needs to run — " +
		"typically a provider credential or model setting on the machine running it.",
	ReasonAgentModelNotFoundOrUnavailable: "The model this agent is set to use is not available to " +
		providerToken + "'s account. Choose a different model for this agent.",
	ReasonAgentRuntimeVersionUnsupported: providerToken + "'s CLI on the machine running this agent " +
		"is too old for Zenith Team. Update it, then try again.",
	ReasonAgentRuntimeMissingExecutable: providerToken + "'s CLI is not installed on the machine " +
		"running this agent, or is not on its PATH.",

	ReasonAgentUnknown: "The agent stopped with an error Zenith Team could not classify.",

	// Daemon dispositions outside the taxonomy.
	ReasonIdleWatchdog: "The agent made no progress for a long time and was stopped. " +
		"Try again, or narrow the request.",
	ReasonCancelled: "This run was stopped before it finished.",
}
