package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"boardly.dev/boardly/server/internal/mcp/tools"
)

type toolsStub struct {
	previewed     tools.PreviewInput
	applyCalls    int
	questionCalls int
	questionInput tools.AskHumanInput
	listCalls     int
	searchCalls   int
	loadCalls     int
	loadInput     tools.LoadSkillReferenceInput
}

func (s *toolsStub) ListAgents(
	context.Context,
	tools.ListAgentsInput,
) (tools.ListAgentsResult, error) {
	s.listCalls++
	return tools.ListAgentsResult{
		CompanyID:   "company-id",
		CompanySlug: "boardly-local",
		TotalCount:  2,
		Agents: []tools.PlanAgent{
			{
				AgentID:          "0198aa00-0000-7000-8000-00000000a001",
				Name:             "Andrew",
				Title:            "CEO",
				IsRoot:           true,
				RuntimeAvailable: true,
			},
			{
				AgentID:          "0198aa00-0000-7000-8000-00000000a002",
				Name:             "Eli",
				Title:            "Engineering Agent",
				RuntimeAvailable: true,
			},
		},
	}, nil
}

func (s *toolsStub) Export(context.Context, tools.ExportInput) (tools.ExportResult, error) {
	return tools.ExportResult{OutputPath: "/tmp/company-plan.yaml", Tasks: 3}, nil
}

func (s *toolsStub) Preview(_ context.Context, input tools.PreviewInput) (tools.PreviewResult, error) {
	s.previewed = input
	return tools.PreviewResult{
		Ready:        true,
		Preview:      "Summary: 2 changes",
		Digest:       "draft-digest",
		TotalChanges: 2,
	}, nil
}

func (s *toolsStub) Apply(context.Context, tools.ApplyInput) (tools.ApplyResult, error) {
	s.applyCalls++
	return tools.ApplyResult{
		ApprovalID: "approval-id", ApprovalStatus: "pending",
		Instruction: "Approval requested. End the run without retrying.",
	}, nil
}

func (s *toolsStub) AskHuman(
	_ context.Context,
	input tools.AskHumanInput,
) (tools.AskHumanResult, error) {
	s.questionCalls++
	s.questionInput = input
	return tools.AskHumanResult{
		QuestionID: "question-id", QuestionStatus: "pending",
		Instruction: "Question requested. End the run without retrying.",
	}, nil
}

func (s *toolsStub) CommerceAccountStatus(
	context.Context,
	tools.CommerceAccountStatusInput,
) (tools.CommerceAccountStatusResult, error) {
	return tools.CommerceAccountStatusResult{}, nil
}

func (s *toolsStub) SubmitCommerceManifest(
	context.Context,
	tools.SubmitCommerceManifestInput,
) (tools.SubmitCommerceManifestResult, error) {
	return tools.SubmitCommerceManifestResult{}, nil
}

func (s *toolsStub) CommerceManifestStatus(
	context.Context,
	tools.CommerceManifestStatusInput,
) (tools.CommerceManifestStatusResult, error) {
	return tools.CommerceManifestStatusResult{}, nil
}

