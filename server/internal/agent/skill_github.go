package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
)

type skillGitHubLocation struct {
	Repository string
	Ref        string
	Path       string
}

var (
	skillGitHubName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	skillGitHubCommit = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

func parseSkillGitHubLocation(input SkillGitHubInput) (skillGitHubLocation, error) {
	parsed, err := url.Parse(input.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return skillGitHubLocation{}, ErrSkillInvalid
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || !skillGitHubName.MatchString(parts[0]) || !skillGitHubName.MatchString(parts[1]) {
		return skillGitHubLocation{}, ErrSkillInvalid
	}
	location := skillGitHubLocation{
		Repository: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"),
	}
	if len(parts) > 2 {
		if len(parts) < 4 || parts[2] != "tree" && parts[2] != "blob" {
			return skillGitHubLocation{}, ErrSkillInvalid
		}
		location.Ref, location.Path = parts[3], strings.Join(parts[4:], "/")
		if parts[2] == "blob" {
			if path.Base(location.Path) != "SKILL.md" {
				return skillGitHubLocation{}, ErrSkillInvalid
			}
			location.Path = path.Dir(location.Path)
			if location.Path == "." {
				location.Path = ""
			}
		}
	}
	if len(location.Ref) > 256 || strings.ContainsAny(location.Ref, "\x00\r\n?\\") ||
		location.Path != "" && !contentbundle.SafeSkillPath(location.Path) {
		return skillGitHubLocation{}, ErrSkillInvalid
	}
	return location, nil
}

func (s *DefaultSkillService) PreviewGitHub(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input SkillGitHubInput,
) (SkillPreview, error) {
	if err := s.authorizeImport(ctx, principal, agentID); err != nil {
		return SkillPreview{}, err
	}
	location, err := parseSkillGitHubLocation(input)
	if err != nil {
		return SkillPreview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ref := location.Ref
	if ref == "" {
		ref = "HEAD"
	}
	commitData, err := s.readSkillGitHub(
		ctx,
		"https://api.github.com/repos/"+location.Repository+"/commits/"+url.PathEscape(ref),
		1<<20,
	)
	if err != nil {
		return SkillPreview{}, err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(commitData, &commit) != nil || !skillGitHubCommit.MatchString(commit.SHA) {
		return SkillPreview{}, ErrSkillUnavailable
	}
	archive, err := s.readSkillGitHub(
		ctx,
		"https://codeload.github.com/"+location.Repository+"/zip/"+commit.SHA,
		MaxSkillUploadBytes,
	)
	if err != nil {
		return SkillPreview{}, err
	}
	files, err := readSkillZIP(archive)
	if err != nil {
		return SkillPreview{}, err
	}
	// GitHub wraps every archive in a repository/commit directory. Discard
	// just that wrapper, preserving the selected skill's own relative files.
	selected := map[string][]byte{}
	for name, data := range files {
		_, relative, found := strings.Cut(name, "/")
		if !found {
			continue
		}
		if location.Path != "" && !strings.HasPrefix(relative, location.Path+"/") {
			continue
		}
		if location.Path != "" {
			relative = strings.TrimPrefix(relative, location.Path+"/")
		}
		selected[relative] = data
	}
	items, err := discoverSkillFiles(selected)
	if err != nil {
		return SkillPreview{}, err
	}
	for i := range items {
		items[i].Path = path.Join(location.Path, items[i].Path)
	}
	return s.saveSkillPreview(
		ctx,
		principal,
		agentID,
		skillImportSource{
			Type:           "github",
			URL:            strings.TrimSpace(input.URL),
			Commit:         commit.SHA,
			ReplaceSkillID: input.ReplaceSkillID,
		},
		items,
	)
}

func (s *DefaultSkillService) readSkillGitHub(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Zenith-Team-Skill-Importer")
	response, err := s.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: GitHub request", ErrSkillUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GitHub requires a public, accessible repository", ErrSkillUnavailable)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, ErrSkillUnavailable
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: GitHub response exceeds size limit", ErrSkillInvalid)
	}
	return data, nil
}
