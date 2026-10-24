package wiki

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/google/uuid"
)

// SourcePreviewPatch is a byte edit for an explicitly negotiated Language 1
// preview stream. Its host must prove the live Run, conversation and generation
// before calling ApplySourcePreviewPatch; this value grants no write authority.
type SourcePreviewPatch struct {
	RequestKey       uuid.UUID `json:"requestKey"`
	ProtocolVersion  int       `json:"protocolVersion"`
	LanguageVersion  int       `json:"languageVersion"`
	ExpectedRevision int64     `json:"expectedRevision"`
	Start            int       `json:"start"`
	DeleteBytes      int       `json:"deleteBytes"`
	Text             string    `json:"text"`
	Discard          bool      `json:"discard"`
}

// SourcePreviewState is separate from a Page's canonical document. A transport
// persists this bounded state under its own Run/generation identity and expiry.
// Only the latest request receipt is retained: retrying it is idempotent; older
// offsets are rejected and the sender must reconnect against the latest revision.
type SourcePreviewState struct {
	Revision        int64         `json:"revision"`
	Source          string        `json:"source"`
	Discarded       bool          `json:"discarded"`
	LastRequestKey  uuid.UUID     `json:"lastRequestKey"`
	LastRequestHash string        `json:"lastRequestHash"`
	Preview         SourcePreview `json:"preview"`
}

// ApplySourcePreviewPatch cannot save a Page or produce an accepted proposal.
// Even a Complete preview must pass ordinary final compilation and authorization.
func ApplySourcePreviewPatch(current SourcePreviewState, patch SourcePreviewPatch) (SourcePreviewState, error) {
	if patch.ProtocolVersion != 1 || patch.LanguageVersion != 1 {
		return SourcePreviewState{}, uiError("preview.version")
	}
	if patch.RequestKey == uuid.Nil || patch.ExpectedRevision < 0 || patch.Start < 0 || patch.DeleteBytes < 0 ||
		len(patch.Text) > 65536 || !utf8.ValidString(patch.Text) {
		return SourcePreviewState{}, uiError("preview.patch")
	}
	if patch.Discard && (patch.Start != 0 || patch.DeleteBytes != 0 || patch.Text != "") {
		return SourcePreviewState{}, uiError("preview.patch")
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		return SourcePreviewState{}, uiError("preview.patch")
	}
	hash := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(hash[:])
	if current.LastRequestKey == patch.RequestKey {
		if current.LastRequestHash != fingerprint {
			return SourcePreviewState{}, uiError("preview.conflict")
		}
		return current, nil
	}
	if current.Discarded || patch.ExpectedRevision != current.Revision {
		return SourcePreviewState{}, uiError("preview.conflict")
	}
	// Bound aggregate stream work as well as each individual compilation. A host
	// may admit another stream generation, but never silently resets this revision.
	if current.Revision >= 4096 {
		return SourcePreviewState{}, uiError("preview.limit")
	}
	next := SourcePreviewState{
		Revision:        current.Revision + 1,
		LastRequestKey:  patch.RequestKey,
		LastRequestHash: fingerprint,
		Discarded:       patch.Discard,
	}
	if patch.Discard {
		next.Preview.Diagnostics = []SourceDiagnostic{}
		return next, nil
	}
	if patch.Start > len(current.Source) || patch.DeleteBytes > len(current.Source)-patch.Start {
		return SourcePreviewState{}, uiError("preview.offset")
	}
	end := patch.Start + patch.DeleteBytes
	if patch.Start < len(current.Source) && !utf8.RuneStart(current.Source[patch.Start]) ||
		end < len(current.Source) && !utf8.RuneStart(current.Source[end]) {
		return SourcePreviewState{}, uiError("preview.offset")
	}
	if len(current.Source)-patch.DeleteBytes+len(patch.Text) > 262144 {
		return SourcePreviewState{}, uiError("preview.size")
	}
	next.Source = current.Source[:patch.Start] + patch.Text + current.Source[end:]
	next.Preview = PreviewSource(next.Source, patch.LanguageVersion)
	return next, nil
}
