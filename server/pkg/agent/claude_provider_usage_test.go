package agent

import (
	"encoding/json"
	"testing"
)

func TestClaudeResultUsagePreservesReportedCost(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  *int64
	}{
		{"model cost", `{"modelUsage":{"sonnet":{"inputTokens":4,"costUSD":0.125}},"num_turns":2,"duration_ms":1200}`, new(int64(1250000000))},
		{"reported zero", `{"modelUsage":{"sonnet":{"inputTokens":4,"costUSD":0}}}`, new(int64(0))},
		{"single model fallback", `{"modelUsage":{"sonnet":{"inputTokens":4}},"total_cost_usd":0.5}`, new(int64(5000000000))},
		{"multiple models", `{"modelUsage":{"sonnet":{"inputTokens":4},"opus":{"inputTokens":5}},"total_cost_usd":0.5}`, nil},
		{"missing", `{"modelUsage":{"sonnet":{"inputTokens":4}}}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var msg claudeSDKMessage
			if err := json.Unmarshal([]byte(test.input), &msg); err != nil {
				t.Fatal(err)
			}
			got := claudeResultUsage(msg, "sonnet")["sonnet"]
			if (got.CostUSDTicks == nil) != (test.want == nil) || test.want != nil && *got.CostUSDTicks != *test.want {
				t.Fatalf("cost = %v, want %v", got.CostUSDTicks, test.want)
			}
			if msg.NumTurns > 0 && (got.Turns == nil || *got.Turns != 2 || got.ProviderDurationMs == nil || *got.ProviderDurationMs != 1200) {
				t.Fatalf("provider evidence = %+v", got)
			}
		})
	}
}