func TestAskHumanToolAdvertisesAndEnforcesFreeTextContract(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := new(toolsStub)
	server := newServer(stub, stub, nil, nil)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, serverTransport) }()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "boardly-mcp-test", Version: "test"},
		nil,
	)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	var askHuman *mcpsdk.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools() error = %v", err)
		}
		if tool.Name == tools.AskHumanToolName {
			askHuman = tool
			break
		}
	}
	if askHuman == nil {
		t.Fatal("ask_human tool is not advertised")
	}
	if !strings.Contains(askHuman.Description, "free-text") ||
		!strings.Contains(askHuman.Description, "selectionMode") ||
		!strings.Contains(askHuman.Description, "allowCustomAnswer") {
		t.Fatalf("ask_human description = %q, want free-text contract guidance", askHuman.Description)
	}
	root, ok := askHuman.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("ask_human input schema = %#v, want object", askHuman.InputSchema)
	}
	properties, ok := root["properties"].(map[string]any)
	if !ok {
		t.Fatalf("ask_human schema properties = %#v, want object", root["properties"])
	}
	prompts, ok := properties["prompts"].(map[string]any)
	if !ok {
		t.Fatalf("prompts schema = %#v, want object", properties["prompts"])
	}
	prompt, ok := prompts["items"].(map[string]any)
	if !ok {
		t.Fatalf("prompt item schema = %#v, want object", prompts["items"])
	}
	promptProperties, ok := prompt["properties"].(map[string]any)
	if !ok {
		t.Fatalf("prompt properties = %#v, want object", prompt["properties"])
	}
	toolSetup, ok := promptProperties["toolSetup"].(map[string]any)
	if !ok || !reflect.DeepEqual(toolSetup["enum"], []any{"zenith-studio"}) {
		t.Fatalf("toolSetup schema = %#v, want explicit Studio action", promptProperties["toolSetup"])
	}
	selectionMode, ok := promptProperties["selectionMode"].(map[string]any)
	if !ok {
		t.Fatalf("selectionMode schema = %#v, want object", promptProperties["selectionMode"])
	}
	if got, want := selectionMode["enum"], []any{"single", "multi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selectionMode enum = %#v, want %#v", got, want)
	}
	selectionDescription, _ := selectionMode["description"].(string)
	if !strings.Contains(selectionDescription, "free text") ||
		!strings.Contains(selectionDescription, "single") {
		t.Fatalf("selectionMode description = %q, want free-text guidance", selectionDescription)
	}
	options, ok := promptProperties["options"].(map[string]any)
	if !ok {
		t.Fatalf("options schema = %#v, want object", promptProperties["options"])
	}
	if options["type"] != "array" {
		t.Fatalf("options type = %#v, want array only", options["type"])
	}
	customAnswer, ok := promptProperties["allowCustomAnswer"].(map[string]any)
	if !ok || customAnswer["type"] != "boolean" {
		t.Fatalf("allowCustomAnswer schema = %#v, want boolean", promptProperties["allowCustomAnswer"])
	}
	optionsDescription, _ := options["description"].(string)
	if !strings.Contains(optionsDescription, "Omit or pass an empty array") {
		t.Fatalf("options description = %q, want omission guidance", optionsDescription)
	}
	requiredFields, ok := prompt["required"].([]any)
	if !ok {
		t.Fatalf("prompt required fields = %#v, want array", prompt["required"])
	}
	for _, required := range requiredFields {
		if required == "options" {
			t.Fatalf("prompt required fields = %#v, options must be optional", prompt["required"])
		}
	}

	invalid, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: tools.AskHumanToolName,
		Arguments: map[string]any{
			"title": "Deployment domain",
			"prompts": []any{map[string]any{
				"id": "domain", "prompt": "What domain should be used?",
				"selectionMode": "freeform", "required": true, "options": nil,
			}},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(invalid free text) error = %v", err)
	}
	if !invalid.IsError || stub.questionCalls != 0 {
		t.Fatalf(
			"CallTool(invalid free text) = %+v, calls = %d; want schema rejection",
			invalid,
			stub.questionCalls,
		)
	}

	valid, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: tools.AskHumanToolName,
		Arguments: map[string]any{
			"title": "Deployment domain",
			"prompts": []any{map[string]any{
				"id": "domain", "prompt": "What domain should be used?",
				"selectionMode": "single", "required": true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(valid free text) error = %v", err)
	}
	if valid.IsError || stub.questionCalls != 1 || len(stub.questionInput.Prompts) != 1 ||
		stub.questionInput.Prompts[0].SelectionMode != "single" ||
		stub.questionInput.Prompts[0].Options != nil {
		t.Fatalf(
			"CallTool(valid free text) = %+v, input = %+v, calls = %d",
			valid,
			stub.questionInput,
			stub.questionCalls,
		)
	}
}

func TestCommerceManifestSubmitAdvertisesLoopbackSignerLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := new(toolsStub)
	server := newServer(
		stub,
		stub,
		nil,
		map[string]struct{}{"commerce_manifest_submit": {}},
		optionalTools{commerce: stub},
	)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, serverTransport) }()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "boardly-mcp-test", Version: "test"},
		nil,
	)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	var submit *mcpsdk.Tool
	for tool, listErr := range session.Tools(ctx, nil) {
		if listErr != nil {
			t.Fatalf("Tools() error = %v", listErr)
		}
		if tool.Name == "commerce_manifest_submit" {
			submit = tool
			break
		}
	}
	if submit == nil {
		t.Fatal("commerce_manifest_submit tool is not advertised")
	}
	for _, want := range []string{
		"For a loopback sandbox",
		"start zen commerce dev in this same Run",
		"register its local signing key",
		"keep the Run active through owner publication and testing",
		"Only non-loopback manifests use the immediate approval handoff",
	} {
		if !strings.Contains(submit.Description, want) {
			t.Fatalf("commerce_manifest_submit description missing %q: %s", want, submit.Description)
		}
	}
}

