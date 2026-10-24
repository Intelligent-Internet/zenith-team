package wiki

import (
	"encoding/json"
	"os"
	"testing"
)

func TestUITableDatesMatchCaptureContract(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/intelligent-table-values.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name         string `json:"name"`
		Type         string `json:"type"`
		Left         any    `json:"left"`
		CaptureValid bool   `json:"captureValid"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, item := range cases {
		t.Run(item.Name, func(t *testing.T) {
			node := BlockNode{
				Type: "uiDataTable",
				Attrs: map[string]any{
					"columns": []any{
						map[string]any{"key": "value", "label": "Value", "type": item.Type},
					},
				},
			}
			err := validateUICaptureData(node, []any{map[string]any{"value": item.Left}})
			if (err == nil) != item.CaptureValid {
				t.Fatalf("capture validity=%v, want %v", err, item.CaptureValid)
			}
		})
	}
}
