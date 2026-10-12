package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	skillSnapshotIndexFilename = "index.json"

	maxSkillFileCount         = 2_000
	maxSkillRegularFileBytes  = 16 << 20
	maxSkillInstructionsBytes = 1 << 20
	maxSkillPackageBytes      = 128 << 20
	maxSkillSearchResults     = 20
	maxSkillIndexBytes        = 192 << 20
)

var (
	ErrSkillSnapshotUnavailable  = errors.New("skill snapshot is unavailable")
	ErrSkillSearchQuery          = errors.New("skill search query is invalid")
	ErrSkillReferenceUnavailable = errors.New("skill reference is unavailable")

	skillKeyPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type skillSnapshotIndex struct {
	SchemaVersion int                  `json:"schemaVersion"`
	CompanyID     string               `json:"companyId"`
	RunID         uuid.UUID            `json:"runId"`
	CreatedAt     string               `json:"createdAt"`
	Skills        []skillSnapshotSkill `json:"skills"`
}

type skillSnapshotSkill struct {
	Key         string              `json:"key"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	ContentHash string              `json:"contentHash"`
	Files       []skillSnapshotFile `json:"files"`
}

type skillSnapshotFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Text bool   `json:"text"`
}

// SkillService owns the verified Skill snapshot: the searchable summaries and
// the indexed reference files served from it.
type SkillService interface {
	Close() error
	SearchSkills(context.Context, SearchSkillsInput) (SearchSkillsResult, error)
	LoadSkillReference(context.Context, LoadSkillReferenceInput) (LoadSkillReferenceResult, error)
}

var _ SkillService = (*DefaultSkillService)(nil)

type DefaultSkillService struct {
	root          *os.Root
	skills        map[string]skillSnapshotSkill
	embeddedFiles map[string]map[string][]byte
	order         []string
}

type SearchSkillsInput struct {
	Query string `json:"query" jsonschema:"Case-insensitive query matched against Skill keys, names, and descriptions. At most 256 UTF-8 bytes."`
}

type SkillSummary struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type SearchSkillsResult struct {
	Skills []SkillSummary `json:"skills"`
}

type LoadSkillReferenceInput struct {
	SkillKey          string `json:"skillKey"                    jsonschema:"Exact Skill key supplied by the Run or returned by search_skills."`
	Path              string `json:"path"                        jsonschema:"Exact indexed relative UTF-8 file path inside the Skill package."`
	IncludeReferences bool   `json:"includeReferences,omitempty" jsonschema:"When reading SKILL.md, explicitly request the full readable reference inventory. Omit for ordinary reads; follow links in the instructions instead."`
}

type LoadSkillReferenceResult struct {
	SkillKey   string   `json:"skillKey"`
	Path       string   `json:"path"`
	Content    string   `json:"content"`
	SHA256     string   `json:"sha256"`
	Size       int64    `json:"size"`
	References []string `json:"references,omitempty"`
}

func NewSkillService(rootPath string) (*DefaultSkillService, error) {
	if !canonicalSnapshotRoot(rootPath) {
		return nil, ErrSkillSnapshotUnavailable
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, ErrSkillSnapshotUnavailable
	}
	index, err := readSkillSnapshotIndex(root)
	if err != nil {
		return nil, errors.Join(ErrSkillSnapshotUnavailable, root.Close())
	}
	if err := validateSkillSnapshot(root, index); err != nil {
		return nil, errors.Join(ErrSkillSnapshotUnavailable, root.Close())
	}
	skills := make(map[string]skillSnapshotSkill, len(index.Skills))
	order := make([]string, 0, len(index.Skills))
	for _, skill := range index.Skills {
		skills[skill.Key] = skill
		order = append(order, skill.Key)
	}
	return &DefaultSkillService{root: root, skills: skills, order: order}, nil
}

func (s *DefaultSkillService) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *DefaultSkillService) SearchSkills(
	ctx context.Context,
	input SearchSkillsInput,
) (SearchSkillsResult, error) {
	if s == nil {
		return SearchSkillsResult{}, ErrSkillSnapshotUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SearchSkillsResult{}, err
	}
	if !utf8.ValidString(input.Query) || len(input.Query) > 256 {
		return SearchSkillsResult{}, ErrSkillSearchQuery
	}
	query := strings.ToLower(input.Query)
	result := SearchSkillsResult{Skills: make([]SkillSummary, 0, min(len(s.order), maxSkillSearchResults))}
	for _, key := range s.order {
		skill := s.skills[key]
		searchable := strings.ToLower(skill.Key + "\n" + skill.Name + "\n" + skill.Description)
		if !strings.Contains(searchable, query) {
			continue
		}
		result.Skills = append(result.Skills, SkillSummary{
			Key: skill.Key, Name: skill.Name, Description: skill.Description,
		})
		if len(result.Skills) == maxSkillSearchResults {
			break
		}
	}
	return result, nil
}

func (s *DefaultSkillService) LoadSkillReference(
	ctx context.Context,
	input LoadSkillReferenceInput,
) (LoadSkillReferenceResult, error) {
	if s == nil {
		return LoadSkillReferenceResult{}, ErrSkillSnapshotUnavailable
	}
	if err := ctx.Err(); err != nil {
		return LoadSkillReferenceResult{}, err
	}
	if !validSkillKey(input.SkillKey) || validateSkillRelativePath(input.Path) != nil {
		return LoadSkillReferenceResult{}, ErrSkillReferenceUnavailable
	}
	skill, ok := s.skills[input.SkillKey]
	if !ok {
		return LoadSkillReferenceResult{}, ErrSkillReferenceUnavailable
	}
	fileIndex, ok := slices.BinarySearchFunc(
		skill.Files,
		input.Path,
		func(file skillSnapshotFile, target string) int {
			return strings.Compare(file.Path, target)
		},
	)
	if !ok {
		return LoadSkillReferenceResult{}, ErrSkillReferenceUnavailable
	}
	indexedFile := skill.Files[fileIndex]
	if !indexedFile.Text || indexedFile.Size > maxSkillInstructionsBytes {
		return LoadSkillReferenceResult{}, ErrSkillReferenceUnavailable
	}
	var contents []byte
	var err error
	if files, embedded := s.embeddedFiles[skill.Key]; embedded {
		contents, err = readVerifiedEmbeddedSkillText(files, indexedFile)
	} else if s.root != nil {
		contents, err = readVerifiedSkillText(s.root, skill.Key, indexedFile)
	} else {
		err = ErrSkillReferenceUnavailable
	}
	if err != nil {
		return LoadSkillReferenceResult{}, ErrSkillReferenceUnavailable
	}
	var references []string
	if input.Path == "SKILL.md" && input.IncludeReferences {
		for _, file := range skill.Files {
			if file.Path != "SKILL.md" && file.Text && file.Size <= maxSkillInstructionsBytes {
				references = append(references, file.Path)
			}
		}
	}
	return LoadSkillReferenceResult{
		SkillKey:   skill.Key,
		Path:       indexedFile.Path,
		Content:    string(contents),
		SHA256:     indexedFile.Hash,
		Size:       indexedFile.Size,
		References: references,
	}, nil
}

func readVerifiedEmbeddedSkillText(
	files map[string][]byte,
	indexedFile skillSnapshotFile,
) ([]byte, error) {
	contents, ok := files[indexedFile.Path]
	if !ok || int64(len(contents)) != indexedFile.Size || !utf8.Valid(contents) {
		return nil, ErrSkillReferenceUnavailable
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != indexedFile.Hash {
		return nil, ErrSkillReferenceUnavailable
	}
	return slices.Clone(contents), nil
}

func canonicalSnapshotRoot(rootPath string) bool {
	if rootPath == "" || !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath {
		return false
	}
	info, err := os.Lstat(rootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	return true
}

func readSkillSnapshotIndex(root *os.Root) (skillSnapshotIndex, error) {
	info, err := root.Lstat(skillSnapshotIndexFilename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSkillIndexBytes {
		return skillSnapshotIndex{}, ErrSkillSnapshotUnavailable
	}
	file, err := root.Open(skillSnapshotIndexFilename)
	if err != nil {
		return skillSnapshotIndex{}, ErrSkillSnapshotUnavailable
	}
	defer file.Close()
	if err := validateUniqueJSONKeys(json.NewDecoder(
		bufio.NewReader(io.LimitReader(file, maxSkillIndexBytes+1)),
	)); err != nil {
		return skillSnapshotIndex{}, ErrSkillSnapshotUnavailable
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return skillSnapshotIndex{}, ErrSkillSnapshotUnavailable
	}
	decoder := json.NewDecoder(bufio.NewReader(io.LimitReader(file, maxSkillIndexBytes+1)))
	decoder.DisallowUnknownFields()
	var index skillSnapshotIndex
	if err := decoder.Decode(&index); err != nil {
		return skillSnapshotIndex{}, ErrSkillSnapshotUnavailable
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return skillSnapshotIndex{}, ErrSkillSnapshotUnavailable
	}
	return index, nil
}

func validateUniqueJSONKeys(decoder *json.Decoder) error {
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("snapshot index contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing snapshot data: %w", err)
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("snapshot index JSON nesting is invalid")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode snapshot index token: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode snapshot index key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("snapshot index object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("snapshot index contains a duplicate field")
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("snapshot index JSON delimiter is invalid")
	}
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode snapshot index closing token: %w", err)
	}
	wantClosing := json.Delim('}')
	if delimiter == '[' {
		wantClosing = ']'
	}
	if closing != wantClosing {
		return errors.New("snapshot index JSON closing delimiter is invalid")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("snapshot index contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing snapshot data: %w", err)
	}
	return nil
}

func validateSkillSnapshot(root *os.Root, index skillSnapshotIndex) error {
	createdAt, err := time.Parse(time.RFC3339Nano, index.CreatedAt)
	canonicalTime := err == nil && !createdAt.IsZero() && createdAt.Location() == time.UTC &&
		createdAt.Format(time.RFC3339Nano) == index.CreatedAt
	validEnvelope := index.SchemaVersion == 1 &&
		canonicalUUID(index.CompanyID) && index.RunID != uuid.Nil &&
		canonicalTime &&
		len(index.Skills) > 0 && len(index.Skills) <= 128
	if !validEnvelope {
		return ErrSkillSnapshotUnavailable
	}
	previousKey := ""
	for _, skill := range index.Skills {
		if !validSkillKey(skill.Key) || skill.Name != skill.Key || skill.Key <= previousKey ||
			!validSkillDescription(skill.Description) || !validSHA256(skill.ContentHash) ||
			len(skill.Files) == 0 || len(skill.Files) > 2_000 {
			return ErrSkillSnapshotUnavailable
		}
		previousKey = skill.Key
		info, err := root.Lstat(skill.Key)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrSkillSnapshotUnavailable
		}
		if err := validateSnapshotSkillFiles(root, skill); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshotSkillFiles(root *os.Root, skill skillSnapshotSkill) error {
	var totalSize int64
	previousPath := ""
	hasInstructions := false
	packageHash := sha256.New()
	for _, indexedFile := range skill.Files {
		validMetadata := validateSkillRelativePath(indexedFile.Path) == nil &&
			indexedFile.Path > previousPath && validSHA256(indexedFile.Hash) &&
			indexedFile.Size >= 0 && indexedFile.Size <= maxSkillRegularFileBytes
		if !validMetadata {
			return ErrSkillSnapshotUnavailable
		}
		previousPath = indexedFile.Path
		totalSize += indexedFile.Size
		if totalSize > 128<<20 {
			return ErrSkillSnapshotUnavailable
		}
		if indexedFile.Path == "SKILL.md" {
			hasInstructions = indexedFile.Text && indexedFile.Size <= maxSkillInstructionsBytes
		}
		if err := writeSkillPackageHashMetadata(packageHash, indexedFile); err != nil {
			return err
		}
		if err := verifyIndexedSkillFile(root, skill.Key, indexedFile, packageHash); err != nil {
			return err
		}
	}
	if !hasInstructions || hex.EncodeToString(packageHash.Sum(nil)) != skill.ContentHash {
		return ErrSkillSnapshotUnavailable
	}
	return nil
}

func writeSkillPackageHashMetadata(writer io.Writer, indexedFile skillSnapshotFile) error {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(indexedFile.Path)))
	if _, err := writer.Write(length[:]); err != nil {
		return ErrSkillSnapshotUnavailable
	}
	if _, err := io.WriteString(writer, indexedFile.Path); err != nil {
		return ErrSkillSnapshotUnavailable
	}
	binary.BigEndian.PutUint64(length[:], uint64(indexedFile.Size))
	if _, err := writer.Write(length[:]); err != nil {
		return ErrSkillSnapshotUnavailable
	}
	return nil
}

func verifyIndexedSkillFile(
	root *os.Root,
	skillKey string,
	indexedFile skillSnapshotFile,
	packageHash io.Writer,
) error {
	name := path.Join(skillKey, indexedFile.Path)
	if err := requireRegularFileWithoutSymlinks(root, name); err != nil {
		return ErrSkillSnapshotUnavailable
	}
	file, err := root.Open(name)
	if err != nil {
		return ErrSkillSnapshotUnavailable
	}
	defer file.Close()
	hash := sha256.New()
	writers := []io.Writer{hash, packageHash}
	var text strings.Builder
	if indexedFile.Text {
		writers = append(writers, &text)
	}
	written, err := io.Copy(io.MultiWriter(writers...), io.LimitReader(file, maxSkillRegularFileBytes+1))
	if err != nil || written != indexedFile.Size || hex.EncodeToString(hash.Sum(nil)) != indexedFile.Hash {
		return ErrSkillSnapshotUnavailable
	}
	if indexedFile.Text && !utf8.ValidString(text.String()) {
		return ErrSkillSnapshotUnavailable
	}
	return nil
}

func readVerifiedSkillText(
	root *os.Root,
	skillKey string,
	indexedFile skillSnapshotFile,
) ([]byte, error) {
	name := path.Join(skillKey, indexedFile.Path)
	if err := requireRegularFileWithoutSymlinks(root, name); err != nil {
		return nil, ErrSkillReferenceUnavailable
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, ErrSkillReferenceUnavailable
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxSkillInstructionsBytes+1))
	if err != nil || int64(len(contents)) != indexedFile.Size || !utf8.Valid(contents) {
		return nil, ErrSkillReferenceUnavailable
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != indexedFile.Hash {
		return nil, ErrSkillReferenceUnavailable
	}
	return contents, nil
}

func requireRegularFileWithoutSymlinks(root *os.Root, name string) error {
	parts := strings.Split(filepath.ToSlash(name), "/")
	for index := range parts {
		candidate := path.Join(parts[:index+1]...)
		info, err := root.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrSkillSnapshotUnavailable
		}
		if index < len(parts)-1 && !info.IsDir() {
			return ErrSkillSnapshotUnavailable
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return ErrSkillSnapshotUnavailable
		}
	}
	return nil
}

func validateSkillRelativePath(value string) error {
	if value == "" || !utf8.ValidString(value) || len(value) > 512 ||
		strings.ContainsRune(value, '\x00') || strings.Contains(value, `\`) ||
		path.IsAbs(value) || path.Clean(value) != value {
		return ErrSkillSnapshotUnavailable
	}
	parts := strings.Split(value, "/")
	if len(parts) > 16 {
		return ErrSkillSnapshotUnavailable
	}
	nestedInstructions := value != "SKILL.md" && parts[len(parts)-1] == "SKILL.md"
	if nestedInstructions || value == ".boardly.json" {
		return ErrSkillSnapshotUnavailable
	}
	return nil
}

func validSkillKey(value string) bool {
	return len(value) >= 1 && len(value) <= 64 && skillKeyPattern.MatchString(value)
}

func validSkillDescription(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" &&
		utf8.RuneCountInString(value) <= 1_024
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}
