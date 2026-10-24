package wiki

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func BenchmarkUIWorkloads(b *testing.B) {
	raw, err := os.ReadFile("testdata/intelligent-expressions.json")
	if err != nil {
		b.Fatal(err)
	}
	var fixtures []struct {
		Name        string
		Expression  any
		Environment UIEnvironment
		Code        string
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		b.Fatal(err)
	}
	for _, fixture := range fixtures {
		if !strings.HasPrefix(fixture.Name, "workload:") {
			continue
		}
		b.Run(strings.TrimPrefix(fixture.Name, "workload:"), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, err := EvaluateUIExpression(fixture.Expression, fixture.Environment)
				if fixture.Code == "" {
					if err != nil {
						b.Fatal(err)
					}
				} else {
					var diagnostic *UIEvaluationError
					if !errors.As(err, &diagnostic) || diagnostic.Code != fixture.Code {
						b.Fatalf("unexpected diagnostic: %v", err)
					}
				}
			}
		})
	}
}
