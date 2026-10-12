//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestAgentCapabilitiesPersistWithRevisionAndCompanyBoundaries(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	foreign := seedAgentCompany(t, ctx, pool, "owner")
	service, eventLog := mustAgentService(t, store)
	before, err := service.Get(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil {
		t.Fatal(err)
	}
	settings := CapabilitySettings{
		EnabledTools:       []string{"zenith-studio"},
		DisabledBuiltIns:   []string{"planning"},
		DisabledPlugins:    []uuid.UUID{},
		DisabledMCPServers: []uuid.UUID{},
	}

	var pluginID, mcpID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_catalog_items(company_id,provider,native_id) VALUES($1,'codex','design@local') RETURNING id`, fixture.companyID).
		Scan(&pluginID); err != nil {
		t.Fatal(err)
	}
	mustAgentExec(
		t,
		ctx,
		pool,
		`INSERT INTO plugin_runtime_observations(runtime_id,plugin_id,company_id,provider,generation,name,category,status,action,catalog_rank,observed_at) VALUES($1,$2,$3,'codex',gen_random_uuid(),'Design','Other','installed','none',0,now())`,
		fixture.runtimeID,
		pluginID,
		fixture.companyID,
	)
	if err := pool.QueryRow(ctx, `INSERT INTO connector_catalog_items(company_id,provider,source,native_id,name,status,action,observed_at) VALUES($1,'codex','codex_mcp','design','Design','unavailable','none',now()) RETURNING id`, fixture.companyID).
		Scan(&mcpID); err != nil {
		t.Fatal(err)
	}
	mustAgentExec(
		t,
		ctx,
		pool,
		`INSERT INTO connector_runtime_observations(runtime_id,connector_id,company_id,provider,generation,name,status,action,observed_at) VALUES($1,$2,$3,'codex',gen_random_uuid(),'Design','unavailable','none',now())`,
		fixture.runtimeID,
		mcpID,
		fixture.companyID,
	)
	settings.DisabledPlugins = []uuid.UUID{pluginID}
	settings.DisabledMCPServers = []uuid.UUID{mcpID}
	input := UpdateInput{ExpectedRevision: before.Revision, Capabilities: &settings}
	updated, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, input)
	if err != nil || updated.Revision != before.Revision+1 ||
		!sameCapabilitySettings(updated.Capabilities, settings) {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	// SQL reads the disabled Plugins from their own column, and the frozen Run
	// brief names exactly those Plugins.
	var disabled []uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT disabled_plugin_ids FROM agents WHERE id = $1`, fixture.rootAgentID).
		Scan(&disabled); err != nil || len(disabled) != 1 || disabled[0] != pluginID {
		t.Fatalf("disabled_plugin_ids = %v, %v", disabled, err)
	}
	// A Run's brief reads Plugins as observed on the runtime it runs on
	// (ADR 0014): here the owner's, where the Plugin was observed.
	brief, err := generated.New(pool).GetRunAgentBrief(ctx, generated.GetRunAgentBriefParams{
		RuntimeID: fixture.runtimeID, AgentID: fixture.rootAgentID,
	})
	var frozen []struct {
		NativeID string `json:"nativeId"`
	}
	if err != nil || json.Unmarshal(brief.DisabledPlugins, &frozen) != nil ||
		len(frozen) != 1 || frozen[0].NativeID != "design@local" {
		t.Fatalf("frozen disabled Plugins = %s, %v", brief.DisabledPlugins, err)
	}
	replayed, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, input)
	if err != nil || replayed.Revision != updated.Revision {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	read, err := service.Get(ctx, fixture.principal, fixture.rootAgentID)
	if err != nil || !sameCapabilitySettings(read.Capabilities, settings) {
		t.Fatalf("read = %+v, %v", read, err)
	}
	if got := agentEventsForCompany(
		eventLog.updated,
		fixture.companyID,
	); len(got) != 1 ||
		!sameCapabilitySettings(got[0].Payload.Capabilities, settings) {
		t.Fatalf("events = %+v", got)
	}
	if _, err := service.Update(ctx, foreign.principal, fixture.rootAgentID, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company = %v", err)
	}
	// Denials name catalog items of this Company for the template's
	// provider; an unknown id, another Company's item, and a legacy runtime
	// key are all refused.
	var foreignPluginID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_catalog_items(company_id,provider,native_id) VALUES($1,'codex','foreign@local') RETURNING id`, foreign.companyID).
		Scan(&foreignPluginID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []CapabilitySettings{
		{DisabledPlugins: []uuid.UUID{uuid.New()}},
		{DisabledMCPServers: []uuid.UUID{uuid.New()}},
		{DisabledPlugins: []uuid.UUID{foreignPluginID}},
	} {
		if _, err := service.Update(
			ctx,
			fixture.principal,
			fixture.rootAgentID,
			UpdateInput{ExpectedRevision: updated.Revision, Capabilities: &target},
		); !errors.Is(
			err,
			ErrInvalidInput,
		) {
			t.Errorf("bad sources = %v", err)
		}
	}
	cleared := CapabilitySettings{}
	if _, err := service.Update(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		UpdateInput{ExpectedRevision: before.Revision, Capabilities: &cleared},
	); !errors.Is(
		err,
		ErrRevisionConflict,
	) {
		t.Fatalf("stale = %v", err)
	}
	// The selection is template state (ADR 0014): moving the owner's binding
	// to another of their machines changes nothing about it, and the stored
	// denials survive the machine losing the observations they name.
	var nextRuntime uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runtimes(company_id, daemon_id, name, provider, owner_user_id) VALUES ($1, 'new-computer', 'New computer', 'codex', $2) RETURNING id`, fixture.companyID, fixture.userID).
		Scan(&nextRuntime); err != nil {
		t.Fatal(err)
	}
	mustAgentExec(t, ctx, pool, `DELETE FROM plugin_runtime_observations WHERE plugin_id=$1`, pluginID)
	mustAgentExec(t, ctx, pool, `DELETE FROM connector_runtime_observations WHERE connector_id=$1`, mcpID)
	var policyCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connector_policies WHERE connector_id=$1`, mcpID).
		Scan(&policyCount); err != nil ||
		policyCount != 0 {
		t.Fatalf("capability preference granted policy: %d, %v", policyCount, err)
	}
	bindings, err := NewBindingService(BindingServiceOptions{Store: store, Bus: mustAgentTestBus(t)})
	if err != nil {
		t.Fatalf("NewBindingService() error = %v", err)
	}
	rebound, err := bindings.Bind(ctx, fixture.principal, fixture.rootAgentID, BindInput{RuntimeID: nextRuntime})
	if err != nil || rebound.Binding == nil || rebound.Binding.Runtime.ID != nextRuntime ||
		rebound.Revision != updated.Revision || len(rebound.Capabilities.EnabledTools) != 1 ||
		len(rebound.Capabilities.DisabledBuiltIns) != 1 ||
		len(rebound.Capabilities.DisabledPlugins) != 1 || len(rebound.Capabilities.DisabledMCPServers) != 1 {
		t.Fatalf("rebind = %+v, %v; want the binding moved and the selection untouched", rebound, err)
	}
	// The column mirrors the selection, which the rebind left alone.
	if err := pool.QueryRow(ctx, `SELECT disabled_plugin_ids FROM agents WHERE id = $1`, fixture.rootAgentID).
		Scan(&disabled); err != nil || len(disabled) != 1 || disabled[0] != pluginID {
		t.Fatalf("disabled_plugin_ids after rebind = %v, %v; want [%s]", disabled, err, pluginID)
	}
}