func (s *toolsStub) SearchSkills(context.Context, tools.SearchSkillsInput) (tools.SearchSkillsResult, error) {
	s.searchCalls++
	return tools.SearchSkillsResult{Skills: []tools.SkillSummary{{
		Key: "incident-response", Name: "incident-response", Description: "Handle incidents.",
	}}}, nil
}

func (s *toolsStub) LoadSkillReference(
	_ context.Context,
	input tools.LoadSkillReferenceInput,
) (tools.LoadSkillReferenceResult, error) {
	s.loadCalls++
	s.loadInput = input
	return tools.LoadSkillReferenceResult{
		SkillKey:   "incident-response",
		Path:       "SKILL.md",
		Content:    "unique instruction",
		SHA256:     strings.Repeat("a", 64),
		Size:       18,
		References: []string{"references/triage.md"},
	}, nil
}

func TestServerAdvertisesFileBasedCompanyPlanToolsAndCallsThem(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := new(toolsStub)
	server := newServer(stub, stub, nil, nil)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "boardly-mcp-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	names := make([]string, 0, 5)
	descriptions := map[string]string{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools() error = %v", err)
		}
		names = append(names, tool.Name)
		descriptions[tool.Name] = tool.Description
	}
	previewDescription := descriptions["company_plan_transaction_preview"]
	if !strings.Contains(previewDescription, "format v5 Task") ||
		!strings.Contains(previewDescription, "backlog or todo Tasks") ||
		!strings.Contains(previewDescription, "failed or error Tasks can only remove existing dependencies") {
		t.Fatalf(
			"preview tool description does not advertise state-scoped update rules: %q",
			previewDescription,
		)
	}
	applyDescription := descriptions["company_plan_transaction_apply"]
	if !strings.Contains(applyDescription, "failed or error Tasks can only remove existing dependencies") {
		t.Fatalf("apply tool description does not advertise dependency-removal exception: %q", applyDescription)
	}
	want := []string{
		"ask_human",
		"company_plan_export",
		"company_plan_transaction_apply",
		"company_plan_transaction_preview",
		"list_agents",
	}
	if len(names) != len(want) {
		t.Fatalf("tool names = %v", names)
	}
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("tool names = %v, missing %q", names, name)
		}
	}

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "company_plan_transaction_preview",
		Arguments: map[string]any{"actionsPath": "/tmp/company-plan-actions.yaml"},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError || stub.previewed.ActionsPath != "/tmp/company-plan-actions.yaml" {
		t.Fatalf("CallTool() result = %+v input = %+v", result, stub.previewed)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["ready"] != true || structured["totalChanges"] != float64(2) {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}

	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "list_agents", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(list_agents) error = %v", err)
	}
	structured, ok = result.StructuredContent.(map[string]any)
	agents, _ := structured["agents"].([]any)
	if result.IsError || stub.listCalls != 1 || !ok ||
		structured["totalCount"] != float64(2) || len(agents) != 2 {
		t.Fatalf("CallTool(list_agents) result = %+v calls = %d", result, stub.listCalls)
	}

	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: tools.AskHumanToolName,
		Arguments: map[string]any{
			"title": "Choose", "prompts": []any{map[string]any{
				"id": "choice", "prompt": "Which?", "selectionMode": "single",
				"required": true, "options": []any{},
			}},
		},
	})
	if err != nil || result.IsError || stub.questionCalls != 1 {
		t.Fatalf("CallTool(ask_human) result = %+v, error = %v, calls = %d", result, err, stub.questionCalls)
	}
}

func TestServerAdvertisesOnlyAllowedTools(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := new(toolsStub)
	server := newServer(stub, stub, nil, map[string]struct{}{"company_plan_export": {}})
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, serverTransport) }()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "boardly-mcp-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools() error = %v", err)
		}
		names = append(names, tool.Name)
	}
	if len(names) != 1 || names[0] != "company_plan_export" {
		t.Fatalf("tool names = %v, want [company_plan_export]", names)
	}
}

