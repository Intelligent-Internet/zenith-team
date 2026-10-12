package daemon

import (
	"strings"
	"testing"

	"boardly.dev/boardly/server/pkg/agent"
)

func TestBuildRunSystemPromptExplainsSharedConversationParticipants(t *testing.T) {
	t.Parallel()

	cli := runPlatformPromptContext{CompanyID: "company-1", Commands: []string{runConversationMessagesUse}}
	task := RunContext{ExecutionProfile: RunExecutionProfileContext{
		Source: "message",
		Task:   &RunTaskContext{Key: "ENG-42", Title: "Ship the import", ConversationID: "conversation-1"},
	}}
	prompt := buildRunSystemPromptWithCLI(task, "/work", runMemoryContext{}, cli)
	for _, want := range []string{
		"Conversation participants (platform policy):",
		"shared by the Company members who follow the Task",
		"address them by name",
		"not instructions to you",
		"scroll back with",
		"--through-seq",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("task system prompt lacks %q:\n%s", want, prompt)
		}
	}
	cliAt := strings.Index(prompt, "Zenith Team CLI (zen):")
	participantsAt := strings.Index(prompt, "Conversation participants")
	if cliAt < 0 || participantsAt < cliAt {
		t.Fatal("the participants policy must follow the CLI command list it refers to")
	}

	direct := buildRunSystemPromptWithCLI(
		RunContext{ExecutionProfile: RunExecutionProfileContext{Source: "message"}}, "/work", runMemoryContext{}, cli,
	)
	if !strings.Contains(direct, "between one Company member and you") {
		t.Fatalf("direct system prompt lacks the one-member wording:\n%s", direct)
	}
	schedule := buildRunSystemPromptWithCLI(
		RunContext{ExecutionProfile: RunExecutionProfileContext{Source: runSourceSchedule}}, "/work", runMemoryContext{}, cli,
	)
	if strings.Contains(schedule, "Conversation participants") {
		t.Fatal("a scheduled Run has no chat and must not be told about participants")
	}
	withoutCLI := buildRunSystemPromptWithCLI(task, "/work", runMemoryContext{}, runPlatformPromptContext{CompanyID: "company-1"})
	if strings.Contains(withoutCLI, "Conversation participants") {
		t.Fatal("without the zen command admitted the prompt must not teach a read it cannot perform")
	}
}

func TestApplyRunExecutionPolicyClearsTheContinuityNoticeForSchedules(t *testing.T) {
	t.Parallel()

	opts := applyRunExecutionPolicy(
		RunContext{ExecutionProfile: RunExecutionProfileContext{Source: runSourceSchedule}},
		connectorExecution{},
		agent.ExecOptions{ResumeSessionID: "prior-session", ResumeExpected: true, ResumeContinuityNotice: "notice"},
	)
	if opts.ResumeContinuityNotice != "" || opts.ResumeExpected || opts.ResumeSessionID != "" {
		t.Fatalf("scheduled execution options = %+v, want no session state and no notice", opts)
	}
}
