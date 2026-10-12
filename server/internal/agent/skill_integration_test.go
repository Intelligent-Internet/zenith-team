//go:build integration

package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestSkillsPreviewOffersReviewedCopyAndUpdateChoices(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSkillService(SkillServiceOptions{Store: store, Bus: bus, Objects: &skillTestObjects{files: map[string][]byte{}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.PreviewUpload(ctx, fixture.principal, fixture.rootAgentID, SkillUploadInput{Filename: "skill.md", Data: skillMarkdown("Market Research")})
	if err != nil || len(first.Skills) != 1 || first.Skills[0].Name != "market-research" || first.Skills[0].OriginalName != "Market Research" {
		t.Fatalf("normalized preview = %+v, %v", first, err)
	}
	installed, err := service.Install(ctx, fixture.principal, fixture.rootAgentID, SkillInstallInput{ImportID: first.ID, VersionIDs: []string{first.Skills[0].VersionID.String()}})
	if err != nil || len(installed) != 1 {
		t.Fatalf("install = %+v, %v", installed, err)
	}
	old := installed[0]
	second, err := service.PreviewUpload(ctx, fixture.principal, fixture.rootAgentID, SkillUploadInput{Filename: "skill.md", Data: append(skillMarkdown("Market Research"), []byte("New instructions.\n")...)})
	if err != nil || len(second.Skills) != 1 {
		t.Fatalf("preview = %+v, %v", second, err)
	}
	candidate := second.Skills[0]
	if candidate.ExistingSkill == nil || candidate.ExistingSkill.ID != old.ID || candidate.ExistingSkill.Revision != old.Revision || candidate.AddAsNew == nil || candidate.AddAsNew.Name != "market-research-2" {
		t.Fatalf("choices = %+v", candidate)
	}
	installed, err = service.Install(ctx, fixture.principal, fixture.rootAgentID, SkillInstallInput{ImportID: second.ID, VersionIDs: []string{candidate.AddAsNew.VersionID.String()}})
	if err != nil || len(installed) != 2 || installed[0].VersionID != old.VersionID {
		t.Fatalf("add separately = %+v, %v", installed, err)
	}
	copy := installed[1]
	ref, err := service.ReadReference(ctx, fixture.principal, SkillReadInput{AgentID: fixture.rootAgentID, SkillID: copy.ID, Path: "SKILL.md"})
	if err != nil || ref.Content != candidate.AddAsNew.Instructions || copy.ContentHash != candidate.AddAsNew.ContentHash || !strings.Contains(ref.Content, "name: market-research-2") {
		t.Fatalf("installed copy differs from review: %+v, %v", ref, err)
	}
	installed, err = service.Install(ctx, fixture.principal, fixture.rootAgentID, SkillInstallInput{
		ImportID: second.ID, VersionIDs: []string{candidate.VersionID.String()},
		Replacements: []SkillReplacementInput{{VersionID: candidate.VersionID, SkillID: old.ID, ExpectedRevision: old.Revision}},
	})
	if err != nil || len(installed) != 2 || installed[0].ID != old.ID || installed[0].Revision != old.Revision+1 || installed[0].VersionID != candidate.VersionID {
		t.Fatalf("update existing = %+v, %v", installed, err)
	}
	third, err := service.PreviewUpload(ctx, fixture.principal, fixture.rootAgentID, SkillUploadInput{Filename: "skill.md", Data: skillMarkdown("market-research")})
	if err != nil || third.Skills[0].AddAsNew.Name != "market-research-3" {
		t.Fatalf("next available name = %+v, %v", third, err)
	}
	copyUpdate, err := service.PreviewUpload(ctx, fixture.principal, fixture.rootAgentID, SkillUploadInput{
		Filename: "skill.md", Data: skillMarkdown("Market Research"), ReplaceSkillID: copy.ID,
	})
	if err != nil || copyUpdate.Skills[0].Name != copy.Name || copyUpdate.Skills[0].OriginalName != "Market Research" {
		t.Fatalf("copy source update = %+v, %v", copyUpdate, err)
	}
	installed, err = service.Install(ctx, fixture.principal, fixture.rootAgentID, SkillInstallInput{
		ImportID: copyUpdate.ID, VersionIDs: []string{copyUpdate.Skills[0].VersionID.String()},
		ReplaceSkillID: copy.ID, ExpectedRevision: copy.Revision,
	})
	if err != nil || installed[1].ID != copy.ID || installed[1].Name != copy.Name {
		t.Fatalf("copy update = %+v, %v", installed, err)
	}
	if _, err := service.PreviewUpload(ctx, fixture.principal, fixture.rootAgentID, SkillUploadInput{
		Filename: "skill.md", Data: skillMarkdown("Market Research"), ReplaceSkillID: uuid.New(),
	}); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("foreign update target = %v, want not found", err)
	}
}

func TestSkillsBatchUpdatesFenceStaleRevisionsAndKeepEnabledState(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSkillService(SkillServiceOptions{Store: store, Bus: bus, Objects: &skillTestObjects{files: map[string][]byte{}}})
	if err != nil {
		t.Fatal(err)
	}
	zip := skillZIP(t, map[string][]byte{"a/SKILL.md": skillMarkdown("review-work"), "b/SKILL.md": skillMarkdown("write-docs")})
	first, err := service.PreviewUpload(ctx, fixture.principal, fixture.rootAgentID, SkillUploadInput{Filename: "skills.zip", Data: zip})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := service.Install(ctx, fixture.principal, fixture.rootAgentID, SkillInstallInput{ImportID: first.ID, VersionIDs: []string{first.Skills[0].VersionID.String(), first.Skills[1].VersionID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, installed[1].ID, SkillUpdateInput{ExpectedRevision: installed[1].Revision, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	second, err := service.PreviewUpload(ctx, fixture.principal, fixture.rootAgentID, SkillUploadInput{Filename: "skills.zip", Data: zip})
	if err != nil {
		t.Fatal(err)
	}
	input := SkillInstallInput{ImportID: second.ID}
	for _, candidate := range second.Skills {
		input.VersionIDs = append(input.VersionIDs, candidate.VersionID.String())
		input.Replacements = append(input.Replacements, SkillReplacementInput{VersionID: candidate.VersionID, SkillID: candidate.ExistingSkill.ID, ExpectedRevision: candidate.ExistingSkill.Revision})
	}
	stale := input
	stale.Replacements = append([]SkillReplacementInput(nil), input.Replacements...)
	stale.Replacements[1].ExpectedRevision = 1
	if _, err := service.Install(ctx, fixture.principal, fixture.rootAgentID, stale); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("stale batch = %v, want conflict", err)
	}
	unchanged, err := service.List(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil || unchanged[0].VersionID != installed[0].VersionID || unchanged[0].Revision != installed[0].Revision {
		t.Fatalf("partial update survived rollback: %+v, %v", unchanged, err)
	}
	updated, err := service.Install(ctx, fixture.principal, fixture.rootAgentID, input)
	if err != nil || len(updated) != 2 || !updated[0].Enabled || updated[1].Enabled {
		t.Fatalf("batch = %+v, %v", updated, err)
	}
	for i, skill := range updated {
		if skill.ID != installed[i].ID || skill.VersionID != second.Skills[i].VersionID {
			t.Fatalf("update %d = %+v", i, skill)
		}
	}
	invalid := input
	invalid.Replacements = []SkillReplacementInput{input.Replacements[0], input.Replacements[0]}
	if _, err := service.Install(ctx, fixture.principal, fixture.rootAgentID, invalid); !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("duplicate replacement = %v, want invalid", err)
	}
}

type skillTestObjects struct {
	mu           sync.Mutex
	files        map[string][]byte
	uploadErr    error
	beforeUpload func()
}

func (s *skillTestObjects) Upload(_ context.Context, key string, r io.Reader, _ int64, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploadErr != nil {
		return s.uploadErr
	}
	if s.beforeUpload != nil {
		s.beforeUpload()
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.files[key] = data
	return nil
}

func (s *skillTestObjects) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *skillTestObjects) DeleteObject(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, key)
	return nil
}

func TestSkillsInstallReviewedBytesAndRespectAgentScope(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := context.Background()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSkillService(
		SkillServiceOptions{Store: store, Bus: bus, Objects: &skillTestObjects{files: map[string][]byte{}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewUpload(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		SkillUploadInput{Filename: "review.md", Data: skillMarkdown("review-work")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Skills) != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	installed, err := service.Install(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		SkillInstallInput{ImportID: preview.ID, VersionIDs: []string{preview.Skills[0].VersionID.String()}},
	)
	if err != nil || len(installed) != 1 {
		t.Fatalf("install = %+v, %v", installed, err)
	}
	if installed[0].Name != "review-work" || !installed[0].Enabled {
		t.Fatalf("installed = %+v", installed[0])
	}
	listed, err := service.List(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	updated, err := service.Update(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		installed[0].ID,
		SkillUpdateInput{ExpectedRevision: 1, Enabled: false},
	)
	if err != nil || updated.Enabled || updated.Revision != 2 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := service.Update(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		installed[0].ID,
		SkillUpdateInput{ExpectedRevision: 1, Enabled: true},
	); !errors.Is(
		err,
		ErrSkillConflict,
	) {
		t.Fatalf("stale update = %v", err)
	}
	if err := service.Remove(ctx, fixture.principal, fixture.rootAgentID, installed[0].ID, 2); err != nil {
		t.Fatal(err)
	}
	listed, err = service.List(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil || len(listed) != 0 {
		t.Fatalf("removed list = %+v, %v", listed, err)
	}
}

func TestSkillImportsRejectForeignAgentAndRevokedConfigurer(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	owner := seedAgentCompany(t, ctx, pool, "owner")
	other := seedAgentCompany(t, ctx, pool, "owner")
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSkillService(
		SkillServiceOptions{Store: store, Bus: bus, Objects: &skillTestObjects{files: map[string][]byte{}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	input := SkillUploadInput{Filename: "work.md", Data: skillMarkdown("review-work")}
	if _, err := service.PreviewUpload(ctx, owner.principal, other.rootAgentID, input); err == nil {
		t.Fatal("foreign Agent import succeeded")
	}
	preview, err := service.PreviewUpload(ctx, owner.principal, owner.rootAgentID, input)
	if err != nil {
		t.Fatal(err)
	}
	selection := SkillInstallInput{ImportID: preview.ID, VersionIDs: []string{preview.Skills[0].VersionID.String()}}
	if _, err := service.Install(ctx, other.principal, other.rootAgentID, selection); err == nil {
		t.Fatal("foreign preview installed")
	}
	mustAgentExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		owner.companyID,
		owner.userID,
	)
	if _, err := service.Install(
		ctx,
		owner.principal,
		owner.rootAgentID,
		selection,
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("stale role installed: %v", err)
	}
}

func TestSkillUpdateAndCleanupRetainRunVersion(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	objects := &skillTestObjects{files: map[string][]byte{}}
	service, err := NewSkillService(SkillServiceOptions{Store: store, Bus: bus, Objects: objects})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.PreviewUpload(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		SkillUploadInput{Filename: "work.md", Data: skillMarkdown("review-work")},
	)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := service.Install(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		SkillInstallInput{ImportID: first.ID, VersionIDs: []string{first.Skills[0].VersionID.String()}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var conversationID, runID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO conversations(company_id,kind,agent_id,created_by_user_id) VALUES($1,'direct',$2,$3) RETURNING id`, fixture.companyID, fixture.rootAgentID, fixture.userID).
		Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,execution_authorized_by_user_id,status,run_type) VALUES($1,$2,$3,$4,$5,'queued','direct_chat') RETURNING id`, fixture.companyID, fixture.rootAgentID, conversationID, fixture.runtimeID, fixture.userID).
		Scan(&runID); err != nil {
		t.Fatal(err)
	}
	mustAgentExec(
		t,
		ctx,
		pool,
		`INSERT INTO run_skills(company_id,run_id,version_id) VALUES($1,$2,$3)`,
		fixture.companyID,
		runID,
		first.Skills[0].VersionID,
	)
	updatedBytes := append(skillMarkdown("review-work"), []byte("\nUpdated instructions.")...)
	second, err := service.PreviewUpload(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		SkillUploadInput{Filename: "work.md", Data: updatedBytes},
	)
	if err != nil {
		t.Fatal(err)
	}
	installed, err = service.Install(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		SkillInstallInput{
			ImportID:         second.ID,
			VersionIDs:       []string{second.Skills[0].VersionID.String()},
			ReplaceSkillID:   installed[0].ID,
			ExpectedRevision: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := service.ReadReference(
		ctx,
		fixture.principal,
		SkillReadInput{AgentID: fixture.rootAgentID, SkillID: installed[0].ID, Path: "SKILL.md"},
	)
	if err != nil || reference.Content != string(updatedBytes) {
		t.Fatalf("updated bytes = %+v, %v", reference, err)
	}
	if err := service.Remove(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		installed[0].ID,
		installed[0].Revision,
	); err != nil {
		t.Fatal(err)
	}
	mustAgentExec(
		t,
		ctx,
		pool,
		`UPDATE skill_imports SET expires_at=now()-interval '1 hour' WHERE company_id=$1`,
		fixture.companyID,
	)
	if err := service.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := service.ResolveForRun(ctx, fixture.companyID, runID, first.Skills[0].VersionID)
	if err != nil || old.Content != string(skillMarkdown("review-work")) {
		t.Fatalf("frozen version lost = %+v, %v", old, err)
	}
	if _, err := service.ResolveForRun(
		ctx,
		fixture.companyID,
		runID,
		second.Skills[0].VersionID,
	); !errors.Is(
		err,
		ErrSkillNotFound,
	) {
		t.Fatalf("unfrozen version accessible: %v", err)
	}
	if len(objects.files) != 1 {
		t.Fatalf("cleanup kept %d objects, want frozen one", len(objects.files))
	}
}

type skillRoundTripper func(*http.Request) (*http.Response, error)

func TestSkillFileEditingIsAtomicScopedAndRetainsFrozenBytes(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	objects := &skillTestObjects{files: map[string][]byte{}}
	service, err := NewSkillService(SkillServiceOptions{Store: store, Bus: bus, Objects: objects})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewUpload(ctx, fixture.principal, fixture.rootAgentID, SkillUploadInput{
		Filename: "work.zip", Data: skillZIP(t, map[string][]byte{
			"SKILL.md": skillMarkdown("review-work"), "scripts/check.py": []byte("original"), "assets/icon.bin": {0, 1, 255},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := service.Install(ctx, fixture.principal, fixture.rootAgentID, SkillInstallInput{ImportID: preview.ID, VersionIDs: []string{preview.Skills[0].VersionID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	old, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, installed[0].ID, SkillUpdateInput{ExpectedRevision: 1, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	var conversationID, runID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO conversations(company_id,kind,agent_id,created_by_user_id) VALUES($1,'direct',$2,$3) RETURNING id`, fixture.companyID, fixture.rootAgentID, fixture.userID).Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,execution_authorized_by_user_id,status,run_type) VALUES($1,$2,$3,$4,$5,'queued','direct_chat') RETURNING id`, fixture.companyID, fixture.rootAgentID, conversationID, fixture.runtimeID, fixture.userID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	mustAgentExec(t, ctx, pool, `INSERT INTO run_skills(company_id,run_id,version_id) VALUES($1,$2,$3)`, fixture.companyID, runID, old.VersionID)
	input := SkillEditInput{ExpectedRevision: old.Revision, Changes: []SkillFileChange{{Path: "scripts/check.py", Content: "edited"}}, DeletePaths: []string{"assets/icon.bin"}}
	if _, err := service.EditFiles(ctx, fixture.principal, uuid.New(), old.ID, input); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("foreign Agent error = %v", err)
	}
	invalid := input
	invalid.DeletePaths = []string{"SKILL.md"}
	if _, err := service.EditFiles(ctx, fixture.principal, fixture.rootAgentID, old.ID, invalid); !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("invalid edit error = %v", err)
	}
	unchanged, err := service.List(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil || unchanged[0].VersionID != old.VersionID || unchanged[0].Revision != old.Revision {
		t.Fatalf("invalid batch changed skill = %+v, %v", unchanged, err)
	}
	objects.uploadErr = errors.New("storage unavailable")
	if _, err := service.EditFiles(ctx, fixture.principal, fixture.rootAgentID, old.ID, input); !errors.Is(err, ErrSkillUnavailable) {
		t.Fatalf("failed upload error = %v", err)
	}
	objects.uploadErr = nil
	unchanged, err = service.List(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil || unchanged[0].VersionID != old.VersionID || unchanged[0].Revision != old.Revision {
		t.Fatalf("failed upload changed skill = %+v, %v", unchanged, err)
	}
	var concurrent SkillView
	objects.beforeUpload = func() {
		updateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		concurrent, err = service.Update(updateCtx, fixture.principal, fixture.rootAgentID, old.ID, SkillUpdateInput{ExpectedRevision: old.Revision, Enabled: true})
		if err != nil {
			t.Fatalf("configuration change blocked behind object upload: %v", err)
		}
	}
	if _, err := service.EditFiles(ctx, fixture.principal, fixture.rootAgentID, old.ID, input); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("concurrent access change was overwritten: %v", err)
	}
	objects.beforeUpload = nil
	old = concurrent
	input.ExpectedRevision = old.Revision
	updated, err := service.EditFiles(ctx, fixture.principal, fixture.rootAgentID, old.ID, input)
	if err != nil || updated.ID != old.ID || updated.Name != old.Name || updated.Enabled != old.Enabled || !updated.Edited || updated.Revision != old.Revision+1 || updated.VersionID == old.VersionID || updated.FileCount != 2 {
		t.Fatalf("edited skill = %+v, %v", updated, err)
	}
	imports, err := store.Queries().CountRecentSkillImports(ctx, generated.CountRecentSkillImportsParams{CompanyID: fixture.companyID, AgentID: fixture.rootAgentID})
	if err != nil || imports != 1 {
		t.Fatalf("edits consumed import allowance: count=%d, err=%v", imports, err)
	}
	version, err := store.Queries().GetSkillVersion(ctx, generated.GetSkillVersionParams{CompanyID: fixture.companyID, ID: updated.VersionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Install(ctx, fixture.principal, fixture.rootAgentID, SkillInstallInput{ImportID: version.ImportID, VersionIDs: []string{updated.VersionID.String()}}); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("edited version accepted as an import preview: %v", err)
	}
	ref, err := service.ReadReference(ctx, fixture.principal, SkillReadInput{AgentID: fixture.rootAgentID, SkillID: old.ID, VersionID: updated.VersionID, Path: "scripts/check.py"})
	if err != nil || ref.Content != "edited" {
		t.Fatalf("edited reference = %+v, %v", ref, err)
	}
	if _, err := service.ReadReference(ctx, fixture.principal, SkillReadInput{AgentID: fixture.rootAgentID, SkillID: old.ID, VersionID: old.VersionID, Path: "scripts/check.py"}); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("stale read = %v", err)
	}
	if _, err := service.EditFiles(ctx, fixture.principal, fixture.rootAgentID, old.ID, input); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("stale save = %v", err)
	}
	mustAgentExec(t, ctx, pool, `UPDATE skill_imports SET expires_at=now()-interval '1 hour' WHERE company_id=$1`, fixture.companyID)
	if err := service.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	frozen, err := service.ResolveForRun(ctx, fixture.companyID, runID, old.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	var script string
	for _, file := range frozen.Files {
		if file.Path == "scripts/check.py" {
			script = string(file.Data)
		}
	}
	if script != "original" || len(frozen.Files) != 2 {
		t.Fatalf("frozen bundle = %+v", frozen)
	}
	input = SkillEditInput{ExpectedRevision: updated.Revision, Changes: []SkillFileChange{{Path: "scripts/check.py", Content: "must not publish"}}}
	objects.beforeUpload = func() {
		mustAgentExec(t, ctx, pool, `UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`, fixture.companyID, fixture.userID)
	}
	if _, err := service.EditFiles(ctx, fixture.principal, fixture.rootAgentID, old.ID, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revocation during upload was ignored: %v", err)
	}
	objects.beforeUpload = nil
	unchanged, err = service.List(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil || unchanged[0].VersionID != updated.VersionID || unchanged[0].Revision != updated.Revision {
		t.Fatalf("revoked edit changed installation = %+v, %v", unchanged, err)
	}
	if _, err := service.EditFiles(ctx, fixture.principal, fixture.rootAgentID, old.ID, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked member edit = %v", err)
	}
	mustAgentExec(t, ctx, pool, `UPDATE skill_imports SET expires_at=now()-interval '1 hour' WHERE company_id=$1`, fixture.companyID)
	if err := service.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	objects.mu.Lock()
	retained := len(objects.files)
	objects.mu.Unlock()
	if retained != 2 {
		t.Fatalf("unused edits were not cleaned up: retained %d objects, want current and frozen versions", retained)
	}
}

func (transport skillRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestGitHubSkillPreviewPinsCommitAndInstallsReviewedBytesWithoutRefetch(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	archive := skillZIP(
		t,
		map[string][]byte{
			"repo-" + commit + "/skills/review-work/SKILL.md":            skillMarkdown("review-work"),
			"repo-" + commit + "/skills/review-work/references/notes.md": []byte("Reviewed reference."),
		},
	)
	var requests []string
	client := &http.Client{Transport: skillRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		var body []byte
		switch request.URL.String() {
		case "https://api.github.com/repos/acme/repo/commits/main":
			body = []byte(`{"sha":"` + commit + `"}`)
		case "https://codeload.github.com/acme/repo/zip/" + commit:
			body = archive
		default:
			t.Fatalf("unexpected network destination %s", request.URL)
			return nil, errors.New("unexpected network")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     http.Header{},
		}, nil
	})}
	service, err := NewSkillService(
		SkillServiceOptions{
			Store:      store,
			Bus:        bus,
			Objects:    &skillTestObjects{files: map[string][]byte{}},
			HTTPClient: client,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewGitHub(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		SkillGitHubInput{URL: "https://github.com/acme/repo/tree/main/skills/review-work"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if preview.SourceCommit != commit || preview.Skills[0].SourcePath != "skills/review-work/SKILL.md" ||
		preview.SourceURL != "https://github.com/acme/repo/tree/main/skills/review-work" {
		t.Fatalf("provenance=%+v", preview)
	}
	archive = nil
	installed, err := service.Install(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		SkillInstallInput{ImportID: preview.ID, VersionIDs: []string{preview.Skills[0].VersionID.String()}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("install refetched mutable source: %v", requests)
	}
	if installed[0].SourceURL != preview.SourceURL {
		t.Fatalf("installed source URL = %q, want %q", installed[0].SourceURL, preview.SourceURL)
	}
	reference, err := service.ReadReference(
		ctx,
		fixture.principal,
		SkillReadInput{AgentID: fixture.rootAgentID, SkillID: installed[0].ID, Path: "references/notes.md"},
	)
	if err != nil || reference.Content != "Reviewed reference." {
		t.Fatalf("reference=%+v,%v", reference, err)
	}
}
