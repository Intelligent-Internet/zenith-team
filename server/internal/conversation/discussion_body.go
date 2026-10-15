package conversation

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// encoding/json replaces malformed surrogates with U+FFFD. Reject them before
// decoding, so the stored document and its retry fingerprint retain exact text.
func (b *DiscussionBody) UnmarshalJSON(data []byte) error {
	if !utf8.Valid(data) {
		return ErrDiscussionInvalid
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) || data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return ErrDiscussionInvalid
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return ErrDiscussionInvalid
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return ErrDiscussionInvalid
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || string(data[i+1:i+3]) != "\\u" {
				return ErrDiscussionInvalid
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return ErrDiscussionInvalid
			}
			i += 6
		}
	}
	type document DiscussionBody
	var value document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*b = DiscussionBody(value)
	return nil
}

type normalizedDiscussionSend struct {
	Input       DiscussionSendInput
	Body        []byte
	Content     string
	Fingerprint []byte
}

type discussionReference struct {
	ConnectorID    *uuid.UUID `json:"connectorId,omitempty"`
	Start          int        `json:"start"`
	End            int        `json:"end"`
	Type           string     `json:"type"`
	UserID         *uuid.UUID `json:"userId,omitempty"`
	AgentID        *uuid.UUID `json:"agentId,omitempty"`
	AttachmentID   *uuid.UUID `json:"attachmentId,omitempty"`
	AssetVersionID *uuid.UUID `json:"assetVersionId,omitempty"`
	SkillID        *uuid.UUID `json:"skillId,omitempty"`
	VersionID      *uuid.UUID `json:"versionId,omitempty"`
	Scope          string     `json:"scope,omitempty"`
}

func decodeDiscussionReferences(body DiscussionBody) ([]discussionReference, error) {
	if len(body.References) > 100 {
		return nil, ErrDiscussionLimit
	}
	references := make([]discussionReference, 0, len(body.References))
	previousEnd := 0
	maxUTF16 := utf16Len(body.Markdown)
	for _, raw := range body.References {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var reference discussionReference
		if err := decoder.Decode(&reference); err != nil {
			return nil, ErrDiscussionInvalid
		}
		if reference.Start < 0 || reference.Start >= reference.End || reference.End > maxUTF16 ||
			reference.Start < previousEnd {
			return nil, ErrDiscussionInvalid
		}
		if !discussionReferenceRangeAllowed(body.Markdown, reference.Start, reference.End) {
			return nil, ErrDiscussionInvalid
		}
		if reference.Type == "user" || reference.Type == "agent" || reference.Type == "broadcast" {
			encoded := utf16.Encode([]rune(body.Markdown))
			label := string(utf16.Decode(encoded[reference.Start:reference.End]))
			if !strings.HasPrefix(label, "@") || len(label) < 2 ||
				(reference.Type == "broadcast" && label != "@"+reference.Scope) {
				return nil, ErrDiscussionInvalid
			}
		}
		if reference.Type == "skill" || reference.Type == "connector" {
			encoded := utf16.Encode([]rune(body.Markdown))
			label := string(utf16.Decode(encoded[reference.Start:reference.End]))
			if !strings.HasPrefix(label, "$") || len(label) < 2 {
				return nil, ErrDiscussionInvalid
			}
		}
		if !validDiscussionReferenceShape(reference) {
			return nil, ErrDiscussionInvalid
		}
		previousEnd = reference.End
		references = append(references, reference)
	}
	return references, nil
}

