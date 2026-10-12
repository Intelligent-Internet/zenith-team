// Package studiocanvas defines the portable Studio source snapshot. It is shared
// by the CLI-linked MCP and the cloud output service; keeping it here prevents
// cmd/zen from linking server services and their database dependencies.
package studiocanvas

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode/utf8"
)

// Canvas is a frozen source artifact, uploaded under the same Run authority
// as the media. It contains no credentials, provider URLs or live Studio access.
type Canvas struct {
	Version   int    `json:"version"   jsonschema:"1"`
	ProjectID string `json:"projectId" jsonschema:"Actual Zenith Studio project identifier."`
	Nodes     []Node `json:"nodes"     jsonschema:"1 to 200 source nodes from Studio. Preserve the returned coordinates; do not invent nodes or prompts."`
	Edges     []Edge `json:"edges"     jsonschema:"Actual dependencies between included nodes; up to 400."`
}

type Node struct {
	ID            string   `json:"id"                      jsonschema:"Actual Studio node identifier."`
	Type          string   `json:"type"                    jsonschema:"Studio node type, for example image, video, audio, voice or text."`
	Label         string   `json:"label"                   jsonschema:"Short node name."`
	Position      Position `json:"position"                jsonschema:"Position returned by Studio, in canvas coordinates."`
	Prompt        string   `json:"prompt,omitempty"        jsonschema:"Actual prompt or text used by this node, if available."`
	Model         string   `json:"model,omitempty"         jsonschema:"Actual generation model, if available."`
	Status        string   `json:"status,omitempty"        jsonschema:"Node status at delivery, if available."`
	OutputOrdinal *int     `json:"outputOrdinal,omitempty" jsonschema:"Zero-based index into this call's items for this node's downloaded media. Omit when its media is not included."`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Edge struct {
	Source string `json:"source" jsonschema:"Source node id."`
	Target string `json:"target" jsonschema:"Target node id."`
}

func Validate(canvas *Canvas, itemCount int) error {
	if canvas == nil {
		return nil
	}
	invalid := errors.New(
		"studio canvas requires version 1, bounded source nodes and valid item/dependency references",
	)
	if canvas.Version != 1 || !validText(canvas.ProjectID, 128) ||
		len(canvas.Nodes) < 1 || len(canvas.Nodes) > 200 || len(canvas.Edges) > 400 ||
		itemCount > 32 {
		return invalid
	}
	ids := make(map[string]bool, len(canvas.Nodes))
	for _, node := range canvas.Nodes {
		if !validText(node.ID, 128) || ids[node.ID] ||
			!validText(node.Type, 80) || !validText(node.Label, 160) ||
			!validStudioCoordinate(node.Position.X) || !validStudioCoordinate(node.Position.Y) {
			return invalid
		}
		for _, value := range []string{node.Prompt, node.Model, node.Status} {
			if value != "" && !validText(value, 16000) {
				return invalid
			}
		}
		if node.OutputOrdinal != nil && (*node.OutputOrdinal < 0 || *node.OutputOrdinal >= itemCount) {
			return invalid
		}
		ids[node.ID] = true
	}
	edges := make(map[Edge]bool, len(canvas.Edges))
	for _, edge := range canvas.Edges {
		if !ids[edge.Source] || !ids[edge.Target] || edge.Source == edge.Target || edges[edge] {
			return invalid
		}
		edges[edge] = true
	}
	encoded, err := json.Marshal(canvas)
	if err != nil || len(encoded) > 1024*1024 {
		return invalid
	}
	return nil
}

func validStudioCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Abs(value) <= 1000000
}

func validText(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00') &&
		utf8.RuneCountInString(value) <= maximum
}
