package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newQuestionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "question",
		Short: "Founder questions: list, ask (Agent mode), answer and withdraw",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List questions (pending by default)",
		Args:  cobra.NoArgs,
		RunE:  runQuestionList,
	}
	addHITLListFlags(list)
	get := &cobra.Command{
		Use:   "get <question-id>",
		Short: "Show one question with its prompts and answers",
		Args:  cobra.ExactArgs(1),
		RunE:  runQuestionGet,
	}
	ask := &cobra.Command{
		Use:   "ask",
		Short: "Raise one founder question from the current Run (Agent mode)",
		Long: "Creates a Question card for the founder. Each --prompt starts a prompt; --option values attach to the " +
			"most recent prompt. Custom answers are allowed unless --no-custom-answer is given. For a complex " +
			"questionnaire pass the exact API prompts array with --prompts-file. End the Run after asking: the " +
			"founder's answer starts a continuation Run.",
		Args: cobra.NoArgs,
		RunE: runQuestionAsk,
	}
	ask.Flags().String("title", "", "question title (required)")
	ask.Flags().StringArray("prompt", nil, "prompt text (repeatable; starts a new prompt)")
	ask.Flags().StringArray("option", nil, "option for the most recent prompt (repeatable)")
	ask.Flags().StringArray("help-text", nil, "help text for the most recent prompt (repeatable)")
	ask.Flags().Bool("required", true, "prompts must be answered")
	ask.Flags().Bool("multi", false, "allow selecting several options per prompt")
	ask.Flags().Bool("no-custom-answer", false, "disallow free-text answers")
	ask.Flags().String("prompts-file", "", "JSON file holding the exact prompts array")
	_ = ask.MarkFlagRequired("title")

	answer := &cobra.Command{
		Use:   "answer <question-id>",
		Short: "Answer a question as the founder",
		Args:  cobra.ExactArgs(1),
		RunE:  runQuestionAnswer,
	}
	answer.Flags().StringArray("text", nil, "free-text answer per prompt, in prompt order (repeatable)")
	answer.Flags().
		StringArray("select", nil, "selected option ids per prompt, comma-separated, in prompt order (repeatable)")
	answer.Flags().String("answers-file", "", "JSON file holding the exact answers array")
	addRevisionFlag(answer)

	withdraw := &cobra.Command{
		Use:   "withdraw <question-id>",
		Short: "Withdraw a pending question",
		Args:  cobra.ExactArgs(1),
		RunE:  runQuestionWithdraw,
	}
	addRevisionFlag(withdraw)
	cmd.AddCommand(list, get, ask, answer, withdraw)
	return cmd
}

func addHITLListFlags(cmd *cobra.Command) {
	cmd.Flags().String("status", "", "comma-separated status filter (default pending)")
	cmd.Flags().String("agent", "", "only items raised by this agent id")
	cmd.Flags().String("task", "", "only items of this task id")
	cmd.Flags().Int("limit", 50, "page size (1-100)")
	cmd.Flags().String("cursor", "", "cursor from a previous page")
}

func hitlListQuery(cmd *cobra.Command) (string, error) {
	flag := func(name string) string { value, _ := cmd.Flags().GetString(name); return strings.TrimSpace(value) }
	for _, name := range []string{"agent", "task"} {
		if value := flag(name); value != "" {
			if _, err := uuidArg("--"+name, value); err != nil {
				return "", err
			}
		}
	}
	limit, _ := cmd.Flags().GetInt("limit")
	return queryString(
		[2]string{"status", flag("status")},
		[2]string{"agent_id", flag("agent")},
		[2]string{"task_id", flag("task")},
		[2]string{"limit", strconv.Itoa(limit)},
		[2]string{"cursor", flag("cursor")},
	), nil
}

