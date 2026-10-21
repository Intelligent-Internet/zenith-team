package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const (
	runtimeModelCatalogMaxBytes  = 1 << 20
	runtimeModelCatalogMaxModels = 100
	runtimeModelMaxLevels        = 16
	runtimeModelMaxIDBytes       = 255
	runtimeModelMaxLabelBytes    = 255
	runtimeModelMaxDetailBytes   = 1024
)

// RuntimeModel is one provider-native model advertised by an exact runtime.
// IDs and reasoning values are passed back to that provider unchanged.
type RuntimeModel struct {
	ID        string                 `json:"id"`
	Label     string                 `json:"label"`
	Default   bool                   `json:"default,omitempty"`
	Reasoning *RuntimeModelReasoning `json:"reasoning,omitempty"`
}

// RuntimeModelReasoning describes the provider-native reasoning choices for
// one model. An absent value means the runtime did not advertise a picker.
type RuntimeModelReasoning struct {
	DefaultLevel string                  `json:"defaultLevel,omitempty"`
	Levels       []RuntimeReasoningLevel `json:"levels"`
}

// RuntimeReasoningLevel is one provider-native reasoning choice.
type RuntimeReasoningLevel struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// NormalizeRuntimeModels validates and clones an untrusted model catalog at
// the daemon/CLI boundary. It is intentionally independent of transport and
// Zenith Team domains so every discovery source applies the same limits.
func NormalizeRuntimeModels(models []RuntimeModel) ([]RuntimeModel, error) {
	if models == nil || len(models) > runtimeModelCatalogMaxModels {
		return nil, errors.New("model catalog size is invalid")
	}
	normalized := make([]RuntimeModel, 0, len(models))
	modelIDs := make(map[string]struct{}, len(models))
	defaultCount := 0
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		model.Label = strings.TrimSpace(model.Label)
		if !boundedRuntimeModelString(model.ID, runtimeModelMaxIDBytes) ||
			!boundedRuntimeModelString(model.Label, runtimeModelMaxLabelBytes) {
			return nil, errors.New("model catalog contains an invalid model")
		}
		if _, exists := modelIDs[model.ID]; exists {
			return nil, errors.New("model catalog contains duplicate model ids")
		}
		modelIDs[model.ID] = struct{}{}
		if model.Default {
			defaultCount++
			if defaultCount > 1 {
				return nil, errors.New("model catalog contains multiple defaults")
			}
		}
		if model.Reasoning != nil {
			reasoning, err := normalizeRuntimeModelReasoning(*model.Reasoning)
			if err != nil {
				return nil, err
			}
			model.Reasoning = &reasoning
		}
		normalized = append(normalized, model)
	}
	return normalized, nil
}

func normalizeRuntimeModelReasoning(reasoning RuntimeModelReasoning) (RuntimeModelReasoning, error) {
	if reasoning.Levels == nil || len(reasoning.Levels) > runtimeModelMaxLevels {
		return RuntimeModelReasoning{}, errors.New("model catalog reasoning size is invalid")
	}
	reasoning.DefaultLevel = strings.TrimSpace(reasoning.DefaultLevel)
	if len(reasoning.DefaultLevel) > runtimeModelMaxIDBytes {
		return RuntimeModelReasoning{}, errors.New("model catalog reasoning default is invalid")
	}
	levels := make([]RuntimeReasoningLevel, 0, len(reasoning.Levels))
	values := make(map[string]struct{}, len(reasoning.Levels))
	for _, level := range reasoning.Levels {
		level.Value = strings.TrimSpace(level.Value)
		level.Label = strings.TrimSpace(level.Label)
		level.Description = strings.TrimSpace(level.Description)
		if !boundedRuntimeModelString(level.Value, runtimeModelMaxIDBytes) ||
			!boundedRuntimeModelString(level.Label, runtimeModelMaxLabelBytes) ||
			len(level.Description) > runtimeModelMaxDetailBytes {
			return RuntimeModelReasoning{}, errors.New("model catalog contains an invalid reasoning level")
		}
		if _, exists := values[level.Value]; exists {
			return RuntimeModelReasoning{}, errors.New("model catalog contains duplicate reasoning levels")
		}
		values[level.Value] = struct{}{}
		levels = append(levels, level)
	}
	if reasoning.DefaultLevel != "" {
		if _, exists := values[reasoning.DefaultLevel]; !exists {
			return RuntimeModelReasoning{}, errors.New("model catalog reasoning default is not supported")
		}
	}
	reasoning.Levels = levels
	return reasoning, nil
}

// DiscoverRuntimeModels discovers the catalog for the exact executable path
// registered for a runtime. Provider execution adapters do not participate.
func DiscoverRuntimeModels(ctx context.Context, provider, executablePath string) ([]RuntimeModel, error) {
	switch provider {
	case "codex":
		if executablePath == "" {
			return nil, errors.New("model discovery executable is unavailable")
		}
		raw, err := runCodexRuntimeModelDiscovery(ctx, executablePath)
		if err != nil {
			return nil, err
		}
		return parseCodexRuntimeModels(raw)
	case "claude":
		return NormalizeRuntimeModels(claudeRuntimeModelAliases())
	default:
		return nil, fmt.Errorf("model discovery provider is unsupported: %q", provider)
	}
}

