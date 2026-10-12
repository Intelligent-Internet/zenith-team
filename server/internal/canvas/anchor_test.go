package canvas

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewAnchor(t *testing.T) {
	t.Parallel()
	valid := `{"type":"media","schemaVersion":1,"target":{"assetVersionId":"11111111-1111-1111-1111-111111111111"},"surface":"image","shapes":[{"type":"freehand","color":"amber","points":[[0.1,0.2],[0.3,0.4]]}]}`
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"image strokes", valid, true},
		{"out of bounds", strings.Replace(valid, "0.1", "1.1", 1), false},
		{"invalid color", strings.Replace(valid, "amber", "url(x)", 1), false},
		{"unknown property", strings.Replace(valid, `"surface"`, `"script":"x","surface"`, 1), false},
		{"pdf missing page", strings.Replace(valid, `"image"`, `"pdf"`, 1), false},
		{"video time", strings.Replace(valid, `"image"`, `"video","timeMs":12400`, 1), true},
		{"video negative time", strings.Replace(valid, `"image"`, `"video","timeMs":-1`, 1), false},
		{"wrong tuple", strings.Replace(valid, "[0.1,0.2]", "[0.1,0.2,0.3]", 1), false},
		{"too many points", strings.Replace(valid, "[[0.1,0.2],[0.3,0.4]]", "["+strings.Repeat("[0.1,0.2],", 512)+"[0.3,0.4]]", 1), false},
		{"unexpected image time", strings.Replace(valid, `"surface":"image"`, `"surface":"image","timeMs":1`, 1), false},
		{"anchor byte limit", strings.Replace(valid, `"amber"`, `"`+strings.Repeat("a", 16384)+`"`, 1), false},
		{"quote", `{"type":"textQuote","exact":"hello"}`, true},
		{"block", `{"type":"block","blockId":"11111111-1111-1111-1111-111111111111"}`, true},
		{"block without id", `{"type":"block"}`, false},
		{"empty quote", `{"type":"textQuote","exact":" "}`, false},
		{"general", `{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseReviewAnchor(json.RawMessage(tc.raw))
			if (err == nil) != tc.want {
				t.Fatalf("valid = %v, want %v: %v", err == nil, tc.want, err)
			}
		})
	}
}