type questionView struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Title        string  `json:"title"`
	TaskID       *string `json:"taskId"`
	Revision     int64   `json:"revision"`
	CreatedAt    string  `json:"createdAt"`
	AskedByAgent struct {
		Name string `json:"name"`
	} `json:"askedByAgent"`
	Prompts []struct {
		ID      string `json:"id"`
		Prompt  string `json:"prompt"`
		Options []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"options"`
	} `json:"prompts"`
}

func runQuestionList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	query, err := hitlListQuery(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("questions")+query, &raw); err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	if format == outputJSON {
		return printJSON(cmd.OutOrStdout(), raw)
	}
	var page struct {
		Items      []questionView `json:"items"`
		NextCursor *string        `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return fmt.Errorf("decode questions: %w", err)
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(
			rows,
			[]string{
				item.ID,
				item.Status,
				truncate(item.Title, 48),
				dashIfEmpty(item.AskedByAgent.Name),
				stringOrDash(item.TaskID),
				item.CreatedAt,
			},
		)
	}
	printTable(cmd.OutOrStdout(), []string{"ID", "STATUS", "TITLE", "AGENT", "TASK", "CREATED"}, rows)
	noteCursor(cmd, page.NextCursor)
	return nil
}

func runQuestionGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	questionID, err := uuidArg("question id", args[0])
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.get(ctx, client.companyPath("questions", questionID), &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

// buildPrompts turns the repeatable flags into the API prompts array. Ids
// are positional (p1, p2, ...) so an answer can name them without a read.
func buildPrompts(cmd *cobra.Command) ([]map[string]any, error) {
	if file, _ := cmd.Flags().GetString("prompts-file"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read --prompts-file: %w", err)
		}
		var prompts []map[string]any
		if err := json.Unmarshal(data, &prompts); err != nil {
			return nil, usagef("--prompts-file must hold a JSON array of prompts: %v", err)
		}
		return prompts, nil
	}
	texts, _ := cmd.Flags().GetStringArray("prompt")
	if len(texts) == 0 {
		return nil, usagef("at least one --prompt (or --prompts-file) is required")
	}
	// Options and help texts belong to the prompt that precedes them on the
	// command line; pflag keeps per-flag order, so replay the original
	// argument order to assign them.
	options := make([][]string, len(texts))
	helpTexts := make([]string, len(texts))
	current := -1
	optionQueue, _ := cmd.Flags().GetStringArray("option")
	helpQueue, _ := cmd.Flags().GetStringArray("help-text")
	optionIndex, helpIndex := 0, 0
	for _, argument := range os.Args {
		switch {
		case argument == "--prompt" || strings.HasPrefix(argument, "--prompt="):
			current++
		case (argument == "--option" || strings.HasPrefix(argument, "--option=")) && optionIndex < len(optionQueue):
			if current < 0 {
				return nil, usagef("--option must follow a --prompt")
			}
			options[current] = append(options[current], optionQueue[optionIndex])
			optionIndex++
		case (argument == "--help-text" || strings.HasPrefix(argument, "--help-text=")) && helpIndex < len(helpQueue):
			if current < 0 {
				return nil, usagef("--help-text must follow a --prompt")
			}
			helpTexts[current] = helpQueue[helpIndex]
			helpIndex++
		}
	}
	// Outside a real process (tests drive the command directly) os.Args has
	// no flags; then every option belongs to the last prompt.
	if optionIndex == 0 && len(optionQueue) != 0 {
		options[len(texts)-1] = append(options[len(texts)-1], optionQueue...)
	}
	if helpIndex == 0 && len(helpQueue) != 0 {
		helpTexts[len(texts)-1] = helpQueue[len(helpQueue)-1]
	}
	required, _ := cmd.Flags().GetBool("required")
	multi, _ := cmd.Flags().GetBool("multi")
	noCustom, _ := cmd.Flags().GetBool("no-custom-answer")
	prompts := make([]map[string]any, 0, len(texts))
	for index, text := range texts {
		// The API vocabulary is "single" and "multi"; sending anything else
		// fails validation for the whole question.
		selection := "single"
		if multi {
			selection = "multi"
		}
		prompt := map[string]any{
			"id":                fmt.Sprintf("p%d", index+1),
			"prompt":            text,
			"selectionMode":     selection,
			"required":          required,
			"allowCustomAnswer": !noCustom,
		}
		if helpTexts[index] != "" {
			prompt["helpText"] = helpTexts[index]
		}
		promptOptions := make([]map[string]string, 0, len(options[index]))
		for optionIndex, label := range options[index] {
			promptOptions = append(promptOptions, map[string]string{
				"id":    fmt.Sprintf("p%d-o%d", index+1, optionIndex+1),
				"label": label,
			})
		}
		prompt["options"] = promptOptions
		prompts = append(prompts, prompt)
	}
	return prompts, nil
}