func runCodexRuntimeModelDiscovery(ctx context.Context, executablePath string) ([]byte, error) {
	output := &boundedModelOutput{limit: runtimeModelCatalogMaxBytes}
	cmd := exec.CommandContext(ctx, executablePath, "debug", "models", "--bundled")
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("model discovery timed out: %w", ctx.Err())
		}
		if errors.Is(output.err, errRuntimeModelOutputTooLarge) {
			return nil, errRuntimeModelOutputTooLarge
		}
		return nil, errors.New("model discovery command failed")
	}
	return bytes.Clone(output.Bytes()), nil
}

var errRuntimeModelOutputTooLarge = errors.New("model discovery output exceeds the size limit")

type boundedModelOutput struct {
	bytes.Buffer
	limit int
	err   error
}

func (w *boundedModelOutput) Write(p []byte) (int, error) {
	remaining := w.limit - w.Len()
	if len(p) > remaining {
		if remaining > 0 {
			_, _ = w.Buffer.Write(p[:remaining])
		}
		w.err = errRuntimeModelOutputTooLarge
		return remaining, w.err
	}
	return w.Buffer.Write(p)
}

type codexRuntimeModelCatalog struct {
	Models []codexRuntimeModel `json:"models"`
}

type codexRuntimeModel struct {
	Slug                     string                       `json:"slug"`
	DisplayName              string                       `json:"display_name"`
	Visibility               string                       `json:"visibility"`
	DefaultReasoningLevel    string                       `json:"default_reasoning_level"`
	SupportedReasoningLevels []codexRuntimeReasoningLevel `json:"supported_reasoning_levels"`
}

type codexRuntimeReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

func parseCodexRuntimeModels(raw []byte) ([]RuntimeModel, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var catalog codexRuntimeModelCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, errors.New("model discovery returned an invalid catalog")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("model discovery returned an invalid catalog")
	}
	if len(catalog.Models) > runtimeModelCatalogMaxModels {
		return nil, errors.New("model discovery returned too many models")
	}

	models := make([]RuntimeModel, 0, len(catalog.Models))
	for _, rawModel := range catalog.Models {
		if rawModel.Visibility == "hide" {
			continue
		}
		model, ok := normalizeCodexRuntimeModel(rawModel)
		if !ok {
			return nil, errors.New("model discovery returned an invalid model")
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, errors.New("model discovery returned no visible models")
	}
	models[0].Default = true
	return NormalizeRuntimeModels(models)
}

func normalizeCodexRuntimeModel(raw codexRuntimeModel) (RuntimeModel, bool) {
	id := strings.TrimSpace(raw.Slug)
	label := strings.TrimSpace(raw.DisplayName)
	if label == "" {
		label = id
	}
	if !boundedRuntimeModelString(id, runtimeModelMaxIDBytes) ||
		!boundedRuntimeModelString(label, runtimeModelMaxLabelBytes) ||
		len(raw.SupportedReasoningLevels) > runtimeModelMaxLevels {
		return RuntimeModel{}, false
	}

	model := RuntimeModel{ID: id, Label: label}
	levels := make([]RuntimeReasoningLevel, 0, len(raw.SupportedReasoningLevels))
	for _, rawLevel := range raw.SupportedReasoningLevels {
		value := strings.TrimSpace(rawLevel.Effort)
		description := strings.TrimSpace(rawLevel.Description)
		if !boundedRuntimeModelString(value, runtimeModelMaxIDBytes) ||
			len(description) > runtimeModelMaxDetailBytes {
			return RuntimeModel{}, false
		}
		levels = append(levels, RuntimeReasoningLevel{
			Value: value, Label: runtimeReasoningLabel(value), Description: description,
		})
	}
	if len(levels) > 0 {
		defaultLevel := strings.TrimSpace(raw.DefaultReasoningLevel)
		if len(defaultLevel) > runtimeModelMaxIDBytes {
			return RuntimeModel{}, false
		}
		model.Reasoning = &RuntimeModelReasoning{DefaultLevel: defaultLevel, Levels: levels}
	}
	return model, true
}

// boundedRuntimeModelString is the shared check for the short, required strings
// in a runtime's model catalog: present, and no longer than that field's own
// byte ceiling.
//
//nolint:unparam // the id and label ceilings are separate knobs that happen to share a value today
func boundedRuntimeModelString(value string, maximum int) bool {
	return value != "" && len(value) <= maximum
}

func runtimeReasoningLabel(value string) string {
	switch value {
	case "none":
		return "None"
	case "minimal":
		return "Minimal"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh":
		return "Extra high"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	default:
		return value
	}
}

func claudeRuntimeModelAliases() []RuntimeModel {
	levels := func(includeExtraHigh, includeMax bool) *RuntimeModelReasoning {
		values := []string{"low", "medium", "high"}
		if includeExtraHigh {
			values = append(values, "xhigh")
		}
		if includeMax {
			values = append(values, "max")
		}
		choices := make([]RuntimeReasoningLevel, 0, len(values))
		for _, value := range values {
			choices = append(
				choices,
				RuntimeReasoningLevel{Value: value, Label: runtimeReasoningLabel(value)},
			)
		}
		return &RuntimeModelReasoning{DefaultLevel: "medium", Levels: choices}
	}
	return []RuntimeModel{
		{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Default: true, Reasoning: levels(false, true)},
		{ID: "claude-opus-4-6", Label: "Claude Opus 4.6", Reasoning: levels(true, true)},
		{ID: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5", Reasoning: levels(false, false)},
	}
}
