package voice

import "testing"

func TestSettingsDefaultsAndValidation(t *testing.T) {
	settings := FromSettings([]byte(`{"capabilities":{"enabledTools":["tasks"]}}`))
	if !settings.Enabled || settings.Voice != "marin" || !settings.Greet || settings.Participation != "addressed" {
		t.Fatalf("defaults = %+v", settings)
	}
	for _, name := range []string{"aster", "beacon", "cinder", "marin", "stone", "vesper"} {
		settings.Voice = name
		if !settings.Valid() {
			t.Errorf("supported voice rejected: %s", name)
		}
	}
	settings.Voice = "unknown"
	if settings.Valid() {
		t.Fatal("unsupported voice accepted")
	}
	settings.Voice = "marin"
	settings.Participation = "always"
	if settings.Valid() {
		t.Fatal("unsupported participation accepted")
	}
}

func TestStoredVoiceOverridesDefaults(t *testing.T) {
	settings := FromSettings([]byte(`{"voice":{"enabled":true,"voice":"stone","greet":false,"participation":"natural"}}`))
	if !settings.Enabled || settings.Voice != "stone" || settings.Greet || settings.Participation != "natural" {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestVoiceIsOnUnlessTurnedOff(t *testing.T) {
	for _, raw := range []string{`{}`, `{"voice":{"voice":"stone"}}`} {
		if settings := FromSettings([]byte(raw)); !settings.Enabled {
			t.Fatalf("FromSettings(%s).Enabled = false, want the on default", raw)
		}
	}
	if FromSettings([]byte(`{"voice":{"enabled":false}}`)).Enabled {
		t.Fatal("an Agent turned off must stay off")
	}
}
