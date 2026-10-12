package agent

import (
	"maps"
	"slices"
)

// Tool images are delivered directly to the model by its provider. The durable
// transcript retains their metadata, not a second copy of the encoded pixels.
// Only native content blocks are inspected; structured customer JSON is intact.
func toolTranscriptContent(content []any) []any {
	result := slices.Clone(content)
	for i, value := range content {
		block, ok := value.(map[string]any)
		if !ok || block["type"] != "image" {
			continue
		}
		clean := maps.Clone(block)
		if _, ok := block["data"]; ok {
			delete(clean, "data")
			clean["dataOmitted"] = true
		}
		if source, ok := block["source"].(map[string]any); ok && source["type"] == "base64" {
			metadata := maps.Clone(source)
			delete(metadata, "data")
			clean["source"] = metadata
			clean["dataOmitted"] = true
		}
		result[i] = clean
	}
	return result
}
