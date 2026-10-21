package runfailure

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// detailMaxRunes bounds what Detail returns. Provider text is usually one or
// two sentences; anything longer is a stack trace or a JSON dump a person
// would not read in a chat bubble.
const detailMaxRunes = 280

// Detail turns the raw text a run failed with (runs.error) into one bounded,
// readable line a surface may show beside MessageFor's sentence.
//
// The sentence says what kind of thing went wrong and who can act; the detail
// is the provider's or daemon's own words — "try again at Sep 22nd", the exact
// model name that was rejected, the link to a billing page. It is the part a
// generic sentence cannot carry, and the part a person needs to fix it.
//
// The text has already passed through redact.Diagnostic before it was
// persisted, so this is shaping, not redaction: a JSON error envelope yields
// its message field, control characters and repeated whitespace collapse to
// one space, the redaction marker is dropped, the first letter is capitalized,
// the line ends with a full stop, and the whole thing is cut at a word
// boundary with an ellipsis when it is too long. The daemon's bounded
// preparation codes (run.preparation_failed.<phase>) are replaced by a plain
// sentence for the phase, because the code is the only thing the daemon sends
// for them. An empty input yields "".
func Detail(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if IsPreparationCode(text) {
		return preparationPhaseDetail(text)
	}
	if message := jsonErrorMessage(text); message != "" {
		text = message
	}
	text = strings.ReplaceAll(text, "[truncated:diagnostic]", " ")
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if text == "" {
		return ""
	}
	text = capitalizeFirst(text)
	if runes := []rune(text); len(runes) > detailMaxRunes {
		// Cut in runes, never bytes: a byte offset can land inside a
		// multi-byte rune and the invalid UTF-8 would be refused by
		// PostgreSQL when the inbox body is written.
		head := runes[:detailMaxRunes-1]
		if space := lastIndexRune(head, ' '); space > 0 {
			head = head[:space]
		}
		return strings.TrimRight(string(head), " .,;:") + "…"
	}
	if last, _ := utf8.DecodeLastRuneInString(text); !strings.ContainsRune(".!?…", last) {
		text += "."
	}
	return text
}

// IsPreparationCode reports whether a run's failure text is one of the
// daemon's bounded preparation diagnostics rather than a provider's words, so
// a surface labels it neutrally ("Details:") instead of quoting the provider.
func IsPreparationCode(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), preparationFailedPrefix)
}

func lastIndexRune(runes []rune, target rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

// jsonErrorMessage reads the human sentence out of a provider's JSON error
// envelope — {"message": …}, {"error": {"message": …}}, or {"error": "…"} —
// wherever it starts in the text, since a CLI often prefixes it ("API Error:
// 529 {…}"). Anything else returns "" and the caller keeps the raw text.
func jsonErrorMessage(text string) string {
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}
	text = text[start:]
	var envelope struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return ""
	}
	if envelope.Message != "" {
		return envelope.Message
	}
	if len(envelope.Error) == 0 {
		return ""
	}
	var nested struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(envelope.Error, &nested); err == nil && nested.Message != "" {
		return nested.Message
	}
	var plain string
	if err := json.Unmarshal(envelope.Error, &plain); err == nil {
		return plain
	}
	return ""
}

// preparationFailedPrefix is the bounded diagnostic the daemon reports in
// place of the raw cause when a run fails before the agent process starts
// (daemon/run_preparation.go). The phase follows a dot.
const preparationFailedPrefix = "run.preparation_failed"

// PreparationPhase is the step of run preparation that failed. The daemon
// reports it through PreparationCode; Detail explains it in plain words.
// One vocabulary, owned here, so the daemon cannot spell a phase this
// package has no sentence for.
type PreparationPhase string

const (
	PreparationPhasePrepare              PreparationPhase = "prepare"
	PreparationPhaseProviderAvailability PreparationPhase = "provider_availability"
	PreparationPhaseSkillSnapshot        PreparationPhase = "skill_snapshot"
	PreparationPhaseConnector            PreparationPhase = "connector"
	PreparationPhaseWorkspace            PreparationPhase = "workspace"
	PreparationPhaseExecution            PreparationPhase = "execution"
	PreparationPhaseStartAuthority       PreparationPhase = "start_authority"
	PreparationPhaseProviderBackend      PreparationPhase = "provider_backend"
	PreparationPhaseProviderSpawn        PreparationPhase = "provider_spawn"
)

// PreparationPhases lists every phase, in preparation order.
func PreparationPhases() []PreparationPhase {
	return []PreparationPhase{
		PreparationPhasePrepare, PreparationPhaseProviderAvailability, PreparationPhaseSkillSnapshot,
		PreparationPhaseConnector, PreparationPhaseWorkspace, PreparationPhaseExecution,
		PreparationPhaseStartAuthority, PreparationPhaseProviderBackend, PreparationPhaseProviderSpawn,
	}
}

// PreparationCode is the diagnostic the daemon records for a failure in
// phase; the bare prefix when the phase is unknown.
func PreparationCode(phase PreparationPhase) string {
	if phase == "" {
		return preparationFailedPrefix
	}
	return preparationFailedPrefix + "." + string(phase)
}

// preparationPhaseDetails names each phase in plain words.
var preparationPhaseDetails = map[PreparationPhase]string{
	PreparationPhasePrepare:              "The run could not be prepared on the machine running this agent.",
	PreparationPhaseProviderAvailability: "The provider CLI was not available on the machine running this agent.",
	PreparationPhaseSkillSnapshot:        "The agent's skills could not be prepared on the machine running this agent.",
	PreparationPhaseConnector:            "A connector this agent needs could not be prepared.",
	PreparationPhaseWorkspace:            "The workspace for this run could not be prepared on the machine running this agent.",
	PreparationPhaseExecution:            "The execution environment for this run could not be prepared.",
	PreparationPhaseStartAuthority:       "The daemon could not get permission from the server to start this run.",
	PreparationPhaseProviderBackend:      "The provider backend could not be prepared on the machine running this agent.",
	PreparationPhaseProviderSpawn:        "The provider process could not be started on the machine running this agent.",
}

// DetailLabel says whose words a detail is. A provider's own failure with
// the provider's own sentence is "<Provider> said"; a failure Zenith Team
// classified for a provider it does not name is "The provider said"; every
// other detail, including a preparation diagnostic, is plain "Details".
func DetailLabel(reason Reason, provider, raw string) string {
	if !strings.HasPrefix(string(reason), "agent_error.provider_") || IsPreparationCode(raw) {
		return "Details"
	}
	if label, ok := providerLabels[provider]; ok {
		return label + " said"
	}
	return "The provider said"
}

func preparationPhaseDetail(code string) string {
	phase := PreparationPhase(strings.TrimPrefix(strings.TrimPrefix(code, preparationFailedPrefix), "."))
	if detail, ok := preparationPhaseDetails[phase]; ok {
		return detail
	}
	return preparationPhaseDetails["prepare"]
}
