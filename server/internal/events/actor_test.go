package events

import "testing"

// IA-FR4: an Agent acting in a Run names the person it acts for and the Run;
// a person and the platform name neither, and no actor names a Run without
// the person that Run works for.
func TestActorValidKeepsTheThreeIdentitiesTogether(t *testing.T) {
	t.Parallel()

	const (
		agent  = "0198bb00-0000-7000-8000-0000000000a1"
		person = "0198bb00-0000-7000-8000-0000000000a2"
		run    = "0198bb00-0000-7000-8000-0000000000a3"
	)
	for _, test := range []struct {
		name  string
		actor Actor
		want  bool
	}{
		{"person", UserActor(person), true},
		{"Agent in a Run for its person", RunActor(agent, run, person), true},
		{"Agent read back from a row with no Run", AgentActor(agent), true},
		{"Agent for a person outside a Run", Actor{Type: ActorTypeAgent, ID: agent, OnBehalfOf: person}, true},
		{"platform", SystemActor(), true},
		{"person with no id", UserActor(""), false},
		{"Agent with no id", RunActor("", run, person), false},
		{"Agent in a Run for nobody", Actor{Type: ActorTypeAgent, ID: agent, RunID: run}, false},
		{"person acting for someone", Actor{Type: ActorTypeUser, ID: person, OnBehalfOf: agent}, false},
		{"person in a Run", Actor{Type: ActorTypeUser, ID: person, RunID: run}, false},
		{"platform with an id", Actor{Type: ActorTypeSystem, ID: agent}, false},
		{"platform for a person", Actor{Type: ActorTypeSystem, OnBehalfOf: person}, false},
		{"platform in a Run", Actor{Type: ActorTypeSystem, RunID: run}, false},
		{"zero value", Actor{}, false},
		{"unknown type", Actor{Type: "robot", ID: agent}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.actor.Valid(); got != test.want {
				t.Fatalf("%+v.Valid() = %v, want %v", test.actor, got, test.want)
			}
		})
	}
}
