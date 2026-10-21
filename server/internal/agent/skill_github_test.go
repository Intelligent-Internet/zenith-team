package agent

import "testing"

func TestSkillGitHubLocationAcceptsOnlyPublicGitHubPaths(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		url   string
		valid bool
	}{
		{"https://github.com/acme/skills", true},
		{"https://github.com/acme/skills/tree/main/skills/review", true},
		{"https://github.com/acme/skills/blob/main/review/SKILL.md", true},
		{"http://github.com/acme/skills", false},
		{"https://github.com.evil.test/acme/skills", false},
		{"https://user:secret@github.com/acme/skills", false},
		{"https://github.com/acme/skills/tree/main/../secrets", false},
		{"https://localhost/acme/skills", false},
	} {
		t.Run(test.url, func(t *testing.T) {
			t.Parallel()
			_, err := parseSkillGitHubLocation(SkillGitHubInput{URL: test.url})
			if (err == nil) != test.valid {
				t.Fatalf("valid = %v, want %v (%v)", err == nil, test.valid, err)
			}
		})
	}
}

func TestSkillGitHubLocationUsesFullURL(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		url  string
		ref  string
		path string
	}{
		{name: "repository", url: "https://github.com/acme/skills"},
		{
			name: "folder",
			url:  "https://github.com/acme/skills/tree/release/skills/review",
			ref:  "release",
			path: "skills/review",
		},
		{
			name: "file",
			url:  "https://github.com/acme/skills/blob/release/skills/review/SKILL.md",
			ref:  "release",
			path: "skills/review",
		},
		{name: "root file", url: "https://github.com/acme/skills/blob/main/SKILL.md", ref: "main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseSkillGitHubLocation(SkillGitHubInput{URL: test.url})
			if err != nil || got.Repository != "acme/skills" || got.Ref != test.ref ||
				got.Path != test.path {
				t.Fatalf(
					"location = %+v, error = %v, want acme/skills at %q/%q",
					got, err, test.ref, test.path,
				)
			}
		})
	}
}
