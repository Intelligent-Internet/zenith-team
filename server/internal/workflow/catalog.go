// Package workflow owns the Workflow catalog: release-owned, ready-to-run
// templates a member fills in on the Workflows page and launches as a Task
// for an Agent.
//
// Each Workflow is one directory of the zenith-workflows skill: workflow.yaml
// is the card, the form and the brief template the page renders; playbook.md
// is the procedure a Task Run loads with load_skill_reference. The content is
// authored in Intelligent-Internet/boardly-plugins and pinned in
// internal/platformbundle (ADR 0016); this package parses the same verified
// bytes the MCP server serves, so the page and the Run can never disagree
// about what ships.
//
// It is its own package because the catalog is a domain noun of its own —
// the MCP tools package would otherwise host two domains — and so the zen
// CLI, which links internal/mcp/tools, never links the catalog parser.
package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/platformbundle"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/task"
	taskmilestone "boardly.dev/boardly/server/internal/task/milestone"
)

// ErrNotFound reports a workflow key the catalog does not ship.
var ErrNotFound = errors.New("workflow not found")

// CategoryType is the closed set of catalog sections, in page order.
type CategoryType string

const (
	CategoryTypeFinance      CategoryType = "finance"
	CategoryTypeLegal        CategoryType = "legal"
	CategoryTypeSales        CategoryType = "sales"
	CategoryTypeMarketing    CategoryType = "marketing"
	CategoryTypeResearch     CategoryType = "research"
	CategoryTypeProductivity CategoryType = "productivity"
	CategoryTypeWriting      CategoryType = "writing"
	CategoryTypeRecruiting   CategoryType = "recruiting"
	CategoryTypeMedia        CategoryType = "media"
	CategoryTypeEcommerce    CategoryType = "ecommerce"
	CategoryTypeProduct      CategoryType = "product"
	CategoryTypeRealEstate   CategoryType = "real_estate"
)

var categoryTypes = []CategoryType{
	CategoryTypeFinance, CategoryTypeLegal, CategoryTypeSales, CategoryTypeMarketing, CategoryTypeResearch,
	CategoryTypeProductivity, CategoryTypeWriting, CategoryTypeRecruiting, CategoryTypeMedia,
	CategoryTypeEcommerce, CategoryTypeProduct, CategoryTypeRealEstate,
}

// FieldType is the closed set of form inputs the page renders.
type FieldType string

const (
	FieldTypeText        FieldType = "text"
	FieldTypeTextarea    FieldType = "textarea"
	FieldTypeSelect      FieldType = "select"
	FieldTypeMultiSelect FieldType = "multi_select"
	FieldTypeDate        FieldType = "date"
	FieldTypeURL         FieldType = "url"
)

var fieldTypes = []FieldType{
	FieldTypeText, FieldTypeTextarea, FieldTypeSelect, FieldTypeMultiSelect, FieldTypeDate, FieldTypeURL,
}

// DeliverableType names what a Workflow hands back, for the card and the
// launch dialog. It is presentation: the playbook owns the actual contract.
type DeliverableType string

const (
	DeliverableTypeReport       DeliverableType = "report"
	DeliverableTypeDocument     DeliverableType = "document"
	DeliverableTypeSpreadsheet  DeliverableType = "spreadsheet"
	DeliverableTypePresentation DeliverableType = "presentation"
	DeliverableTypePDF          DeliverableType = "pdf"
	DeliverableTypeCSV          DeliverableType = "csv"
	DeliverableTypeImage        DeliverableType = "image"
	DeliverableTypeVideo        DeliverableType = "video"
	DeliverableTypeWebApp       DeliverableType = "web_app"
	DeliverableTypeEmailDrafts  DeliverableType = "email_drafts"
)

var deliverableTypes = []DeliverableType{
	DeliverableTypeReport, DeliverableTypeDocument, DeliverableTypeSpreadsheet,
	DeliverableTypePresentation, DeliverableTypePDF, DeliverableTypeCSV, DeliverableTypeImage,
	DeliverableTypeVideo, DeliverableTypeWebApp, DeliverableTypeEmailDrafts,
}

