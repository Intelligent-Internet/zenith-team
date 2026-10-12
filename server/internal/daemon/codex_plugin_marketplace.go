package daemon

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// codexPluginMaximumMarketplaceManifestBytes bounds the marketplace manifest
// read for listings; the Claude marketplace manifest is about 190 KB.
const codexPluginMaximumMarketplaceManifestBytes = 4 << 20

// codexFirstPartyMarketplacePrefix names OpenAI's own marketplaces
// (openai-primary-runtime, openai-bundled, openai-curated-remote). Their
// plugins rank ahead of every other marketplace's.
const codexFirstPartyMarketplacePrefix = "openai-"

// codexMarketplaceListing is what a marketplace's own manifest says about one
// of its plugins, sanitized by the rules of the Codex interface field it
// stands in for. Codex copies little of a Claude-format marketplace into
// plugin/list (no short description, rarely a display name), so discovery
// completes a plugin's interface from it; Codex's own values always win.
type codexMarketplaceListing struct {
	DisplayName   string
	Description   string
	DeveloperName string
	WebsiteURL    string
}

// codexPluginNeedsListing reports whether a plugin's interface lacks the
// display name or short description, the case in which discovery reads its
// marketplace's manifest.
func codexPluginNeedsListing(summary codexPluginSummary) bool {
	pluginInterface := summary.Interface
	return pluginInterface == nil ||
		pluginInterface.DisplayName == nil ||
		sanitizeCodexConnectorText(*pluginInterface.DisplayName, codexConnectorMaximumName) == "" ||
		pluginInterface.ShortDescription == nil ||
		sanitizeCodexConnectorText(
			*pluginInterface.ShortDescription,
			codexConnectorMaximumDescription,
		) == ""
}

// readCodexMarketplaceListings reads the plugin entries of the marketplace
// manifest at the path plugin/list reported, keyed by plugin name. Only the
// Claude plugin-marketplace format (<root>/.claude-plugin/marketplace.json)
// is read, and only when that path is a regular file of at most 4 MiB inside
// the marketplace root (a symbolic link may not leave it). Any other path or
// format, or a file that does not parse, gives no listings: they are
// presentation only, so discovery never fails for them.
func readCodexMarketplaceListings(marketplacePath string) map[string]codexMarketplaceListing {
	if !validCodexPluginAssetPath(marketplacePath) {
		return nil
	}
	manifestPath := filepath.Clean(marketplacePath)
	if filepath.Base(manifestPath) != "marketplace.json" ||
		filepath.Base(filepath.Dir(manifestPath)) != ".claude-plugin" {
		return nil
	}
	rootDirectory := codexPluginMarketplaceRoot(manifestPath)
	relativePath, err := filepath.Rel(rootDirectory, manifestPath)
	if err != nil || !filepath.IsLocal(relativePath) {
		return nil
	}
	root, err := os.OpenRoot(rootDirectory)
	if err != nil {
		return nil
	}
	defer root.Close()
	file, err := root.Open(relativePath)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 ||
		info.Size() > codexPluginMaximumMarketplaceManifestBytes {
		return nil
	}
	content, err := io.ReadAll(
		io.LimitReader(file, codexPluginMaximumMarketplaceManifestBytes+1),
	)
	if err != nil || len(content) > codexPluginMaximumMarketplaceManifestBytes {
		return nil
	}
	return parseClaudeMarketplaceListings(content)
}