func runQuestionAsk(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if client.context.mode != modeAgent {
		return &modeError{msg: "`zen question ask` is proven by a Run and only works in Agent mode; " +
			"an operator answers questions with `zen question answer`"}
	}
	title, _ := cmd.Flags().GetString("title")
	if strings.TrimSpace(title) == "" {
		return usagef("--title is required")
	}
	prompts, err := buildPrompts(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	var raw json.RawMessage
	if err := client.post(
		ctx,
		client.companyPath("questions"),
		map[string]any{"title": strings.TrimSpace(title), "prompts": prompts},
		&raw,
	); err != nil {
		return err
	}
	fmt.Fprintln(
		cmd.ErrOrStderr(),
		"question raised; end the Run now — the founder's answer starts a continuation Run",
	)
	return printResult(cmd, raw, nil)
}

func runQuestionAnswer(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	questionID, err := uuidArg("question id", args[0])
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	path := client.companyPath("questions", questionID)
	var answers []map[string]any
	var revision int64
	if file, _ := cmd.Flags().GetString("answers-file"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read --answers-file: %w", err)
		}
		if err := json.Unmarshal(data, &answers); err != nil {
			return usagef("--answers-file must hold a JSON array of answers: %v", err)
		}
		if revision, err = resolveRevision(ctx, cmd, client, path); err != nil {
			return err
		}
	} else {
		texts, _ := cmd.Flags().GetStringArray("text")
		selections, _ := cmd.Flags().GetStringArray("select")
		if len(texts)+len(selections) == 0 {
			return usagef("pass --text or --select per prompt, or --answers-file")
		}
		// One read supplies both the prompt ids the answers name and the
		// revision the POST must carry; --revision only overrides the latter.
		var question questionView
		if err := client.get(ctx, path, &question); err != nil {
			return err
		}
		revision = question.Revision
		if pinned, _ := cmd.Flags().GetInt64("revision"); pinned > 0 {
			revision = pinned
		}
		for index, prompt := range question.Prompts {
			answer := map[string]any{"promptId": prompt.ID, "optionIds": []string{}}
			if index < len(selections) && strings.TrimSpace(selections[index]) != "" {
				answer["optionIds"] = strings.Split(strings.ReplaceAll(selections[index], " ", ""), ",")
			}
			if index < len(texts) && texts[index] != "" {
				answer["text"] = texts[index]
			}
			answers = append(answers, answer)
		}
	}
	var raw json.RawMessage
	if err := client.post(
		ctx,
		path+"/answer",
		map[string]any{"expectedRevision": revision, "answers": answers},
		&raw,
	); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}

func runQuestionWithdraw(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	questionID, err := uuidArg("question id", args[0])
	if err != nil {
		return err
	}
	ctx, cancel := apiContext()
	defer cancel()
	path := client.companyPath("questions", questionID)
	revision, err := resolveRevision(ctx, cmd, client, path)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := client.post(ctx, path+"/withdraw", map[string]any{"expectedRevision": revision}, &raw); err != nil {
		return err
	}
	return printResult(cmd, raw, nil)
}
