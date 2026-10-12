// Package voice owns the shared Agent voice configuration and invitation snapshot.
// It is separate because agent depends on conversation; Huddle cannot import its
// parent without a cycle. Voice grants no runtime or Connector execution authority.
package voice

import (
	"encoding/json"

	"github.com/google/uuid"
)

type Settings struct {
	Enabled       bool   `json:"enabled"`
	Voice         string `json:"voice"`
	Greet         bool   `json:"greet"`
	Participation string `json:"participation"`
}

func (s Settings) Valid() bool {
	switch s.Voice {
	case "aster", "beacon", "cinder", "marin", "stone", "vesper":
	default:
		return false
	}
	return s.Participation == "addressed" || s.Participation == "natural"
}

func FromSettings(raw []byte) Settings {
	stored := struct {
		Voice Settings `json:"voice"`
	}{Voice: Settings{Voice: "marin", Greet: true, Participation: "addressed"}}
	// Settings are trusted product state, written only after boundary validation.
	_ = json.Unmarshal(raw, &stored)
	return stored.Voice
}

type Identity struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	AvatarKey   *string   `json:"avatarKey"`
	AccentColor *string   `json:"accentColor"`
}

// Profile is frozen when invited. Subsequent edits apply on the next invitation;
// disabling Voice or archiving the Agent still revokes the current lease.
type Profile struct {
	Identity
	Revision     int64    `json:"revision"`
	Instructions string   `json:"instructions"`
	Settings     Settings `json:"settings"`
}

func FromSnapshot(raw []byte) Profile {
	var profile Profile
	_ = json.Unmarshal(raw, &profile)
	return profile
}