// ConnectorType names an external app a Workflow works best with. It is a
// hint the page shows; Connector authority stays with Company Connector
// policy and nothing here grants or checks it. The list is the
// WorkflowConnectorType enum of docs/api/spec/workflows.yaml, in order.
type ConnectorType string

var connectorTypes = []ConnectorType{
	"gmail", "google_calendar", "google_drive", "slack", "notion", "linear", "jira", "asana",
	"clickup", "github", "hubspot", "salesforce", "apollo", "gong", "fireflies", "zendesk",
	"stripe", "shopify", "xero", "linkedin", "figma", "firecrawl", "ashby", "typeform",
}

// IconType is the closed set of Iconsax Outline glyphs the frontend bundles
// for workflow cards (frontend/src/assets/iconsax). The list is the
// WorkflowIconType enum of docs/api/spec/workflows.yaml, in order.
type IconType string

var iconTypes = []IconType{
	"award", "book-open", "box", "box-tick", "briefcase", "brush", "building", "buildings-2",
	"calendar-2", "calendar-tick", "card", "chart-2", "clock", "code", "coin", "color-swatch",
	"cpu", "crown", "designtools", "direct-inbox", "discover", "document-code", "document-like",
	"document-normal", "document-text", "dollar-circle", "driver", "edit-2", "flag", "flash",
	"gallery", "global", "gps", "graph", "hashtag", "hierarchy", "key", "lamp-on", "layer",
	"magic-star", "map", "message-question", "message-text", "messages", "microphone",
	"monitor", "monitor-recorder", "note-text", "people", "profile-2user", "receipt-2",
	"repeat-arrow", "routing-2", "scan", "search-zoom-in-1", "send-2", "shield-security",
	"shield-tick", "sms", "speedometer", "star", "status", "stickynote", "tag", "task-square",
	"text-block", "timer-start", "user-add", "user-tag", "video", "volume-high", "wallet",
}

// Option is one choice of a select or multi_select field.
type Option struct {
	Value string `json:"value" yaml:"value"`
	Label string `json:"label" yaml:"label"`
}

// Field is one form input. Default seeds text, textarea, select, date and
// url fields; Defaults seeds a multi_select. AcceptsFiles lets a textarea take
// files beside, or in place of, the pasted text: the launched Task carries
// them to every Run it starts, so a recurring Workflow never takes them, since
// a Schedule carries no files.
type Field struct {
	Key          string    `json:"key" yaml:"key"`
	Type         FieldType `json:"type" yaml:"type"`
	Label        string    `json:"label" yaml:"label"`
	Required     bool      `json:"required" yaml:"required"`
	Placeholder  string    `json:"placeholder,omitempty" yaml:"placeholder"`
	Description  string    `json:"description,omitempty" yaml:"description"`
	Options      []Option  `json:"options,omitempty" yaml:"options"`
	Default      string    `json:"default,omitempty" yaml:"default"`
	Defaults     []string  `json:"defaults,omitempty" yaml:"defaults"`
	AcceptsFiles bool      `json:"acceptsFiles,omitempty" yaml:"acceptsFiles"`
}

// Page is one step of the launch form.
type Page struct {
	Title  string  `json:"title" yaml:"title"`
	Fields []Field `json:"fields" yaml:"fields"`
}

// Schedule is the optional recurring form of a Workflow. Schedule Runs are
// connector-only (no Skills, web, files or product tools), so Instructions is
// a self-contained template of its own rather than the playbook.
type Schedule struct {
	Cron         string `json:"cron" yaml:"cron"`
	Instructions string `json:"instructions" yaml:"instructions"`
}

// Requirement is an app a Workflow cannot do its job without. It is met when
// at least one app of AnyOf reaches the Run or, when OrField names a field,
// the person answered that field instead (an export attached in place of the
// app). The page holds a launch until every Requirement is met, and the Task
// footer tells the Run to stop rather than guess when one is not. Like the
// connector hints it names, it grants no Connector authority.
type Requirement struct {
	AnyOf   []ConnectorType `json:"anyOf" yaml:"anyOf"`
	OrField string          `json:"orField,omitempty" yaml:"orField"`
}