// parseClaudeMarketplaceListings decodes a Claude plugin-marketplace
// manifest ({name, owner, plugins: [{name, displayName?, description,
// author: {name}, homepage, ...}]}). An entry is decoded field by field, so
// a field of an unexpected type loses only that field; an entry without a
// valid name is skipped, and a name listed twice is dropped rather than
// guessed.
func parseClaudeMarketplaceListings(content []byte) map[string]codexMarketplaceListing {
	var manifest struct {
		Plugins []json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(content, &manifest) != nil || len(manifest.Plugins) == 0 {
		return nil
	}
	listings := make(map[string]codexMarketplaceListing, len(manifest.Plugins))
	duplicates := make(map[string]struct{})
	for _, raw := range manifest.Plugins {
		var entry map[string]json.RawMessage
		if json.Unmarshal(raw, &entry) != nil {
			continue
		}
		name := claudeMarketplaceString(entry["name"])
		if !validCodexConnectorNativeID(name) {
			continue
		}
		if _, duplicate := duplicates[name]; duplicate {
			continue
		}
		if _, duplicate := listings[name]; duplicate {
			delete(listings, name)
			duplicates[name] = struct{}{}
			continue
		}
		listings[name] = codexMarketplaceListing{
			DisplayName: sanitizeCodexConnectorText(
				claudeMarketplaceString(entry["displayName"]),
				codexConnectorMaximumName,
			),
			Description: sanitizeCodexConnectorText(
				claudeMarketplaceString(entry["description"]),
				codexConnectorMaximumDescription,
			),
			DeveloperName: sanitizeCodexPluginLine(
				claudeMarketplaceAuthorName(entry["author"]),
				120,
			),
			WebsiteURL: validatedCodexPluginPresentationURL(
				claudeMarketplaceString(entry["homepage"]),
			),
		}
	}
	return listings
}

func claudeMarketplaceString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// claudeMarketplaceAuthorName reads author.name; a bare author string is
// accepted as the name.
func claudeMarketplaceAuthorName(raw json.RawMessage) string {
	var author struct {
		Name json.RawMessage `json:"name"`
	}
	if len(raw) != 0 && json.Unmarshal(raw, &author) == nil {
		return claudeMarketplaceString(author.Name)
	}
	return claudeMarketplaceString(raw)
}

// codexFirstPartyMarketplace reports whether a marketplace is one of
// OpenAI's own.
func codexFirstPartyMarketplace(name string) bool {
	return strings.HasPrefix(name, codexFirstPartyMarketplacePrefix)
}

var (
	// codexPluginNameAcronyms spells the slug words that read as acronyms.
	codexPluginNameAcronyms = map[string]string{
		"api": "API", "ai": "AI", "mcp": "MCP", "ui": "UI", "ux": "UX",
		"sdk": "SDK", "cli": "CLI", "aws": "AWS", "gcp": "GCP", "pdf": "PDF",
		"sql": "SQL", "html": "HTML", "css": "CSS", "js": "JS", "ts": "TS",
		"lsp": "LSP", "seo": "SEO", "crm": "CRM", "erp": "ERP", "hr": "HR",
		"ios": "iOS", "macos": "macOS", "llm": "LLM", "ml": "ML", "qa": "QA",
		"ci": "CI", "cd": "CD", "db": "DB", "url": "URL", "json": "JSON",
		"yaml": "YAML", "3d": "3D", "2d": "2D", "tdd": "TDD", "e2e": "E2E",
		"md": "MD",
	}
	// codexPluginNameSmallWords stay lowercase except as the first word.
	codexPluginNameSmallWords = map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "as": {}, "at": {}, "by": {}, "for": {},
		"from": {}, "in": {}, "of": {}, "on": {}, "or": {}, "the": {}, "to": {},
		"with": {}, "via": {},
	}
)

// humanizedCodexPluginName is the name of a plugin that neither Codex nor its
// marketplace gave a display name, so the page does not show a raw slug. It
// is deterministic and at most codexConnectorMaximumName bytes:
//
//   - a slug that is the developer's name, ignoring case and separators,
//     takes the developer's spelling ("canva" by Canva is "Canva");
//   - a slug of several words that the description writes as one term with a
//     capital takes that spelling ("bigdata-com" is "Bigdata.com");
//   - otherwise the slug splits on "-" and "_", and each word takes the first
//     of: lowercase for a small word after the first; the spelling the
//     description, else the developer's name, gives it when that says more
//     than a capital initial ("HyperFrames", "API"; see
//     codexPluginNameCasings); a known acronym; a capital initial.
func humanizedCodexPluginName(slug, developerName, description string) string {
	key := codexPluginNameKey(slug)
	if key != "" && key == codexPluginNameKey(developerName) {
		if name := sanitizeCodexConnectorText(developerName, codexConnectorMaximumName); name != "" {
			return name
		}
	}
	tokens := strings.FieldsFunc(slug, func(character rune) bool {
		return character == '-' || character == '_'
	})
	if len(tokens) > 1 {
		if term := codexPluginDescriptionTerm(key, description); term != "" {
			return term
		}
	}
	descriptionCasings := codexPluginNameCasings(description)
	developerCasings := codexPluginNameCasings(developerName)
	words := make([]string, 0, len(tokens))
	for index, token := range tokens {
		lower := strings.ToLower(token)
		if _, small := codexPluginNameSmallWords[lower]; small && index > 0 {
			words = append(words, lower)
			continue
		}
		if casing, found := descriptionCasings[lower]; found {
			words = append(words, casing)
			continue
		}
		if casing, found := developerCasings[lower]; found {
			words = append(words, casing)
			continue
		}
		if acronym, found := codexPluginNameAcronyms[lower]; found {
			words = append(words, acronym)
			continue
		}
		first, size := utf8.DecodeRuneInString(token)
		words = append(words, string(unicode.ToUpper(first))+token[size:])
	}
	if name := sanitizeCodexConnectorText(
		strings.Join(words, " "),
		codexConnectorMaximumName,
	); name != "" {
		return name
	}
	return sanitizeCodexConnectorText(slug, codexConnectorMaximumName)
}

