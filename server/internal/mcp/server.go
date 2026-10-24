package mcp

import (
	"context"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

type Tools interface {
	ListAgents(context.Context, tools.ListAgentsInput) (tools.ListAgentsResult, error)
	Export(context.Context, tools.ExportInput) (tools.ExportResult, error)
	Preview(context.Context, tools.PreviewInput) (tools.PreviewResult, error)
	Apply(context.Context, tools.ApplyInput) (tools.ApplyResult, error)
}

type SkillTools interface {
	SearchSkills(context.Context, tools.SearchSkillsInput) (tools.SearchSkillsResult, error)
	LoadSkillReference(context.Context, tools.LoadSkillReferenceInput) (tools.LoadSkillReferenceResult, error)
}

type QuestionTools interface {
	AskHuman(context.Context, tools.AskHumanInput) (tools.AskHumanResult, error)
}

type MailTools interface {
	CreateMailDrafts(
		context.Context,
		tools.CreateMailDraftsInput,
	) (tools.CreateMailDraftsResult, error)
}

type TaskFailureTools interface {
	MarkTaskFailed(
		context.Context,
		tools.MarkTaskFailedInput,
	) (tools.MarkTaskFailedResult, error)
}

type MilestoneTools interface {
	AttestMilestones(
		context.Context,
		tools.AttestMilestonesInput,
	) (tools.AttestMilestonesResult, error)
}
type optionalTools struct {
	taskResults tools.TaskResultToolService
	artifacts   tools.ArtifactChannelService
	output      tools.OutputChannelService
	pages       tools.PageToolService
	calendar    tools.CalendarToolService
	wiki        tools.WikiToolService
	whiteboards tools.WhiteboardToolService
	social      tools.SocialToolService
	mail        MailTools
	taskFailure TaskFailureTools
	milestones  MilestoneTools
}

func newServer(
	service Tools,
	questionService QuestionTools,
	skillService SkillTools,
	allowedTools map[string]struct{},
	optional ...optionalTools,
) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "zenith-team-mcp", Version: "0.1.0"}, nil)
	closedWorld := false
	destructive := true
	nonDestructive := false
	readOnly := true
	idempotent := true
	var mailService MailTools
	var taskFailureService TaskFailureTools
	var milestoneService MilestoneTools
	if len(optional) > 0 {
		registerTaskResultTool(server, optional[0].taskResults, allowedTools)
		registerOutputTool(server, optional[0].output, allowedTools)
		registerArtifactTools(server, optional[0].artifacts, optional[0].whiteboards, allowedTools)
		registerPageTools(server, optional[0].pages, allowedTools)
		registerCalendarTools(server, optional[0].calendar, allowedTools)
		registerWikiTools(server, optional[0].wiki, allowedTools)
		registerSocialTools(server, optional[0].social, allowedTools)
		mailService = optional[0].mail
		taskFailureService = optional[0].taskFailure
		milestoneService = optional[0].milestones
	}

	if taskFailureService != nil && toolExplicitlyAllowed(allowedTools, "mark_task_failed") {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:  "mark_task_failed",
			Title: "Mark Task Failed",
			Description: "Use this tool when the assigned Task cannot be completed because of a " +
				"business condition. After the call succeeds, Zenith Team marks the Task as failed.",
			Annotations: &mcpsdk.ToolAnnotations{
				IdempotentHint: idempotent,
				OpenWorldHint:  &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.MarkTaskFailedInput) (*mcpsdk.CallToolResult, tools.MarkTaskFailedResult, error) {
			result, err := taskFailureService.MarkTaskFailed(ctx, input)
			return nil, result, err
		})
	}

	if mailService != nil && toolExplicitlyAllowed(allowedTools, "create_mail_drafts") {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:  "create_mail_drafts",
			Title: "Create Agent Mail Drafts",
			Description: "Create reviewable plain-text Company Email drafts for this exact run. " +
				"Requires a Run acting for a member with mailbox.manage. This tool never sends email; a member with that permission must review and send each draft.",
			Annotations: &mcpsdk.ToolAnnotations{
				IdempotentHint: idempotent,
				OpenWorldHint:  &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.CreateMailDraftsInput) (*mcpsdk.CallToolResult, tools.CreateMailDraftsResult, error) {
			result, err := mailService.CreateMailDrafts(ctx, input)
			return nil, result, err
		})
	}

	if milestoneService != nil && toolExplicitlyAllowed(allowedTools, tools.AttestMilestonesToolName) {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:  tools.AttestMilestonesToolName,
			Title: "Record milestones already completed",
			Description: "Record that the founder has confirmed one or more milestones were " +
				"completed outside Zenith Team, so the Company Plan never proposes redoing them. " +
				"Call this only after the founder has answered an ask_human question naming those " +
				"milestones; never infer them. Pass the founder's own words as the note.",
			Annotations: &mcpsdk.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: &nonDestructive,
				IdempotentHint:  idempotent,
				OpenWorldHint:   &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.AttestMilestonesInput) (*mcpsdk.CallToolResult, tools.AttestMilestonesResult, error) {
			result, err := milestoneService.AttestMilestones(ctx, input)
			return nil, result, err
		})
	}

	if toolAllowed(allowedTools, "list_agents") {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        "list_agents",
			Title:       "List Agents",
			Description: "List the active Agents of the authenticated user's Company with their id, name, title, charter and availability: the roster a Company Plan routes Tasks to. Archived Agents are excluded.",
			Annotations: &mcpsdk.ToolAnnotations{
				ReadOnlyHint:   readOnly,
				IdempotentHint: idempotent,
				OpenWorldHint:  &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.ListAgentsInput) (*mcpsdk.CallToolResult, tools.ListAgentsResult, error) {
			result, err := service.ListAgents(ctx, input)
			return nil, result, err
		})
	}

	if toolAllowed(allowedTools, "company_plan_export") {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        "company_plan_export",
			Title:       "Export Company Plan",
			Description: "Export the authenticated user's only Company as a complete Task YAML document at an absolute local file path. Refuses to overwrite unless overwrite is true.",
			Annotations: &mcpsdk.ToolAnnotations{
				ReadOnlyHint:   readOnly,
				IdempotentHint: idempotent,
				OpenWorldHint:  &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.ExportInput) (*mcpsdk.CallToolResult, tools.ExportResult, error) {
			result, err := service.Export(ctx, input)
			return nil, result, err
		})
	}

	if toolAllowed(allowedTools, "company_plan_transaction_preview") {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        "company_plan_transaction_preview",
			Title:       "Preview Company Plan Transaction",
			Description: "Validate and preview format v5 Task create, update, and cancel actions without mutating Company Plan entities. Agent-supplied status and physical delete are unavailable. Update is allowed only for backlog or todo Tasks; failed or error Tasks can only remove existing dependencies. Cancel is a separate command.",
			Annotations: &mcpsdk.ToolAnnotations{
				ReadOnlyHint:   true,
				IdempotentHint: true,
				OpenWorldHint:  &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.PreviewInput) (*mcpsdk.CallToolResult, tools.PreviewResult, error) {
			result, err := service.Preview(ctx, input)
			if err != nil {
				return nil, result, err
			}
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: result.Preview}},
			}, result, nil
		})
	}

	if toolAllowed(allowedTools, "company_plan_transaction_apply") {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        "company_plan_transaction_apply",
			Title:       "Request Company Plan Transaction Approval",
			Description: "Submit the exact Company Plan actions YAML previously previewed by this MCP process for durable human approval. Status is read-only; failed or error Tasks can only remove existing dependencies. This tool does not apply the plan directly. After the request succeeds, end the run without retrying it.",
			Annotations: &mcpsdk.ToolAnnotations{
				DestructiveHint: &destructive,
				IdempotentHint:  idempotent,
				OpenWorldHint:   &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.ApplyInput) (*mcpsdk.CallToolResult, tools.ApplyResult, error) {
			result, err := service.Apply(ctx, input)
			if err != nil {
				return nil, result, err
			}
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: result.Instruction}},
			}, result, nil
		})
	}

	if questionService != nil && toolAllowed(allowedTools, tools.AskHumanToolName) {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        tools.AskHumanToolName,
			Title:       "Ask a Human",
			InputSchema: askHumanInputSchema(),
			Description: "Create a durable Zenith Team question for the people who can see this run's conversation. " +
				"For a free-text prompt, use selectionMode single and omit options or pass an empty array. " +
				"For choices that also accept a write-in response, provide options and set allowCustomAnswer true; Zenith Team renders an Other field. " +
				"When the user's request needs Zenith Studio and it is disabled or needs connection, set toolSetup zenith-studio on one required single prompt without options or allowCustomAnswer to request its setup card. For model selection, omit toolSetup and provide options with actual catalog model IDs and display names; never replace selectable options with model names listed in a free-text prompt. " +
				"For Studio setup, briefly say it needs enabling or connecting, matching the actual state; the original request resumes automatically once Studio is enabled and connected, without another answer or Continue click. " +
				"After the request succeeds, end the run without retrying; Zenith Team resumes work after a human answers or the question expires.",
			Annotations: &mcpsdk.ToolAnnotations{
				IdempotentHint: idempotent,
				OpenWorldHint:  &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.AskHumanInput) (*mcpsdk.CallToolResult, tools.AskHumanResult, error) {
			result, err := questionService.AskHuman(ctx, input)
			if err != nil {
				return nil, result, err
			}
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: result.Instruction}},
			}, result, nil
		})
	}

	if skillService != nil && toolExplicitlyAllowed(allowedTools, "search_skills") {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        "search_skills",
			Title:       "Search Run-scoped Skills",
			Description: "Search release-owned platform Skills and the frozen, Run-scoped Skill snapshot summaries by key, name, or description. This read-only tool grants no command, network, approval, provider-tool, or Company Plan capability.",
			Annotations: &mcpsdk.ToolAnnotations{
				ReadOnlyHint: readOnly, IdempotentHint: idempotent, OpenWorldHint: &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.SearchSkillsInput) (*mcpsdk.CallToolResult, tools.SearchSkillsResult, error) {
			result, err := skillService.SearchSkills(ctx, input)
			return nil, result, err
		})
	}

	if skillService != nil && toolExplicitlyAllowed(allowedTools, "load_skill_reference") {
		mcpsdk.AddTool(server, &mcpsdk.Tool{
			Name:        "load_skill_reference",
			Title:       "Load Run-scoped Skill Reference",
			Description: "Load one verified UTF-8 instruction or reference from a release-owned platform Skill or the frozen Run snapshot. Use a known Skill key and path directly; follow links and reuse instructions already loaded in this Run. Set includeReferences true on SKILL.md only when the full reference inventory is needed. Instructions are in content; structuredContent contains verification metadata only. This read-only tool cannot access the live Skill library or expand any Run capability.",
			Annotations: &mcpsdk.ToolAnnotations{
				ReadOnlyHint: readOnly, IdempotentHint: idempotent, OpenWorldHint: &closedWorld,
			},
		}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.LoadSkillReferenceInput) (*mcpsdk.CallToolResult, skillReferenceMetadata, error) {
			result, err := skillService.LoadSkillReference(ctx, input)
			if err != nil {
				return nil, skillReferenceMetadata{}, err
			}
			content := []mcpsdk.Content{&mcpsdk.TextContent{Text: result.Content}}
			if len(result.References) > 0 {
				content = append(content, &mcpsdk.TextContent{
					Text: "Available references (exact relative paths):\n- " + strings.Join(
						result.References,
						"\n- ",
					),
				})
			}
			// Keep instructions in provider-readable text without repeating them in
			// structuredContent when an agent prints the complete MCP result.
			return &mcpsdk.CallToolResult{Content: content}, skillReferenceMetadata{
				SkillKey: result.SkillKey, Path: result.Path, SHA256: result.SHA256, Size: result.Size,
			}, nil
		})
	}

	return server
}