// PlaybookRef is where a Task Run loads the Workflow's procedure.
type PlaybookRef struct {
	SkillKey string `json:"skillKey"`
	Path     string `json:"path"`
}

// Summary is one catalog card.
type Summary struct {
	Key          string            `json:"key"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Categories   []CategoryType    `json:"categories"`
	Icon         IconType          `json:"icon"`
	Deliverables []DeliverableType `json:"deliverables"`
	Connectors   []ConnectorType   `json:"connectors"`
	Recurring    bool              `json:"recurring"`
	Featured     bool              `json:"featured"`
}

// Workflow is one card plus everything the launch dialog needs.
type Workflow struct {
	Summary
	Highlights    []string           `json:"highlights"`
	Requirements  []Requirement      `json:"requirements"`
	MilestoneType taskmilestone.Type `json:"milestoneType"`
	SubmitLabel   string             `json:"submitLabel"`
	TitleField    string             `json:"titleField,omitempty"`
	Pages         []Page             `json:"pages"`
	Brief         string             `json:"brief"`
	Playbook      PlaybookRef        `json:"playbook"`
	Schedule      *Schedule          `json:"schedule,omitempty"`
}

// catalog is the parsed release catalog. Its values are shared by every
// request: callers serialize them and must never modify them.
type catalog struct {
	workflows []Workflow
	summaries []Summary
	byKey     map[string]int
}

// List returns every Workflow card, ordered by name. The catalog is
// release-owned and identical for every Company. The result is read-only.
func List() ([]Summary, error) {
	parsed, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	return parsed.summaries, nil
}

// Get returns one Workflow by key, or ErrNotFound. The result is read-only.
func Get(key string) (Workflow, error) {
	parsed, err := loadCatalog()
	if err != nil {
		return Workflow{}, err
	}
	index, ok := parsed.byKey[key]
	if !ok {
		return Workflow{}, ErrNotFound
	}
	return parsed.workflows[index], nil
}

var loadCatalog = sync.OnceValues(func() (catalog, error) {
	files, _, err := platformbundle.WorkflowCatalog()
	if err != nil {
		return catalog{}, err
	}
	workflows, err := parseCatalog(files)
	if err != nil {
		return catalog{}, err
	}
	parsed := catalog{
		workflows: workflows,
		summaries: make([]Summary, 0, len(workflows)),
		byKey:     make(map[string]int, len(workflows)),
	}
	for index, workflow := range workflows {
		parsed.summaries = append(parsed.summaries, workflow.Summary)
		parsed.byKey[workflow.Key] = index
	}
	return parsed, nil
})

const (
	workflowFilename = "workflow.yaml"
	playbookFilename = "playbook.md"
	// A launched Task carries the label workflow:<key>, so a key plus that
	// prefix must fit a Task label.
	maxKeyRunes   = task.MaxLabelRunes - len("workflow:")
	maxLabelRunes = 80
	maxHelpRunes  = 240
)

var (
	keyPattern         = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	fieldKeyPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
	optionValuePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// placeholderPattern is the frontend renderer's pattern
	// (frontend/src/features/workflows/model/workflow-template.ts): one line,
	// optional spaces or tabs inside the braces, a field key.
	placeholderPattern = regexp.MustCompile(`\{\{[ \t]*([a-z][a-z0-9_]{0,47})[ \t]*\}\}`)
	// cronCheckTime anchors the Schedule-domain cadence check, which needs a
	// fixed "after" instant to stay deterministic.
	cronCheckTime = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// document is the strict on-disk shape of workflow.yaml.
type document struct {
	SchemaVersion int               `yaml:"schemaVersion"`
	Key           string            `yaml:"key"`
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	Categories    []CategoryType    `yaml:"categories"`
	Icon          IconType          `yaml:"icon"`
	Featured      bool              `yaml:"featured"`
	MilestoneType string            `yaml:"milestoneType"`
	Highlights    []string          `yaml:"highlights"`
	Deliverables  []DeliverableType `yaml:"deliverables"`
	Connectors    []ConnectorType   `yaml:"connectors"`
	Requires      []Requirement     `yaml:"requires"`
	SubmitLabel   string            `yaml:"submitLabel"`
	TitleField    string            `yaml:"titleField"`
	Pages         []Page            `yaml:"pages"`
	Brief         string            `yaml:"brief"`
	Schedule      *Schedule         `yaml:"schedule"`
}

func parseCatalog(root fs.FS) ([]Workflow, error) {
	workflows, problems := readCatalog(root)
	if len(problems) > 0 {
		return nil, problems[0]
	}
	slices.SortFunc(workflows, func(left, right Workflow) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	return workflows, nil
}

// CheckCatalog validates a catalog root exactly as the server loads it and
// reports every problem instead of the first: one error per failing
// Workflow, plus catalog-level errors. bundlectl runs it on a plugins
// checkout, so an author sees them all at once and the validator can never
// disagree with production.
func CheckCatalog(root fs.FS) []error {
	_, problems := readCatalog(root)
	return problems
}

func readCatalog(root fs.FS) ([]Workflow, []error) {
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		return nil, []error{err}
	}
	workflows := make([]Workflow, 0, len(entries))
	var problems []error
	names := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			if entry.Name() != "SKILL.md" {
				problems = append(
					problems,
					fmt.Errorf("workflow catalog: unexpected file %q", entry.Name()),
				)
			}
			continue
		}
		workflow, err := parseWorkflow(root, entry.Name())
		if err != nil {
			problems = append(problems, fmt.Errorf("workflow %q: %w", entry.Name(), err))
			continue
		}
		if previous, taken := names[strings.ToLower(workflow.Name)]; taken {
			problems = append(problems, fmt.Errorf("workflow %q: name %q is already used by %q",
				workflow.Key, workflow.Name, previous))
			continue
		}
		names[strings.ToLower(workflow.Name)] = workflow.Key
		workflows = append(workflows, workflow)
	}
	if len(workflows) == 0 && len(problems) == 0 {
		problems = append(problems, errors.New("workflow catalog is empty"))
	}
	return workflows, problems
}

func parseWorkflow(root fs.FS, key string) (Workflow, error) {
	files, err := fs.ReadDir(root, key)
	if err != nil {
		return Workflow{}, err
	}
	present := make([]string, 0, len(files))
	for _, file := range files {
		present = append(present, file.Name())
	}
	slices.Sort(present)
	if !slices.Equal(present, []string{playbookFilename, workflowFilename}) {
		return Workflow{}, fmt.Errorf(
			"directory must hold exactly %s and %s, found %v", playbookFilename, workflowFilename, present,
		)
	}
	raw, err := fs.ReadFile(root, path.Join(key, workflowFilename))
	if err != nil {
		return Workflow{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return Workflow{}, fmt.Errorf("%s: %w", workflowFilename, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Workflow{}, fmt.Errorf("%s must hold exactly one YAML document", workflowFilename)
	}
	playbook, err := fs.ReadFile(root, path.Join(key, playbookFilename))
	if err != nil {
		return Workflow{}, err
	}
	if len(playbook) == 0 || len(playbook) > 64<<10 || !utf8.Valid(playbook) {
		return Workflow{}, fmt.Errorf("%s must be UTF-8 text of 1 to %d bytes", playbookFilename, 64<<10)
	}
	if !bytes.HasPrefix(playbook, []byte("# ")) {
		return Workflow{}, fmt.Errorf(`%s must open with a "# " title line`, playbookFilename)
	}
	return buildWorkflow(key, doc)
}

func buildWorkflow(key string, doc document) (Workflow, error) {
	if doc.SchemaVersion != 1 {
		return Workflow{}, fmt.Errorf("schemaVersion must be 1, got %d", doc.SchemaVersion)
	}
	if doc.Key != key || !keyPattern.MatchString(key) || utf8.RuneCountInString(key) > maxKeyRunes {
		return Workflow{}, fmt.Errorf(
			"key %q must equal its directory and be a kebab-case slug of at most %d characters", doc.Key, maxKeyRunes,
		)
	}
	checks := []error{
		requireText("name", doc.Name, 80),
		requireText("description", doc.Description, 240),
		requireText("submitLabel", doc.SubmitLabel, 40),
		validateClosedSet("categories", doc.Categories, categoryTypes, 1, 3),
		validateClosedSet("deliverables", doc.Deliverables, deliverableTypes, 1, 4),
		validateClosedSet("connectors", doc.Connectors, connectorTypes, 0, 6),
	}
	if !slices.Contains(iconTypes, doc.Icon) {
		checks = append(checks, fmt.Errorf("icon %q is not a bundled workflow icon", doc.Icon))
	}
	milestoneType := taskmilestone.Type(doc.MilestoneType)
	if !taskmilestone.ValidType(milestoneType) {
		checks = append(checks, fmt.Errorf("milestoneType %q is not a Task milestone", doc.MilestoneType))
	}
	if len(doc.Highlights) != 3 {
		checks = append(checks, errors.New("highlights must hold exactly 3 lines"))
	}
	for index, highlight := range doc.Highlights {
		checks = append(checks, requireText(fmt.Sprintf("highlights[%d]", index), highlight, 100))
	}
	fields, err := validatePages(doc.Pages)
	checks = append(checks, err)
	if doc.TitleField != "" {
		field, ok := fields[doc.TitleField]
		if !ok || (field.Type != FieldTypeText && field.Type != FieldTypeURL && field.Type != FieldTypeSelect) {
			checks = append(checks, fmt.Errorf("titleField %q must name a text, url or select field", doc.TitleField))
		} else if !field.Required {
			checks = append(checks, fmt.Errorf("titleField %q must name a required field", doc.TitleField))
		}
	}
	checks = append(checks, validateRequirements(doc.Requires, doc.Connectors, fields))
	checks = append(checks, validateTemplate("brief", doc.Brief, fields, true))
	if doc.Schedule != nil {
		checks = append(checks,
			validateScheduleCron(doc.Schedule.Cron),
			validateTemplate("schedule.instructions", doc.Schedule.Instructions, fields, false),
		)
		for _, page := range doc.Pages {
			for _, field := range page.Fields {
				if field.AcceptsFiles {
					checks = append(checks, fmt.Errorf(
						"field %q: a recurring workflow cannot take files, because a Schedule carries none", field.Key,
					))
				}
			}
		}
	}
	if err := errors.Join(checks...); err != nil {
		return Workflow{}, err
	}
	return Workflow{
		Summary: Summary{
			Key:          key,
			Name:         doc.Name,
			Description:  doc.Description,
			Categories:   doc.Categories,
			Icon:         doc.Icon,
			Deliverables: doc.Deliverables,
			Connectors:   nonNil(doc.Connectors),
			Recurring:    doc.Schedule != nil,
			Featured:     doc.Featured,
		},
		Highlights:    doc.Highlights,
		Requirements:  nonNil(doc.Requires),
		MilestoneType: milestoneType,
		SubmitLabel:   doc.SubmitLabel,
		TitleField:    doc.TitleField,
		Pages:         doc.Pages,
		Brief:         strings.TrimSpace(doc.Brief),
		Playbook:      PlaybookRef{SkillKey: tools.ZenithWorkflowsSkillKey, Path: key + "/" + playbookFilename},
		Schedule:      trimSchedule(doc.Schedule),
	}, nil
}

func validatePages(pages []Page) (map[string]Field, error) {
	fields := map[string]Field{}
	if len(pages) < 1 || len(pages) > 3 {
		return fields, errors.New("pages must hold 1 to 3 pages")
	}
	var checks []error
	count := 0
	for pageIndex, page := range pages {
		checks = append(checks, requireText(fmt.Sprintf("pages[%d].title", pageIndex), page.Title, maxLabelRunes))
		if len(page.Fields) == 0 {
			checks = append(checks, fmt.Errorf("pages[%d] has no fields", pageIndex))
		}
		for _, field := range page.Fields {
			count++
			name := fmt.Sprintf("field %q", field.Key)
			if !fieldKeyPattern.MatchString(field.Key) {
				checks = append(checks, fmt.Errorf("%s: key must be snake_case", name))
			}
			if _, duplicate := fields[field.Key]; duplicate {
				checks = append(checks, fmt.Errorf("%s: duplicate key", name))
			}
			fields[field.Key] = field
			checks = append(checks, validateField(name, field))
		}
	}
	if count > 16 {
		checks = append(checks, fmt.Errorf("a workflow holds at most 16 fields, found %d", count))
	}
	return fields, errors.Join(checks...)
}

func validateField(name string, field Field) error {
	checks := []error{
		requireText(name+" label", field.Label, maxLabelRunes),
		optionalText(name+" placeholder", field.Placeholder, maxHelpRunes),
		optionalText(name+" description", field.Description, maxHelpRunes),
		optionalText(name+" default", field.Default, maxHelpRunes),
	}
	if !slices.Contains(fieldTypes, field.Type) {
		checks = append(checks, fmt.Errorf("%s: type %q is not supported", name, field.Type))
	}
	if field.AcceptsFiles && field.Type != FieldTypeTextarea {
		checks = append(checks, fmt.Errorf("%s: acceptsFiles belongs to a textarea field", name))
	}
	if field.Type != FieldTypeSelect && field.Type != FieldTypeMultiSelect {
		if len(field.Options) > 0 || len(field.Defaults) > 0 {
			checks = append(checks, fmt.Errorf("%s: options and defaults belong to select fields", name))
		}
		checks = append(checks, validateScalarDefault(name, field))
		return errors.Join(checks...)
	}
	if len(field.Options) < 2 || len(field.Options) > 30 {
		checks = append(checks, fmt.Errorf("%s: needs 2 to 30 options", name))
	}
	options := map[string]bool{}
	for _, option := range field.Options {
		if !optionValuePattern.MatchString(option.Value) || options[option.Value] {
			checks = append(checks, fmt.Errorf("%s: option value %q must be a unique lowercase code", name, option.Value))
		}
		options[option.Value] = true
		checks = append(checks, requireText(name+" option label", option.Label, maxLabelRunes))
	}
	if field.Default != "" && (field.Type != FieldTypeSelect || !options[field.Default]) {
		checks = append(checks, fmt.Errorf("%s: default %q must be one of its options", name, field.Default))
	}
	chosen := map[string]bool{}
	for _, value := range field.Defaults {
		if field.Type != FieldTypeMultiSelect || !options[value] || chosen[value] {
			checks = append(checks, fmt.Errorf("%s: defaults must be distinct options of a multi_select", name))
		}
		chosen[value] = true
	}
	return errors.Join(checks...)
}

// validateRequirements holds each Requirement to apps the Workflow already
// names, each app to at most one Requirement, and an OrField to an optional
// field a person can answer in the app's place: a required field would make
// the app pointless, and a choice cannot stand in for data.
func validateRequirements(requirements []Requirement, connectors []ConnectorType, fields map[string]Field) error {
	if len(requirements) > 3 {
		return errors.New("requires must hold at most 3 requirements")
	}
	var checks []error
	claimed := map[ConnectorType]bool{}
	for index, requirement := range requirements {
		name := fmt.Sprintf("requires[%d]", index)
		if err := validateClosedSet(name+".anyOf", requirement.AnyOf, connectors, 1, 4); err != nil {
			checks = append(checks, fmt.Errorf("%w (each app must be one of the workflow's connectors)", err))
		}
		for _, app := range requirement.AnyOf {
			if claimed[app] {
				checks = append(checks, fmt.Errorf("%s: %s already belongs to another requirement", name, app))
			}
			claimed[app] = true
		}
		if requirement.OrField == "" {
			continue
		}
		field, ok := fields[requirement.OrField]
		switch {
		case !ok:
			checks = append(checks, fmt.Errorf("%s: orField %q names no field", name, requirement.OrField))
		case field.Required:
			checks = append(checks, fmt.Errorf("%s: orField %q must name an optional field", name, requirement.OrField))
		case field.Type != FieldTypeTextarea && field.Type != FieldTypeURL:
			checks = append(checks, fmt.Errorf("%s: orField %q must name a textarea or url field", name, requirement.OrField))
		}
	}
	return errors.Join(checks...)
}

// validateScalarDefault holds a date or url default to the rule the page
// applies to the person's own answer, so a form never opens already invalid.
func validateScalarDefault(name string, field Field) error {
	if field.Default == "" {
		return nil
	}
	switch field.Type {
	case FieldTypeDate:
		if _, err := time.Parse(time.DateOnly, field.Default); err != nil {
			return fmt.Errorf("%s: a date default is a real YYYY-MM-DD date", name)
		}
	case FieldTypeURL:
		parsed, err := url.Parse(field.Default)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("%s: a url default is an absolute http or https URL", name)
		}
	}
	return nil
}

// validateScheduleCron holds a Schedule template to the Schedule domain's own
// cron rules, so "Schedule it" never seeds an expression the editor's save
// would refuse.
func validateScheduleCron(expression string) error {
	zone := "UTC"
	_, err := schedule.PreviewTrigger(schedule.Trigger{
		Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone,
	}, cronCheckTime, 1)
	if err != nil {
		return fmt.Errorf("schedule.cron %q is not a valid Schedule cron expression", expression)
	}
	return nil
}

// validateTemplate checks {{field}} placeholders. The brief must place every
// field, so no answer a person gives is silently dropped from the Task, and a
// brace outside a well-formed placeholder is refused because the page would
// print it literally.
func validateTemplate(name, template string, fields map[string]Field, placeAll bool) error {
	if strings.TrimSpace(template) == "" || len(template) > 16<<10 || !utf8.ValidString(template) {
		return fmt.Errorf("%s must be non-empty text of at most %d bytes", name, 16<<10)
	}
	used := map[string]bool{}
	var checks []error
	for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		if _, ok := fields[match[1]]; !ok {
			checks = append(checks, fmt.Errorf("%s: placeholder {{%s}} names no field", name, match[1]))
		}
		used[match[1]] = true
	}
	if rest := placeholderPattern.ReplaceAllString(template, ""); strings.Contains(rest, "{{") ||
		strings.Contains(rest, "}}") {
		checks = append(checks, fmt.Errorf("%s: stray braces outside a {{field}} placeholder", name))
	}
	if placeAll {
		for key := range fields {
			if !used[key] {
				checks = append(checks, fmt.Errorf("%s: field %q is never placed", name, key))
			}
		}
	}
	return errors.Join(checks...)
}

// requireText holds one-line UI text: present, trimmed, bounded and free of
// control characters.
func requireText(name, value string, maxRunes int) error {
	if value == "" {
		return fmt.Errorf("%s must be one line of 1 to %d characters", name, maxRunes)
	}
	return optionalText(name, value, maxRunes)
}

func optionalText(name, value string, maxRunes int) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > maxRunes ||
		!utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) ||
		strings.ContainsAny(value, "  ") {
		return fmt.Errorf("%s must be one trimmed line of at most %d characters", name, maxRunes)
	}
	return nil
}

func validateClosedSet[T comparable](name string, values, allowed []T, minCount, maxCount int) error {
	if len(values) < minCount || len(values) > maxCount {
		return fmt.Errorf("%s must hold %d to %d values", name, minCount, maxCount)
	}
	seen := map[T]bool{}
	for _, value := range values {
		if !slices.Contains(allowed, value) || seen[value] {
			return fmt.Errorf("%s: %v is unknown or repeated", name, value)
		}
		seen[value] = true
	}
	return nil
}

func trimSchedule(value *Schedule) *Schedule {
	if value == nil {
		return nil
	}
	return &Schedule{Cron: strings.TrimSpace(value.Cron), Instructions: strings.TrimSpace(value.Instructions)}
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