// codexPluginNameKey is a name folded for comparison: its letters and digits,
// lowercased, so "monday-com" and "monday.com" compare equal.
func codexPluginNameKey(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(unicode.ToLower(character))
		}
	}
	return builder.String()
}

// codexPluginDescriptionTerm finds the first whitespace-separated term of the
// description, without its surrounding punctuation, that folds to key and is
// printable ASCII with a capital letter ("Bigdata.com").
func codexPluginDescriptionTerm(key, description string) string {
	for _, field := range strings.Fields(description) {
		term := strings.TrimFunc(field, func(character rune) bool {
			return !unicode.IsLetter(character) && !unicode.IsDigit(character)
		})
		if term == "" || !strings.ContainsFunc(term, unicode.IsUpper) ||
			strings.ContainsFunc(term, func(character rune) bool {
				return character < ' ' || character > '~'
			}) ||
			codexPluginNameKey(term) != key {
			continue
		}
		return term
	}
	return ""
}

// codexPluginNameCasings maps each lowercased ASCII word of a text to the
// spelling a name should use for it, when the text says more than a capital
// initial: the first spelling that mixes cases with a capital after the
// first character ("HyperFrames", "iOS", "42Crunch"); else an all-capitals
// spelling ("API", "3D"), unless the text also writes the word with only a
// capital initial, which marks the capitals as emphasis ("Context7" beside
// "CONTEXT7"). All capitals inside an identifier or file name say nothing
// either: a word followed directly by "." or "_" and a letter or digit, or
// preceded by "_" ("CLAUDE.md", "CONTEXT7_API_KEY").
func codexPluginNameCasings(text string) map[string]string {
	mixed := make(map[string]string)
	capitals := make(map[string]string)
	initialOnly := make(map[string]struct{})
	for start := 0; start < len(text); {
		if !codexPluginNameByte(text[start]) {
			start++
			continue
		}
		end := start
		for end < len(text) && codexPluginNameByte(text[end]) {
			end++
		}
		word := text[start:end]
		lower := strings.ToLower(word)
		innerCapital := strings.ContainsFunc(word[1:], unicode.IsUpper)
		hasLower := strings.ContainsFunc(word, unicode.IsLower)
		identifier := start > 0 && text[start-1] == '_' ||
			end+1 < len(text) && (text[end] == '.' || text[end] == '_') &&
				codexPluginNameByte(text[end+1])
		switch {
		case innerCapital && hasLower:
			if _, seen := mixed[lower]; !seen {
				mixed[lower] = word
			}
		case innerCapital && !identifier:
			if _, seen := capitals[lower]; !seen {
				capitals[lower] = word
			}
		case unicode.IsUpper(rune(word[0])) && hasLower:
			initialOnly[lower] = struct{}{}
		}
		start = end
	}
	for lower, word := range capitals {
		if _, found := mixed[lower]; found {
			continue
		}
		if _, found := initialOnly[lower]; !found {
			mixed[lower] = word
		}
	}
	return mixed
}

// codexPluginNameByte reports whether a byte belongs to an ASCII word.
func codexPluginNameByte(character byte) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9'
}
