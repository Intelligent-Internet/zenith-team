package daemon

import (
	"slices"
	"strings"
	"testing"
)

// The research Run gets the initialization policy and exactly the Wiki tools;
// the policy must name every tool the Run was granted, and no other Run gets it.
func TestResearchRunGetsItsPolicyAndOnlyTheWikiTools(t *testing.T) {
	t.Parallel()
	research := RunContext{ExecutionProfile: RunExecutionProfileContext{Source: "company_research"}, Agent: RunAgentContext{IsRoot: true}}
	tools := boardlyMCPToolsForRun(research)
	if want := []string{"wiki_read", "wiki_search", "wiki_write"}; !slices.Equal(tools, want) {
		t.Fatalf("research platform tools = %v, want exactly %v", tools, want)
	}
	prompt := buildRunSystemPrompt(research, "/work/current", runMemoryContext{})
	if !strings.Contains(prompt, "Company initialization policy") {
		t.Fatal("research prompt lacks the initialization policy")
	}
	var policy strings.Builder
	appendCompanyInitializationPolicy(&policy)
	for _, tool := range tools {
		if !strings.Contains(policy.String(), tool) {
			t.Errorf("research policy never names its tool %s", tool)
		}
	}
	if direct := buildRunSystemPrompt(RunContext{}, "/work/current", runMemoryContext{}); strings.Contains(direct, "Company initialization policy") {
		t.Fatal("a direct Run received the initialization policy")
	}
}

func TestOnlyTheResearchRunGetsWikiWrite(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]RunContext{
		"direct":   {},
		"root":     {Agent: RunAgentContext{IsRoot: true}},
		"task":     {ExecutionProfile: RunExecutionProfileContext{Task: &RunTaskContext{}}},
		"human":    {ExecutionProfile: RunExecutionProfileContext{Source: "human_input"}},
		"report":   {ExecutionProfile: RunExecutionProfileContext{Source: "task_report"}},
		"schedule": {ExecutionProfile: RunExecutionProfileContext{Source: "schedule"}},
		"channel":  {ChannelInvocation: &RunChannelInvocation{}},
	} {
		if tools := boardlyMCPToolsForRun(doc); slices.Contains(tools, "wiki_write") {
			t.Errorf("%s Run was given wiki_write: %v", name, tools)
		}
		if prompt := buildRunSystemPrompt(doc, "/work/current", runMemoryContext{}); strings.Contains(prompt, "wiki_write") {
			t.Errorf("%s prompt mentions wiki_write", name)
		}
	}
}

func TestResearchRunHonoursTheWikiSwitchAndFailsClearlyWithoutIt(t *testing.T) {
	t.Parallel()
	research := RunContext{ExecutionProfile: RunExecutionProfileContext{Source: "company_research"}}
	if err := requireResearchWikiTools(research); err != nil {
		t.Fatalf("research with the Wiki on = %v", err)
	}
	research.Agent.Capabilities = RunCapabilitySettings{DisabledBuiltIns: []string{"wiki"}}
	if tools := boardlyMCPToolsForRun(research); len(tools) != 0 {
		t.Fatalf("research tools with the Wiki off = %v, want none", tools)
	}
	err := requireResearchWikiTools(research)
	if err == nil || !strings.Contains(err.Error(), "Company Wiki") {
		t.Fatalf("research with the Wiki off = %v, want a clear Company Wiki error", err)
	}
	// Other Runs never need the Wiki.
	if err := requireResearchWikiTools(RunContext{Agent: research.Agent}); err != nil {
		t.Fatalf("direct Run with the Wiki off = %v", err)
	}
}

// A Run must never be told it cannot change the Wiki while it holds wiki_write,
// nor be offered wiki_write by a hint written for read-only Runs.
func TestWikiHintMatchesTheToolsTheRunHas(t *testing.T) {
	t.Parallel()
	research := buildRunSystemPrompt(
		RunContext{ExecutionProfile: RunExecutionProfileContext{Source: "company_research"}},
		"/work/current", runMemoryContext{},
	)
	if strings.Contains(research, "cannot create or change") {
		t.Error("research prompt says it cannot change the Wiki while it holds wiki_write")
	}
	direct := buildRunSystemPrompt(RunContext{}, "/work/current", runMemoryContext{})
	if !strings.Contains(direct, "cannot create or change") {
		t.Error("a read-only Run is not told the Wiki is read-only")
	}
}
