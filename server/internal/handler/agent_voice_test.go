package handler

import (
	"encoding/json"
	"testing"
)

func TestAgentVoiceRequiresCompleteStrictSettings(t *testing.T) {
	const valid = `{"enabled":true,"voice":"marin","greet":false,"participation":"addressed"}`
	got, err := parseAgentVoice(json.RawMessage(valid))
	if err != nil || got == nil || !got.Enabled || got.Greet {
		t.Fatalf("parse = %+v, %v", got, err)
	}
	for _, raw := range []string{
		`null`, `{}`, `{"enabled":true}`, valid[:len(valid)-1] + `,"tools":["shell"]}`,
		`{"enabled":true,"voice":"bad","greet":false,"participation":"addressed"}`,
		`{"enabled":true,"voice":"marin","greet":false,"participation":"always"}`,
	} {
		if _, err := parseAgentVoice(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if got, err := parseAgentVoice(nil); got != nil || err != nil {
		t.Fatalf("omitted settings = %+v, %v", got, err)
	}
}