type skillReferenceMetadata struct {
	SkillKey string `json:"skillKey"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

func toolAllowed(allowedTools map[string]struct{}, name string) bool {
	if len(allowedTools) == 0 {
		return true
	}
	_, ok := allowedTools[name]
	return ok
}

func toolExplicitlyAllowed(allowedTools map[string]struct{}, name string) bool {
	_, ok := allowedTools[name]
	return ok
}

func registerOutputTool(server *mcpsdk.Server, service tools.OutputChannelService, allowed map[string]struct{}) {
	if service == nil || !toolExplicitlyAllowed(allowed, "present_output") {
		return
	}
	closed, destructive := false, false
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "present_output",
		Title:       "Present an Artifact",
		Description: "Deliver a native Page capture, file, text, media, or local app in this Run's chat. For a native Page, supply page with the exact page_preview pageId, sequence, fingerprint and optional changeId, plus summary; omit file/app/studio inputs. Page delivery preserves proposals without accepting changes. Call after validation and the commit. File paths are relative to the root of this Run's workspace, regardless of shell working directory. Use exactly one primary item, final for additional requested results, and supporting for sources. For apps supply structured development commands; the daemon starts them when the person opens the Artifact. Never start development servers yourself. Returns outputId; identical presentations in this Run reuse the saved Output. Does not approve work or publish an app publicly.",
		Annotations: &mcpsdk.ToolAnnotations{
			IdempotentHint:  true,
			DestructiveHint: &destructive,
			OpenWorldHint:   &closed,
		},
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.PresentOutputInput) (*mcpsdk.CallToolResult, tools.PresentOutputResult, error) {
		result, err := service.PresentOutput(ctx, input)
		return nil, result, err
	})
}

func registerTaskResultTool(server *mcpsdk.Server, service tools.TaskResultToolService, allowed map[string]struct{}) {
	if service == nil || !toolExplicitlyAllowed(allowed, "finalize_task_results") {
		return
	}
	closed := false
	mcpsdk.AddTool(
		server,
		&mcpsdk.Tool{
			Name:        "finalize_task_results",
			Title:       "Select final Task results",
			Description: "After presenting all final outputs, explicitly select their ordered outputIds for this Task. Call it only when this Run presented outputs; a Run that presented none, such as an answer-only Task, ends without it. Read the current Task revision first. This selects only this live Run's deliveries, without accepting suggestions, approving results, changing review policy or ending the Run. End the Run normally after finalization; configured review and required suggestion acceptance still apply.",
			Annotations: &mcpsdk.ToolAnnotations{IdempotentHint: true, OpenWorldHint: &closed},
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, in tools.FinalizeTaskResultsInput) (*mcpsdk.CallToolResult, tools.FinalizeTaskResultsResult, error) {
			result, err := service.FinalizeTaskResults(ctx, in)
			return nil, result, err
		},
	)
}