// References use UTF-16 offsets, but only scalar boundaries in ordinary text
// are addressable. This also prevents mentions from being attached to code or
// link nodes where the displayed text is not a chat target.
func discussionReferenceRangeAllowed(markdown string, start, end int) bool {
	bytesAt := make([]int, 0, utf16Len(markdown)+1)
	validBoundary := make([]bool, 0, utf16Len(markdown)+1)
	bytesAt = append(bytesAt, 0)
	validBoundary = append(validBoundary, true)
	for offset, r := range markdown {
		width := 1
		if r > 0xffff {
			width = 2
		}
		for i := range width {
			bytesAt = append(bytesAt, offset+len(string(r)))
			validBoundary = append(validBoundary, width == 1 || i == width-1)
		}
	}
	if start >= len(bytesAt) || end >= len(bytesAt) || !validBoundary[start] || !validBoundary[end] {
		return false
	}
	bs, be := bytesAt[start], bytesAt[end]

	source := []byte(markdown)
	slashes := 0
	for i := bs - 1; i >= 0 && source[i] == '\\'; i-- {
		slashes++
	}
	if slashes%2 != 0 {
		return false
	}
	document := goldmark.DefaultParser().Parse(text.NewReader(source))
	allowed := false
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.CodeSpan,
			*ast.CodeBlock,
			*ast.FencedCodeBlock,
			*ast.Blockquote,
			*ast.Link,
			*ast.AutoLink,
			*ast.Image,
			*ast.RawHTML,
			*ast.HTMLBlock:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			if n.Segment.Start <= bs && n.Segment.Stop >= be {
				allowed = true
				return ast.WalkStop, nil
			}
		}
		return ast.WalkContinue, nil
	})
	return allowed
}

func validDiscussionReferenceShape(reference discussionReference) bool {
	count := 0
	for _, id := range []*uuid.UUID{reference.UserID, reference.AgentID, reference.AttachmentID, reference.AssetVersionID, reference.SkillID, reference.VersionID} {
		if id != nil && *id == uuid.Nil {
			return false
		}
	}
	if reference.UserID != nil {
		count++
	}
	if reference.AgentID != nil {
		count++
	}
	if reference.AttachmentID != nil {
		count++
	}
	if reference.AssetVersionID != nil {
		count++
	}
	if reference.SkillID != nil {
		count++
	}
	if reference.VersionID != nil {
		count++
	}
	if reference.ConnectorID != nil {
		count++
	}
	switch reference.Type {
	case "connector":
		return count == 1 && reference.ConnectorID != nil && *reference.ConnectorID != uuid.Nil &&
			reference.Scope == ""
	case "skill":
		return count == 2 && reference.SkillID != nil && reference.VersionID != nil && reference.Scope == ""
	case "user":
		return count == 1 && reference.UserID != nil && reference.Scope == ""
	case "agent":
		return count == 1 && reference.AgentID != nil && reference.Scope == ""
	case "attachment":
		return count == 1 && reference.AttachmentID != nil && reference.Scope == ""
	case "asset_version":
		return count == 1 && reference.AssetVersionID != nil && reference.Scope == ""
	case "broadcast":
		return count == 0 && (reference.Scope == "everyone" || reference.Scope == "here")
	default:
		return false
	}
}

func utf16Len(value string) int {
	length := 0
	for _, r := range value {
		if r > 0xffff {
			length += 2
		} else {
			length++
		}
	}
	return length
}

