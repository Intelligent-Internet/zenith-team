package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// RunIdentityChange describes an offline conversion without moving working files.
type RunIdentityChange struct {
	Path         string    `json:"path"`
	LegacyID     string    `json:"legacyId"`
	RunID        uuid.UUID `json:"runId"`
	Branch       string    `json:"branch"`
	MergedCommit string    `json:"mergedCommit,omitempty"`
}

// MigrateRunIdentities authenticates every retained sidecar before applying a
// database-exported mapping. The daemon must be stopped. A shared file lock
// excludes managed Git mutations; no commit, working file or directory is moved.
// Git branch renaming precedes the atomic signed sidecar replacement, allowing
// a retry to recognize an interruption between the two operations.
func (m *Manager) MigrateRunIdentities(
	ctx context.Context,
	mappings map[string]uuid.UUID,
	apply bool,
) ([]RunIdentityChange, error) {
	seen := make(map[uuid.UUID]bool, len(mappings))
	for legacy, id := range mappings {
		number, err := strconv.ParseInt(legacy, 10, 64)
		if err != nil || number < 1 || strconv.FormatInt(number, 10) != legacy || id == uuid.Nil || seen[id] {
			return nil, errors.New("Run identity mapping is invalid or ambiguous")
		}
		seen[id] = true
	}
	var changes []RunIdentityChange
	err := m.withMainMutation(ctx, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.inUse) != 0 {
			return errors.New("stop all workspace execution before Run identity migration")
		}
		paths, err := m.records()
		if err != nil {
			return err
		}
		entries := make(map[string]metadata)
		for _, path := range paths {
			entry, err := m.readMetadata(path)
			if err != nil {
				return fmt.Errorf("authenticate retained workspace %s: %w", path, err)
			}
			if _, err := strconv.ParseInt(entry.LastRunID, 10, 64); err != nil {
				continue
			}
			id, ok := mappings[entry.LastRunID]
			if !ok {
				return fmt.Errorf("missing database mapping for retained Run %s", entry.LastRunID)
			}
			change := RunIdentityChange{
				Path:     path,
				LegacyID: entry.LastRunID,
				RunID:    id,
				Branch:   entry.Branch,
			}
			if entry.Branch == "zenith-team/task-"+entry.LastRunID {
				change.Branch = taskBranch(id)
				if err := checkRunIdentityBranch(ctx, path, entry.Branch, change.Branch); err != nil {
					return err
				}
				commit, err := gitOutput(
					ctx,
					filepath.Join(path, workdirDirectoryName),
					"log",
					"main",
					"--max-count=1",
					"--format=%H",
					"--grep=^Merge Zenith Team task run "+entry.LastRunID+"$",
				)
				if err != nil {
					return fmt.Errorf("inspect retained merge receipt: %w", err)
				}
				change.MergedCommit = strings.TrimSpace(commit)
				if change.MergedCommit != "" && !validCommitID(change.MergedCommit) {
					return errors.New("retained merge receipt is invalid")
				}
				mapped, err := gitOutput(
					ctx,
					filepath.Join(path, workdirDirectoryName),
					"for-each-ref",
					"--format=%(objectname)",
					taskMergeReceiptRef(id),
				)
				if err != nil {
					return err
				}
				if mapped = strings.TrimSpace(mapped); mapped != "" && mapped != change.MergedCommit {
					return errors.New("retained merge receipt conflicts with the UUID mapping")
				}
			}
			entries[path] = entry
			changes = append(changes, change)
		}
		if !apply {
			return nil
		}
		for _, change := range changes {
			entry := entries[change.Path]
			if change.MergedCommit != "" {
				// A UUID-named receipt points at the existing commit. This
				// preserves idempotent merge review without rewriting main,
				// commit messages, or retaining numeric IDs in runtime reads.
				if err := runGit(
					ctx,
					filepath.Join(change.Path, workdirDirectoryName),
					nil,
					nil,
					"update-ref",
					taskMergeReceiptRef(change.RunID),
					change.MergedCommit,
				); err != nil {
					return fmt.Errorf("preserve converted merge receipt: %w", err)
				}
			}
			if change.Branch != entry.Branch {
				workDir := filepath.Join(change.Path, workdirDirectoryName)
				current, err := gitOutput(ctx, workDir, "symbolic-ref", "--short", "HEAD")
				if err != nil {
					return err
				}
				if strings.TrimSpace(current) == entry.Branch {
					if err := runGit(
						ctx,
						workDir,
						nil,
						nil,
						"branch",
						"-m",
						entry.Branch,
						change.Branch,
					); err != nil {
						return fmt.Errorf("rename retained Run branch: %w", err)
					}
				}
			}
			entry.LastRunID = change.RunID.String()
			entry.Branch = change.Branch
			if err := m.writeMetadata(change.Path, entry); err != nil {
				return err
			}
		}
		return nil
	})
	return changes, err
}

func checkRunIdentityBranch(ctx context.Context, path, previous, next string) error {
	workDir := filepath.Join(path, workdirDirectoryName)
	if err := requireRealDirectory(workDir, false); err != nil {
		return err
	}
	current, err := gitOutput(ctx, workDir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("inspect retained Run branch: %w", err)
	}
	current = strings.TrimSpace(current)
	if current != previous && current != next {
		return errors.New("retained Run workspace has an unexpected branch")
	}
	branches, err := gitOutput(
		ctx,
		workDir,
		"for-each-ref",
		"--format=%(refname)",
		"refs/heads/"+previous,
		"refs/heads/"+next,
	)
	if err != nil {
		return err
	}
	// Exactly one branch may exist, including on resumption after a rename.
	if strings.TrimSpace(branches) != "refs/heads/"+current {
		return errors.New("retained Run branch mapping conflicts with an existing branch")
	}
	return nil
}
