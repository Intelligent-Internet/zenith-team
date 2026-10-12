package handler

import "testing"

func TestStudioUnicodeJSON(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		raw  string
		want bool
	}{
		{`{"title":"\ud800"}`, false},
		{`{"title":"\udc00"}`, false},
		{`{"title":"\ud83d\ude00"}`, true},
		{`{"title":"\\ud800"}`, true},
		{`{"title":"漢"}`, true},
	} {
		if got := validStudioUnicodeJSON([]byte(tt.raw)); got != tt.want {
			t.Fatalf("%s = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