func TestApplyRequestsDurableCompanyPlanApproval(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := new(toolsStub)
	server := newServer(stub, stub, nil, nil)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "boardly-mcp-test", Version: "test"},
		nil,
	)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      tools.CompanyPlanApplyToolName,
		Arguments: map[string]any{"actionsPath": "/tmp/company-plan-actions.yaml"},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError || stub.applyCalls != 1 || len(result.Content) != 1 {
		t.Fatalf("CallTool() result = %+v, apply calls = %d", result, stub.applyCalls)
	}
	content, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(content.Text, "without retrying") {
		t.Fatalf("CallTool() content = %#v", result.Content)
	}
}

func TestServerAdvertisesSkillToolsOnlyWithSnapshotAndExplicitAllowlist(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		snapshot   bool
		allowed    map[string]struct{}
		wantSkills bool
	}{
		{
			name:       "valid snapshot and explicit allowlist",
			snapshot:   true,
			allowed:    map[string]struct{}{"search_skills": {}, "load_skill_reference": {}},
			wantSkills: true,
		},
		{name: "snapshot without allowlist", snapshot: true},
		{
			name:    "allowlist without snapshot",
			allowed: map[string]struct{}{"search_skills": {}, "load_skill_reference": {}},
		},
		{name: "partial allowlist", snapshot: true, allowed: map[string]struct{}{"search_skills": {}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cloud := new(toolsStub)
			var skills SkillTools
			if test.snapshot {
				skills = cloud
			}
			server := newServer(cloud, cloud, skills, test.allowed)
			serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(ctx, serverTransport) }()
			client := mcpsdk.NewClient(
				&mcpsdk.Implementation{Name: "boardly-mcp-test", Version: "test"},
				nil,
			)
			session, err := client.Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatalf("Connect() error = %v", err)
			}
			defer session.Close()
			seen := map[string]bool{}
			for tool, err := range session.Tools(ctx, nil) {
				if err != nil {
					t.Fatalf("Tools() error = %v", err)
				}
				seen[tool.Name] = true
			}
			gotBoth := seen["search_skills"] && seen["load_skill_reference"]
			if gotBoth != test.wantSkills {
				t.Fatalf("Skill tools = %v", seen)
			}
			if !test.wantSkills && seen["load_skill_reference"] {
				t.Fatalf("load_skill_reference unexpectedly advertised: %v", seen)
			}
		})
	}
}

func TestServerCallsReadOnlySkillTools(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := new(toolsStub)
	allowed := map[string]struct{}{"search_skills": {}, "load_skill_reference": {}}
	server := newServer(stub, stub, stub, allowed)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "boardly-mcp-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()
	seen := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools() error = %v", err)
		}
		seen[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint ||
			!tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil ||
			*tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %q annotations = %+v", tool.Name, tool.Annotations)
		}
	}
	if len(seen) != 2 || !seen["search_skills"] || !seen["load_skill_reference"] {
		t.Fatalf("tool names = %v", seen)
	}

	if result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "search_skills", Arguments: map[string]any{"query": "incident"},
	}); err != nil || result.IsError || stub.searchCalls != 1 {
		t.Fatalf("CallTool(search_skills) result = %+v, error = %v, calls = %d", result, err, stub.searchCalls)
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "load_skill_reference",
		Arguments: map[string]any{
			"skillKey":          "incident-response",
			"path":              "SKILL.md",
			"includeReferences": true,
		},
	})
	if err != nil || result.IsError || stub.loadCalls != 1 {
		t.Fatalf(
			"CallTool(load_skill_reference) result = %+v, error = %v, calls = %d",
			result,
			err,
			stub.loadCalls,
		)
	}
	if len(result.Content) != 2 {
		t.Fatalf("CallTool(load_skill_reference) content = %#v", result.Content)
	}
	if !stub.loadInput.IncludeReferences {
		t.Fatal("explicit inventory request did not reach the skill service")
	}
	content, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok || content.Text != "unique instruction" {
		t.Fatalf("CallTool(load_skill_reference) content = %#v", result.Content)
	}
	references, ok := result.Content[1].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(references.Text, "references/triage.md") {
		t.Fatalf("Skill references are absent from provider text: %#v", result.Content)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"unique instruction", "references/triage.md"} {
		if got := strings.Count(string(wire), text); got != 1 {
			t.Errorf("wire occurrences of %q = %d, want 1", text, got)
		}
	}
	metadata, ok := result.StructuredContent.(map[string]any)
	if !ok || metadata["skillKey"] != "incident-response" || metadata["sha256"] != strings.Repeat("a", 64) {
		t.Fatalf("verification metadata = %#v", result.StructuredContent)
	}
}