func normalizeDiscussionSend(input DiscussionSendInput) (normalizedDiscussionSend, error) {
	if input.RequestKey == uuid.Nil || input.Body.Version != 1 ||
		len(input.AttachmentIDs) > maxMessageAttachments || len(input.ArtifactIDs) > 10 ||
		(input.ThreadRootID != nil && *input.ThreadRootID == uuid.Nil) {
		return normalizedDiscussionSend{}, ErrDiscussionInvalid
	}
	// A Whiteboard is made at the chat's top level, never in a thread.
	if input.Whiteboard != nil {
		title := strings.TrimSpace(input.Whiteboard.Title)
		if title == "" || utf8.RuneCountInString(title) > 200 || input.ThreadRootID != nil {
			return normalizedDiscussionSend{}, ErrDiscussionInvalid
		}
		input.Whiteboard = &DiscussionWhiteboard{Title: title}
	}
	for _, ids := range [][]uuid.UUID{input.AttachmentIDs, input.ArtifactIDs} {
		seen := map[uuid.UUID]bool{}
		for _, id := range ids {
			if id == uuid.Nil || seen[id] {
				return normalizedDiscussionSend{}, ErrDiscussionInvalid
			}
			seen[id] = true
		}
	}
	if input.AttachmentIDs == nil {
		input.AttachmentIDs = []uuid.UUID{}
	}
	source := input.Body.Markdown
	if !utf8.ValidString(source) ||
		(strings.TrimSpace(source) == "" && len(input.AttachmentIDs) == 0 && len(input.ArtifactIDs) == 0 &&
			input.Whiteboard == nil) {
		return normalizedDiscussionSend{}, ErrDiscussionInvalid
	}
	for _, r := range source {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return normalizedDiscussionSend{}, ErrDiscussionInvalid
		}
	}
	references, err := decodeDiscussionReferences(input.Body)
	if err != nil {
		return normalizedDiscussionSend{}, err
	}
	input.Body.References = make([]json.RawMessage, 0, len(references))
	for _, reference := range references {
		encoded, marshalErr := json.Marshal(reference)
		if marshalErr != nil {
			return normalizedDiscussionSend{}, fmt.Errorf("encode discussion reference: %w", marshalErr)
		}
		input.Body.References = append(input.Body.References, encoded)
	}
	body, err := json.Marshal(input.Body)
	if err != nil {
		return normalizedDiscussionSend{}, fmt.Errorf("encode discussion body: %w", err)
	}
	if len(body) > maxDiscussionBodyBytes {
		return normalizedDiscussionSend{}, ErrDiscussionLimit
	}
	content, err := discussionText(source)
	if err != nil {
		return normalizedDiscussionSend{}, err
	}
	if input.AttachmentIDs == nil {
		input.AttachmentIDs = []uuid.UUID{}
	}
	canonical, err := json.Marshal(struct {
		Operation     string                `json:"operation"`
		Version       int                   `json:"version"`
		Body          DiscussionBody        `json:"body"`
		AttachmentIDs []uuid.UUID           `json:"attachmentIds"`
		ArtifactIDs   []uuid.UUID           `json:"artifactIds,omitempty"`
		ThreadRootID  *uuid.UUID            `json:"threadRootId"`
		Whiteboard    *DiscussionWhiteboard `json:"whiteboard,omitempty"`
	}{"discussion.send", 1, input.Body, input.AttachmentIDs, input.ArtifactIDs, input.ThreadRootID, input.Whiteboard})
	if err != nil {
		return normalizedDiscussionSend{}, fmt.Errorf("encode send fingerprint: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return normalizedDiscussionSend{Input: input, Body: body, Content: content, Fingerprint: digest[:]}, nil
}

func discussionText(markdown string) (string, error) {
	source := []byte(markdown)
	parser := goldmark.New(goldmark.WithExtensions(extension.Strikethrough, extension.Table))
	document := parser.Parser().Parse(text.NewReader(source))
	var plain strings.Builder
	err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if node.Type() == ast.TypeBlock && plain.Len() > 0 {
				plain.WriteByte('\n')
			}
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Document,
			*ast.Paragraph,
			*ast.TextBlock,
			*ast.List,
			*ast.ListItem,
			*ast.Blockquote,
			*ast.Emphasis,
			*ast.CodeSpan:
		case *ast.Text:
			plain.Write(n.Segment.Value(source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				plain.WriteByte('\n')
			}
		case *ast.String:
			plain.Write(n.Value)
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			for i := 0; i < node.Lines().Len(); i++ {
				line := node.Lines().At(i)
				plain.Write(line.Value(source))
			}
		case *ast.Link:
			if !discussionLinkAllowed(string(n.Destination)) {
				return ast.WalkStop, ErrDiscussionInvalid
			}
		case *ast.AutoLink:
			if !discussionLinkAllowed(string(n.URL(source))) {
				return ast.WalkStop, ErrDiscussionInvalid
			}
			plain.Write(n.Label(source))
		default:
			if node.Kind().String() != "Strikethrough" {
				return ast.WalkStop, ErrDiscussionInvalid
			}
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(plain.String()), nil
}

func discussionLinkAllowed(destination string) bool {
	parsed, err := url.Parse(destination)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" &&
		parsed.User == nil
}

func boundedDiscussionLabel(value string) string {
	runes := []rune(value)
	if len(runes) > 256 {
		return string(runes[:256])
	}
	return value
}