func TestServerDoesNotAdvertisePresentOutputWithoutService(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	stub := new(toolsStub)
	server := newServer(
		stub,
		stub,
		nil,
		map[string]struct{}{"present_output": {}},
		optionalTools{},
	)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "present_output" {
			t.Fatal("unconfigured present_output tool is still advertised")
		}
	}
	result, err := session.CallTool(
		ctx,
		&mcpsdk.CallToolParams{Name: "present_output"},
	)
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("unconfigured present_output call = %+v, %v; want unavailable", result, err)
	}
}

type outputToolsStub struct {
	input tools.PresentOutputInput
}

func (s *outputToolsStub) PresentOutput(
	_ context.Context,
	input tools.PresentOutputInput,
) (tools.PresentOutputResult, error) {
	s.input = input
	return tools.PresentOutputResult{OutputID: "0198aa00-0000-7000-8000-00000000ff01"}, nil
}

func TestServerPresentOutputRequiresExplicitPermission(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			t.Parallel()
			output := &outputToolsStub{}
			var allowlist map[string]struct{}
			if allowed {
				allowlist = map[string]struct{}{"present_output": {}}
			}
			stub := new(toolsStub)
			server := newServer(stub, stub, nil, allowlist, optionalTools{output: output})
			transport, peer := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(t.Context(), transport) }()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
			session, err := client.Connect(t.Context(), peer, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			found := false
			for tool, err := range session.Tools(t.Context(), nil) {
				if err != nil {
					t.Fatal(err)
				}
				if tool.Name == "present_output" {
					found = true
				}
			}
			if found != allowed {
				t.Fatalf("advertised = %v, want %v", found, allowed)
			}
			result, err := session.CallTool(
				t.Context(),
				&mcpsdk.CallToolParams{Name: "present_output", Arguments: map[string]any{
					"type":    "app",
					"title":   "Preview",
					"summary": "Local app",
					"app": map[string]any{
						"commands": []any{
							map[string]any{
								"command": "npm",
								"args":    []string{"run", "dev"},
								"port":    5173,
							},
						},
					},
				}},
			)
			if !allowed {
				if err == nil && !result.IsError {
					t.Fatal("unallowed call succeeded")
				}
				return
			}
			if err != nil || result.IsError {
				t.Fatalf("call = %+v, %v", result, err)
			}
			if output.input.App == nil || output.input.App.Commands[0].Port != 5173 {
				t.Fatalf("input = %+v", output.input)
			}
		})
	}
}

type taskResultToolsStub struct{ calls int }

func (s *taskResultToolsStub) FinalizeTaskResults(
	_ context.Context,
	_ tools.FinalizeTaskResultsInput,
) (tools.FinalizeTaskResultsResult, error) {
	s.calls++
	return tools.FinalizeTaskResultsResult{Status: "in_progress", Revision: 2}, nil
}

func TestServerTaskFinalizationRequiresExplicitPermission(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			service := &taskResultToolsStub{}
			var allowlist map[string]struct{}
			if allowed {
				allowlist = map[string]struct{}{"finalize_task_results": {}}
			}
			stub := new(toolsStub)
			server := newServer(stub, stub, nil, allowlist, optionalTools{taskResults: service})
			transport, peer := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(t.Context(), transport) }()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
			session, err := client.Connect(t.Context(), peer, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			found := false
			for tool, err := range session.Tools(t.Context(), nil) {
				if err != nil {
					t.Fatal(err)
				}
				if tool.Name == "finalize_task_results" {
					found = true
				}
			}
			if found != allowed {
				t.Fatalf("advertised=%v, allowed=%v", found, allowed)
			}
			result, err := session.CallTool(
				t.Context(),
				&mcpsdk.CallToolParams{
					Name: "finalize_task_results",
					Arguments: map[string]any{
						"expectedRevision": 1,
						"outputIds":        []string{"0198aa00-0000-4000-8000-000000000001"},
					},
				},
			)
			if allowed {
				if err != nil || result.IsError || service.calls != 1 {
					t.Fatalf("call=%+v %v calls=%d", result, err, service.calls)
				}
			} else if err == nil && !result.IsError || service.calls != 0 {
				t.Fatal("unallowed finalization succeeded")
			}
		})
	}
}
